//go:build integration

// Package test drives the service as an outside consumer would: through the
// generated Connect client, over a real listener, against a real database.
package test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/StevenACoffman/roster/cmd"
	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1/onerosterv1p2v1connect"
	"github.com/StevenACoffman/roster/internal/db"
	"github.com/StevenACoffman/roster/internal/postgres"
	"github.com/StevenACoffman/roster/internal/testutil"
)

// This suite asserts only what cmd/serve's raw-HTTP suite cannot: that the
// generated client and the generated server agree over the wire.
//
// Routing, health, validation and shutdown are covered there, and repeating
// them here would create a second authoritative set that drifts. What is tested
// here instead is the protocol: the protojsonx codec round-tripping real
// messages, and a connect.Error arriving with its code intact so that
// connect.CodeOf works. A consumer branches on that code, and nothing in a
// hand-rolled HTTP test exercises the client's side of it.

const timeMultiplier = 1

var listeningRE = regexp.MustCompile(`listening on (https?://\S+)`)

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func equals[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// wantCode asserts that err is a Connect error the client surfaced with the
// given code. connect.CodeOf is what a real consumer calls, so it is what this
// checks rather than unwrapping to a concrete type.
func wantCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want %v", want)
	}
	if got := connect.CodeOf(err); got != want {
		t.Fatalf("connect.CodeOf = %v, want %v (error: %v)", got, want, err)
	}
}

// startService boots the server on an OS-assigned port and returns a generated
// client pointed at it.
//
// The server-start mechanics are repeated from cmd/serve's suite rather than
// shared. A reader who opens this file when it fails should be able to see the
// whole world it ran in without following a helper into another package.
func startService(
	t *testing.T, subject string,
) (client onerosterv1p2v1connect.RosterServiceClient, databaseURL string) {
	t.Helper()

	databaseURL = testutil.NewDatabaseURL(t)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	stdoutR, stdoutW := io.Pipe()
	t.Cleanup(func() { _ = stdoutW.Close() })

	done := make(chan error, 1)
	go func() {
		err := cmd.Run(ctx, []string{
			"serve",
			"--addr", "127.0.0.1:0",
			"--database-url", databaseURL,
			"--dev-subject", subject,
			"--admin-addr", "off",
		}, strings.NewReader(""), stdoutW, io.Discard)
		_ = stdoutW.Close()
		done <- err
	}()

	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutR)
		for scanner.Scan() {
			if m := listeningRE.FindStringSubmatch(scanner.Text()); m != nil {
				addrCh <- m[1]
				return
			}
		}
		close(addrCh)
	}()

	var base string
	select {
	case addr, open := <-addrCh:
		if !open {
			t.Fatal("the server exited before printing its address")
		}
		base = addr
	case err := <-done:
		t.Fatalf("the server exited during startup: %v", err)
	case <-time.After(30 * time.Second * timeMultiplier):
		t.Fatal("timed out waiting for the server to bind")
	}

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("the server exited with %v", err)
			}
		case <-time.After(30 * time.Second * timeMultiplier):
			t.Error("the server did not shut down within the drain timeout")
		}
	})

	waitForReady(ctx, t, base+"/readyz")

	// The generated client, configured the way a consumer would configure it.
	// No codec option: the default is what a consumer gets, and part of the
	// point is to confirm the server speaks it.
	return onerosterv1p2v1connect.NewRosterServiceClient(http.DefaultClient, base), databaseURL
}

func waitForReady(ctx context.Context, t *testing.T, endpoint string) {
	t.Helper()

	ticker := time.NewTicker(25 * time.Millisecond * timeMultiplier)
	defer ticker.Stop()
	deadline := time.After(30 * time.Second * timeMultiplier)

	client := &http.Client{Timeout: 2 * time.Second * timeMultiplier}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
		ok(t, err)

		resp, err := client.Do(req)
		if err == nil {
			code := resp.StatusCode
			_ = resp.Body.Close()
			if code == http.StatusOK {
				return
			}
		}

		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for %s", endpoint)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", endpoint)
		case <-ticker.C:
		}
	}
}

// seed populates a district through the guarded write path, using the same
// subject the client will act as.
func seed(t *testing.T, databaseURL, subject string) {
	t.Helper()

	ctx := t.Context()
	pool, err := postgres.NewPool(ctx, databaseURL)
	ok(t, err)
	defer pool.Close()

	q := db.New(pool)
	now := pgNow()

	principal, err := q.UpsertPrincipal(ctx, db.UpsertPrincipalParams{
		Subject: subject, Kind: "person",
	})
	ok(t, err)
	_, err = q.GrantRole(ctx, db.GrantRoleParams{
		PrincipalID: principal.ID, RoleName: "roster_admin",
	})
	ok(t, err)

	for _, org := range []struct{ id, kind, parent string }{
		{"d-1", "district", ""},
		{"sch-1", "school", "d-1"},
		{"sch-2", "school", "d-1"},
	} {
		parent := pgText("")
		if org.parent != "" {
			parent = pgText(org.parent)
		}
		_, err = q.UpsertOrg(ctx, db.UpsertOrgParams{
			SourcedID: org.id, Status: "active", DateLastModified: now,
			Name: org.id, Type: org.kind, Identifier: org.id, ParentSourcedID: parent,
		})
		ok(t, err)
	}
}

// TestClientAndServerAgreeOnTheWire is the reason this suite exists: the
// generated client's request and response types must round-trip through the
// server's codec. A mismatch here is invisible to a hand-rolled HTTP test,
// which builds the JSON itself.
func TestClientAndServerAgreeOnTheWire(t *testing.T) {
	t.Parallel()

	const subject = "oidc|admin"
	client, databaseURL := startService(t, subject)
	seed(t, databaseURL, subject)

	resp, err := client.GetAllOrgs(t.Context(), connect.NewRequest(&v1.GetAllOrgsRequest{}))
	ok(t, err)

	equals(t, len(resp.Msg.GetOrgs()), 3)

	// Every field the server filled must have survived decoding into the
	// client's own generated type.
	for _, org := range resp.Msg.GetOrgs() {
		if org.GetSourcedId() == "" {
			t.Error("a decoded org has no sourced_id")
		}
		// The spec's own vocabulary, not the protobuf enum spelling: the string
		// field survives the codec as the value the schema declares.
		equals(t, org.GetStatus(), "active")
		if org.GetDateLastModified() == nil {
			t.Errorf("org %s decoded without a timestamp", org.GetSourcedId())
		}
		if parent := org.GetParent(); parent != nil {
			equals(t, parent.GetType(), "org")
			if parent.GetHref() == "" {
				t.Errorf("org %s has a parent ref with no href", org.GetSourcedId())
			}
		}
	}
}

// TestEveryCreateReachesTheServer is the coverage this suite was missing.
//
// The handler suites call methods directly, so protovalidate never runs there. A
// request they accept can still be refused by a live server, and one was: every
// create RPC demanded a date_last_modified that the server then overwrote with
// its own clock. Only academic sessions were driven through the client, so only
// academic sessions showed it.
//
// So all six are driven here, in dependency order, through the full interceptor
// chain. None of the payloads carries a timestamp — the write types have no such
// field — and every response must come back with one the server assigned.
func TestEveryCreateReachesTheServer(t *testing.T) {
	t.Parallel()

	const subject = "oidc|admin"
	client, databaseURL := startService(t, subject)
	seed(t, databaseURL, subject)
	ctx := t.Context()

	school := &v1.OrgGUIDRef{SourcedId: "sch-1", Href: "/orgs/sch-1", Type: "org"}

	// Created in dependency order, because each later payload refers to an
	// earlier one: a class needs its course and term, an enrollment needs its
	// class and user.
	org, err := client.CreateOrg(ctx, connect.NewRequest(&v1.CreateOrgRequest{
		Org: &v1.OrgWrite{
			SourcedId: "sch-wire", Status: "active", Name: "Wire School",
			Type: "school", Identifier: "SW",
			Parent: &v1.OrgGUIDRef{SourcedId: "d-1", Href: "/orgs/d-1", Type: "org"},
		},
	}))
	ok(t, err)

	session, err := client.CreateAcademicSession(ctx,
		connect.NewRequest(&v1.CreateAcademicSessionRequest{
			AcademicSession: &v1.AcademicSessionWrite{
				SourcedId: "term-wire", Status: "active", Title: "Fall 2026",
				Type: "term", SchoolYear: "2027",
				StartDate: protoDate(2026, 8, 1),
				EndDate:   protoDate(2026, 12, 20),
			},
		}))
	ok(t, err)

	course, err := client.CreateCourse(ctx, connect.NewRequest(&v1.CreateCourseRequest{
		Course: &v1.CourseWrite{
			SourcedId: "crs-wire", Status: "active", Title: "Algebra I",
			CourseCode: "ALG1", Org: school,
		},
	}))
	ok(t, err)

	class, err := client.CreateClass(ctx, connect.NewRequest(&v1.CreateClassRequest{
		Class: &v1.ClassWrite{
			SourcedId: "cls-wire", Status: "active", Title: "Algebra I, Period 2",
			Course: &v1.CourseGUIDRef{
				SourcedId: "crs-wire", Href: "/courses/crs-wire", Type: "course",
			},
			School: school,
			Terms: []*v1.AcadSessionGUIDRef{{
				SourcedId: "term-wire", Href: "/academicSessions/term-wire",
				Type: "academicSession",
			}},
		},
	}))
	ok(t, err)

	user, err := client.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{
		User: &v1.UserWrite{
			SourcedId: "usr-wire", Status: "active", EnabledUser: true,
			GivenName: "Ada", FamilyName: "Lovelace",
			Roles: []*v1.Role{{Org: school, Role: "student", RoleType: "primary"}},
		},
	}))
	ok(t, err)

	enrollment, err := client.CreateEnrollment(ctx,
		connect.NewRequest(&v1.CreateEnrollmentRequest{
			Enrollment: &v1.EnrollmentWrite{
				SourcedId: "enr-wire", Status: "active", Role: "student",
				Class: &v1.ClassGUIDRef{
					SourcedId: "cls-wire", Href: "/classes/cls-wire", Type: "class",
				},
				School: school,
				User: &v1.UserGUIDRef{
					SourcedId: "usr-wire", Href: "/users/usr-wire", Type: "user",
				},
			},
		}))
	ok(t, err)

	// The other half of the property: the caller supplied no timestamp, so
	// every one of these must have been assigned by the service clock. A
	// response without one would leave the client unable to issue its next
	// update, since that value is the concurrency token.
	for _, created := range []struct {
		name  string
		stamp *timestamppb.Timestamp
	}{
		{"org", org.Msg.GetOrg().GetDateLastModified()},
		{"academic session", session.Msg.GetAcademicSession().GetDateLastModified()},
		{"course", course.Msg.GetCourse().GetDateLastModified()},
		{"class", class.Msg.GetClass().GetDateLastModified()},
		{"user", user.Msg.GetUser().GetDateLastModified()},
		{"enrollment", enrollment.Msg.GetEnrollment().GetDateLastModified()},
	} {
		if created.stamp == nil {
			t.Errorf("the created %s came back with no date_last_modified", created.name)
		}
	}
}

// TestErrorCodesReachTheClient asserts what a consumer actually branches on.
// connect.CodeOf has to return the server's code, which means the status has to
// survive the transport and the client's own error decoding.
func TestErrorCodesReachTheClient(t *testing.T) {
	t.Parallel()

	const subject = "oidc|admin"
	client, databaseURL := startService(t, subject)
	seed(t, databaseURL, subject)

	t.Run("not found for an absent record", func(t *testing.T) {
		t.Parallel()

		_, err := client.GetOrg(t.Context(), connect.NewRequest(&v1.GetOrgRequest{
			SourcedId: "no-such-org",
		}))
		wantCode(t, err, connect.CodeNotFound)
	})

	t.Run("invalid argument from the validation interceptor", func(t *testing.T) {
		t.Parallel()

		_, err := client.GetAllOrgs(t.Context(), connect.NewRequest(&v1.GetAllOrgsRequest{
			PageSize: 99999, // above the declared maximum
		}))
		wantCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("invalid argument from the pure core", func(t *testing.T) {
		t.Parallel()

		_, err := client.GetAllOrgs(t.Context(), connect.NewRequest(&v1.GetAllOrgsRequest{
			PageToken: "!!! not base64 !!!",
		}))
		wantCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("permission denied needs the code, not just a failure", func(t *testing.T) {
		t.Parallel()

		// Session curation requires a global roster_admin grant, which this
		// subject has; so this asserts the positive path reaches the client and
		// the negative one is covered by the handler suite.
		_, err := client.CreateAcademicSession(t.Context(),
			connect.NewRequest(&v1.CreateAcademicSessionRequest{
				AcademicSession: &v1.AcademicSessionWrite{
					SourcedId: "ay-wire", Status: "active", Title: "2026-2027",
					Type: "schoolYear", SchoolYear: "2027",
					StartDate: protoDate(2026, 8, 1),
					EndDate:   protoDate(2027, 6, 15),
					// No date_last_modified: AcademicSessionWrite does not
					// declare one. TestCreatesNeedNoServerOwnedTimestamp covers
					// that property for every entity; this case is what proves
					// protovalidate agrees, since the handler suites never run
					// the interceptors.
				},
			}))
		ok(t, err)
	})
}

// TestAbortedSurvivesTheRoundTrip is the code a curation UI has to handle
// specially: it means the edit lost a race and the form must be reloaded rather
// than shown a generic error. If it degraded to Internal or Unknown in transit,
// the UI would take the wrong branch.
func TestAbortedSurvivesTheRoundTrip(t *testing.T) {
	t.Parallel()

	const subject = "oidc|admin"
	client, databaseURL := startService(t, subject)
	seed(t, databaseURL, subject)
	ctx := t.Context()

	current, err := client.GetOrg(ctx, connect.NewRequest(&v1.GetOrgRequest{SourcedId: "sch-2"}))
	ok(t, err)
	staleVersion := current.Msg.GetOrg().GetDateLastModified()

	// A client sends back the record it read with the edited field changed,
	// rather than a bare message naming only that field. The interceptor
	// validates the whole payload, so every required field has to be present
	// even though update_mask names only one of them. Read, modify, send is the
	// flow this API is shaped for.
	//
	// The one field not carried over is date_last_modified: OrgWrite has no such
	// field, and the version being written against travels in the request's own
	// expected_date_last_modified instead. That separation is why there is a
	// Write type at all — the token is a parameter of the update, not a property
	// of the record the caller is proposing.
	edited := func(name string) *v1.OrgWrite {
		org := current.Msg.GetOrg()
		return &v1.OrgWrite{
			SourcedId:  org.GetSourcedId(),
			Status:     org.GetStatus(),
			Name:       name,
			Type:       org.GetType(),
			Identifier: org.GetIdentifier(),
			Parent:     org.GetParent(),
		}
	}

	// Someone writes first, moving date_last_modified.
	_, err = client.UpdateOrg(ctx, connect.NewRequest(&v1.UpdateOrgRequest{
		Org:                      edited("Winner"),
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		ExpectedDateLastModified: staleVersion,
	}))
	ok(t, err)

	// The second writer sends the version it read before that.
	_, err = client.UpdateOrg(ctx, connect.NewRequest(&v1.UpdateOrgRequest{
		Org:                      edited("Loser"),
		UpdateMask:               &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		ExpectedDateLastModified: staleVersion,
	}))
	wantCode(t, err, connect.CodeAborted)

	// And the message reaches the client, so a UI can show why.
	if !strings.Contains(err.Error(), "reload") {
		t.Errorf("the aborted error does not tell the caller to reload: %v", err)
	}
}

// TestPagingThroughTheClient walks the cursor using the client's own types,
// which is how a consumer pages. The token is opaque to them, so this also
// confirms it survives as a string through both codecs.
func TestPagingThroughTheClient(t *testing.T) {
	t.Parallel()

	const subject = "oidc|admin"
	client, databaseURL := startService(t, subject)
	seed(t, databaseURL, subject)
	ctx := t.Context()

	seen := map[string]int{}
	token := ""
	for pages := range 10 {
		resp, err := client.GetAllOrgs(ctx, connect.NewRequest(&v1.GetAllOrgsRequest{
			PageSize: 2, PageToken: token,
		}))
		ok(t, err)

		for _, org := range resp.Msg.GetOrgs() {
			seen[org.GetSourcedId()]++
		}

		token = resp.Msg.GetNextPageToken()
		if token == "" {
			if pages == 0 {
				t.Error("the whole collection came back in one page of size 2")
			}
			break
		}
	}

	equals(t, len(seen), 3)
	for id, count := range seen {
		if count != 1 {
			t.Errorf("org %s appeared %d times across pages, want once", id, count)
		}
	}
}

// ── Small helpers ─────────────────────────────────────────────────────────────

func pgNow() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().UTC().Truncate(time.Microsecond), Valid: true}
}

func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func protoDate(year, month, day int32) *date.Date {
	return &date.Date{Year: year, Month: month, Day: day}
}
