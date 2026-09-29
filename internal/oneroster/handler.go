package oneroster

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1/onerosterv1p2v1connect"
	"github.com/StevenACoffman/roster/internal/db"
	"github.com/StevenACoffman/roster/internal/resilience"
)

// This file is the imperative shell: subject in, query out, response back. Every
// decision — paging arithmetic, wire conversion, cursor encoding — is delegated
// to the pure core in core.go and cursor.go.
//
// Authorization is not performed here. Each query filters on the calling subject
// against auth_effective_access, so an out-of-scope row is never read. A handler
// that filtered results afterwards would put that decision in a place where a
// missed branch leaks data; see sql/queries/rostering.sql.

// Handler serves the RosterService RPCs.
//
// The pool is held alongside the queries because GetRoster opens an explicit
// transaction to read every entity from one snapshot; the other RPCs use a
// single statement and need only queries.
type Handler struct {
	pool    *pgxpool.Pool
	queries *db.Queries

	// resilientDB applies retry and circuit-breaker policies. May be nil, in
	// which case operations run unprotected — which is what makes the handler
	// constructible without them.
	resilientDB *resilience.DB

	// now supplies the write timestamp. A field rather than a direct time.Now()
	// call so a test can pin it: the curation writes compare timestamps for
	// optimistic concurrency, and a test that could not control the clock would
	// have to assert on whatever the machine happened to report.
	now func() time.Time
}

var _ onerosterv1p2v1connect.RosterServiceHandler = (*Handler)(nil)

// NewHandler builds a Handler over the given pool.
//
// A nil pool is accepted so the routing table can be constructed without a
// database — which is what makes the route wiring testable without a container.
// Every RPC then answers Unavailable rather than panicking, enforced by caller
// below.
func NewHandler(pool *pgxpool.Pool) *Handler {
	h := &Handler{now: time.Now}
	if pool != nil {
		h.pool = pool
		h.queries = db.New(pool)
	}
	return h
}

// WithResilience returns a copy of h whose database calls run under the given
// policies.
//
// A copy rather than a mutation, so the handler stays safe to share: the routing
// table holds one and every request reads it concurrently.
func (h *Handler) WithResilience(policies *resilience.DB) *Handler {
	clone := *h
	clone.resilientDB = policies
	return &clone
}

// caller resolves the authenticated subject, refusing an anonymous request.
//
// The refusal is explicit rather than implicit: an empty subject matches no row
// in auth_effective_access, so an anonymous caller would otherwise receive an
// empty roster and be unable to tell that from a real one.
func (h *Handler) caller(ctx context.Context) (string, error) {
	if h.queries == nil {
		return "", errNoDatabase
	}
	subject, ok := SubjectFromContext(ctx)
	if !ok {
		return "", errUnauthenticated
	}
	return subject, nil
}

// listPage runs one scoped, keyset-paged list query and converts the result.
//
// The three function parameters are what differ between the sixteen list RPCs;
// everything else — resolving the caller, sizing the page, trimming the sentinel
// row, encoding the next token, translating errors — is identical and lives
// here rather than being repeated at each call site.
//
// query receives the resolved subject, the keyset cursor to resume after, and a
// limit one larger than the requested page size.
func listPage[Row, Msg any](
	ctx context.Context,
	h *Handler,
	op string,
	pageSize int32,
	pageToken string,
	query func(ctx context.Context, subject, after string, limit int32) ([]Row, error),
	sourcedID func(Row) string,
	convert func(Row) Msg,
) (page []Msg, nextPageToken string, err error) {
	subject, err := h.caller(ctx)
	if err != nil {
		return nil, "", translate(ctx, op, err)
	}

	limit, after, err := pageBounds(pageSize, pageToken)
	if err != nil {
		return nil, "", translate(ctx, op, err)
	}

	rows, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) ([]Row, error) {
		return query(c, subject, after, limit)
	})
	if err != nil {
		return nil, "", translate(ctx, op, err)
	}

	rowPage, nextToken := paginate(rows, limit, sourcedID)

	// Non-nil even when empty: a nil repeated field and an empty one are the same
	// on the wire, but the JSON rendering differs, and a client parsing `null`
	// where it expected `[]` is a bug this need not create.
	msgs := make([]Msg, 0, len(rowPage))
	for _, row := range rowPage {
		msgs = append(msgs, convert(row))
	}
	return msgs, nextToken, nil
}

// getOne runs one scoped single-entity query and converts the result.
//
// A row the caller may not see and a row that does not exist both arrive here as
// pgx.ErrNoRows and both leave as NotFound — see translate.
func getOne[Row, Msg any](
	ctx context.Context,
	h *Handler,
	op string,
	sourcedID string,
	query func(ctx context.Context, subject, id string) (Row, error),
	convert func(Row) Msg,
) (Msg, error) {
	var zero Msg

	subject, err := h.caller(ctx)
	if err != nil {
		return zero, translate(ctx, op, err)
	}

	id, err := requireSourcedID(sourcedID)
	if err != nil {
		return zero, translate(ctx, op, err)
	}

	row, err := resilience.Read(ctx, h.resilientDB, func(c context.Context) (Row, error) {
		return query(c, subject, id)
	})
	if err != nil {
		return zero, translate(ctx, op, err)
	}
	return convert(row), nil
}

// ── Org ───────────────────────────────────────────────────────────────────────

func (h *Handler) GetAllOrgs(
	ctx context.Context, req *connect.Request[v1.GetAllOrgsRequest],
) (*connect.Response[v1.GetAllOrgsResponse], error) {
	orgs, next, err := listPage(ctx, h, "Handler.GetAllOrgs",
		req.Msg.GetPageSize(), req.Msg.GetPageToken(),
		func(ctx context.Context, subject, after string, limit int32) ([]db.Org, error) {
			return h.queries.ListOrgsForSubject(ctx, db.ListOrgsForSubjectParams{
				Subject: subject, AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.Org) string { return row.SourcedID },
		toProtoOrg,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllOrgsResponse{Orgs: orgs, NextPageToken: next}), nil
}

func (h *Handler) GetAllSchools(
	ctx context.Context, req *connect.Request[v1.GetAllSchoolsRequest],
) (*connect.Response[v1.GetAllSchoolsResponse], error) {
	orgs, next, err := listPage(ctx, h, "Handler.GetAllSchools",
		req.Msg.GetPageSize(), req.Msg.GetPageToken(),
		func(ctx context.Context, subject, after string, limit int32) ([]db.Org, error) {
			return h.queries.ListSchoolsForSubject(ctx, db.ListSchoolsForSubjectParams{
				Subject: subject, AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.Org) string { return row.SourcedID },
		toProtoOrg,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllSchoolsResponse{Orgs: orgs, NextPageToken: next}), nil
}

func (h *Handler) GetOrg(
	ctx context.Context, req *connect.Request[v1.GetOrgRequest],
) (*connect.Response[v1.GetOrgResponse], error) {
	org, err := getOne(ctx, h, "Handler.GetOrg", req.Msg.GetSourcedId(),
		func(ctx context.Context, subject, id string) (db.Org, error) {
			return h.queries.GetOrgForSubject(ctx, db.GetOrgForSubjectParams{
				Subject: subject, SourcedID: id,
			})
		},
		toProtoOrg,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetOrgResponse{Org: org}), nil
}

// ── AcademicSession ───────────────────────────────────────────────────────────

// listAcademicSessions backs three RPCs that differ only by type filter: an
// empty sessionType lists every type, matching the predicate that treats an
// empty filter as "every type".
func (h *Handler) listAcademicSessions(
	ctx context.Context, op, sessionType string, pageSize int32, pageToken string,
) ([]*v1.AcademicSession, string, error) {
	return listPage(ctx, h, op, pageSize, pageToken,
		func(ctx context.Context, subject, after string, limit int32) ([]db.AcademicSession, error) {
			return h.queries.ListAcademicSessionsForSubject(ctx, db.ListAcademicSessionsForSubjectParams{
				Subject: subject, SessionType: sessionType,
				AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.AcademicSession) string { return row.SourcedID },
		toProtoAcademicSession,
	)
}

func (h *Handler) GetAllAcademicSessions(
	ctx context.Context, req *connect.Request[v1.GetAllAcademicSessionsRequest],
) (*connect.Response[v1.GetAllAcademicSessionsResponse], error) {
	sessions, next, err := h.listAcademicSessions(ctx, "Handler.GetAllAcademicSessions",
		"", req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllAcademicSessionsResponse{
		AcademicSessions: sessions, NextPageToken: next,
	}), nil
}

func (h *Handler) GetAllTerms(
	ctx context.Context, req *connect.Request[v1.GetAllTermsRequest],
) (*connect.Response[v1.GetAllTermsResponse], error) {
	sessions, next, err := h.listAcademicSessions(ctx, "Handler.GetAllTerms",
		sessionTypeTerm, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllTermsResponse{
		AcademicSessions: sessions, NextPageToken: next,
	}), nil
}

func (h *Handler) GetAllGradingPeriods(
	ctx context.Context, req *connect.Request[v1.GetAllGradingPeriodsRequest],
) (*connect.Response[v1.GetAllGradingPeriodsResponse], error) {
	sessions, next, err := h.listAcademicSessions(ctx, "Handler.GetAllGradingPeriods",
		sessionTypeGradingPeriod, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllGradingPeriodsResponse{
		AcademicSessions: sessions, NextPageToken: next,
	}), nil
}

func (h *Handler) GetAcademicSession(
	ctx context.Context, req *connect.Request[v1.GetAcademicSessionRequest],
) (*connect.Response[v1.GetAcademicSessionResponse], error) {
	session, err := getOne(ctx, h, "Handler.GetAcademicSession", req.Msg.GetSourcedId(),
		func(ctx context.Context, subject, id string) (db.AcademicSession, error) {
			return h.queries.GetAcademicSessionForSubject(ctx, db.GetAcademicSessionForSubjectParams{
				Subject: subject, SourcedID: id,
			})
		},
		toProtoAcademicSession,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAcademicSessionResponse{AcademicSession: session}), nil
}

// ── Course ────────────────────────────────────────────────────────────────────

func (h *Handler) GetAllCourses(
	ctx context.Context, req *connect.Request[v1.GetAllCoursesRequest],
) (*connect.Response[v1.GetAllCoursesResponse], error) {
	courses, next, err := listPage(ctx, h, "Handler.GetAllCourses",
		req.Msg.GetPageSize(), req.Msg.GetPageToken(),
		func(ctx context.Context, subject, after string, limit int32) ([]db.Course, error) {
			return h.queries.ListCoursesForSubject(ctx, db.ListCoursesForSubjectParams{
				Subject: subject, OrgSourcedID: req.Msg.GetOrgSourcedId(),
				AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.Course) string { return row.SourcedID },
		toProtoCourse,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllCoursesResponse{Courses: courses, NextPageToken: next}), nil
}

func (h *Handler) GetCourse(
	ctx context.Context, req *connect.Request[v1.GetCourseRequest],
) (*connect.Response[v1.GetCourseResponse], error) {
	course, err := getOne(ctx, h, "Handler.GetCourse", req.Msg.GetSourcedId(),
		func(ctx context.Context, subject, id string) (db.Course, error) {
			return h.queries.GetCourseForSubject(ctx, db.GetCourseForSubjectParams{
				Subject: subject, SourcedID: id,
			})
		},
		toProtoCourse,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetCourseResponse{Course: course}), nil
}

// ── Class ─────────────────────────────────────────────────────────────────────

func (h *Handler) GetAllClasses(
	ctx context.Context, req *connect.Request[v1.GetAllClassesRequest],
) (*connect.Response[v1.GetAllClassesResponse], error) {
	classes, next, err := listPage(ctx, h, "Handler.GetAllClasses",
		req.Msg.GetPageSize(), req.Msg.GetPageToken(),
		func(ctx context.Context, subject, after string, limit int32) ([]db.Class, error) {
			return h.queries.ListClassesForSubject(ctx, db.ListClassesForSubjectParams{
				Subject: subject, OrgSourcedID: req.Msg.GetOrgSourcedId(),
				AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.Class) string { return row.SourcedID },
		toProtoClass,
	)
	if err != nil {
		return nil, err
	}
	if err := h.attachClassTerms(ctx, classes); err != nil {
		return nil, translate(ctx, "Handler.GetAllClasses", err)
	}
	return connect.NewResponse(&v1.GetAllClassesResponse{Classes: classes, NextPageToken: next}), nil
}

func (h *Handler) GetClass(
	ctx context.Context, req *connect.Request[v1.GetClassRequest],
) (*connect.Response[v1.GetClassResponse], error) {
	class, err := getOne(ctx, h, "Handler.GetClass", req.Msg.GetSourcedId(),
		func(ctx context.Context, subject, id string) (db.Class, error) {
			return h.queries.GetClassForSubject(ctx, db.GetClassForSubjectParams{
				Subject: subject, SourcedID: id,
			})
		},
		toProtoClass,
	)
	if err != nil {
		return nil, err
	}
	if err := h.attachClassTerms(ctx, []*v1.Class{class}); err != nil {
		return nil, translate(ctx, "Handler.GetClass", err)
	}
	return connect.NewResponse(&v1.GetClassResponse{Class: class}), nil
}

// ── User ──────────────────────────────────────────────────────────────────────

// listUsers backs three RPCs that differ only by role filter; an empty role
// lists every user, matching the predicate that treats an
// empty filter as "every type".
func (h *Handler) listUsers(
	ctx context.Context, op, role, orgSourcedID string, pageSize int32, pageToken string,
) ([]*v1.User, string, error) {
	users, next, err := listPage(ctx, h, op, pageSize, pageToken,
		func(ctx context.Context, subject, after string, limit int32) ([]db.OnerosterUser, error) {
			return h.queries.ListUsersForSubject(ctx, db.ListUsersForSubjectParams{
				Subject: subject, Role: role, OrgSourcedID: orgSourcedID,
				AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.OnerosterUser) string { return row.SourcedID },
		toProtoUser,
	)
	if err != nil {
		return nil, "", err
	}
	if err := h.attachUserAssociations(ctx, users); err != nil {
		return nil, "", translate(ctx, op, err)
	}
	return users, next, nil
}

func (h *Handler) GetAllUsers(
	ctx context.Context, req *connect.Request[v1.GetAllUsersRequest],
) (*connect.Response[v1.GetAllUsersResponse], error) {
	users, next, err := h.listUsers(ctx, "Handler.GetAllUsers", "",
		req.Msg.GetOrgSourcedId(), req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllUsersResponse{Users: users, NextPageToken: next}), nil
}

func (h *Handler) GetAllStudents(
	ctx context.Context, req *connect.Request[v1.GetAllStudentsRequest],
) (*connect.Response[v1.GetAllStudentsResponse], error) {
	users, next, err := h.listUsers(ctx, "Handler.GetAllStudents", roleStudent,
		req.Msg.GetOrgSourcedId(), req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllStudentsResponse{Users: users, NextPageToken: next}), nil
}

func (h *Handler) GetAllTeachers(
	ctx context.Context, req *connect.Request[v1.GetAllTeachersRequest],
) (*connect.Response[v1.GetAllTeachersResponse], error) {
	users, next, err := h.listUsers(ctx, "Handler.GetAllTeachers", roleTeacher,
		req.Msg.GetOrgSourcedId(), req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllTeachersResponse{Users: users, NextPageToken: next}), nil
}

func (h *Handler) GetUser(
	ctx context.Context, req *connect.Request[v1.GetUserRequest],
) (*connect.Response[v1.GetUserResponse], error) {
	user, err := getOne(ctx, h, "Handler.GetUser", req.Msg.GetSourcedId(),
		func(ctx context.Context, subject, id string) (db.OnerosterUser, error) {
			return h.queries.GetUserForSubject(ctx, db.GetUserForSubjectParams{
				Subject: subject, SourcedID: id,
			})
		},
		toProtoUser,
	)
	if err != nil {
		return nil, err
	}
	if err := h.attachUserAssociations(ctx, []*v1.User{user}); err != nil {
		return nil, translate(ctx, "Handler.GetUser", err)
	}
	return connect.NewResponse(&v1.GetUserResponse{User: user}), nil
}

// ── Enrollment ────────────────────────────────────────────────────────────────

func (h *Handler) GetAllEnrollments(
	ctx context.Context, req *connect.Request[v1.GetAllEnrollmentsRequest],
) (*connect.Response[v1.GetAllEnrollmentsResponse], error) {
	enrollments, next, err := listPage(ctx, h, "Handler.GetAllEnrollments",
		req.Msg.GetPageSize(), req.Msg.GetPageToken(),
		func(ctx context.Context, subject, after string, limit int32) ([]db.Enrollment, error) {
			return h.queries.ListEnrollmentsForSubject(ctx, db.ListEnrollmentsForSubjectParams{
				Subject: subject, OrgSourcedID: req.Msg.GetOrgSourcedId(),
				AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.Enrollment) string { return row.SourcedID },
		toProtoEnrollment,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllEnrollmentsResponse{
		Enrollments: enrollments, NextPageToken: next,
	}), nil
}

func (h *Handler) GetEnrollment(
	ctx context.Context, req *connect.Request[v1.GetEnrollmentRequest],
) (*connect.Response[v1.GetEnrollmentResponse], error) {
	enrollment, err := getOne(ctx, h, "Handler.GetEnrollment", req.Msg.GetSourcedId(),
		func(ctx context.Context, subject, id string) (db.Enrollment, error) {
			return h.queries.GetEnrollmentForSubject(ctx, db.GetEnrollmentForSubjectParams{
				Subject: subject, SourcedID: id,
			})
		},
		toProtoEnrollment,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetEnrollmentResponse{Enrollment: enrollment}), nil
}

// ── Demographics ──────────────────────────────────────────────────────────────

func (h *Handler) GetAllDemographics(
	ctx context.Context, req *connect.Request[v1.GetAllDemographicsRequest],
) (*connect.Response[v1.GetAllDemographicsResponse], error) {
	demographics, next, err := listPage(ctx, h, "Handler.GetAllDemographics",
		req.Msg.GetPageSize(), req.Msg.GetPageToken(),
		func(ctx context.Context, subject, after string, limit int32) ([]db.Demographic, error) {
			return h.queries.ListDemographicsForSubject(ctx, db.ListDemographicsForSubjectParams{
				Subject: subject, OrgSourcedID: req.Msg.GetOrgSourcedId(),
				AfterSourcedID: after, PageSize: limit,
			})
		},
		func(row db.Demographic) string { return row.SourcedID },
		toProtoDemographics,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetAllDemographicsResponse{
		Demographics: demographics, NextPageToken: next,
	}), nil
}

func (h *Handler) GetDemographics(
	ctx context.Context, req *connect.Request[v1.GetDemographicsRequest],
) (*connect.Response[v1.GetDemographicsResponse], error) {
	demographics, err := getOne(ctx, h, "Handler.GetDemographics", req.Msg.GetSourcedId(),
		func(ctx context.Context, subject, id string) (db.Demographic, error) {
			return h.queries.GetDemographicsForSubject(ctx, db.GetDemographicsForSubjectParams{
				Subject: subject, SourcedID: id,
			})
		},
		toProtoDemographics,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetDemographicsResponse{Demographics: demographics}), nil
}
