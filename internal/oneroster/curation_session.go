package oneroster

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/internal/db"
	"github.com/StevenACoffman/roster/internal/resilience"
)

// Academic session curation, which differs from every other entity's in two
// ways — both traceable to one fact in the OneRoster schema.
//
// AcademicSessionDType has no org reference. Its parent and children chain only
// to other sessions, and schoolYear is a "YYYY" label rather than a link.
// Classes and courses point AT a session, so a session is reachable from every
// org whose classes use it: a union, not a scope. A brand-new session has
// nothing pointing at it at all.
//
// So, first: these require a deployment-wide roster_admin grant and are scoped
// to no org. A district_admin cannot create a school year, because a school
// year is not a district's to own.
//
// And second: a refusal is permission_denied, not not_found. Everywhere else a
// write is refused with not_found so it cannot be used to probe for an entity's
// existence — but sessions are readable by any principal holding any grant, so
// their existence is not a secret and pretending otherwise would only mislead a
// caller who is entitled to see them.

// errNotRosterAdmin is what a caller without the global grant is told.
var errNotRosterAdmin = errors.New(
	"curating academic sessions requires a deployment-wide roster_admin grant; " +
		"sessions have no owning org to scope a narrower grant against")

// requireRosterAdmin reports whether the caller holds a live global
// roster_admin grant, translating the refusal if not.
func (h *Handler) requireRosterAdmin(ctx context.Context, op, subject string) error {
	allowed, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (bool, error) {
		return h.queries.SubjectIsGlobalRosterAdmin(c, subject)
	})
	if err != nil {
		return translate(ctx, op, err)
	}
	if !allowed {
		return connect.NewError(connect.CodePermissionDenied, errNotRosterAdmin)
	}
	return nil
}

// sessionWriteOutcome maps a guarded session write's zero-row result.
//
// The caller has already been confirmed as a roster_admin, so the grant is not
// the explanation. What remains is a missing row or a moved version, and the
// existence probe separates them.
func (h *Handler) sessionWriteOutcome(
	ctx context.Context, op, sourcedID string, err error,
) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return translate(ctx, op, err)
	}

	present, probeErr := h.queries.AcademicSessionExists(ctx, sourcedID)
	if probeErr != nil {
		return translate(ctx, op, probeErr)
	}
	if present {
		return connect.NewError(connect.CodeAborted, errStaleWrite)
	}
	return connect.NewError(connect.CodeNotFound, errNotFound)
}

func (h *Handler) CreateAcademicSession(
	ctx context.Context, req *connect.Request[v1.CreateAcademicSessionRequest],
) (*connect.Response[v1.CreateAcademicSessionResponse], error) {
	const op = "Handler.CreateAcademicSession"

	session := req.Msg.GetAcademicSession()
	subject, id, err := h.planWrite(ctx, op, session.GetSourcedId())
	if err != nil {
		return nil, err
	}
	if adminErr := h.requireRosterAdmin(ctx, op, subject); adminErr != nil {
		return nil, adminErr
	}
	if err = requireSessionDates(session); err != nil {
		return nil, translate(ctx, op, err)
	}

	fields := toStorageAcademicSession(session, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.AcademicSession, error) {
		return h.queries.CreateAcademicSessionGuarded(c, db.CreateAcademicSessionGuardedParams{
			SourcedID:        id,
			Status:           fields.Status,
			DateLastModified: fields.DateLastModified,
			Metadata:         fields.Metadata,
			Title:            fields.Title,
			StartDate:        fields.StartDate,
			EndDate:          fields.EndDate,
			Type:             fields.Type,
			SchoolYear:       fields.SchoolYear,
			ParentSourcedID:  fields.ParentSourcedID,
			ParentHref:       fields.ParentHref,
			Subject:          subject,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	return connect.NewResponse(&v1.CreateAcademicSessionResponse{
		AcademicSession: toProtoAcademicSession(row),
	}), nil
}

func (h *Handler) UpdateAcademicSession(
	ctx context.Context, req *connect.Request[v1.UpdateAcademicSessionRequest],
) (*connect.Response[v1.UpdateAcademicSessionResponse], error) {
	const op = "Handler.UpdateAcademicSession"

	incoming := req.Msg.GetAcademicSession()
	subject, id, paths, version, err := h.planUpdate(ctx, op,
		incoming.GetSourcedId(), req.Msg.GetUpdateMask(),
		req.Msg.GetExpectedDateLastModified(), incoming)
	if err != nil {
		return nil, err
	}
	if adminErr := h.requireRosterAdmin(ctx, op, subject); adminErr != nil {
		return nil, adminErr
	}

	stored, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (db.AcademicSession, error) {
		return h.queries.GetAcademicSessionForSubject(c, db.GetAcademicSessionForSubjectParams{
			Subject: subject, SourcedID: id,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	merged, err := mergeWrite(toProtoAcademicSession(stored), incoming, paths)
	if err != nil {
		return nil, mergeFault(ctx, op, err)
	}
	// Checked after the merge, not before: the mask may change only one of the
	// two dates, so the pair has to be validated as it will actually be stored.
	if err = requireSessionDates(merged); err != nil {
		return nil, translate(ctx, op, err)
	}

	fields := toStorageAcademicSession(merged, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.AcademicSession, error) {
		return h.queries.UpdateAcademicSessionGuarded(c, db.UpdateAcademicSessionGuardedParams{
			SourcedID:                id,
			Status:                   fields.Status,
			DateLastModified:         fields.DateLastModified,
			Metadata:                 fields.Metadata,
			Title:                    fields.Title,
			StartDate:                fields.StartDate,
			EndDate:                  fields.EndDate,
			Type:                     fields.Type,
			SchoolYear:               fields.SchoolYear,
			ParentSourcedID:          fields.ParentSourcedID,
			ParentHref:               fields.ParentHref,
			ExpectedDateLastModified: version,
			Subject:                  subject,
		})
	})
	if err != nil {
		return nil, h.sessionWriteOutcome(ctx, op, id, err)
	}

	return connect.NewResponse(&v1.UpdateAcademicSessionResponse{
		AcademicSession: toProtoAcademicSession(row),
	}), nil
}

func (h *Handler) DeleteAcademicSession(
	ctx context.Context, req *connect.Request[v1.DeleteAcademicSessionRequest],
) (*connect.Response[v1.DeleteAcademicSessionResponse], error) {
	const op = "Handler.DeleteAcademicSession"

	subject, id, err := h.planWrite(ctx, op, req.Msg.GetSourcedId())
	if err != nil {
		return nil, err
	}
	if adminErr := h.requireRosterAdmin(ctx, op, subject); adminErr != nil {
		return nil, adminErr
	}

	if _, err = resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.AcademicSession, error) {
		return h.queries.DeleteAcademicSessionGuarded(c, db.DeleteAcademicSessionGuardedParams{
			SourcedID:        id,
			DateLastModified: writeTimestamp(h.now()),
			Subject:          subject,
		})
	}); err != nil {
		// No version check on a delete, so a missing row is the only remaining
		// explanation; a row already flagged deleted still matches and succeeds.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errNotFound)
		}
		return nil, translate(ctx, op, err)
	}

	return connect.NewResponse(&v1.DeleteAcademicSessionResponse{}), nil
}
