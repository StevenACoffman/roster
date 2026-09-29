package oneroster

import (
	"context"
	"fmt"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/internal/db"
)

// Association loading for entities whose children live in other tables.
//
// Each helper takes the whole page and issues one query for it, keyed by an
// array of ids. PostgreSQL is a remote server, so the per-record alternative
// costs a round trip per row — §8 permits that only for embedded databases.
//
// These run after the scoped primary query, so every id passed here has already
// been authorized. The association queries therefore need no subject filter:
// re-checking would cost a second join to answer a question already settled.

// attachClassTerms fills in Class.Terms for a page of classes.
//
// Terms are a required field in the OneRoster schema, so a class served without
// them fails a strict consumer's validation — this is not an optional enrichment.
func (h *Handler) attachClassTerms(ctx context.Context, classes []*v1.Class) error {
	if len(classes) == 0 {
		return nil
	}

	ids := make([]string, 0, len(classes))
	for _, c := range classes {
		ids = append(ids, c.GetSourcedId())
	}

	rows, err := h.queries.ListClassTermsForClasses(ctx, ids)
	if err != nil {
		return fmt.Errorf("attach class terms: %w", err)
	}

	byClass := make(map[string][]*v1.AcadSessionGUIDRef, len(classes))
	// Indexed rather than ranged by value: these rows are large enough that
	// copying each one costs more than the loop body.
	for i := range rows {
		row := &rows[i]
		byClass[row.ClassSourcedID] = append(byClass[row.ClassSourcedID],
			sessionRefRequired(row.SourcedID, row.AcademicSessionHref))
	}

	for _, c := range classes {
		c.Terms = byClass[c.GetSourcedId()]
	}
	return nil
}

// attachUserAssociations fills in a page of users' roles, external identifiers,
// and agents.
//
// Three queries for the whole page rather than three per user. Roles in
// particular are required by the schema and are also what the authorization
// queries scope on, so a user without them is both invalid and misleading.
func (h *Handler) attachUserAssociations(ctx context.Context, users []*v1.User) error {
	if len(users) == 0 {
		return nil
	}

	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.GetSourcedId())
	}

	roleRows, err := h.queries.ListUserRolesForUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("attach user roles: %w", err)
	}
	rolesByUser := make(map[string][]*v1.Role, len(users))
	for i := range roleRows {
		userID := roleRows[i].UserSourcedID
		rolesByUser[userID] = append(rolesByUser[userID], toProtoRole(roleRows[i]))
	}

	idRows, err := h.queries.ListUserIdentifiersForUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("attach user identifiers: %w", err)
	}
	idsByUser := make(map[string][]*v1.UserId, len(users))
	for _, row := range idRows {
		idsByUser[row.UserSourcedID] = append(idsByUser[row.UserSourcedID], toProtoUserID(row))
	}

	agentRows, err := h.queries.ListUserAgentsForUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("attach user agents: %w", err)
	}
	agentsByUser := make(map[string][]*v1.UserGUIDRef, len(users))
	for _, row := range agentRows {
		agentsByUser[row.UserSourcedID] = append(agentsByUser[row.UserSourcedID],
			userRef(row.AgentSourcedID, row.AgentHref))
	}

	for _, u := range users {
		id := u.GetSourcedId()
		u.Roles = rolesByUser[id]
		u.UserIds = idsByUser[id]
		u.Agents = agentsByUser[id]
	}
	return nil
}

// collectAll drains every page of a scoped list query.
//
// GetRoster is defined as a whole-roster snapshot, so it must read past the page
// boundary that the list RPCs stop at. The cap is what keeps "unpaged" from
// meaning "unbounded": a roster larger than it is refused with a clear error
// rather than being silently truncated or held entirely in memory.
func collectAll[Row any](
	ctx context.Context,
	subject string,
	query func(ctx context.Context, subject, after string, limit int32) ([]Row, error),
	sourcedID func(Row) string,
) ([]Row, error) {
	const (
		batchSize int32 = 1000
		// Compared against len(all), so an int rather than the int32 the query
		// limit uses.
		maxSnapshot = 100_000
	)

	all := make([]Row, 0, batchSize)
	after := ""
	for {
		rows, err := query(ctx, subject, after, batchSize)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)

		if len(rows) < int(batchSize) {
			return all, nil
		}
		if len(all) > maxSnapshot {
			return nil, fmt.Errorf(
				"%w: roster exceeds %d entities; use the per-entity list methods",
				errInvalid, maxSnapshot)
		}
		after = sourcedID(rows[len(rows)-1])
	}
}

// snapshotSource pairs a scoped list query with the field it fills on the
// Roster message. Naming the pair keeps GetRoster a loop rather than seven
// near-identical blocks.
type snapshotSource func(ctx context.Context, subject string, roster *v1.Roster) error

// rosterSources returns one loader per entity in the snapshot.
//
// orgSourcedID narrows the snapshot to a subtree; empty means everything the
// caller may see. Academic sessions carry no org, so their loader ignores it —
// see the note in sql/queries/rostering.sql.
func (h *Handler) rosterSources(orgSourcedID string) []snapshotSource {
	return []snapshotSource{
		func(ctx context.Context, subject string, roster *v1.Roster) error {
			rows, err := collectAll(ctx, subject,
				func(ctx context.Context, subject, after string, limit int32) ([]db.Org, error) {
					return h.queries.ListOrgsForSubject(ctx, db.ListOrgsForSubjectParams{
						Subject: subject, AfterSourcedID: after, PageSize: limit,
					})
				},
				func(row db.Org) string { return row.SourcedID })
			if err != nil {
				return err
			}
			roster.Orgs = mapRows(rows, toProtoOrg)
			return nil
		},
		func(ctx context.Context, subject string, roster *v1.Roster) error {
			rows, err := collectAll(ctx, subject,
				func(ctx context.Context, subject, after string, limit int32) ([]db.AcademicSession, error) {
					return h.queries.ListAcademicSessionsForSubject(ctx, db.ListAcademicSessionsForSubjectParams{
						Subject: subject, SessionType: "", AfterSourcedID: after, PageSize: limit,
					})
				},
				func(row db.AcademicSession) string { return row.SourcedID })
			if err != nil {
				return err
			}
			roster.AcademicSessions = mapRows(rows, toProtoAcademicSession)
			return nil
		},
		func(ctx context.Context, subject string, roster *v1.Roster) error {
			rows, err := collectAll(ctx, subject,
				func(ctx context.Context, subject, after string, limit int32) ([]db.Course, error) {
					return h.queries.ListCoursesForSubject(ctx, db.ListCoursesForSubjectParams{
						Subject: subject, OrgSourcedID: orgSourcedID,
						AfterSourcedID: after, PageSize: limit,
					})
				},
				func(row db.Course) string { return row.SourcedID })
			if err != nil {
				return err
			}
			roster.Courses = mapRows(rows, toProtoCourse)
			return nil
		},
		func(ctx context.Context, subject string, roster *v1.Roster) error {
			rows, err := collectAll(ctx, subject,
				func(ctx context.Context, subject, after string, limit int32) ([]db.Class, error) {
					return h.queries.ListClassesForSubject(ctx, db.ListClassesForSubjectParams{
						Subject: subject, OrgSourcedID: orgSourcedID,
						AfterSourcedID: after, PageSize: limit,
					})
				},
				func(row db.Class) string { return row.SourcedID })
			if err != nil {
				return err
			}
			roster.Classes = mapRows(rows, toProtoClass)
			return h.attachClassTerms(ctx, roster.GetClasses())
		},
		func(ctx context.Context, subject string, roster *v1.Roster) error {
			rows, err := collectAll(ctx, subject,
				func(ctx context.Context, subject, after string, limit int32) ([]db.OnerosterUser, error) {
					return h.queries.ListUsersForSubject(ctx, db.ListUsersForSubjectParams{
						Subject: subject, Role: "", OrgSourcedID: orgSourcedID,
						AfterSourcedID: after, PageSize: limit,
					})
				},
				func(row db.OnerosterUser) string { return row.SourcedID })
			if err != nil {
				return err
			}
			roster.Users = mapRows(rows, toProtoUser)
			return h.attachUserAssociations(ctx, roster.GetUsers())
		},
		func(ctx context.Context, subject string, roster *v1.Roster) error {
			rows, err := collectAll(ctx, subject,
				func(ctx context.Context, subject, after string, limit int32) ([]db.Enrollment, error) {
					return h.queries.ListEnrollmentsForSubject(ctx, db.ListEnrollmentsForSubjectParams{
						Subject: subject, OrgSourcedID: orgSourcedID,
						AfterSourcedID: after, PageSize: limit,
					})
				},
				func(row db.Enrollment) string { return row.SourcedID })
			if err != nil {
				return err
			}
			roster.Enrollments = mapRows(rows, toProtoEnrollment)
			return nil
		},
		func(ctx context.Context, subject string, roster *v1.Roster) error {
			rows, err := collectAll(ctx, subject,
				func(ctx context.Context, subject, after string, limit int32) ([]db.Demographic, error) {
					return h.queries.ListDemographicsForSubject(ctx, db.ListDemographicsForSubjectParams{
						Subject: subject, OrgSourcedID: orgSourcedID,
						AfterSourcedID: after, PageSize: limit,
					})
				},
				func(row db.Demographic) string { return row.SourcedID })
			if err != nil {
				return err
			}
			roster.Demographics = mapRows(rows, toProtoDemographics)
			return nil
		},
	}
}

// mapRows converts a slice of stored rows to their wire form, never returning
// nil — see the note in listPage.
func mapRows[Row, Msg any](rows []Row, convert func(Row) Msg) []Msg {
	out := make([]Msg, 0, len(rows))
	for _, row := range rows {
		out = append(out, convert(row))
	}
	return out
}
