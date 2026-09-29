package oneroster

import (
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	"google.golang.org/genproto/googleapis/type/date"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

// Every create and update RPC takes an <Entity>Write payload rather than the
// entity itself. A caller cannot supply date_last_modified because the write
// types do not declare it: the server sets it from its own clock on every write.
// Responses still carry the full entity, so a client gets the token back for its
// next update.
//
// The price of that split is duplication — six messages restating the entities'
// fields — and these two tests are what make it survivable. The first fails when
// a pair drifts. The second states the property the split exists for, in both
// directions.

// writePair is an entity, its write type, and a payload that is valid in every
// respect except that it has no timestamp to carry.
type writePair struct {
	name    string
	entity  proto.Message
	payload proto.Message
}

func schoolRef() *v1.OrgGUIDRef {
	return &v1.OrgGUIDRef{SourcedId: "sch-1", Href: "/orgs/sch-1", Type: "org"}
}

// writePairs is the fixture both tests run over. The payloads are written out
// rather than derived, because deriving them from the entities would mean
// trusting the very correspondence under test.
func writePairs() []writePair {
	return []writePair{
		{
			name:   "Org",
			entity: &v1.Org{},
			payload: &v1.OrgWrite{
				SourcedId: "sch-1", Status: "active",
				Name: "Example School", Type: "school", Identifier: "S1",
			},
		},
		{
			name:   "Course",
			entity: &v1.Course{},
			payload: &v1.CourseWrite{
				SourcedId: "crs-1", Status: "active",
				Title: "Algebra I", CourseCode: "ALG1", Org: schoolRef(),
			},
		},
		{
			name:   "Class",
			entity: &v1.Class{},
			payload: &v1.ClassWrite{
				SourcedId: "cls-1", Status: "active",
				Title:  "Algebra I, Period 2",
				Course: &v1.CourseGUIDRef{SourcedId: "crs-1", Href: "/courses/crs-1", Type: "course"},
				School: schoolRef(),
				// Required with minItems: 1 in the JSON Schema. A class with no
				// term is unreachable, which is why the constraint is here and
				// not only in the handler.
				Terms: []*v1.AcadSessionGUIDRef{
					{SourcedId: "term-1", Href: "/academicSessions/term-1", Type: "academicSession"},
				},
			},
		},
		{
			name:   "Enrollment",
			entity: &v1.Enrollment{},
			payload: &v1.EnrollmentWrite{
				SourcedId: "enr-1", Status: "active", Role: "student",
				Class:  &v1.ClassGUIDRef{SourcedId: "cls-1", Href: "/classes/cls-1", Type: "class"},
				School: schoolRef(),
				User:   &v1.UserGUIDRef{SourcedId: "usr-1", Href: "/users/usr-1", Type: "user"},
			},
		},
		{
			name:   "User",
			entity: &v1.User{},
			payload: &v1.UserWrite{
				SourcedId: "usr-1", Status: "active",
				GivenName: "Ada", FamilyName: "Lovelace", EnabledUser: true,
				// Required with minItems: 1, and the field visibility is scoped
				// through: a user stored without roles is invisible to every
				// query.
				Roles: []*v1.Role{{Org: schoolRef(), Role: "student", RoleType: "primary"}},
			},
		},
		{
			name:   "AcademicSession",
			entity: &v1.AcademicSession{},
			payload: &v1.AcademicSessionWrite{
				SourcedId: "term-1", Status: "active",
				Title: "Fall 2026", Type: "term", SchoolYear: "2027",
				StartDate: &date.Date{Year: 2026, Month: 8, Day: 1},
				EndDate:   &date.Date{Year: 2026, Month: 12, Day: 20},
			},
		},
	}
}

// TestWriteTypesMatchTheirEntities is the test that makes the write types safe
// to maintain. A field added to an entity and not to its twin is silently
// unwritable — no compile error, no validation failure, just a value a curator
// can never set. This turns that into a failing test.
//
// The correspondence asserted is exact in both directions, including the field
// numbers. Nothing on the wire depends on the two agreeing, but a reader
// comparing the pair does, and that reading is how the next field gets added
// correctly.
func TestWriteTypesMatchTheirEntities(t *testing.T) {
	t.Parallel()

	for _, pair := range writePairs() {
		t.Run(pair.name, func(t *testing.T) {
			t.Parallel()

			record := pair.entity.ProtoReflect().Descriptor()
			write := pair.payload.ProtoReflect().Descriptor()
			recordFields, writeFields := record.Fields(), write.Fields()

			for i := range recordFields.Len() {
				field := recordFields.Get(i)
				twin := writeFields.ByName(field.Name())

				// The one intended difference, and the reservation is what says
				// so. Without it the number could be reused for something
				// unrelated, and mergeWrite's projection guard would stop
				// distinguishing a deliberate omission from a mistake.
				if string(field.Name()) == dateLastModifiedField {
					if twin != nil {
						t.Errorf("%s declares date_last_modified; it is set by the server and must not be writable",
							write.Name())
					}
					if !reservesNumber(write, field.Number()) {
						t.Errorf("%s does not reserve %d, so the number could be reused for an unrelated field",
							write.Name(), field.Number())
					}

					continue
				}

				switch {
				case twin == nil:
					t.Errorf("%s has no %q, so the field is silently unwritable: add it to the write type",
						write.Name(), field.Name())
				case twin.Number() != field.Number():
					t.Errorf("%q is field %d on %s but %d on %s; keep the numbering aligned so the pair reads as a pair",
						field.Name(), field.Number(), record.Name(), twin.Number(), write.Name())
				case twin.Kind() != field.Kind():
					t.Errorf("%q is %s on %s but %s on %s",
						field.Name(), field.Kind(), record.Name(), twin.Kind(), write.Name())
				case twin.Cardinality() != field.Cardinality():
					t.Errorf("%q is %s on %s but %s on %s",
						field.Name(), field.Cardinality(), record.Name(),
						twin.Cardinality(), write.Name())
				case field.Message() != nil && twin.Message().FullName() != field.Message().FullName():
					t.Errorf("%q holds a %s on %s but a %s on %s",
						field.Name(), field.Message().FullName(), record.Name(),
						twin.Message().FullName(), write.Name())
				}
			}

			// And nothing extra. A field on the write type with no counterpart
			// would be accepted from a client, validated, and then dropped,
			// because there is nowhere to store it and nothing to return it in.
			for i := range writeFields.Len() {
				if name := writeFields.Get(i).Name(); recordFields.ByName(name) == nil {
					t.Errorf("%s declares %q, which %s does not; a value sent for it could never be stored or returned",
						write.Name(), name, record.Name())
				}
			}
		})
	}
}

// TestTheTimestampIsServerOwnedNotClientSupplied states the before and after of
// the write split as one property per entity.
//
// Before it, every create RPC refused a request that did not carry a
// date_last_modified — a value the server then immediately overwrote with its
// own clock. A caller had to invent a timestamp to have it discarded.
//
// Both halves matter. The payload must be accepted without one, and the record
// must still refuse to exist without one, because that timestamp is the
// concurrency token every subsequent update is checked against.
func TestTheTimestampIsServerOwnedNotClientSupplied(t *testing.T) {
	t.Parallel()

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}

	for _, pair := range writePairs() {
		t.Run(pair.name, func(t *testing.T) {
			t.Parallel()

			// These payloads go through the real validator, not the handlers,
			// because the handler suites never run the interceptor. This is the
			// check a live server performs.
			if validateErr := validator.Validate(pair.payload); validateErr != nil {
				t.Errorf("a write payload with no date_last_modified was rejected: %v", validateErr)
			}

			// Projecting the payload onto the entity carries every other required
			// field across and leaves the timestamp unset, so a rejection here
			// can only be about the timestamp.
			record, mergeErr := mergeWrite(pair.payload, pair.entity, nil)
			if mergeErr != nil {
				t.Fatalf("projecting the payload onto %s: %v", pair.name, mergeErr)
			}
			recordErr := validator.Validate(record)
			if recordErr == nil {
				t.Fatalf("%s validated without date_last_modified; a stored record must always carry the concurrency token",
					pair.name)
			}

			// And rejected for that reason, not because the projection dropped
			// some other required field. Without this the test would still pass
			// if mergeWrite silently carried nothing across.
			if !strings.Contains(recordErr.Error(), dateLastModifiedField) {
				t.Errorf("%s was rejected, but not for the timestamp: %v", pair.name, recordErr)
			}
		})
	}
}
