//go:build integration

package oneroster

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/internal/db"
	"github.com/StevenACoffman/roster/internal/testutil"
)

// These run against a real PostgreSQL because the behaviour under test lives in
// the queries, not in Go: authorization is a join, and paging is a WHERE clause.
// A fake store would assert that the fake behaves as written, which is not the
// question.

// seedRoster populates a small district and returns the pool.
//
// Deliberately one long flat function rather than a set of per-entity helpers:
// when a test fails months from now the reader can see the entire world it ran
// against without opening another file.
func seedRoster(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := t.Context()
	q := db.New(pool)
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

	// st-1 ── d-1 ── sch-1
	//             └─ sch-2
	for _, o := range []struct{ id, kind, parent string }{
		{"st-1", "state", ""},
		{"d-1", "district", "st-1"},
		{"sch-1", "school", "d-1"},
		{"sch-2", "school", "d-1"},
	} {
		parent := pgtype.Text{}
		if o.parent != "" {
			parent = text(o.parent)
		}
		_, err := q.UpsertOrg(ctx, db.UpsertOrgParams{
			SourcedID: o.id, Status: "active", DateLastModified: now,
			Name: o.id, Type: o.kind, Identifier: o.id, ParentSourcedID: parent,
		})
		ok(t, err)
	}

	_, err := q.UpsertAcademicSession(ctx, db.UpsertAcademicSessionParams{
		SourcedID: "term-1", Status: "active", DateLastModified: now,
		Title:      "Fall Term",
		StartDate:  pgtype.Date{Time: time.Date(2025, time.August, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		EndDate:    pgtype.Date{Time: time.Date(2025, time.December, 20, 0, 0, 0, 0, time.UTC), Valid: true},
		Type:       "term",
		SchoolYear: "2026",
	})
	ok(t, err)

	_, err = q.UpsertCourse(ctx, db.UpsertCourseParams{
		SourcedID: "c-1", Status: "active", DateLastModified: now,
		Title: "Algebra I", CourseCode: "ALG1", OrgSourcedID: text("sch-2"),
		Grades: []string{"09"}, Subjects: []string{"Math"}, SubjectCodes: []string{},
	})
	ok(t, err)

	_, err = q.UpsertClass(ctx, db.UpsertClassParams{
		SourcedID: "cl-1", Status: "active", DateLastModified: now,
		Title: "Algebra I - P3", CourseSourcedID: "c-1", SchoolSourcedID: "sch-2",
		Grades: []string{"09"}, Subjects: []string{}, SubjectCodes: []string{}, Periods: []string{},
	})
	ok(t, err)
	ok(t, q.ReplaceClassTerms(ctx, db.ReplaceClassTermsParams{
		ClassSourcedID: "cl-1",
		Column2:        []string{"term-1"},
		Column3:        []string{""},
	}))

	// u-teach and u-stud are at sch-2; u-other is at sch-1, so a caller scoped to
	// sch-2 must never see them.
	for _, u := range []struct{ id, given, family, org, role string }{
		{"u-other", "Cy", "Diaz", "sch-1", "student"},
		{"u-stud", "Bo", "Chen", "sch-2", "student"},
		{"u-teach", "Ana", "Rivera", "sch-2", "teacher"},
	} {
		_, err = q.UpsertUser(ctx, db.UpsertUserParams{
			SourcedID: u.id, Status: "active", DateLastModified: now,
			EnabledUser: true, GivenName: u.given, FamilyName: u.family,
			PrimaryOrgSourcedID: text(u.org), Grades: []string{},
		})
		ok(t, err)
		_, err = q.InsertUserRole(ctx, db.InsertUserRoleParams{
			UserSourcedID: u.id, RoleType: "primary", Role: u.role,
			OrgSourcedID: u.org, Ordinal: 0,
		})
		ok(t, err)
	}

	_, err = q.UpsertEnrollment(ctx, db.UpsertEnrollmentParams{
		SourcedID: "e-1", Status: "active", DateLastModified: now,
		UserSourcedID: "u-teach", ClassSourcedID: "cl-1", SchoolSourcedID: "sch-2",
		Role: "teacher", IsPrimary: pgtype.Bool{Bool: true, Valid: true},
	})
	ok(t, err)

	// dee curates the whole district; ana only sch-2; nobody holds no grant.
	for _, p := range []struct{ subject, role, org string }{
		{"oidc|dee", "district_admin", "d-1"},
		{"oidc|ana", "school_admin", "sch-2"},
	} {
		principal, principalErr := q.UpsertPrincipal(ctx, db.UpsertPrincipalParams{
			Subject: p.subject, Kind: "person",
		})
		ok(t, principalErr)
		_, err = q.GrantRole(ctx, db.GrantRoleParams{
			PrincipalID: principal.ID, RoleName: p.role, OrgSourcedID: text(p.org),
		})
		ok(t, err)
	}
}

// callerCtx returns a context carrying an authenticated subject, standing in for
// what the authentication interceptor does in a live server.
func callerCtx(t *testing.T, subject string) context.Context {
	t.Helper()
	return NewContextWithSubject(t.Context(), subject)
}

func TestAuthorizationScopesEveryList(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)

	tests := []struct {
		name      string
		subject   string
		wantOrgs  int
		wantUsers int
	}{
		{
			name:      "district admin sees the whole subtree",
			subject:   "oidc|dee",
			wantOrgs:  3, // d-1, sch-1, sch-2 — st-1 is above the grant
			wantUsers: 3,
		},
		{
			name:      "school admin sees only their school",
			subject:   "oidc|ana",
			wantOrgs:  1,
			wantUsers: 2, // u-stud and u-teach; u-other is at sch-1
		},
		{
			name:      "a subject with no grant sees nothing",
			subject:   "oidc|nobody",
			wantOrgs:  0,
			wantUsers: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := callerCtx(t, tt.subject)

			orgs, err := h.GetAllOrgs(ctx, connect.NewRequest(&v1.GetAllOrgsRequest{}))
			ok(t, err)
			equals(t, len(orgs.Msg.GetOrgs()), tt.wantOrgs)

			users, err := h.GetAllUsers(ctx, connect.NewRequest(&v1.GetAllUsersRequest{}))
			ok(t, err)
			equals(t, len(users.Msg.GetUsers()), tt.wantUsers)
		})
	}
}

func TestOutOfScopeGetIsNotFound(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)

	ctx := callerCtx(t, "oidc|ana") // scoped to sch-2 only

	// In scope: found.
	got, err := h.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)
	equals(t, got.Msg.GetOrg().GetSourcedId(), "sch-2")

	// Out of scope and nonexistent must be indistinguishable, so that a caller
	// cannot use the response to learn that sch-1 exists.
	_, existsButForbidden := h.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "sch-1"}))
	wantCode(t, existsButForbidden, connect.CodeNotFound)

	_, doesNotExist := h.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "no-such-org"}))
	wantCode(t, doesNotExist, connect.CodeNotFound)
}

func TestAnonymousCallerIsRejected(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)

	// No subject on the context: the handler must say so rather than returning an
	// empty roster, which a caller could not tell from a genuinely empty one.
	_, err := h.GetAllOrgs(t.Context(), connect.NewRequest(&v1.GetAllOrgsRequest{}))
	wantCode(t, err, connect.CodeUnauthenticated)
}

func TestHandlerWithoutADatabaseIsUnavailable(t *testing.T) {
	t.Parallel()

	// NewHandler accepts a nil pool so the routing table can be built without a
	// database. Every RPC must then answer Unavailable rather than panicking.
	h := NewHandler(nil)

	_, err := h.GetAllOrgs(callerCtx(t, "oidc|dee"), connect.NewRequest(&v1.GetAllOrgsRequest{}))
	wantCode(t, err, connect.CodeUnavailable)
}

func TestKeysetPagingWalksEveryRowExactlyOnce(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)
	ctx := callerCtx(t, "oidc|dee")

	seen := map[string]int{}
	token := ""
	pages := 0
	for pages < 10 { // bounded so a paging bug fails rather than spins
		resp, err := h.GetAllOrgs(ctx, connect.NewRequest(&v1.GetAllOrgsRequest{
			PageSize: 2, PageToken: token,
		}))
		ok(t, err)
		pages++

		for _, org := range resp.Msg.GetOrgs() {
			seen[org.GetSourcedId()]++
		}
		token = resp.Msg.GetNextPageToken()
		if token == "" {
			break
		}
	}

	equals(t, len(seen), 3)
	for id, count := range seen {
		if count != 1 {
			t.Errorf("org %s returned %d times across pages, want exactly 1", id, count)
		}
	}
	if pages < 2 {
		t.Errorf("walked %d page(s) at size 2 over 3 rows; paging did not engage", pages)
	}
}

func TestMalformedPageTokenIsInvalidArgument(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)

	_, err := h.GetAllOrgs(callerCtx(t, "oidc|dee"), connect.NewRequest(&v1.GetAllOrgsRequest{
		PageToken: "!!! not base64 !!!",
	}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestAssociationsAreAttached(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)
	ctx := callerCtx(t, "oidc|dee")

	classes, err := h.GetAllClasses(ctx, connect.NewRequest(&v1.GetAllClassesRequest{}))
	ok(t, err)
	equals(t, len(classes.Msg.GetClasses()), 1)

	class := classes.Msg.GetClasses()[0]
	// Terms live in class_term and are required by the schema, so a class served
	// without them would fail a strict consumer's validation.
	equals(t, len(class.GetTerms()), 1)
	equals(t, class.GetTerms()[0].GetSourcedId(), "term-1")
	equals(t, class.GetTerms()[0].GetType(), "academicSession")
	equals(t, class.GetCourse().GetSourcedId(), "c-1")

	users, err := h.GetAllUsers(ctx, connect.NewRequest(&v1.GetAllUsersRequest{}))
	ok(t, err)
	for _, u := range users.Msg.GetUsers() {
		if len(u.GetRoles()) == 0 {
			t.Errorf("user %s came back with no roles; the batched load did not attach them",
				u.GetSourcedId())
		}
	}
}

func TestGetRosterIsScopedAndComplete(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)

	district, err := h.GetRoster(callerCtx(t, "oidc|dee"), connect.NewRequest(&v1.GetRosterRequest{}))
	ok(t, err)
	got := district.Msg.GetRoster()
	equals(t, len(got.GetOrgs()), 3)
	equals(t, len(got.GetUsers()), 3)
	equals(t, len(got.GetClasses()), 1)
	equals(t, len(got.GetEnrollments()), 1)

	school, err := h.GetRoster(callerCtx(t, "oidc|ana"), connect.NewRequest(&v1.GetRosterRequest{}))
	ok(t, err)
	scoped := school.Msg.GetRoster()
	equals(t, len(scoped.GetOrgs()), 1)
	equals(t, len(scoped.GetUsers()), 2)
}

func TestStatusSerializesAsTheOneRosterValue(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedRoster(t, pool)
	h := NewHandler(pool)

	resp, err := h.GetAllOrgs(callerCtx(t, "oidc|dee"), connect.NewRequest(&v1.GetAllOrgsRequest{}))
	ok(t, err)

	for _, org := range resp.Msg.GetOrgs() {
		// Not "ORG_STATUS_ACTIVE": status is a plain string carrying the spec's
		// own vocabulary, and a regression to a protobuf enum would show here.
		equals(t, org.GetStatus(), "active")
		if parent := org.GetParent(); parent != nil {
			equals(t, parent.GetType(), "org")
		}
	}
}
