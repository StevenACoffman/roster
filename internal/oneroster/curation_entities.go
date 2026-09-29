package oneroster

import (
	"context"

	"connectrpc.com/connect"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/internal/db"
	"github.com/StevenACoffman/roster/internal/resilience"
)

// One Create/Update/Delete trio per curatable entity. Each is the same five
// steps — plan, load, merge, write, map the outcome — differing only in which
// table and which fields. The steps that involve a decision live in curation.go
// (pure) and curation_handler.go (shared shell); what remains here is the field
// mapping, which is genuinely per-entity.
//
// Every write runs through resilience.Write, which applies the circuit breaker
// but never the retry policy: replaying a write that may already have committed
// would duplicate an insert, or re-apply an update against a version that has
// since moved.

// ── Org ───────────────────────────────────────────────────────────────────────

func (h *Handler) CreateOrg(
	ctx context.Context, req *connect.Request[v1.CreateOrgRequest],
) (*connect.Response[v1.CreateOrgResponse], error) {
	const op = "Handler.CreateOrg"

	org := req.Msg.GetOrg()
	subject, id, err := h.planWrite(ctx, op, org.GetSourcedId())
	if err != nil {
		return nil, err
	}

	fields := toStorageOrg(org, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Org, error) {
		return h.queries.CreateOrgGuarded(c, db.CreateOrgGuardedParams{
			SourcedID:        id,
			Status:           fields.Status,
			DateLastModified: fields.DateLastModified,
			Metadata:         fields.Metadata,
			Name:             fields.Name,
			Type:             fields.Type,
			Identifier:       fields.Identifier,
			ParentSourcedID:  fields.ParentSourcedID,
			ParentHref:       fields.ParentHref,
			Subject:          subject,
		})
	})
	if err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.CreateOrgResponse{Org: toProtoOrg(row)}), nil
}

func (h *Handler) UpdateOrg(
	ctx context.Context, req *connect.Request[v1.UpdateOrgRequest],
) (*connect.Response[v1.UpdateOrgResponse], error) {
	const op = "Handler.UpdateOrg"

	incoming := req.Msg.GetOrg()
	subject, id, paths, version, err := h.planUpdate(ctx, op,
		incoming.GetSourcedId(), req.Msg.GetUpdateMask(),
		req.Msg.GetExpectedDateLastModified(), incoming)
	if err != nil {
		return nil, err
	}

	// Read the current record through the same scoped query the read RPCs use,
	// so an update cannot see a row a get could not.
	stored, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (db.Org, error) {
		return h.queries.GetOrgForSubject(c, db.GetOrgForSubjectParams{
			Subject: subject, SourcedID: id,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	merged, err := mergeWrite(toProtoOrg(stored), incoming, paths)
	if err != nil {
		return nil, mergeFault(ctx, op, err)
	}

	fields := toStorageOrg(merged, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Org, error) {
		return h.queries.UpdateOrgGuarded(c, db.UpdateOrgGuardedParams{
			SourcedID:                id,
			Status:                   fields.Status,
			DateLastModified:         fields.DateLastModified,
			Metadata:                 fields.Metadata,
			Name:                     fields.Name,
			Type:                     fields.Type,
			Identifier:               fields.Identifier,
			ParentSourcedID:          fields.ParentSourcedID,
			ParentHref:               fields.ParentHref,
			ExpectedDateLastModified: version,
			Subject:                  subject,
		})
	})
	if err != nil {
		return nil, h.writeOutcome(ctx, op, err, func(c context.Context) (bool, error) {
			return h.queries.OrgIsVisibleForCuration(c, db.OrgIsVisibleForCurationParams{
				Subject: subject, SourcedID: id,
			})
		})
	}

	return connect.NewResponse(&v1.UpdateOrgResponse{Org: toProtoOrg(row)}), nil
}

func (h *Handler) DeleteOrg(
	ctx context.Context, req *connect.Request[v1.DeleteOrgRequest],
) (*connect.Response[v1.DeleteOrgResponse], error) {
	const op = "Handler.DeleteOrg"

	subject, id, err := h.planWrite(ctx, op, req.Msg.GetSourcedId())
	if err != nil {
		return nil, err
	}

	if _, err = resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Org, error) {
		return h.queries.DeleteOrgGuarded(c, db.DeleteOrgGuardedParams{
			SourcedID:        id,
			DateLastModified: writeTimestamp(h.now()),
			Subject:          subject,
		})
	}); err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.DeleteOrgResponse{}), nil
}

// ── Course ───────────────────────────────────────────────────────────────────────

func (h *Handler) CreateCourse(
	ctx context.Context, req *connect.Request[v1.CreateCourseRequest],
) (*connect.Response[v1.CreateCourseResponse], error) {
	const op = "Handler.CreateCourse"

	course := req.Msg.GetCourse()
	subject, id, err := h.planWrite(ctx, op, course.GetSourcedId())
	if err != nil {
		return nil, err
	}

	fields := toStorageCourse(course, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Course, error) {
		return h.queries.CreateCourseGuarded(c, db.CreateCourseGuardedParams{
			SourcedID:           id,
			Status:              fields.Status,
			DateLastModified:    fields.DateLastModified,
			Metadata:            fields.Metadata,
			Title:               fields.Title,
			CourseCode:          fields.CourseCode,
			Grades:              fields.Grades,
			Subjects:            fields.Subjects,
			SubjectCodes:        fields.SubjectCodes,
			OrgSourcedID:        fields.OrgSourcedID,
			OrgHref:             fields.OrgHref,
			SchoolYearSourcedID: fields.SchoolYearSourcedID,
			SchoolYearHref:      fields.SchoolYearHref,
			Subject:             subject,
		})
	})
	if err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.CreateCourseResponse{Course: toProtoCourse(row)}), nil
}

func (h *Handler) UpdateCourse(
	ctx context.Context, req *connect.Request[v1.UpdateCourseRequest],
) (*connect.Response[v1.UpdateCourseResponse], error) {
	const op = "Handler.UpdateCourse"

	incoming := req.Msg.GetCourse()
	subject, id, paths, version, err := h.planUpdate(ctx, op,
		incoming.GetSourcedId(), req.Msg.GetUpdateMask(),
		req.Msg.GetExpectedDateLastModified(), incoming)
	if err != nil {
		return nil, err
	}

	// Read the current record through the same scoped query the read RPCs use,
	// so an update cannot see a row a get could not.
	stored, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (db.Course, error) {
		return h.queries.GetCourseForSubject(c, db.GetCourseForSubjectParams{
			Subject: subject, SourcedID: id,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	merged, err := mergeWrite(toProtoCourse(stored), incoming, paths)
	if err != nil {
		return nil, mergeFault(ctx, op, err)
	}

	fields := toStorageCourse(merged, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Course, error) {
		return h.queries.UpdateCourseGuarded(c, db.UpdateCourseGuardedParams{
			SourcedID:                id,
			Status:                   fields.Status,
			DateLastModified:         fields.DateLastModified,
			Metadata:                 fields.Metadata,
			Title:                    fields.Title,
			CourseCode:               fields.CourseCode,
			Grades:                   fields.Grades,
			Subjects:                 fields.Subjects,
			SubjectCodes:             fields.SubjectCodes,
			OrgSourcedID:             fields.OrgSourcedID,
			OrgHref:                  fields.OrgHref,
			SchoolYearSourcedID:      fields.SchoolYearSourcedID,
			SchoolYearHref:           fields.SchoolYearHref,
			ExpectedDateLastModified: version,
			Subject:                  subject,
		})
	})
	if err != nil {
		return nil, h.writeOutcome(ctx, op, err, func(c context.Context) (bool, error) {
			return h.queries.CourseIsVisibleForCuration(c, db.CourseIsVisibleForCurationParams{
				Subject: subject, SourcedID: id,
			})
		})
	}

	return connect.NewResponse(&v1.UpdateCourseResponse{Course: toProtoCourse(row)}), nil
}

func (h *Handler) DeleteCourse(
	ctx context.Context, req *connect.Request[v1.DeleteCourseRequest],
) (*connect.Response[v1.DeleteCourseResponse], error) {
	const op = "Handler.DeleteCourse"

	subject, id, err := h.planWrite(ctx, op, req.Msg.GetSourcedId())
	if err != nil {
		return nil, err
	}

	if _, err = resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Course, error) {
		return h.queries.DeleteCourseGuarded(c, db.DeleteCourseGuardedParams{
			SourcedID:        id,
			DateLastModified: writeTimestamp(h.now()),
			Subject:          subject,
		})
	}); err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.DeleteCourseResponse{}), nil
}

// ── Class ───────────────────────────────────────────────────────────────────────

func (h *Handler) CreateClass(
	ctx context.Context, req *connect.Request[v1.CreateClassRequest],
) (*connect.Response[v1.CreateClassResponse], error) {
	const op = "Handler.CreateClass"

	class := req.Msg.GetClass()
	subject, id, err := h.planWrite(ctx, op, class.GetSourcedId())
	if err != nil {
		return nil, err
	}

	// terms is required with minItems 1 in the OneRoster schema, and a junction
	// table cannot enforce that, so it is checked before anything is written.
	if err = requireTerms(class.GetTerms()); err != nil {
		return nil, translate(ctx, op, err)
	}

	fields := toStorageClass(class, h.now())
	termIDs, termHrefs := termRefs(class.GetTerms())

	var row db.Class
	err = h.inTx(ctx, func(q *db.Queries) error {
		created, insertErr := q.CreateClassGuarded(ctx, db.CreateClassGuardedParams{
			SourcedID:        id,
			Status:           fields.Status,
			DateLastModified: fields.DateLastModified,
			Metadata:         fields.Metadata,
			Title:            fields.Title,
			ClassCode:        fields.ClassCode,
			ClassType:        fields.ClassType,
			Location:         fields.Location,
			Grades:           fields.Grades,
			Subjects:         fields.Subjects,
			SubjectCodes:     fields.SubjectCodes,
			Periods:          fields.Periods,
			CourseSourcedID:  fields.CourseSourcedID,
			CourseHref:       fields.CourseHref,
			SchoolSourcedID:  fields.SchoolSourcedID,
			SchoolHref:       fields.SchoolHref,
			Subject:          subject,
		})
		if insertErr != nil {
			return insertErr
		}
		row = created

		// Same transaction: a class visible without its terms would be invalid,
		// and a term referencing an uncommitted class would violate the FK.
		return q.ReplaceClassTerms(ctx, db.ReplaceClassTermsParams{
			ClassSourcedID: id,
			Column2:        termIDs,
			Column3:        termHrefs,
		})
	})
	if err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	created := toProtoClass(row)
	created.Terms = class.GetTerms()
	return connect.NewResponse(&v1.CreateClassResponse{Class: created}), nil
}

func (h *Handler) UpdateClass(
	ctx context.Context, req *connect.Request[v1.UpdateClassRequest],
) (*connect.Response[v1.UpdateClassResponse], error) {
	const op = "Handler.UpdateClass"

	incoming := req.Msg.GetClass()
	subject, id, paths, version, err := h.planUpdate(ctx, op,
		incoming.GetSourcedId(), req.Msg.GetUpdateMask(),
		req.Msg.GetExpectedDateLastModified(), incoming)
	if err != nil {
		return nil, err
	}

	// Read the current record through the same scoped query the read RPCs use,
	// so an update cannot see a row a get could not.
	stored, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (db.Class, error) {
		return h.queries.GetClassForSubject(c, db.GetClassForSubjectParams{
			Subject: subject, SourcedID: id,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	merged, err := mergeWrite(toProtoClass(stored), incoming, paths)
	if err != nil {
		return nil, mergeFault(ctx, op, err)
	}

	fields := toStorageClass(merged, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Class, error) {
		return h.queries.UpdateClassGuarded(c, db.UpdateClassGuardedParams{
			SourcedID:                id,
			Status:                   fields.Status,
			DateLastModified:         fields.DateLastModified,
			Metadata:                 fields.Metadata,
			Title:                    fields.Title,
			ClassCode:                fields.ClassCode,
			ClassType:                fields.ClassType,
			Location:                 fields.Location,
			Grades:                   fields.Grades,
			Subjects:                 fields.Subjects,
			SubjectCodes:             fields.SubjectCodes,
			Periods:                  fields.Periods,
			CourseSourcedID:          fields.CourseSourcedID,
			CourseHref:               fields.CourseHref,
			SchoolSourcedID:          fields.SchoolSourcedID,
			SchoolHref:               fields.SchoolHref,
			ExpectedDateLastModified: version,
			Subject:                  subject,
		})
	})
	if err != nil {
		return nil, h.writeOutcome(ctx, op, err, func(c context.Context) (bool, error) {
			return h.queries.ClassIsVisibleForCuration(c, db.ClassIsVisibleForCurationParams{
				Subject: subject, SourcedID: id,
			})
		})
	}

	return connect.NewResponse(&v1.UpdateClassResponse{Class: toProtoClass(row)}), nil
}

func (h *Handler) DeleteClass(
	ctx context.Context, req *connect.Request[v1.DeleteClassRequest],
) (*connect.Response[v1.DeleteClassResponse], error) {
	const op = "Handler.DeleteClass"

	subject, id, err := h.planWrite(ctx, op, req.Msg.GetSourcedId())
	if err != nil {
		return nil, err
	}

	if _, err = resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Class, error) {
		return h.queries.DeleteClassGuarded(c, db.DeleteClassGuardedParams{
			SourcedID:        id,
			DateLastModified: writeTimestamp(h.now()),
			Subject:          subject,
		})
	}); err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.DeleteClassResponse{}), nil
}

// ── Enrollment ───────────────────────────────────────────────────────────────────────

func (h *Handler) CreateEnrollment(
	ctx context.Context, req *connect.Request[v1.CreateEnrollmentRequest],
) (*connect.Response[v1.CreateEnrollmentResponse], error) {
	const op = "Handler.CreateEnrollment"

	enrollment := req.Msg.GetEnrollment()
	subject, id, err := h.planWrite(ctx, op, enrollment.GetSourcedId())
	if err != nil {
		return nil, err
	}

	fields := toStorageEnrollment(enrollment, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Enrollment, error) {
		return h.queries.CreateEnrollmentGuarded(c, db.CreateEnrollmentGuardedParams{
			SourcedID:        id,
			Status:           fields.Status,
			DateLastModified: fields.DateLastModified,
			Metadata:         fields.Metadata,
			UserSourcedID:    fields.UserSourcedID,
			UserHref:         fields.UserHref,
			ClassSourcedID:   fields.ClassSourcedID,
			ClassHref:        fields.ClassHref,
			SchoolSourcedID:  fields.SchoolSourcedID,
			SchoolHref:       fields.SchoolHref,
			Role:             fields.Role,
			IsPrimary:        fields.IsPrimary,
			BeginDate:        fields.BeginDate,
			EndDate:          fields.EndDate,
			Subject:          subject,
		})
	})
	if err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.CreateEnrollmentResponse{Enrollment: toProtoEnrollment(row)}), nil
}

func (h *Handler) UpdateEnrollment(
	ctx context.Context, req *connect.Request[v1.UpdateEnrollmentRequest],
) (*connect.Response[v1.UpdateEnrollmentResponse], error) {
	const op = "Handler.UpdateEnrollment"

	incoming := req.Msg.GetEnrollment()
	subject, id, paths, version, err := h.planUpdate(ctx, op,
		incoming.GetSourcedId(), req.Msg.GetUpdateMask(),
		req.Msg.GetExpectedDateLastModified(), incoming)
	if err != nil {
		return nil, err
	}

	// Read the current record through the same scoped query the read RPCs use,
	// so an update cannot see a row a get could not.
	stored, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (db.Enrollment, error) {
		return h.queries.GetEnrollmentForSubject(c, db.GetEnrollmentForSubjectParams{
			Subject: subject, SourcedID: id,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	merged, err := mergeWrite(toProtoEnrollment(stored), incoming, paths)
	if err != nil {
		return nil, mergeFault(ctx, op, err)
	}

	fields := toStorageEnrollment(merged, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Enrollment, error) {
		return h.queries.UpdateEnrollmentGuarded(c, db.UpdateEnrollmentGuardedParams{
			SourcedID:                id,
			Status:                   fields.Status,
			DateLastModified:         fields.DateLastModified,
			Metadata:                 fields.Metadata,
			UserSourcedID:            fields.UserSourcedID,
			UserHref:                 fields.UserHref,
			ClassSourcedID:           fields.ClassSourcedID,
			ClassHref:                fields.ClassHref,
			SchoolSourcedID:          fields.SchoolSourcedID,
			SchoolHref:               fields.SchoolHref,
			Role:                     fields.Role,
			IsPrimary:                fields.IsPrimary,
			BeginDate:                fields.BeginDate,
			EndDate:                  fields.EndDate,
			ExpectedDateLastModified: version,
			Subject:                  subject,
		})
	})
	if err != nil {
		return nil, h.writeOutcome(ctx, op, err, func(c context.Context) (bool, error) {
			return h.queries.EnrollmentIsVisibleForCuration(c, db.EnrollmentIsVisibleForCurationParams{
				Subject: subject, SourcedID: id,
			})
		})
	}

	return connect.NewResponse(&v1.UpdateEnrollmentResponse{Enrollment: toProtoEnrollment(row)}), nil
}

func (h *Handler) DeleteEnrollment(
	ctx context.Context, req *connect.Request[v1.DeleteEnrollmentRequest],
) (*connect.Response[v1.DeleteEnrollmentResponse], error) {
	const op = "Handler.DeleteEnrollment"

	subject, id, err := h.planWrite(ctx, op, req.Msg.GetSourcedId())
	if err != nil {
		return nil, err
	}

	if _, err = resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.Enrollment, error) {
		return h.queries.DeleteEnrollmentGuarded(c, db.DeleteEnrollmentGuardedParams{
			SourcedID:        id,
			DateLastModified: writeTimestamp(h.now()),
			Subject:          subject,
		})
	}); err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.DeleteEnrollmentResponse{}), nil
}

// ── User ───────────────────────────────────────────────────────────────────────

func (h *Handler) CreateUser(
	ctx context.Context, req *connect.Request[v1.CreateUserRequest],
) (*connect.Response[v1.CreateUserResponse], error) {
	const op = "Handler.CreateUser"

	user := req.Msg.GetUser()
	subject, id, err := h.planWrite(ctx, op, user.GetSourcedId())
	if err != nil {
		return nil, err
	}

	// roles is required with minItems 1, and here that is more than a validity
	// rule: every authorization predicate scopes a user through their roles, so a
	// user stored without any would be invisible and unreachable even to the
	// caller who created them.
	if err = requireRoles(user.GetRoles()); err != nil {
		return nil, translate(ctx, op, err)
	}

	fields := toStorageUser(user, h.now())
	// Scoped against the org of the user's primary role rather than primaryOrg,
	// which the schema leaves optional: the role edge is the one that decides
	// visibility, so it must also be the one that authorizes creation.
	scopeOrg := primaryRoleOrg(user.GetRoles())

	var row db.OnerosterUser
	err = h.inTx(ctx, func(q *db.Queries) error {
		created, insertErr := q.CreateUserGuarded(ctx, db.CreateUserGuardedParams{
			SourcedID:            id,
			Status:               fields.Status,
			DateLastModified:     fields.DateLastModified,
			Metadata:             fields.Metadata,
			UserMasterIdentifier: fields.UserMasterIdentifier,
			Username:             fields.Username,
			EnabledUser:          fields.EnabledUser,
			GivenName:            fields.GivenName,
			FamilyName:           fields.FamilyName,
			MiddleName:           fields.MiddleName,
			PreferredFirstName:   fields.PreferredFirstName,
			PreferredLastName:    fields.PreferredLastName,
			PreferredMiddleName:  fields.PreferredMiddleName,
			Pronouns:             fields.Pronouns,
			Grades:               fields.Grades,
			Identifier:           fields.Identifier,
			Email:                fields.Email,
			Sms:                  fields.Sms,
			Phone:                fields.Phone,
			PrimaryOrgSourcedID:  fields.PrimaryOrgSourcedID,
			PrimaryOrgHref:       fields.PrimaryOrgHref,
			Subject:              subject,
			ScopeOrgSourcedID:    scopeOrg,
		})
		if insertErr != nil {
			return insertErr
		}
		row = created

		for ordinal, role := range user.GetRoles() {
			if _, roleErr := q.InsertUserRole(ctx, db.InsertUserRoleParams{
				UserSourcedID: id,
				RoleType:      checkedRoleType(role.GetRoleType()),
				Role:          role.GetRole(),
				OrgSourcedID:  role.GetOrg().GetSourcedId(),
				OrgHref:       refText(role.GetOrg().GetHref(), true),
				BeginDate:     storageDate(role.GetBeginDate()),
				EndDate:       storageDate(role.GetEndDate()),
				UserProfile:   refText(role.GetUserProfile(), true),
				Ordinal:       int32(ordinal),
			}); roleErr != nil {
				return roleErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	created := toProtoUser(row)
	created.Roles = user.GetRoles()
	return connect.NewResponse(&v1.CreateUserResponse{User: created}), nil
}

func (h *Handler) UpdateUser(
	ctx context.Context, req *connect.Request[v1.UpdateUserRequest],
) (*connect.Response[v1.UpdateUserResponse], error) {
	const op = "Handler.UpdateUser"

	incoming := req.Msg.GetUser()
	subject, id, paths, version, err := h.planUpdate(ctx, op,
		incoming.GetSourcedId(), req.Msg.GetUpdateMask(),
		req.Msg.GetExpectedDateLastModified(), incoming)
	if err != nil {
		return nil, err
	}

	// Read the current record through the same scoped query the read RPCs use,
	// so an update cannot see a row a get could not.
	stored, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (db.OnerosterUser, error) {
		return h.queries.GetUserForSubject(c, db.GetUserForSubjectParams{
			Subject: subject, SourcedID: id,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	merged, err := mergeWrite(toProtoUser(stored), incoming, paths)
	if err != nil {
		return nil, mergeFault(ctx, op, err)
	}

	fields := toStorageUser(merged, h.now())
	row, err := resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.OnerosterUser, error) {
		return h.queries.UpdateUserGuarded(c, db.UpdateUserGuardedParams{
			SourcedID:                id,
			Status:                   fields.Status,
			DateLastModified:         fields.DateLastModified,
			Metadata:                 fields.Metadata,
			UserMasterIdentifier:     fields.UserMasterIdentifier,
			Username:                 fields.Username,
			EnabledUser:              fields.EnabledUser,
			GivenName:                fields.GivenName,
			FamilyName:               fields.FamilyName,
			MiddleName:               fields.MiddleName,
			PreferredFirstName:       fields.PreferredFirstName,
			PreferredLastName:        fields.PreferredLastName,
			PreferredMiddleName:      fields.PreferredMiddleName,
			Pronouns:                 fields.Pronouns,
			Grades:                   fields.Grades,
			Identifier:               fields.Identifier,
			Email:                    fields.Email,
			Sms:                      fields.Sms,
			Phone:                    fields.Phone,
			PrimaryOrgSourcedID:      fields.PrimaryOrgSourcedID,
			PrimaryOrgHref:           fields.PrimaryOrgHref,
			ExpectedDateLastModified: version,
			Subject:                  subject,
		})
	})
	if err != nil {
		return nil, h.writeOutcome(ctx, op, err, func(c context.Context) (bool, error) {
			return h.queries.UserIsVisibleForCuration(c, db.UserIsVisibleForCurationParams{
				Subject: subject, SourcedID: id,
			})
		})
	}

	return connect.NewResponse(&v1.UpdateUserResponse{User: toProtoUser(row)}), nil
}

func (h *Handler) DeleteUser(
	ctx context.Context, req *connect.Request[v1.DeleteUserRequest],
) (*connect.Response[v1.DeleteUserResponse], error) {
	const op = "Handler.DeleteUser"

	subject, id, err := h.planWrite(ctx, op, req.Msg.GetSourcedId())
	if err != nil {
		return nil, err
	}

	if _, err = resilience.Write(ctx, h.resilientDB, func(c context.Context) (db.OnerosterUser, error) {
		return h.queries.DeleteUserGuarded(c, db.DeleteUserGuardedParams{
			SourcedID:        id,
			DateLastModified: writeTimestamp(h.now()),
			Subject:          subject,
		})
	}); err != nil {
		return nil, refusedWrite(ctx, op, err)
	}

	return connect.NewResponse(&v1.DeleteUserResponse{}), nil
}
