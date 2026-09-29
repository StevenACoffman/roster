package oneroster

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// These exercise the pure core only. Nothing here opens a connection or reads
// the clock, which is the property that makes the core worth separating: every
// case below is a value in and a value out.

func TestPageBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pageSize  int32
		pageToken string
		wantLimit int32
		wantAfter string
		wantErr   bool
	}{
		{
			name:      "zero page size takes the default",
			pageSize:  0,
			wantLimit: defaultPageSize + 1,
		},
		{
			name:      "negative page size takes the default",
			pageSize:  -5,
			wantLimit: defaultPageSize + 1,
		},
		{
			name:      "oversized page size is clamped, not refused",
			pageSize:  maxPageSize * 10,
			wantLimit: maxPageSize + 1,
		},
		{
			name:      "requested size is honoured with one sentinel row added",
			pageSize:  25,
			wantLimit: 26,
		},
		{
			name:      "empty token starts before every identifier",
			pageSize:  10,
			pageToken: "",
			wantLimit: 11,
			wantAfter: "",
		},
		{
			name:      "valid token resumes after its identifier",
			pageSize:  10,
			pageToken: encodeCursor("sch-2"),
			wantLimit: 11,
			wantAfter: "sch-2",
		},
		{
			name:      "malformed token is the caller's error",
			pageSize:  10,
			pageToken: "not!valid!base64",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limit, after, err := pageBounds(tt.pageSize, tt.pageToken)
			if tt.wantErr {
				if !errors.Is(err, errInvalid) {
					t.Fatalf("pageBounds error = %v, want one wrapping errInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("pageBounds: unexpected error %v", err)
			}
			if limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", limit, tt.wantLimit)
			}
			if after != tt.wantAfter {
				t.Errorf("after = %q, want %q", after, tt.wantAfter)
			}
		})
	}
}

func TestPaginate(t *testing.T) {
	t.Parallel()

	id := func(s string) string { return s }

	tests := []struct {
		name      string
		rows      []string
		limit     int32
		wantPage  []string
		wantToken string
	}{
		{
			name:      "short read is the last page",
			rows:      []string{"a", "b"},
			limit:     4,
			wantPage:  []string{"a", "b"},
			wantToken: "",
		},
		{
			name:      "empty read is the last page",
			rows:      []string{},
			limit:     4,
			wantPage:  []string{},
			wantToken: "",
		},
		{
			name:      "full read drops the sentinel and emits a token",
			rows:      []string{"a", "b", "c", "d"},
			limit:     4,
			wantPage:  []string{"a", "b", "c"},
			wantToken: encodeCursor("c"),
		},
		{
			name:      "exactly one row short of full is the last page",
			rows:      []string{"a", "b", "c"},
			limit:     4,
			wantPage:  []string{"a", "b", "c"},
			wantToken: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			page, token := paginate(tt.rows, tt.limit, id)
			if strings.Join(page, ",") != strings.Join(tt.wantPage, ",") {
				t.Errorf("page = %v, want %v", page, tt.wantPage)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
		})
	}
}

// TestPaginateNeverRepeatsOrSkips walks a collection the way a client does and
// asserts the whole sequence comes back exactly once. This is the property the
// keyset design exists for, and it is cheaper to assert here than against a
// database.
func TestPaginateNeverRepeatsOrSkips(t *testing.T) {
	t.Parallel()

	all := []string{"a", "b", "c", "d", "e", "f", "g"}
	const pageSize int32 = 3

	var seen []string
	token := ""
	for range len(all) + 1 { // bounded so a paging bug fails rather than hangs
		limit, after, err := pageBounds(pageSize, token)
		if err != nil {
			t.Fatalf("pageBounds: %v", err)
		}

		// Stand in for the keyset query: everything strictly after the cursor.
		rows := make([]string, 0, limit)
		for _, s := range all {
			if s > after && len(rows) < int(limit) {
				rows = append(rows, s)
			}
		}

		page, next := paginate(rows, limit, func(s string) string { return s })
		seen = append(seen, page...)
		if next == "" {
			break
		}
		token = next
	}

	if strings.Join(seen, ",") != strings.Join(all, ",") {
		t.Errorf("walked %v, want %v", seen, all)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sourcedID string
	}{
		{name: "plain identifier", sourcedID: "sch-1"},
		{name: "uuid-shaped", sourcedID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8"},
		{name: "contains separator characters", sourcedID: "a/b+c=d:e"},
		{name: "non-ascii", sourcedID: "école-01"},
		{name: "empty", sourcedID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeCursor(encodeCursor(tt.sourcedID))
			if err != nil {
				t.Fatalf("decodeCursor: %v", err)
			}
			if got != tt.sourcedID {
				t.Errorf("round trip = %q, want %q", got, tt.sourcedID)
			}
		})
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	t.Parallel()

	// A client pasting junk must get InvalidArgument, not a silent restart from
	// the beginning of the collection.
	if _, err := decodeCursor("!!!not base64!!!"); !errors.Is(err, errInvalid) {
		t.Errorf("decodeCursor(garbage) error = %v, want one wrapping errInvalid", err)
	}
}

// TestCheckedStatus covers the outbound half of the closed-vocabulary defence:
// whatever the database hands back, only a value the API declares reaches the
// wire.
func TestCheckedStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "active passes through", status: statusActive, want: "active"},
		{name: "tobedeleted passes through", status: statusToBeDeleted, want: "tobedeleted"},
		{
			name:   "unknown value is dropped, not passed through",
			status: "inactive",
			want:   "",
		},
		{
			name:   "protobuf enum spelling is not a valid value either",
			status: "ORG_STATUS_ACTIVE",
			want:   "",
		},
		{
			name:   "case variation is not accepted; the vocabulary is exact",
			status: "Active",
			want:   "",
		},
		{name: "empty stays empty", status: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := checkedStatus(tt.status); got != tt.want {
				t.Errorf("checkedStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestCheckedRoleType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		roleType string
		want     string
	}{
		{name: "primary", roleType: "primary", want: "primary"},
		{name: "secondary", roleType: "secondary", want: "secondary"},
		{name: "unknown is dropped", roleType: "tertiary", want: ""},
		{name: "enum spelling is dropped", roleType: "ROLE_ROLE_TYPE_PRIMARY", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := checkedRoleType(tt.roleType); got != tt.want {
				t.Errorf("checkedRoleType(%q) = %q, want %q", tt.roleType, got, tt.want)
			}
		})
	}
}

// TestVocabulariesMatchTheSchema guards the invariant that makes the outbound
// check meaningful: the values this package accepts must be exactly the values
// the database CHECK constraints allow. If someone widens one and not the other,
// the two silently disagree and rows start vanishing from responses.
func TestVocabulariesMatchTheSchema(t *testing.T) {
	t.Parallel()

	schema, err := os.ReadFile(filepath.Join("..", "..", "sql", "schema", "001_oneroster.sql"))
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}

	// Every status CHECK in the schema is written with this exact vocabulary.
	const statusCheck = "status IN ('active', 'tobedeleted')"
	if !strings.Contains(string(schema), statusCheck) {
		t.Errorf("schema no longer contains %q; validStatuses may be stale", statusCheck)
	}

	const roleTypeCheck = "role_type IN ('primary', 'secondary')"
	if !strings.Contains(string(schema), roleTypeCheck) {
		t.Errorf("schema no longer contains %q; validRoleTypes may be stale", roleTypeCheck)
	}

	for _, status := range validStatuses {
		if !strings.Contains(string(schema), "'"+status+"'") {
			t.Errorf("validStatuses has %q, which the schema does not allow", status)
		}
	}
	for _, roleType := range validRoleTypes {
		if !strings.Contains(string(schema), "'"+roleType+"'") {
			t.Errorf("validRoleTypes has %q, which the schema does not allow", roleType)
		}
	}
}

func TestRefHref(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		href       pgtype.Text
		collection string
		sourcedID  string
		want       string
	}{
		{
			name:       "stored href wins",
			href:       pgtype.Text{String: "https://sis.example/orgs/1", Valid: true},
			collection: collectionOrgs,
			sourcedID:  "1",
			want:       "https://sis.example/orgs/1",
		},
		{
			name:       "null href is synthesized from the spec path",
			href:       pgtype.Text{},
			collection: collectionOrgs,
			sourcedID:  "sch-1",
			want:       "/ims/oneroster/rostering/v1p2/orgs/sch-1",
		},
		{
			name:       "empty stored href is synthesized too",
			href:       pgtype.Text{String: "", Valid: true},
			collection: collectionClasses,
			sourcedID:  "cl-9",
			want:       "/ims/oneroster/rostering/v1p2/classes/cl-9",
		},
		{
			// "class" does not pluralise by appending "s"; an earlier version
			// derived the path from the type name and emitted "classs".
			name:       "class pluralises to classes, not classs",
			href:       pgtype.Text{},
			collection: collectionClasses,
			sourcedID:  "cl-1",
			want:       "/ims/oneroster/rostering/v1p2/classes/cl-1",
		},
		{
			name:       "academicSessions keeps its camelCase segment",
			href:       pgtype.Text{},
			collection: collectionAcademicSessions,
			sourcedID:  "term-1",
			want:       "/ims/oneroster/rostering/v1p2/academicSessions/term-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := refHref(tt.href, tt.collection, tt.sourcedID); got != tt.want {
				t.Errorf("refHref = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTextPtrDistinguishesNullFromEmpty(t *testing.T) {
	t.Parallel()

	if got := textPtr(pgtype.Text{}); got != nil {
		t.Errorf("textPtr(NULL) = %v, want nil", got)
	}
	got := textPtr(pgtype.Text{String: "", Valid: true})
	if got == nil || *got != "" {
		t.Errorf("textPtr(empty) = %v, want pointer to empty string", got)
	}
}

func TestProtoDate(t *testing.T) {
	t.Parallel()

	if got := protoDate(pgtype.Date{}); got != nil {
		t.Errorf("protoDate(NULL) = %v, want nil", got)
	}

	d := pgtype.Date{Time: time.Date(2026, time.March, 9, 0, 0, 0, 0, time.UTC), Valid: true}
	got := protoDate(d)
	if got.GetYear() != 2026 || got.GetMonth() != 3 || got.GetDay() != 9 {
		t.Errorf("protoDate = %d-%d-%d, want 2026-3-9", got.GetYear(), got.GetMonth(), got.GetDay())
	}
}

func TestMetadataDropsUnparseableExtensionBag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     []byte
		wantNil bool
	}{
		{name: "absent", raw: nil, wantNil: true},
		{name: "empty", raw: []byte{}, wantNil: true},
		{name: "malformed is dropped, not fatal", raw: []byte("{not json"), wantNil: true},
		{name: "valid object is decoded", raw: []byte(`{"sis":"powerschool"}`), wantNil: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := metadata(tt.raw)
			if tt.wantNil && got != nil {
				t.Errorf("metadata = %v, want nil", got)
			}
			if !tt.wantNil && got == nil {
				t.Error("metadata = nil, want a decoded struct")
			}
		})
	}
}

func TestRequireSourcedID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sourcedID string
		want      string
		wantErr   bool
	}{
		{name: "plain", sourcedID: "sch-1", want: "sch-1"},
		{name: "surrounding space is trimmed", sourcedID: "  sch-1  ", want: "sch-1"},
		{name: "empty is refused", sourcedID: "", wantErr: true},
		{name: "whitespace only is refused", sourcedID: "   ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := requireSourcedID(tt.sourcedID)
			if tt.wantErr {
				if !errors.Is(err, errInvalid) {
					t.Fatalf("error = %v, want one wrapping errInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
