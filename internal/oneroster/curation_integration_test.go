//go:build integration

package oneroster

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/internal/db"
	"github.com/StevenACoffman/roster/internal/testutil"
)

// Curation against a real database. The behaviour under test is in the guarded
// statements, so a fake store would only confirm the fake.

// curatingHandler returns a handler whose clock is pinned, so the
// optimistic-concurrency assertions compare against a value the test chose
// rather than whatever the machine reported.
func curatingHandler(t *testing.T, pool *pgxpool.Pool, at time.Time) *Handler {
	t.Helper()

	h := NewHandler(pool)
	h.now = func() time.Time { return at }
	return h
}

// seedCurators creates a global curator, a school-scoped curator, and a
// read-only principal, over a district with two schools.
func seedCurators(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := t.Context()
	q := db.New(pool)
	now := writeTimestamp(time.Now())

	grant := func(subject, role, org string) {
		p, err := q.UpsertPrincipal(ctx, db.UpsertPrincipalParams{Subject: subject, Kind: "person"})
		ok(t, err)
		scope := pgText(org)
		_, err = q.GrantRole(ctx, db.GrantRoleParams{
			PrincipalID: p.ID, RoleName: role, OrgSourcedID: scope,
		})
		ok(t, err)
	}

	grant("global", "roster_admin", "")

	// The district is created through the guarded path by the global curator, so
	// the bootstrap case is exercised rather than sidestepped with a raw insert.
	_, err := q.CreateOrgGuarded(ctx, db.CreateOrgGuardedParams{
		SourcedID: "d-1", Status: "active", DateLastModified: now,
		Name: "Springfield District", Type: "district", Identifier: "D1",
		Subject: "global",
	})
	ok(t, err)

	for _, school := range []string{"sch-1", "sch-2"} {
		_, err = q.CreateOrgGuarded(ctx, db.CreateOrgGuardedParams{
			SourcedID: school, Status: "active", DateLastModified: now,
			Name: school, Type: "school", Identifier: school,
			ParentSourcedID: pgText("d-1"), Subject: "global",
		})
		ok(t, err)
	}

	grant("ana", "school_admin", "sch-2")
	grant("reader", "roster_reader", "d-1")
}

func TestCreateOrgBootstrapsAndThenScopes(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())

	// A school curator may create beneath their own school.
	_, err := h.CreateOrg(callerCtx(t, "ana"), connect.NewRequest(&v1.CreateOrgRequest{
		Org: &v1.Org{
			SourcedId: "dept-1", Status: "active", Name: "Maths Department",
			Type: "department", Identifier: "M1",
			Parent: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
		},
	}))
	ok(t, err)

	// But not beneath a school they do not hold.
	_, err = h.CreateOrg(callerCtx(t, "ana"), connect.NewRequest(&v1.CreateOrgRequest{
		Org: &v1.Org{
			SourcedId: "dept-2", Status: "active", Name: "Elsewhere",
			Type: "department", Identifier: "M2",
			Parent: &v1.OrgGUIDRef{SourcedId: "sch-1", Href: "/x", Type: "org"},
		},
	}))
	wantCode(t, err, connect.CodeNotFound)
}

func TestReadOnlyGrantCannotCurate(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "reader")

	// roster_reader can read.
	listed, err := h.GetAllOrgs(ctx, connect.NewRequest(&v1.GetAllOrgsRequest{}))
	ok(t, err)
	if len(listed.Msg.GetOrgs()) == 0 {
		t.Fatal("a reader saw no orgs; the fixture is wrong")
	}

	// And must not write, on any of the three verbs.
	_, err = h.CreateOrg(ctx, connect.NewRequest(&v1.CreateOrgRequest{
		Org: &v1.Org{
			SourcedId: "nope", Status: "active", Name: "Nope", Type: "school",
			Identifier: "N", Parent: &v1.OrgGUIDRef{SourcedId: "d-1", Href: "/x", Type: "org"},
		},
	}))
	wantCode(t, err, connect.CodeNotFound)

	_, err = h.UpdateOrg(ctx, connect.NewRequest(&v1.UpdateOrgRequest{
		Org:                      &v1.Org{SourcedId: "sch-2", Name: "Renamed"},
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		ExpectedDateLastModified: timestamppb.Now(),
	}))
	wantCode(t, err, connect.CodeNotFound)

	_, err = h.DeleteOrg(ctx, connect.NewRequest(&v1.DeleteOrgRequest{SourcedId: "sch-2"}))
	wantCode(t, err, connect.CodeNotFound)
}

func TestUpdateAppliesOnlyTheMaskedFields(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	at := time.Now()
	h := curatingHandler(t, pool, at)
	ctx := callerCtx(t, "ana")

	before, err := h.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)
	stored := before.Msg.GetOrg()

	updated, err := h.UpdateOrg(ctx, connect.NewRequest(&v1.UpdateOrgRequest{
		Org: &v1.Org{
			SourcedId:  "sch-2",
			Name:       "Shelbyville High",
			Identifier: "SHOULD-NOT-APPLY",
			Type:       "department",
		},
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		ExpectedDateLastModified: stored.GetDateLastModified(),
	}))
	ok(t, err)

	got := updated.Msg.GetOrg()
	equals(t, got.GetName(), "Shelbyville High")
	// Not in the mask, so both must retain their stored values.
	equals(t, got.GetIdentifier(), stored.GetIdentifier())
	equals(t, got.GetType(), stored.GetType())
}

func TestUpdateRejectsAnAbsentOrUnusableMask(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "ana")

	current, err := h.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)
	version := current.Msg.GetOrg().GetDateLastModified()

	tests := []struct {
		name string
		mask *fieldmaskpb.FieldMask
	}{
		{name: "nil mask is not replace-everything", mask: nil},
		{name: "empty mask", mask: &fieldmaskpb.FieldMask{}},
		{name: "sourced_id is not editable", mask: &fieldmaskpb.FieldMask{Paths: []string{"sourced_id"}}},
		{name: "date_last_modified is the server's", mask: &fieldmaskpb.FieldMask{Paths: []string{"date_last_modified"}}},
		{name: "unknown field", mask: &fieldmaskpb.FieldMask{Paths: []string{"nonsense"}}},
		{name: "nested path", mask: &fieldmaskpb.FieldMask{Paths: []string{"parent.sourced_id"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := h.UpdateOrg(ctx, connect.NewRequest(&v1.UpdateOrgRequest{
				Org:                      &v1.Org{SourcedId: "sch-2", Name: "X"},
				UpdateMask:               tt.mask,
				ExpectedDateLastModified: version,
			}))
			wantCode(t, err, connect.CodeInvalidArgument)
		})
	}
}

// TestStaleUpdateIsAbortedNotNotFound is the whole point of the concurrency
// token: a curator who lost a race must be told to reload, not told the record
// is gone.
func TestStaleUpdateIsAbortedNotNotFound(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	ctx := callerCtx(t, "ana")

	first := time.Now().Truncate(time.Microsecond)
	h := curatingHandler(t, pool, first)

	current, err := h.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)
	staleVersion := current.Msg.GetOrg().GetDateLastModified()

	// Someone else writes first, moving date_last_modified.
	h.now = func() time.Time { return first.Add(time.Minute) }
	_, err = h.UpdateOrg(ctx, connect.NewRequest(&v1.UpdateOrgRequest{
		Org:                      &v1.Org{SourcedId: "sch-2", Name: "Winner"},
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		ExpectedDateLastModified: staleVersion,
	}))
	ok(t, err)

	// Our curator now writes with the version they read before that.
	_, err = h.UpdateOrg(ctx, connect.NewRequest(&v1.UpdateOrgRequest{
		Org:                      &v1.Org{SourcedId: "sch-2", Name: "Loser"},
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		ExpectedDateLastModified: staleVersion,
	}))
	wantCode(t, err, connect.CodeAborted)

	// And the winner's value survived.
	after, err := h.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)
	equals(t, after.Msg.GetOrg().GetName(), "Winner")
}

func TestCreateTwiceIsAlreadyExists(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "global")

	org := &v1.Org{
		SourcedId: "sch-3", Status: "active", Name: "New School", Type: "school",
		Identifier: "S3", Parent: &v1.OrgGUIDRef{SourcedId: "d-1", Href: "/x", Type: "org"},
	}

	_, err := h.CreateOrg(ctx, connect.NewRequest(&v1.CreateOrgRequest{Org: org}))
	ok(t, err)

	// Creating over an existing record is refused rather than silently merged:
	// a curator who thought they were adding something new should hear about it.
	_, err = h.CreateOrg(ctx, connect.NewRequest(&v1.CreateOrgRequest{Org: org}))
	wantCode(t, err, connect.CodeAlreadyExists)
}

func TestDeleteIsSoftAndIdempotent(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "ana")

	_, err := h.DeleteOrg(ctx, connect.NewRequest(&v1.DeleteOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)

	// Gone from listings.
	listed, err := h.GetAllOrgs(callerCtx(t, "global"), connect.NewRequest(&v1.GetAllOrgsRequest{}))
	ok(t, err)
	for _, org := range listed.Msg.GetOrgs() {
		if org.GetSourcedId() == "sch-2" {
			t.Error("a deleted org is still listed")
		}
	}

	// Deleting again still succeeds: the row is still there, still matches the
	// guard, and is simply flagged again.
	_, err = h.DeleteOrg(ctx, connect.NewRequest(&v1.DeleteOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)
}

// TestCurateAClassAndEnrollment walks the workflow a curator actually performs:
// create a course, a class in it with its term, a user with their role, and
// then enrol them.
//
// An earlier version of this test created the class with no terms and the user
// with no roles, and passed — which is how the schema's minItems:1 requirement
// went unnoticed until the JSON Schemas were checked against the DDL.
func TestCurateAClassAndEnrollment(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	seedCourseAndTerm(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "ana")

	_, err := h.CreateClass(ctx, connect.NewRequest(&v1.CreateClassRequest{
		Class: &v1.Class{
			SourcedId: "cl-1", Status: "active", Title: "Algebra I - P3",
			Course: &v1.CourseGUIDRef{SourcedId: "c-1", Href: "/x", Type: "course"},
			School: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
			Terms: []*v1.AcadSessionGUIDRef{
				{SourcedId: "term-1", Href: "/terms/term-1", Type: "academicSession"},
			},
		},
	}))
	ok(t, err)

	_, err = h.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{
		User: &v1.User{
			SourcedId: "u-1", Status: "active", EnabledUser: true,
			GivenName: "Bo", FamilyName: "Chen",
			PrimaryOrg: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
			Roles: []*v1.Role{{
				RoleType: "primary", Role: "student",
				Org: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
			}},
		},
	}))
	ok(t, err)

	_, err = h.CreateEnrollment(ctx, connect.NewRequest(&v1.CreateEnrollmentRequest{
		Enrollment: &v1.Enrollment{
			SourcedId: "e-1", Status: "active", Role: "student",
			User:   &v1.UserGUIDRef{SourcedId: "u-1", Href: "/x", Type: "user"},
			Class:  &v1.ClassGUIDRef{SourcedId: "cl-1", Href: "/x", Type: "class"},
			School: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
		},
	}))
	ok(t, err)

	enrollments, err := h.GetAllEnrollments(ctx, connect.NewRequest(&v1.GetAllEnrollmentsRequest{}))
	ok(t, err)
	equals(t, len(enrollments.Msg.GetEnrollments()), 1)
	equals(t, enrollments.Msg.GetEnrollments()[0].GetRole(), "student")

	// Everything the curator created is a valid OneRoster entity: the class has
	// its required term, the user their required role.
	class, err := h.GetClass(ctx, connect.NewRequest(&v1.GetClassRequest{SourcedId: "cl-1"}))
	ok(t, err)
	equals(t, len(class.Msg.GetClass().GetTerms()), 1)

	user, err := h.GetUser(ctx, connect.NewRequest(&v1.GetUserRequest{SourcedId: "u-1"}))
	ok(t, err)
	equals(t, len(user.Msg.GetUser().GetRoles()), 1)
}

// ── Required repeated fields ──────────────────────────────────────────────────
//
// ClassDType.terms and UserDType.roles are `required` with `minItems: 1` in the
// OneRoster JSON Schema. A junction table cannot enforce that, and an earlier
// version of the curation path wrote neither — producing classes with no terms
// and, worse, users invisible to every query because visibility is scoped
// through their roles.

func TestCreateClassRequiresTerms(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	seedCourseAndTerm(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "ana")

	// No terms: refused.
	_, err := h.CreateClass(ctx, connect.NewRequest(&v1.CreateClassRequest{
		Class: &v1.Class{
			SourcedId: "cl-none", Status: "active", Title: "No Terms",
			Course: &v1.CourseGUIDRef{SourcedId: "c-1", Href: "/x", Type: "course"},
			School: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
		},
	}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// With a term: stored, and the term comes back on a subsequent read.
	_, err = h.CreateClass(ctx, connect.NewRequest(&v1.CreateClassRequest{
		Class: &v1.Class{
			SourcedId: "cl-ok", Status: "active", Title: "With Terms",
			Course: &v1.CourseGUIDRef{SourcedId: "c-1", Href: "/x", Type: "course"},
			School: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
			Terms: []*v1.AcadSessionGUIDRef{
				{SourcedId: "term-1", Href: "/terms/term-1", Type: "academicSession"},
			},
		},
	}))
	ok(t, err)

	got, err := h.GetClass(ctx, connect.NewRequest(&v1.GetClassRequest{SourcedId: "cl-ok"}))
	ok(t, err)
	equals(t, len(got.Msg.GetClass().GetTerms()), 1)
	equals(t, got.Msg.GetClass().GetTerms()[0].GetSourcedId(), "term-1")
}

// TestCreateUserRequiresRolesAndIsThenReachable is the regression test for the
// worst defect the schema verification found: a user created without roles was
// stored and then invisible to every list, unfetchable by id, and impossible to
// update or delete.
func TestCreateUserRequiresRolesAndIsThenReachable(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "ana")

	// No roles: refused rather than stored into an unreachable state.
	_, err := h.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{
		User: &v1.User{
			SourcedId: "u-noroles", Status: "active", EnabledUser: true,
			GivenName: "Nina", FamilyName: "Novak",
		},
	}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// Two primary roles: also refused, matching the partial unique index.
	twice := []*v1.Role{
		{RoleType: "primary", Role: "student", Org: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"}},
		{RoleType: "primary", Role: "aide", Org: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"}},
	}
	_, err = h.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{
		User: &v1.User{
			SourcedId: "u-twoprimary", Status: "active", EnabledUser: true,
			GivenName: "Two", FamilyName: "Primary", Roles: twice,
		},
	}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// With one primary role: stored AND reachable.
	_, err = h.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{
		User: &v1.User{
			SourcedId: "u-ok", Status: "active", EnabledUser: true,
			GivenName: "Nina", FamilyName: "Novak",
			Roles: []*v1.Role{{
				RoleType: "primary", Role: "student",
				Org: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"},
			}},
		},
	}))
	ok(t, err)

	fetched, err := h.GetUser(ctx, connect.NewRequest(&v1.GetUserRequest{SourcedId: "u-ok"}))
	ok(t, err)
	equals(t, len(fetched.Msg.GetUser().GetRoles()), 1)
	equals(t, fetched.Msg.GetUser().GetRoles()[0].GetRole(), "student")

	listed, err := h.GetAllUsers(ctx, connect.NewRequest(&v1.GetAllUsersRequest{}))
	ok(t, err)
	seen := false
	for _, u := range listed.Msg.GetUsers() {
		if u.GetSourcedId() == "u-ok" {
			seen = true
		}
	}
	if !seen {
		t.Error("the created user is not visible to GetAllUsers; it was created unreachable")
	}
}

// TestCreateUserRollsBackWhenARoleFails proves the transaction: a user must not
// survive without the roles that make them reachable.
func TestCreateUserRollsBackWhenARoleFails(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())
	ctx := callerCtx(t, "global")

	// The second role names an org that does not exist, so its insert violates
	// the foreign key after the user row has already been written.
	_, err := h.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{
		User: &v1.User{
			SourcedId: "u-rollback", Status: "active", EnabledUser: true,
			GivenName: "Roll", FamilyName: "Back",
			Roles: []*v1.Role{
				{RoleType: "primary", Role: "student",
					Org: &v1.OrgGUIDRef{SourcedId: "sch-2", Href: "/x", Type: "org"}},
				{RoleType: "secondary", Role: "aide",
					Org: &v1.OrgGUIDRef{SourcedId: "no-such-org", Href: "/x", Type: "org"}},
			},
		},
	}))
	if err == nil {
		t.Fatal("a role referencing a nonexistent org was accepted")
	}

	// The user row must have gone with it.
	_, getErr := h.GetUser(callerCtx(t, "global"),
		connect.NewRequest(&v1.GetUserRequest{SourcedId: "u-rollback"}))
	wantCode(t, getErr, connect.CodeNotFound)
}

// ── Academic session curation ─────────────────────────────────────────────────

func TestAcademicSessionCurationRequiresGlobalRosterAdmin(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())

	session := &v1.AcademicSession{
		SourcedId: "ay-2027", Status: "active", Title: "2026-2027",
		Type: "schoolYear", SchoolYear: "2027",
		StartDate: &date.Date{Year: 2026, Month: 8, Day: 1},
		EndDate:   &date.Date{Year: 2027, Month: 6, Day: 15},
	}

	// A district- or school-scoped curator cannot: a school year is not theirs
	// to own, and there is no org for a narrower grant to be scoped against.
	for _, subject := range []string{"ana", "reader"} {
		_, err := h.CreateAcademicSession(callerCtx(t, subject),
			connect.NewRequest(&v1.CreateAcademicSessionRequest{AcademicSession: session}))
		wantCode(t, err, connect.CodePermissionDenied)
	}

	// The global roster_admin can.
	created, err := h.CreateAcademicSession(callerCtx(t, "global"),
		connect.NewRequest(&v1.CreateAcademicSessionRequest{AcademicSession: session}))
	ok(t, err)
	equals(t, created.Msg.GetAcademicSession().GetSourcedId(), "ay-2027")
	equals(t, created.Msg.GetAcademicSession().GetType(), "schoolYear")

	// And every principal with any grant can still read it: sessions are not
	// org-scoped on the read path either.
	for _, subject := range []string{"global", "ana", "reader"} {
		got, readErr := h.GetAcademicSession(callerCtx(t, subject),
			connect.NewRequest(&v1.GetAcademicSessionRequest{SourcedId: "ay-2027"}))
		ok(t, readErr)
		equals(t, got.Msg.GetAcademicSession().GetTitle(), "2026-2027")
	}
}

func TestAcademicSessionUpdateAndDelete(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	at := time.Now()
	h := curatingHandler(t, pool, at)
	ctx := callerCtx(t, "global")

	created, err := h.CreateAcademicSession(ctx, connect.NewRequest(&v1.CreateAcademicSessionRequest{
		AcademicSession: &v1.AcademicSession{
			SourcedId: "term-x", Status: "active", Title: "Autumn",
			Type: "term", SchoolYear: "2027",
			StartDate: &date.Date{Year: 2026, Month: 9, Day: 1},
			EndDate:   &date.Date{Year: 2026, Month: 12, Day: 20},
		},
	}))
	ok(t, err)
	version := created.Msg.GetAcademicSession().GetDateLastModified()

	// A scoped curator cannot update it either.
	_, err = h.UpdateAcademicSession(callerCtx(t, "ana"), connect.NewRequest(&v1.UpdateAcademicSessionRequest{
		AcademicSession:          &v1.AcademicSession{SourcedId: "term-x", Title: "Hijacked"},
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		ExpectedDateLastModified: version,
	}))
	wantCode(t, err, connect.CodePermissionDenied)

	h.now = func() time.Time { return at.Add(time.Minute) }
	updated, err := h.UpdateAcademicSession(ctx, connect.NewRequest(&v1.UpdateAcademicSessionRequest{
		AcademicSession:          &v1.AcademicSession{SourcedId: "term-x", Title: "Fall Term"},
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		ExpectedDateLastModified: version,
	}))
	ok(t, err)
	equals(t, updated.Msg.GetAcademicSession().GetTitle(), "Fall Term")
	// Not in the mask, so the dates are untouched.
	equals(t, updated.Msg.GetAcademicSession().GetEndDate().GetMonth(), int32(12))

	// A stale version is aborted, not not_found.
	_, err = h.UpdateAcademicSession(ctx, connect.NewRequest(&v1.UpdateAcademicSessionRequest{
		AcademicSession:          &v1.AcademicSession{SourcedId: "term-x", Title: "Loser"},
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		ExpectedDateLastModified: version,
	}))
	wantCode(t, err, connect.CodeAborted)

	// Soft delete, then idempotent.
	_, err = h.DeleteAcademicSession(ctx, connect.NewRequest(&v1.DeleteAcademicSessionRequest{SourcedId: "term-x"}))
	ok(t, err)
	_, err = h.DeleteAcademicSession(ctx, connect.NewRequest(&v1.DeleteAcademicSessionRequest{SourcedId: "term-x"}))
	ok(t, err)
}

func TestAcademicSessionRejectsInvertedDates(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	seedCurators(t, pool)
	h := curatingHandler(t, pool, time.Now())

	_, err := h.CreateAcademicSession(callerCtx(t, "global"),
		connect.NewRequest(&v1.CreateAcademicSessionRequest{
			AcademicSession: &v1.AcademicSession{
				SourcedId: "bad-dates", Status: "active", Title: "Backwards",
				Type: "term", SchoolYear: "2027",
				StartDate: &date.Date{Year: 2027, Month: 6, Day: 15},
				EndDate:   &date.Date{Year: 2026, Month: 8, Day: 1},
			},
		}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

// seedCourseAndTerm adds the course and academic session a class needs.
func seedCourseAndTerm(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := t.Context()
	q := db.New(pool)
	now := writeTimestamp(time.Now())

	_, err := q.UpsertAcademicSession(ctx, db.UpsertAcademicSessionParams{
		SourcedID: "term-1", Status: "active", DateLastModified: now,
		Title:     "Fall Term",
		StartDate: pgtype.Date{Time: time.Date(2025, time.August, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		EndDate:   pgtype.Date{Time: time.Date(2025, time.December, 20, 0, 0, 0, 0, time.UTC), Valid: true},
		Type:      "term", SchoolYear: "2026",
	})
	ok(t, err)

	_, err = q.UpsertCourse(ctx, db.UpsertCourseParams{
		SourcedID: "c-1", Status: "active", DateLastModified: now,
		Title: "Algebra I", CourseCode: "ALG1",
		OrgSourcedID: pgText("sch-2"),
		Grades:       []string{}, Subjects: []string{}, SubjectCodes: []string{},
	})
	ok(t, err)
}
