package oneroster

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/reflect/protoreflect"

	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

func TestRequireMask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		paths     []string
		nilMask   bool
		wantPaths []string
		wantErr   string
	}{
		{
			name:      "a single field",
			paths:     []string{"name"},
			wantPaths: []string{"name"},
		},
		{
			name:      "several fields",
			paths:     []string{"name", "identifier", "type"},
			wantPaths: []string{"name", "identifier", "type"},
		},
		{
			name:    "a nil mask is refused, not read as replace-everything",
			nilMask: true,
			wantErr: "update_mask is required",
		},
		{
			name:    "an empty mask is refused too",
			paths:   []string{},
			wantErr: "update_mask is required",
		},
		{
			name:    "sourced_id cannot be updated",
			paths:   []string{"name", "sourced_id"},
			wantErr: "cannot be updated",
		},
		{
			name:    "date_last_modified is the server's, not the caller's",
			paths:   []string{"date_last_modified"},
			wantErr: "set by the server",
		},
		{
			name:    "an unknown field is refused rather than ignored",
			paths:   []string{"name", "nonsense"},
			wantErr: "not a field of",
		},
		{
			name:    "a nested path is refused rather than guessed at",
			paths:   []string{"parent.sourced_id"},
			wantErr: "is nested",
		},
		{
			name:    "a duplicate path means the mask was built wrong",
			paths:   []string{"name", "name"},
			wantErr: "more than once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mask *fieldmaskpb.FieldMask
			if !tt.nilMask {
				mask = &fieldmaskpb.FieldMask{Paths: tt.paths}
			}

			got, err := requireMask(mask, &v1.Org{})

			if tt.wantErr != "" {
				if !errors.Is(err, errInvalid) {
					t.Fatalf("error = %v, want one wrapping errInvalid", err)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantPaths, ",") {
				t.Errorf("paths = %v, want %v", got, tt.wantPaths)
			}
		})
	}
}

func TestMergeWriteCopiesOnlyNamedFields(t *testing.T) {
	t.Parallel()

	stored := &v1.Org{
		SourcedId:  "sch-1",
		Name:       "Old Name",
		Type:       "school",
		Identifier: "S1",
		Status:     "active",
	}
	incoming := &v1.OrgWrite{
		SourcedId:  "sch-1",
		Name:       "New Name",
		Type:       "district", // named in no mask below, so must not move
		Identifier: "CHANGED",
		Status:     "tobedeleted",
	}

	merged, err := mergeWrite(stored, incoming, []string{"name", "identifier"})
	if err != nil {
		t.Fatalf("mergeWrite: %v", err)
	}

	if got := merged.GetName(); got != "New Name" {
		t.Errorf("name = %q, want the incoming value", got)
	}
	if got := merged.GetIdentifier(); got != "CHANGED" {
		t.Errorf("identifier = %q, want the incoming value", got)
	}
	if got := merged.GetType(); got != "school" {
		t.Errorf("type = %q, want the stored value: it was not in the mask", got)
	}
	if got := merged.GetStatus(); got != "active" {
		t.Errorf("status = %q, want the stored value: it was not in the mask", got)
	}
	if got := merged.GetSourcedId(); got != "sch-1" {
		t.Errorf("sourced_id = %q, want it untouched", got)
	}
}

// TestMergeWriteDoesNotMutateTheStoredMessage guards the property that makes
// this a pure function: the caller's copy of the record must be unchanged, so a
// failed write leaves nothing half-applied in memory.
func TestMergeWriteDoesNotMutateTheStoredMessage(t *testing.T) {
	t.Parallel()

	stored := &v1.Org{SourcedId: "sch-1", Name: "Original"}
	incoming := &v1.OrgWrite{SourcedId: "sch-1", Name: "Replacement"}

	merged, err := mergeWrite(stored, incoming, []string{"name"})
	if err != nil {
		t.Fatalf("mergeWrite: %v", err)
	}

	if stored.GetName() != "Original" {
		t.Errorf("stored.name = %q, want it unmodified", stored.GetName())
	}
	if merged.GetName() != "Replacement" {
		t.Errorf("merged.name = %q, want the incoming value", merged.GetName())
	}
}

// TestMergeWriteClearsAnUnsetField is the reason a mask exists at all: naming a
// field whose incoming value is absent must erase it. Without this there would
// be no way to remove an optional value, only to change it.
func TestMergeWriteClearsAnUnsetField(t *testing.T) {
	t.Parallel()

	location := "Room 12"
	stored := &v1.Class{SourcedId: "cl-1", Title: "Algebra", Location: &location}
	incoming := &v1.ClassWrite{SourcedId: "cl-1", Title: "Algebra"} // Location unset

	merged, err := mergeWrite(stored, incoming, []string{"location"})
	if err != nil {
		t.Fatalf("mergeWrite: %v", err)
	}

	if merged.Location != nil {
		t.Errorf("location = %q, want it cleared", merged.GetLocation())
	}
	if merged.GetTitle() != "Algebra" {
		t.Errorf("title = %q, want it untouched", merged.GetTitle())
	}
}

func TestMergeWriteHandlesRepeatedFields(t *testing.T) {
	t.Parallel()

	stored := &v1.Course{SourcedId: "c-1", Grades: []string{"09"}, Subjects: []string{"Math"}}
	incoming := &v1.CourseWrite{SourcedId: "c-1", Grades: []string{"10", "11"}}

	merged, err := mergeWrite(stored, incoming, []string{"grades"})
	if err != nil {
		t.Fatalf("mergeWrite: %v", err)
	}

	if strings.Join(merged.GetGrades(), ",") != "10,11" {
		t.Errorf("grades = %v, want the incoming slice", merged.GetGrades())
	}
	if strings.Join(merged.GetSubjects(), ",") != "Math" {
		t.Errorf("subjects = %v, want the stored slice: it was not in the mask", merged.GetSubjects())
	}
}

// TestMergeWriteSharesNothingWithItsInputs pins what the closing proto.Clone is
// for. protoreflect's Set stores a list or sub-message by reference, so without
// that clone the merged payload would alias the record it was projected from,
// and a later edit of one would silently change the other.
func TestMergeWriteSharesNothingWithItsInputs(t *testing.T) {
	t.Parallel()

	stored := &v1.Course{SourcedId: "c-1", Subjects: []string{"Math"}}
	incoming := &v1.CourseWrite{SourcedId: "c-1", Grades: []string{"09"}}

	merged, err := mergeWrite(stored, incoming, []string{"grades"})
	if err != nil {
		t.Fatalf("mergeWrite: %v", err)
	}

	// Both a projected field and a masked one, since they arrive by different
	// paths through the function.
	merged.Subjects[0] = "Science"
	merged.Grades[0] = "12"

	if stored.GetSubjects()[0] != "Math" {
		t.Errorf("stored subject = %q; the merged payload aliases the record",
			stored.GetSubjects()[0])
	}
	if incoming.GetGrades()[0] != "09" {
		t.Errorf("incoming grade = %q; the merged payload aliases the request",
			incoming.GetGrades()[0])
	}
}

// TestMergeWriteRefusesADriftedPair covers the guard that makes the duplication
// between an entity and its write twin survivable. A field on the record with no
// counterpart on the write type, and no reservation saying the omission was
// deliberate, is refused rather than dropped.
//
// Org against CourseWrite is a stand-in for the real failure — a field added to
// an entity and not to its twin — which cannot be constructed from generated
// types precisely because the drift test keeps the pairs aligned.
func TestMergeWriteRefusesADriftedPair(t *testing.T) {
	t.Parallel()

	_, err := mergeWrite(&v1.Org{SourcedId: "sch-1"}, &v1.CourseWrite{}, nil)
	if err == nil {
		t.Fatal("mergeWrite accepted a mismatched pair; it must refuse one")
	}
	if !strings.Contains(err.Error(), "drifted") {
		t.Errorf("error = %v, want it to name the drift", err)
	}
}

// TestStorageTimestampTruncatesToMicroseconds pins the precision reasoning
// behind the version check. PostgreSQL stores microseconds; a nanosecond
// remainder in a client-supplied timestamp would make the check fail forever
// with no way for the caller to see why.
func TestStorageTimestampTruncatesToMicroseconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{
			name: "nanoseconds are dropped",
			in:   time.Date(2026, time.March, 9, 12, 0, 0, 123456789, time.UTC),
			want: time.Date(2026, time.March, 9, 12, 0, 0, 123456000, time.UTC),
		},
		{
			name: "an already-aligned value is unchanged",
			in:   time.Date(2026, time.March, 9, 12, 0, 0, 123456000, time.UTC),
			want: time.Date(2026, time.March, 9, 12, 0, 0, 123456000, time.UTC),
		},
		{
			name: "a whole second is unchanged",
			in:   time.Date(2026, time.March, 9, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, time.March, 9, 12, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := storageTimestamp(timestamppb.New(tt.in))
			if !got.Valid {
				t.Fatal("storageTimestamp returned an invalid value")
			}
			if !got.Time.Equal(tt.want) {
				t.Errorf("got %v, want %v", got.Time, tt.want)
			}
		})
	}
}

// TestStorageTimestampRoundTripsAWriteTimestamp is the case the version check
// actually depends on: what writeTimestamp stored must compare equal when the
// client sends it back.
func TestStorageTimestampRoundTripsAWriteTimestamp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 9, 12, 0, 0, 987654321, time.UTC)

	stored := writeTimestamp(now)
	returned := storageTimestamp(timestamppb.New(stored.Time))

	if !returned.Time.Equal(stored.Time) {
		t.Errorf("round trip gave %v, want %v", returned.Time, stored.Time)
	}
}

func TestStorageTimestampOfNilIsInvalid(t *testing.T) {
	t.Parallel()

	if got := storageTimestamp(nil); got.Valid {
		t.Errorf("storageTimestamp(nil) = %v, want an invalid value", got)
	}
}

func TestRequireExpectedVersion(t *testing.T) {
	t.Parallel()

	if _, err := requireExpectedVersion(nil); !errors.Is(err, errInvalid) {
		t.Errorf("error = %v, want one wrapping errInvalid", err)
	}

	now := time.Date(2026, time.March, 9, 12, 0, 0, 0, time.UTC)
	got, err := requireExpectedVersion(timestamppb.New(now))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Time.Equal(now) {
		t.Errorf("got %v, want %v", got.Time, now)
	}
}

// TestWriteTimestampIsAValidUTCMicrosecond pins the three things every write
// depends on, because none of them fails loudly when it is wrong.
//
// A Timestamptz with Valid false writes SQL NULL, and a NULL date_last_modified
// makes the record's own optimistic-concurrency check unsatisfiable: no expected
// version a caller sends can ever match, so every later update loses its race
// forever. The truncation matters for the same reason, since PostgreSQL keeps
// microseconds and would round away a nanosecond remainder the caller then sent
// back verbatim.
func TestWriteTimestampIsAValidUTCMicrosecond(t *testing.T) {
	t.Parallel()

	// Deliberately not UTC and not microsecond-aligned: a service clock read
	// through time.Now() is both.
	zone := time.FixedZone("UTC-5", -5*60*60)
	now := time.Date(2026, time.March, 9, 7, 0, 0, 123456789, zone)

	got := writeTimestamp(now)

	if !got.Valid {
		t.Fatal("writeTimestamp returned Valid false, which stores NULL and breaks every later version check")
	}
	if got.Time.Location() != time.UTC {
		t.Errorf("stored instant is in %v, want UTC", got.Time.Location())
	}
	if got.Time.Nanosecond()%1000 != 0 {
		t.Errorf("stored instant keeps sub-microsecond precision (%d ns) that PostgreSQL would round away",
			got.Time.Nanosecond())
	}
	if want := now.UTC().Truncate(time.Microsecond); !got.Time.Equal(want) {
		t.Errorf("stored instant = %v, want %v", got.Time, want)
	}
}

// TestRequireSessionDatesOrdersLexicographically pins that the comparison is on
// (year, month, day) in that order of significance.
//
// Every case differs from its partner in exactly one component, with the less
// significant components deliberately pointing the other way. A comparison that
// mixed up which component it was reading — or weighed them in the wrong order —
// passes a single mid-range example and fails here. The consequence of getting
// it wrong is a term stored with its end before its start, which every report
// built on the roster then inherits.
func TestRequireSessionDatesOrdersLexicographically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		start, end [3]int32
		wantErr    bool
	}{
		{
			name:  "the same day is allowed, since a session may be one day long",
			start: [3]int32{2026, 6, 15},
			end:   [3]int32{2026, 6, 15},
		},
		{
			// Year dominates: the month and day both go backwards and it is
			// still in order.
			name:  "a later year wins over an earlier month and day",
			start: [3]int32{2026, 12, 31},
			end:   [3]int32{2027, 1, 1},
		},
		{
			name:    "an earlier year loses to a later month and day",
			start:   [3]int32{2026, 1, 1},
			end:     [3]int32{2025, 12, 31},
			wantErr: true,
		},
		{
			// The case a school year actually takes: the same month a year
			// later, with an earlier day. Both the month and the day comparisons
			// point the wrong way here, so only the year check can accept it.
			name:  "the same month in a later year is in order",
			start: [3]int32{2026, 6, 15},
			end:   [3]int32{2027, 6, 10},
		},
		{
			name:    "the same month in an earlier year is refused",
			start:   [3]int32{2027, 6, 10},
			end:     [3]int32{2026, 6, 15},
			wantErr: true,
		},
		{
			// Month decides within a year, and the day goes backwards.
			name:  "a later month wins over an earlier day",
			start: [3]int32{2026, 6, 30},
			end:   [3]int32{2026, 7, 1},
		},
		{
			name:    "an earlier month loses to a later day",
			start:   [3]int32{2026, 7, 1},
			end:     [3]int32{2026, 6, 30},
			wantErr: true,
		},
		{
			name:  "the day decides when the year and month match",
			start: [3]int32{2026, 6, 1},
			end:   [3]int32{2026, 6, 2},
		},
		{
			name:    "an earlier day is refused when the year and month match",
			start:   [3]int32{2026, 6, 2},
			end:     [3]int32{2026, 6, 1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := requireSessionDates(&v1.AcademicSessionWrite{
				StartDate: &date.Date{Year: tt.start[0], Month: tt.start[1], Day: tt.start[2]},
				EndDate:   &date.Date{Year: tt.end[0], Month: tt.end[1], Day: tt.end[2]},
			})

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("%v to %v was refused: %v", tt.start, tt.end, err)
				}

				return
			}

			if err == nil {
				t.Fatalf("%v to %v was accepted, but the end precedes the start", tt.start, tt.end)
			}
			if !errors.Is(err, errInvalid) {
				t.Errorf("error = %v, want one wrapping errInvalid", err)
			}

			// The message has to name the dates the caller actually sent. A
			// curator reading "end_date is before start_date" with the wrong
			// numbers in it goes looking at the wrong field.
			for _, want := range []string{
				fmt.Sprintf("%d-%02d-%02d", tt.end[0], tt.end[1], tt.end[2]),
				fmt.Sprintf("%d-%02d-%02d", tt.start[0], tt.start[1], tt.start[2]),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %s", err, want)
				}
			}
		})
	}
}

// TestReservesNumberTreatsRangesAsHalfOpen covers the guard that lets mergeWrite
// tell a deliberate omission from an accident.
//
// "reserved 2;" means the range [2, 3). If the upper bound were inclusive, the
// field numbered just past a reservation would read as server-owned, and a field
// genuinely missing from a write type would be skipped in silence rather than
// reported as drift. That is the failure this whole pairing exists to prevent.
func TestReservesNumberTreatsRangesAsHalfOpen(t *testing.T) {
	t.Parallel()

	// OrgWrite reserves exactly 2, the number date_last_modified holds on Org,
	// and declares real fields either side of it.
	desc := (&v1.OrgWrite{}).ProtoReflect().Descriptor()

	tests := []struct {
		name   string
		number protoreflect.FieldNumber
		want   bool
	}{
		{name: "the number below the range is not reserved", number: 1},
		{name: "the first number in the range is reserved", number: 2, want: true},
		{name: "the number above the range is not reserved", number: 3},
		{name: "a number well clear of it is not reserved", number: 99},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := reservesNumber(desc, tt.number); got != tt.want {
				t.Errorf("reservesNumber(OrgWrite, %d) = %v, want %v", tt.number, got, tt.want)
			}
		})
	}
}

// TestMergeWriteDropsAStoredTimestamp uses the shape production always has.
//
// Every stored record reaching mergeWrite comes from toProto*, which always sets
// date_last_modified, so a record without one never occurs outside a test. The
// projection has to skip that field rather than try to copy it to a write type
// that has nowhere to put it.
func TestMergeWriteDropsAStoredTimestamp(t *testing.T) {
	t.Parallel()

	stored := &v1.Org{
		SourcedId: "sch-1", Status: "active", Name: "Old Name",
		Type: "school", Identifier: "S1",
		DateLastModified: timestamppb.New(time.Date(2026, time.March, 9, 12, 0, 0, 0, time.UTC)),
	}

	merged, err := mergeWrite(stored, &v1.OrgWrite{Name: "New Name"}, []string{"name"})
	if err != nil {
		t.Fatalf("mergeWrite: %v", err)
	}

	// Everything else came across, so the skip was of that one field and not of
	// the rest of the record.
	for _, field := range []struct{ name, got, want string }{
		{"name", merged.GetName(), "New Name"},
		{"identifier", merged.GetIdentifier(), "S1"},
		{"status", merged.GetStatus(), "active"},
		{"type", merged.GetType(), "school"},
	} {
		if field.got != field.want {
			t.Errorf("%s = %q, want %q", field.name, field.got, field.want)
		}
	}

	if stored.GetDateLastModified() == nil {
		t.Error("the stored record's timestamp was cleared; the projection must not modify it")
	}
}

// TestMergeWriteRefusesAPathTheWriteTypeLacks is the property the earlier
// implementation got wrong: it skipped a field it could not find, so an update
// naming a misspelled field reported success having changed nothing. A curator
// would have watched their edit vanish with no error to explain it.
func TestMergeWriteRefusesAPathTheWriteTypeLacks(t *testing.T) {
	t.Parallel()

	_, err := mergeWrite(&v1.Org{SourcedId: "sch-1"}, &v1.OrgWrite{}, []string{"nonsense"})
	if err == nil {
		t.Fatal("mergeWrite accepted a path that is not a field; the edit would silently do nothing")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("error = %v, want it to name the offending path", err)
	}
}
