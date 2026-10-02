package oneroster

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	"connectrpc.com/connect"

	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

// Fuzz targets for the pure core.
//
// Every target asserts a named property rather than merely the absence of a
// panic. A target that only checks "it did not crash" finds crashers and nothing
// else; these fail on a wrong-but-quiet answer too, which is the failure mode
// that actually reaches a caller.
//
// Untagged deliberately: `just fuzz-all` derives its target list from
// `go test -list '^Fuzz'` in the default build, so a tagged target would be
// invisible to the sweep and silently skipped. Nothing here touches a database
// or reads the clock, which is what lets the CI fuzz job run with no services.
//
// Corpora are seeded from the values the example-based tests already found
// interesting, so the fuzzer starts at those boundaries instead of rediscovering
// them.

// FuzzCursorRoundTrip asserts the round-trip property: whatever goes into a page
// token comes back out.
//
// Takes []byte rather than string because encodeCursor is base64url and must
// survive any byte sequence — a sourced_id is opaque, and a corpus of
// well-formed identifiers would only test the easy half.
func FuzzCursorRoundTrip(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("sch-1"))
	f.Add([]byte("6ba7b810-9dad-11d1-80b4-00c04fd430c8"))
	f.Add([]byte("a/b+c=d:e"))      // base64 and URL metacharacters
	f.Add([]byte("école-01"))       // multi-byte UTF-8
	f.Add([]byte{0xff, 0xfe, 0x00}) // invalid UTF-8 with an embedded NUL
	f.Add([]byte("\n\t\r"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		original := string(raw)

		got, err := decodeCursor(encodeCursor(original))
		if err != nil {
			t.Fatalf("a token this package encoded was rejected on decode: %v", err)
		}
		if got != original {
			t.Errorf("round trip changed the value: got %q, want %q", got, original)
		}
	})
}

// FuzzDecodeCursorIsTotal asserts totality: an arbitrary client-supplied token
// either decodes or reports errInvalid, and never panics.
//
// A page token arrives from outside, so this is the one core function whose
// input is entirely attacker-chosen.
func FuzzDecodeCursorIsTotal(f *testing.F) {
	f.Add("")
	f.Add("c2NoLTE") // valid base64url
	f.Add("!!!not base64!!!")
	f.Add("====")
	f.Add("YWJj\n") // trailing newline
	f.Add("_-_-_-")

	f.Fuzz(func(t *testing.T, token string) {
		got, err := decodeCursor(token)
		if err != nil {
			if !errors.Is(err, errInvalid) {
				t.Errorf("rejected %q with %v, want an error wrapping errInvalid", token, err)
			}
			if got != "" {
				t.Errorf("returned %q alongside an error; want the zero value", got)
			}
			return
		}
		// Accepted. Re-encoding must then be stable, or paging could walk in
		// circles on a token the client echoed back.
		if again, reErr := decodeCursor(encodeCursor(got)); reErr != nil || again != got {
			t.Errorf("accepted %q -> %q, which does not re-encode stably (%q, %v)",
				token, got, again, reErr)
		}
	})
}

// FuzzPageBoundsClamps asserts the boundary-preserving property: whatever page
// size a caller asks for, the limit handed to the database stays inside the
// configured window.
//
// This is what stops one request asking for an unbounded result set.
func FuzzPageBoundsClamps(f *testing.F) {
	f.Add(int32(0), "")
	f.Add(int32(-1), "")
	f.Add(int32(1), "")
	f.Add(maxPageSize, "")
	f.Add(int32(2147483647), "") // math.MaxInt32
	f.Add(int32(-2147483648), "")
	f.Add(int32(20), encodeCursor("sch-2"))

	f.Fuzz(func(t *testing.T, pageSize int32, token string) {
		limit, after, err := pageBounds(pageSize, token)
		if err != nil {
			// The only failure mode is a malformed token.
			if !errors.Is(err, errInvalid) {
				t.Errorf("pageBounds(%d, %q) failed with %v, want errInvalid", pageSize, token, err)
			}
			return
		}

		// The sentinel row means the limit is always one above the page size.
		if limit < defaultPageSize+1 && limit < 2 {
			t.Errorf("limit %d for page size %d is below any usable value", limit, pageSize)
		}
		if limit > maxPageSize+1 {
			t.Errorf("limit %d for page size %d exceeds maxPageSize+1 (%d)",
				limit, pageSize, maxPageSize+1)
		}
		if limit < 1 {
			t.Errorf("limit %d would ask the database for nothing", limit)
		}
		// An empty token must start from the beginning, not from some decoded value.
		if token == "" && after != "" {
			t.Errorf("empty token produced cursor %q, want the empty string", after)
		}
	})
}

// FuzzMergeWriteIsIdempotent asserts three properties at once: merging a mask
// twice equals merging it once, the stored record is never mutated, and an
// unusable path is reported rather than skipped.
//
// Idempotence matters because a retried update must not compound, and
// non-mutation is what makes mergeWrite safe to call before a write that may
// then fail.
func FuzzMergeWriteIsIdempotent(f *testing.F) {
	f.Add("Old Name", "New Name", "name")
	f.Add("", "", "name")
	f.Add("same", "same", "identifier")
	f.Add("x", "y", "type")
	f.Add("x", "y", "nonsense") // not a field: must be refused, not skipped

	f.Fuzz(func(t *testing.T, storedName, incomingName, path string) {
		stored := &v1.Org{SourcedId: "sch-1", Name: storedName, Identifier: "S1", Type: "school"}
		incoming := &v1.OrgWrite{
			SourcedId: "sch-1", Name: incomingName, Identifier: "S2", Type: "district",
		}

		before := proto.Clone(stored)

		once, err := mergeWrite(stored, incoming, []string{path})
		if err != nil {
			// Only an unknown path can fail here; the pair itself is fixed.
			if (&v1.OrgWrite{}).ProtoReflect().Descriptor().Fields().ByName(protoName(path)) != nil {
				t.Errorf("refused the real field %q: %v", path, err)
			}
			return
		}

		// The second application projects the first result rather than the record,
		// which is the shape a retry takes: merging again must reach the same
		// value, not compound on it.
		twice, err := mergeWrite(once, incoming, []string{path})
		if err != nil {
			t.Fatalf("re-merging %q failed: %v", path, err)
		}

		if !proto.Equal(once, twice) {
			t.Errorf("applying %q a second time changed the result:\n first: %v\n again: %v",
				path, once, twice)
		}
		if !proto.Equal(stored, before) {
			t.Errorf("mergeWrite mutated the stored record: %v, want %v", stored, before)
		}
	})
}

// FuzzRequireMaskIsTotal asserts totality: an arbitrary mask either yields paths
// that all name real fields, or an errInvalid error.
//
// A mask comes from the client, so a path that slipped through and then failed
// inside mergeWrite would be a crash on untrusted input.
func FuzzRequireMaskIsTotal(f *testing.F) {
	f.Add("name")
	f.Add("")
	f.Add("sourced_id")         // the primary key, refused
	f.Add("date_last_modified") // the server's, refused
	f.Add("parent.sourced_id")  // nested, refused
	f.Add("name,name")          // duplicate, refused
	f.Add("name,identifier")
	f.Add("NAME") // wrong case: not a field

	f.Fuzz(func(t *testing.T, joined string) {
		// One fuzzed string split into paths, because f.Fuzz cannot take a slice.
		paths := splitMaskPaths(joined)

		got, err := requireMask(&fieldmaskpb.FieldMask{Paths: paths}, &v1.Org{})
		if err != nil {
			if !errors.Is(err, errInvalid) {
				t.Errorf("rejected %q with %v, want an error wrapping errInvalid", joined, err)
			}
			return
		}

		// Accepted: every path must be a real, settable field, or mergeWrite would
		// silently skip it and the update would appear to succeed having done
		// nothing.
		fields := (&v1.Org{}).ProtoReflect().Descriptor().Fields()
		for _, p := range got {
			if fields.ByName(protoName(p)) == nil {
				t.Errorf("accepted path %q, which is not a field of Org", p)
			}
			if p == sourcedIDField || p == dateLastModifiedField {
				t.Errorf("accepted path %q, which must never be updatable", p)
			}
		}
	})
}

// FuzzCheckedVocabularyIsClosed asserts the closed-vocabulary property: the
// outbound check returns a declared value or the empty string, and never
// anything else.
//
// This is the defence against a database whose CHECK constraint has drifted from
// the API's vocabulary: whatever the column holds, only a declared value reaches
// the wire.
func FuzzCheckedVocabularyIsClosed(f *testing.F) {
	f.Add("active")
	f.Add("tobedeleted")
	f.Add("inactive")
	f.Add("ORG_STATUS_ACTIVE") // the protobuf spelling, must not pass
	f.Add("Active")            // wrong case
	f.Add("")
	f.Add("active ") // trailing space

	f.Fuzz(func(t *testing.T, value string) {
		status := checkedStatus(value)
		if status != "" && status != statusActive && status != statusToBeDeleted {
			t.Errorf("checkedStatus(%q) = %q, which is outside the declared vocabulary", value, status)
		}
		if status != "" && status != value {
			t.Errorf("checkedStatus(%q) = %q; it must pass a value through or drop it, never alter it",
				value, status)
		}

		roleType := checkedRoleType(value)
		if roleType != "" && roleType != "primary" && roleType != "secondary" {
			t.Errorf("checkedRoleType(%q) = %q, outside the declared vocabulary", value, roleType)
		}
	})
}

// FuzzStorageTimestampIsMicrosecondAligned asserts the boundary property that
// the optimistic-concurrency check depends on.
//
// PostgreSQL stores microseconds. A nanosecond remainder surviving into the
// comparison would make a version check fail forever, with the caller unable to
// see why.
func FuzzStorageTimestampIsMicrosecondAligned(f *testing.F) {
	f.Add(int64(0), int32(0))
	f.Add(int64(1767225600), int32(123456789)) // nanoseconds present
	f.Add(int64(1767225600), int32(999999999))
	f.Add(int64(-62135596800), int32(0)) // year 1
	f.Add(int64(253402300799), int32(1)) // year 9999

	f.Fuzz(func(t *testing.T, seconds int64, nanos int32) {
		// Keep nanos in the range protobuf allows; outside it the value is not a
		// Timestamp at all and the SDK's own validation is what should reject it.
		if nanos < 0 || nanos > 999999999 {
			t.Skip("nanos outside the protobuf range")
		}

		ts := &timestamppb.Timestamp{Seconds: seconds, Nanos: nanos}
		got := storageTimestamp(ts)
		if !got.Valid {
			t.Fatal("storageTimestamp returned an invalid value for a well-formed timestamp")
		}
		if got.Time.Nanosecond()%1000 != 0 {
			t.Errorf("result %v is not microsecond-aligned (nanosecond remainder %d)",
				got.Time, got.Time.Nanosecond()%1000)
		}

		// And what was stored must compare equal when the client sends it back.
		returned := storageTimestamp(timestamppb.New(got.Time))
		if !returned.Time.Equal(got.Time) {
			t.Errorf("a stored value did not round-trip: %v then %v", got.Time, returned.Time)
		}
	})
}

// FuzzStorageDateNeverShiftsADate asserts that an out-of-range date component is
// dropped rather than normalised.
//
// time.Date would silently turn month 13 into January of the following year. A
// plausible wrong date is worse than an absent one: nobody notices it.
func FuzzStorageDateNeverShiftsADate(f *testing.F) {
	f.Add(int32(2026), int32(3), int32(9))
	f.Add(int32(2026), int32(13), int32(1)) // month out of range
	f.Add(int32(2026), int32(0), int32(1))
	f.Add(int32(2026), int32(2), int32(31)) // impossible day for the month
	f.Add(int32(0), int32(1), int32(1))     // year below the bound
	f.Add(int32(10000), int32(1), int32(1)) // year above the bound
	f.Add(int32(-1), int32(-1), int32(-1))

	f.Fuzz(func(t *testing.T, year, month, day int32) {
		got := storageDate(&date.Date{Year: year, Month: month, Day: day})
		if !got.Valid {
			return // out of range and dropped, which is the contract
		}

		// Accepted: the stored value must be exactly what was asked for. Anything
		// else is a silent shift. Compared in int, widening the inputs rather
		// than narrowing the stored components, so the comparison cannot itself
		// wrap.
		y, m, d := got.Time.Date()
		if y != int(year) || int(m) != int(month) || d != int(day) {
			t.Errorf("storageDate(%d-%d-%d) stored %d-%d-%d; a date was shifted rather than dropped",
				year, month, day, y, m, d)
		}
		if got.Time.Location() != time.UTC {
			t.Errorf("stored date is in %v, want UTC", got.Time.Location())
		}
	})
}

// FuzzRequireSessionDatesOrdering asserts the ordering property: a session is
// accepted only when its end is not before its start.
func FuzzRequireSessionDatesOrdering(f *testing.F) {
	f.Add(int32(2026), int32(8), int32(1), int32(2027), int32(6), int32(15))
	f.Add(int32(2027), int32(6), int32(15), int32(2026), int32(8), int32(1)) // inverted
	f.Add(int32(2026), int32(1), int32(1), int32(2026), int32(1), int32(1))  // same day
	f.Add(int32(2026), int32(12), int32(31), int32(2027), int32(1), int32(1))
	f.Add(int32(2026), int32(2), int32(31), int32(2026), int32(6), int32(1)) // February 31 does not exist

	f.Fuzz(func(t *testing.T, sy, sm, sd, ey, em, ed int32) {
		session := &v1.AcademicSessionWrite{
			StartDate: &date.Date{Year: sy, Month: sm, Day: sd},
			EndDate:   &date.Date{Year: ey, Month: em, Day: ed},
		}
		err := requireSessionDates(session)

		// Two independent reasons to refuse, and the property has to allow for
		// both: a date that is not a real calendar date, or a pair in the wrong
		// order. Asserting only the ordering would make every impossible date
		// look like a false rejection.
		_, _, _, startReal := calendarDate(session.GetStartDate())
		_, _, _, endReal := calendarDate(session.GetEndDate())

		startKey := [3]int32{sy, sm, sd}
		endKey := [3]int32{ey, em, ed}
		inverted := endKey[0] < startKey[0] ||
			(endKey[0] == startKey[0] && endKey[1] < startKey[1]) ||
			(endKey[0] == startKey[0] && endKey[1] == startKey[1] && endKey[2] < startKey[2])

		mustRefuse := !startReal || !endReal || inverted

		if mustRefuse && err == nil {
			t.Errorf("accepted start %v (real=%v) end %v (real=%v), inverted=%v",
				startKey, startReal, endKey, endReal, inverted)
		}
		if !mustRefuse && err != nil {
			t.Errorf("rejected a valid ordered pair start %v end %v with %v",
				startKey, endKey, err)
		}
		if err != nil && !errors.Is(err, errInvalid) {
			t.Errorf("error %v does not wrap errInvalid", err)
		}
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// splitMaskPaths turns one fuzzed string into a path list.
//
// f.Fuzz cannot take a []string, so the corpus carries comma-separated paths. An
// empty input becomes an empty list rather than a list containing "", because
// those are different cases and requireMask treats them differently.
func splitMaskPaths(joined string) []string {
	if joined == "" {
		return nil
	}
	paths := make([]string, 0, 4)
	start := 0
	for i := range len(joined) {
		if joined[i] == ',' {
			paths = append(paths, joined[start:i])
			start = i + 1
		}
	}
	return append(paths, joined[start:])
}

// protoName converts a mask path to a protoreflect field name.
//
// Guards against a path that is not valid UTF-8: protoreflect.Name performs no
// validation of its own, and raw bytes would be compared against descriptor
// names that cannot contain them.
func protoName(path string) protoreflect.Name {
	if !utf8.ValidString(path) {
		return ""
	}
	return protoreflect.Name(path)
}

// FuzzProxyIdentityRespectsTheTrustBoundary asserts the two properties the
// proxy-authentication path rests on, against arbitrary header values.
//
// The first is the security boundary: with trustProxy false, nothing a client
// can put in a header may name a caller. On a listener reachable without a
// proxy, a single missed branch there would let anyone set X-Forwarded-User and
// become anyone, and that is not a bug a table of examples reliably catches.
//
// The second is the contract the shell depends on: a reported identity always
// names someone, so an empty or blank subject can never reach the context.
func FuzzProxyIdentityRespectsTheTrustBoundary(f *testing.F) {
	f.Add("ada", "ada@example.test", "12345", "jwt")
	f.Add("", "", "", "")
	f.Add("   ", "\t", " ", "")
	f.Add("accounts.google.com:", "accounts.google.com:", "accounts.google.com:", "x")
	f.Add("a\nb", "c\rd", "e f", "g")

	f.Fuzz(func(t *testing.T, user, email, iapID, assertion string) {
		// http.Header.Set panics on some byte sequences a fuzzer will produce, and
		// a header value containing a newline could never arrive over HTTP anyway:
		// net/http rejects it at the transport. Assigning the map directly models
		// what a real request can carry.
		h := http.Header{
			headerForwardedUser:  []string{user},
			headerForwardedEmail: []string{email},
			headerIAPID:          []string{iapID},
			headerIAPAssertion:   []string{assertion},
		}

		if got, ok := proxyIdentity(h, false); ok {
			t.Errorf("proxyIdentity named %q with trustProxy false; "+
				"any caller could then become anyone", got.subject)
		}

		got, ok := proxyIdentity(h, true)
		if !ok {
			return
		}
		if strings.TrimSpace(got.subject) == "" {
			t.Errorf("reported an identity with a blank subject %q", got.subject)
		}
		if got.provider != providerIAP && got.provider != providerProxy {
			t.Errorf("provider = %q, want one of the two known upstreams", got.provider)
		}
	})
}

// FuzzSQLStateMappingIsClosed asserts the closed-set property that keeps a
// database's own words out of a client's error.
//
// A PgError carries Message and Detail written by PostgreSQL, and those can name
// a constraint, a column, or the value that violated it, which on this service
// means a student's name. translate never passes that text on: it looks the
// SQLSTATE up and returns one of a few fixed sentinels instead. The protection
// is that the *set* of possible messages is closed, which is what these two
// functions decide, so that is what is fuzzed.
//
// translate itself is deliberately not the subject. It logs through the
// package-level slog default, so a fuzz run would spend its budget formatting
// log lines, and silencing it means replacing a global that another test reads.
// Threading a logger through translate to quieten a test would be a seam added
// for the test rather than for the design. TestTranslateNeverLeaksServerText
// pins translate's use of these mappings with a table.
//
// There is deliberately no "the message does not repeat the SQLSTATE back"
// assertion. It looks like the property worth having and is not: a message
// proven to be one of three constants cannot carry arbitrary input, so the
// check is redundant, and it reports a failure for any input that happens to be
// a substring of English prose. The fuzzer found that in under a second by
// trying "a", which appears in "request violates a data constraint".
func FuzzSQLStateMappingIsClosed(f *testing.F) {
	for _, seed := range []string{
		sqlStateUniqueViolation,
		sqlStateForeignKeyViolation,
		sqlStateCheckViolation,
		sqlStateNotNullViolation,
		sqlStateSerializationFail,
		sqlStateDeadlockDetected,
		"99999", // a real SQLSTATE this service does not model
		"",
		"23505 ", // trailing space: close enough to be worth refusing
		strings.Repeat("9", 4096),
	} {
		f.Add(seed)
	}

	// Every code translate can answer with, and every message it can attach.
	// A new SQLSTATE case must be added here as well, which is the point: the
	// list is the claim about what a client can receive.
	codes := []connect.Code{
		connect.CodeAlreadyExists,
		connect.CodeFailedPrecondition,
		connect.CodeAborted,
		connect.CodeInternal,
	}
	messages := []error{errAlreadyExists, errConflict, errConstraint}
	modelled := []string{
		sqlStateUniqueViolation, sqlStateForeignKeyViolation,
		sqlStateCheckViolation, sqlStateNotNullViolation,
		sqlStateSerializationFail, sqlStateDeadlockDetected,
	}

	f.Fuzz(func(t *testing.T, sqlState string) {
		code := connectCodeForSQLState(sqlState)
		if !slices.Contains(codes, code) {
			t.Errorf("sqlstate %q produced code %v, which is outside the modelled set %v",
				sqlState, code, codes)
		}

		message := messageForSQLState(sqlState)
		if !slices.Contains(messages, message) {
			t.Errorf("sqlstate %q produced message %v, which is not one of the fixed sentinels %v",
				sqlState, message, messages)
		}

		// An unmodelled SQLSTATE must map to Internal, which is what stops
		// translate reaching for a message at all: it only attaches one when
		// the code is something other than Internal.
		if !slices.Contains(modelled, sqlState) && code != connect.CodeInternal {
			t.Errorf("unmodelled sqlstate %q produced %v, want Internal", sqlState, code)
		}
	})
}

// FuzzPaginateWalksEveryRowOnce asserts the property keyset paging exists for:
// a client that follows the tokens sees every row exactly once, in order.
//
// Offset paging was removed from this service because it cannot promise that
// under concurrent writes, and the regression test for it walks a fixed seven
// rows at a page size of three. That pins one combination. The interesting
// failures live at the boundaries between row count and page size, where a page
// exactly fills, or holds one row, or the collection is empty, and this walks
// all of them.
//
// The walk is bounded at one iteration per row plus one. A cursor that fails to
// advance would otherwise loop forever, and a fuzz target that hangs reports a
// timeout with no reproducer, which is worse than a failure.
func FuzzPaginateWalksEveryRowOnce(f *testing.F) {
	f.Add(uint8(7), int32(3))
	f.Add(uint8(0), int32(10))  // empty collection
	f.Add(uint8(1), int32(1))   // a page size of one, the slowest legal walk
	f.Add(uint8(10), int32(10)) // the page exactly fills
	f.Add(uint8(10), int32(11)) // one row of slack
	f.Add(uint8(9), int32(10))  // one row short
	f.Add(uint8(200), int32(-5))
	f.Add(uint8(5), int32(0)) // zero asks for the default

	f.Fuzz(func(t *testing.T, rowCount uint8, pageSize int32) {
		// Identifiers that sort the way the database would, so the cursor
		// comparison below is the same comparison the SQL does. Zero-padded
		// because "sch-10" sorts before "sch-9" as text.
		all := make([]string, rowCount)
		for i := range all {
			all[i] = fmt.Sprintf("sch-%04d", i)
		}

		var seen []string
		token := ""
		for range int(rowCount) + 1 {
			limit, after, err := pageBounds(pageSize, token)
			if err != nil {
				// pageBounds owns the page size and the token. A token this
				// walk produced must always decode again.
				t.Fatalf("pageBounds(%d, %q): %v", pageSize, token, err)
			}
			if limit <= 0 {
				t.Fatalf("pageBounds(%d, %q) allowed a limit of %d, which fetches nothing",
					pageSize, token, limit)
			}

			// Stands in for the keyset query: everything strictly after the
			// cursor, up to the limit.
			rows := make([]string, 0, limit)
			for _, id := range all {
				if id > after && len(rows) < int(limit) {
					rows = append(rows, id)
				}
			}

			page, next := paginate(rows, limit, func(s string) string { return s })
			seen = append(seen, page...)
			if next == "" {
				break
			}
			if next == token {
				t.Fatalf("the cursor did not advance past %q; the walk would not terminate", token)
			}
			token = next
		}

		if strings.Join(seen, ",") != strings.Join(all, ",") {
			t.Errorf("walking %d rows at page size %d visited %d: %v, want %v",
				rowCount, pageSize, len(seen), seen, all)
		}
	})
}

// FuzzMergeWriteAcrossEveryEntity extends the idempotence properties to all six
// entity pairs, not only Org.
//
// FuzzMergeWriteIsIdempotent only ever instantiates Org, which has eight scalar
// fields. The reflective projection inside mergeWrite is at its most
// interesting on the others: Class carries repeated message fields, User
// carries twenty-five fields including nested Roles, and AcademicSession
// carries google.type.Date sub-messages. A by-name copy that mishandled a
// repeated or nested field would pass every Org case and fail on those.
//
// The pairs come from writePairs(), the fixture the drift test already uses.
// A second list here would be a second place to add an entity, and keeping one
// is the same reasoning that put the drift test there.
func FuzzMergeWriteAcrossEveryEntity(f *testing.F) {
	pairs := writePairs()

	// One seed per entity on a field it actually declares, so plain `go test`
	// exercises every pair rather than whichever one index 0 lands on.
	for i, seed := range []string{"status", "status", "title", "role", "given_name", "title"} {
		f.Add(uint8(i), seed)
	}
	f.Add(uint8(0), "nonsense")   // not a field: must be refused
	f.Add(uint8(0), "")           // empty path
	f.Add(uint8(99), "status")    // index past the end
	f.Add(uint8(2), "terms")      // a repeated message field
	f.Add(uint8(5), "start_date") // a sub-message field
	f.Add(uint8(4), "roles")      // repeated nested messages
	f.Add(uint8(0), "sourced_id") // the key, which an update may not move
	f.Add(uint8(3), "date_last_modified")

	f.Fuzz(func(t *testing.T, which uint8, path string) {
		pair := pairs[int(which)%len(pairs)]
		before := proto.Clone(pair.payload)

		once, err := mergeWrite(pair.entity, pair.payload, []string{path})
		if err != nil {
			// The only reachable failure is a path the write type does not
			// declare; the pairs themselves are fixed and aligned.
			if pair.payload.ProtoReflect().Descriptor().Fields().ByName(protoName(path)) != nil {
				t.Errorf("%s: refused its own field %q: %v", pair.name, path, err)
			}

			return
		}

		// Merging the result again must reach the same value, which is the
		// shape a retried update takes.
		twice, err := mergeWrite(once, pair.payload, []string{path})
		if err != nil {
			t.Fatalf("%s: re-merging %q failed: %v", pair.name, path, err)
		}
		if !proto.Equal(once, twice) {
			t.Errorf("%s: applying %q twice differs from once:\n first: %v\n again: %v",
				pair.name, path, once, twice)
		}

		// The request is an input, not a scratch buffer. A merge that wrote
		// through it would corrupt the message the interceptor validated.
		if !proto.Equal(pair.payload, before) {
			t.Errorf("%s: mergeWrite mutated the incoming payload", pair.name)
		}
	})
}

// FuzzProtoDateNeverShiftsAStoredDate is the mirror of
// FuzzStorageDateNeverShiftsADate, over the read direction.
//
// That target guards the write path: a client's impossible date is dropped
// rather than normalised into a plausible wrong one. This guards the read path,
// where a stored date must come back as itself or not at all. Serving a date
// nobody stored is the failure worth preventing, because a shifted term boundary
// looks like data rather than like a bug.
//
// Worth locking now in particular: protoDate used to carry month and day range
// checks that no input could reach, because time.Time cannot hold a month
// outside 1..12 or a day outside 1..31. Mutation testing found them unreachable
// and they were removed, which leaves the year bound as the only guard and this
// as the property that says the rest of the conversion is faithful.
func FuzzProtoDateNeverShiftsAStoredDate(f *testing.F) {
	f.Add(2026, 3, 9)
	f.Add(1, 1, 1)         // the first representable year
	f.Add(9999, 12, 31)    // the last
	f.Add(0, 1, 1)         // below it
	f.Add(10000, 1, 1)     // above it
	f.Add(-4713, 1, 1)     // PostgreSQL's lower bound
	f.Add(5874897, 12, 31) // and its upper
	f.Add(2026, 2, 29)     // not a date in 2026; time.Date normalises it

	f.Fuzz(func(t *testing.T, year, month, day int) {
		// time.Date normalises, so the instant may not carry the components
		// that went in. What protoDate must preserve is the instant's own
		// components, which is what a database row holds.
		stored := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		wantYear, wantMonth, wantDay := stored.Date()

		got := protoDate(pgtype.Date{Time: stored, Valid: true})
		if got == nil {
			// Only a year outside the representable range may be dropped.
			if wantYear >= minProtoYear && wantYear <= maxProtoYear {
				t.Errorf("protoDate dropped %04d-%02d-%02d, which google.type.Date can represent",
					wantYear, wantMonth, wantDay)
			}

			return
		}

		if wantYear < minProtoYear || wantYear > maxProtoYear {
			t.Errorf("protoDate kept year %d, which is outside [%d, %d]",
				wantYear, minProtoYear, maxProtoYear)
		}
		if int(got.GetYear()) != wantYear ||
			int(got.GetMonth()) != int(wantMonth) ||
			int(got.GetDay()) != wantDay {
			t.Errorf("protoDate returned %d-%02d-%02d for a row holding %04d-%02d-%02d",
				got.GetYear(), got.GetMonth(), got.GetDay(), wantYear, wantMonth, wantDay)
		}
	})
}

// FuzzMetadataIsTotal asserts that an arbitrary extension bag either decodes or
// is dropped, and never takes the read down with it.
//
// metadata is handed the raw contents of a JSONB column. The service does not
// write that column's contents itself: a vendor extension bag arrives from
// whatever loaded the roster, so the bytes are outside this code's control in
// the same way a page token is.
//
// The second assertion is what stops this degenerating into a panic check. A
// non-nil result is handed to protojson on the way out, so "decoded" has to mean
// "can be marshalled again", not merely "did not crash".
func FuzzMetadataIsTotal(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte(""))
	f.Add([]byte("{}"))
	f.Add([]byte(`{"district":"north","wave":2}`))
	f.Add([]byte(`{"nested":{"a":[1,2,{"b":null}]}}`))
	f.Add([]byte(`{"district":`)) // truncated
	f.Add([]byte(`[1,2,3]`))      // valid JSON, wrong shape for a Struct
	f.Add([]byte(`"a string"`))
	f.Add([]byte{0xff, 0xfe, 0x00}) // not UTF-8

	f.Fuzz(func(t *testing.T, raw []byte) {
		got := metadata(raw)
		if got == nil {
			return
		}

		if _, err := protojson.Marshal(got); err != nil {
			t.Errorf("metadata accepted %q and produced a Struct that will not marshal: %v",
				raw, err)
		}
	})
}

// FuzzRefHrefIsNeverEmpty asserts the invariant refHref exists for.
//
// href is a required field in the OneRoster schema, so an empty one produces a
// response that fails the consumer's own validation. The function synthesises a
// path rather than returning nothing, and that promise is stated in its comment
// and asserted nowhere else.
//
// The collection comes from the declared constants rather than from the fuzzer.
// In production it is always one of those five, so arbitrary bytes there would
// assert a property about inputs the function never receives, and would dilute
// the half that matters: that "class" still pluralises to "classes" and not to
// the "classs" an earlier version emitted.
func FuzzRefHrefIsNeverEmpty(f *testing.F) {
	f.Add(uint8(3), "", false, "cl-1")
	f.Add(uint8(0), "/stored/path", true, "sch-1")
	f.Add(uint8(0), "", true, "sch-1")          // Valid but blank: synthesise
	f.Add(uint8(0), "  ", true, "sch-1")        // whitespace is not a path
	f.Add(uint8(1), "", false, "")              // no identifier either
	f.Add(uint8(4), "/users/u-1", false, "u-1") // a stale String behind a NULL

	collections := []string{
		collectionOrgs, collectionAcademicSessions,
		collectionCourses, collectionClasses, collectionUsers,
	}

	f.Fuzz(func(t *testing.T, which uint8, href string, valid bool, sourcedID string) {
		collection := collections[int(which)%len(collections)]

		got := refHref(pgtype.Text{String: href, Valid: valid}, collection, sourcedID)
		if got == "" {
			t.Fatalf("refHref returned an empty href for collection %q, "+
				"which fails the consumer's own schema validation", collection)
		}

		// A stored href is authoritative and must survive verbatim. Valid is
		// the authority on whether one exists, not String.
		if valid && href != "" {
			if got != href {
				t.Errorf("refHref rewrote the stored href %q as %q", href, got)
			}

			return
		}

		// Otherwise it is synthesised, and must name the collection it was
		// asked for rather than one derived from a type name.
		if !strings.Contains(got, "/"+collection+"/") {
			t.Errorf("synthesised href %q does not sit under collection %q", got, collection)
		}
	})
}
