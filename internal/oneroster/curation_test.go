package oneroster

import (
	"errors"
	"strings"
	"testing"
	"time"

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

func TestApplyMaskCopiesOnlyNamedFields(t *testing.T) {
	t.Parallel()

	stored := &v1.Org{
		SourcedId:  "sch-1",
		Name:       "Old Name",
		Type:       "school",
		Identifier: "S1",
		Status:     "active",
	}
	incoming := &v1.Org{
		SourcedId:  "sch-1",
		Name:       "New Name",
		Type:       "district", // named in no mask below, so must not move
		Identifier: "CHANGED",
		Status:     "tobedeleted",
	}

	merged := applyMask(stored, incoming, []string{"name", "identifier"})

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

// TestApplyMaskDoesNotMutateTheStoredMessage guards the property that makes this
// a pure function: the caller's copy of the record must be unchanged, so a
// failed write leaves nothing half-applied in memory.
func TestApplyMaskDoesNotMutateTheStoredMessage(t *testing.T) {
	t.Parallel()

	stored := &v1.Org{SourcedId: "sch-1", Name: "Original"}
	incoming := &v1.Org{SourcedId: "sch-1", Name: "Replacement"}

	merged := applyMask(stored, incoming, []string{"name"})

	if stored.GetName() != "Original" {
		t.Errorf("stored.name = %q, want it unmodified", stored.GetName())
	}
	if merged.GetName() != "Replacement" {
		t.Errorf("merged.name = %q, want the incoming value", merged.GetName())
	}
	if merged == stored {
		t.Error("applyMask returned the same pointer; it must return a distinct value")
	}
}

// TestApplyMaskClearsAnUnsetField is the reason a mask exists at all: naming a
// field whose incoming value is absent must erase it. Without this there would
// be no way to remove an optional value, only to change it.
func TestApplyMaskClearsAnUnsetField(t *testing.T) {
	t.Parallel()

	location := "Room 12"
	stored := &v1.Class{SourcedId: "cl-1", Title: "Algebra", Location: &location}
	incoming := &v1.Class{SourcedId: "cl-1", Title: "Algebra"} // Location unset

	merged := applyMask(stored, incoming, []string{"location"})

	if merged.Location != nil {
		t.Errorf("location = %q, want it cleared", merged.GetLocation())
	}
	if merged.GetTitle() != "Algebra" {
		t.Errorf("title = %q, want it untouched", merged.GetTitle())
	}
}

func TestApplyMaskHandlesRepeatedFields(t *testing.T) {
	t.Parallel()

	stored := &v1.Course{SourcedId: "c-1", Grades: []string{"09"}, Subjects: []string{"Math"}}
	incoming := &v1.Course{SourcedId: "c-1", Grades: []string{"10", "11"}}

	merged := applyMask(stored, incoming, []string{"grades"})

	if strings.Join(merged.GetGrades(), ",") != "10,11" {
		t.Errorf("grades = %v, want the incoming slice", merged.GetGrades())
	}
	if strings.Join(merged.GetSubjects(), ",") != "Math" {
		t.Errorf("subjects = %v, want the stored slice: it was not in the mask", merged.GetSubjects())
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
