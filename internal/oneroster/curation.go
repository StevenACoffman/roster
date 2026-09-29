package oneroster

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

// The pure half of curation: deciding which fields an update changes, and
// deciding whether the caller's view of the record is still current. Neither
// touches a database or reads a clock, so both are testable by passing values.

// sourcedIDField is the primary key, and is therefore not editable. An update
// that moved it would be an insert wearing a disguise, leaving the original row
// behind under a name nobody asked about.
const sourcedIDField = "sourced_id"

// dateLastModifiedField is excluded from a mask for a different reason than
// sourced_id: it is the server's own bookkeeping and the concurrency token. A
// caller that could set it directly could defeat the version check.
const dateLastModifiedField = "date_last_modified"

// requireMask validates an update mask against the message it will be applied
// to, returning the field paths to copy.
//
// A nil or empty mask is refused rather than treated as "replace every field".
// That default would mean a client bug which dropped the mask silently blanked
// every field it had not sent — the most destructive possible reading of an
// ambiguous request. A caller that genuinely wants to replace everything must
// name the fields.
//
// Ensures: every returned path names a settable top-level field of msg; the
// error wraps errInvalid so the shell maps it to InvalidArgument.
func requireMask(mask *fieldmaskpb.FieldMask, msg proto.Message) ([]string, error) {
	if mask == nil || len(mask.GetPaths()) == 0 {
		return nil, fmt.Errorf(
			"%w: update_mask is required and must name at least one field; "+
				"an absent mask is not treated as a request to replace every field",
			errInvalid)
	}

	fields := msg.ProtoReflect().Descriptor().Fields()
	seen := make(map[string]bool, len(mask.GetPaths()))
	paths := make([]string, 0, len(mask.GetPaths()))

	for _, path := range mask.GetPaths() {
		switch {
		case strings.Contains(path, "."):
			// Nested paths would need merge semantics for the intermediate
			// message — whether "parent.sourced_id" clears the rest of parent or
			// leaves it. Refused rather than guessed; a caller updates the whole
			// sub-message by naming it.
			return nil, fmt.Errorf(
				"%w: update_mask path %q is nested; name the top-level field instead",
				errInvalid, path)
		case path == sourcedIDField:
			return nil, fmt.Errorf(
				"%w: sourced_id identifies the record and cannot be updated",
				errInvalid)
		case path == dateLastModifiedField:
			return nil, fmt.Errorf(
				"%w: date_last_modified is set by the server and is the concurrency token",
				errInvalid)
		case seen[path]:
			// Harmless to apply twice, but a duplicate means the caller built the
			// mask wrong and would rather hear about it.
			return nil, fmt.Errorf("%w: update_mask names %q more than once", errInvalid, path)
		}

		if fields.ByName(protoreflect.Name(path)) == nil {
			return nil, fmt.Errorf(
				"%w: update_mask names %q, which is not a field of %s",
				errInvalid, path, msg.ProtoReflect().Descriptor().Name())
		}

		seen[path] = true
		paths = append(paths, path)
	}

	return paths, nil
}

// mergeWrite returns the write payload describing stored after the named fields
// have been replaced by the ones incoming carries.
//
// stored and incoming are deliberately different message types: the stored
// record (Org) against the write payload (OrgWrite), which omits the
// server-owned date_last_modified. The merge runs in the write type, so what
// comes back is exactly what a caller could have sent to replace the record
// wholesale, and one toStorage function per entity serves both creates and
// updates.
//
// Two phases, and only the first crosses the type boundary:
//
//  1. project every field of stored onto the write type, matched by name. This
//     is total: a field of the entity that the write type does not declare is an
//     error unless the write type reserves its number, which is how the
//     deliberate omission of date_last_modified is distinguished from a field
//     someone forgot to add to the pair.
//  2. overwrite the masked paths from incoming, which is the same type as the
//     result, so there is no cross-type question left to get wrong.
//
// One function for every entity rather than one per entity: a field mask is a
// protobuf concept, so protoreflect can honour it generically. Six hand-written
// merges would be the same logic six times, each able to drift from the others
// and each needing its own copy of these tests.
//
// A field named in paths but unset on incoming is cleared on the result, not
// skipped. That is what makes a mask able to erase an optional value — without
// it there would be no way to remove a class's location, only to change it.
//
// Requires: paths came from requireMask against incoming.
// Ensures:  stored and incoming are not modified, and the result shares no
//
//	list, map or sub-message with either.
func mergeWrite[W proto.Message](stored proto.Message, incoming W, paths []string) (W, error) {
	var zero W

	draft := incoming.ProtoReflect().New()
	writeDesc := draft.Descriptor()
	writeFields := writeDesc.Fields()

	record := stored.ProtoReflect()
	recordFields := record.Descriptor().Fields()

	for i := range recordFields.Len() {
		recordField := recordFields.Get(i)
		writeField := writeFields.ByName(recordField.Name())

		switch {
		case writeField == nil && reservesNumber(writeDesc, recordField.Number()):
			// The intended asymmetry: a server-owned field, absent from the write
			// type and its number reserved there to say so.
			continue
		case writeField == nil:
			return zero, fmt.Errorf(
				"mergeWrite: %s has no field %q and does not reserve %d, so the pair has drifted",
				writeDesc.Name(), recordField.Name(), recordField.Number())
		case writeField.Kind() != recordField.Kind():
			return zero, fmt.Errorf(
				"mergeWrite: field %q is %s on %s and %s on %s",
				recordField.Name(), recordField.Kind(), record.Descriptor().Name(),
				writeField.Kind(), writeDesc.Name())
		}

		if record.Has(recordField) {
			draft.Set(writeField, record.Get(recordField))
		}
	}

	edit := incoming.ProtoReflect()
	for _, path := range paths {
		field := writeFields.ByName(protoreflect.Name(path))
		if field == nil {
			// requireMask validated these paths against this same descriptor, so
			// reaching here means a caller bypassed it.
			return zero, fmt.Errorf(
				"mergeWrite: %q is not a field of %s", path, writeDesc.Name())
		}
		if edit.Has(field) {
			draft.Set(field, edit.Get(field))
		} else {
			draft.Clear(field)
		}
	}

	// Set stores composite values by reference, so the draft still shares lists
	// and sub-messages with stored and incoming. Cloning once severs all of it,
	// which is cheaper to verify than a per-kind deep copy in the loops above.
	merged, _ := proto.Clone(draft.Interface()).(W)

	return merged, nil
}

// reservesNumber reports whether desc reserves the given field number.
//
// Reserved ranges are half-open, as in "reserved 2;" giving [2, 3).
func reservesNumber(desc protoreflect.MessageDescriptor, number protoreflect.FieldNumber) bool {
	ranges := desc.ReservedRanges()
	for i := range ranges.Len() {
		if bounds := ranges.Get(i); number >= bounds[0] && number < bounds[1] {
			return true
		}
	}
	return false
}

// storageTimestamp converts a wire timestamp for comparison against a stored
// one, truncating to microseconds.
//
// PostgreSQL TIMESTAMPTZ holds microseconds; protobuf Timestamp holds
// nanoseconds. A value that round-tripped through the database is already
// microsecond-aligned, but a client is free to synthesise one that is not — and
// a nanosecond remainder would make the version check fail forever, with the
// caller unable to see why. Truncating makes the comparison mean what the
// storage can actually represent.
func storageTimestamp(ts *timestamppb.Timestamp) pgtype.Timestamptz {
	if ts == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{
		Time:  ts.AsTime().UTC().Truncate(time.Microsecond),
		Valid: true,
	}
}

// writeTimestamp converts the instant a write happened into storage form.
//
// The clock is read by the shell and passed in, so nothing in this file calls
// time.Now(). That is what lets these functions be tested without fake time,
// and what stops a test that depends on the clock from flaking.
func writeTimestamp(now time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{
		Time:  now.UTC().Truncate(time.Microsecond),
		Valid: true,
	}
}

// requireExpectedVersion rejects an update that carries no concurrency token.
//
// Without one the write would be last-writer-wins, and a curator's edit could be
// discarded by an overlapping import with neither party told. The token is
// required rather than optional so that the safe path is the only path.
func requireExpectedVersion(ts *timestamppb.Timestamp) (pgtype.Timestamptz, error) {
	if ts == nil {
		return pgtype.Timestamptz{}, fmt.Errorf(
			"%w: expected_date_last_modified is required; send the value read from the record "+
				"so a concurrent write is reported rather than silently overwritten",
			errInvalid)
	}
	return storageTimestamp(ts), nil
}

// ── Required repeated fields ──────────────────────────────────────────────────
//
// Two OneRoster fields are `required` with `minItems: 1` in the JSON Schema:
// ClassDType.terms and UserDType.roles. A junction table cannot express that
// with a foreign key, so it is enforced here and the rows are written in the
// same transaction as their parent.
//
// For a user this is not merely a validity question. The read and write
// authorization predicates both scope a user through their roles, so a user
// stored with none is invisible to every list, unfetchable by id, and
// impossible to update or delete — created into a state nothing can reach.

// requireTerms rejects a class whose term list is empty.
//
// Terms are how a class is placed in time; a class belonging to no academic
// session cannot be scheduled, reported on, or rolled over at year end.
func requireTerms(terms []*v1.AcadSessionGUIDRef) error {
	if len(terms) == 0 {
		return fmt.Errorf(
			"%w: terms must name at least one academic session; "+
				"a class that belongs to no term cannot be scheduled",
			errInvalid)
	}
	for i, term := range terms {
		if term.GetSourcedId() == "" {
			return fmt.Errorf("%w: terms[%d] has no sourced_id", errInvalid, i)
		}
	}
	return nil
}

// requireRoles rejects a user with no roles.
//
// Beyond the schema requirement, a role is what places a user in an org, and an
// org is what every authorization decision about them is scoped to. A user with
// no roles would be stored and then be unreachable by any caller.
func requireRoles(roles []*v1.Role) error {
	if len(roles) == 0 {
		return fmt.Errorf(
			"%w: roles must name at least one role; a user with no role belongs to no org "+
				"and would not be visible to any caller, including the one creating them",
			errInvalid)
	}
	primaries := 0
	for i, role := range roles {
		if role.GetOrg().GetSourcedId() == "" {
			return fmt.Errorf("%w: roles[%d] names no org", errInvalid, i)
		}
		if checkedRoleType(role.GetRoleType()) == "" {
			return fmt.Errorf("%w: roles[%d] has role_type %q, want primary or secondary",
				errInvalid, i, role.GetRoleType())
		}
		if role.GetRoleType() == "primary" {
			primaries++
		}
	}
	// The schema does not say so, but the database does: a partial unique index
	// permits one primary role per user. Reporting it here names the field
	// rather than surfacing a constraint violation.
	if primaries != 1 {
		return fmt.Errorf(
			"%w: exactly one role must have role_type \"primary\", got %d",
			errInvalid, primaries)
	}
	return nil
}

// termRefs splits a term list into the parallel arrays ReplaceClassTerms takes.
func termRefs(terms []*v1.AcadSessionGUIDRef) (sourcedIDs, hrefs []string) {
	sourcedIDs = make([]string, 0, len(terms))
	hrefs = make([]string, 0, len(terms))
	for _, term := range terms {
		sourcedIDs = append(sourcedIDs, term.GetSourcedId())
		hrefs = append(hrefs, term.GetHref())
	}
	return sourcedIDs, hrefs
}

// primaryRoleOrg returns the org of the user's primary role.
//
// That org, not primaryOrg, is what a creation is authorized against:
// primaryOrg is optional in the schema, while the primary role is required and
// is what every visibility predicate joins on. requireRoles has already
// established that exactly one primary role exists.
func primaryRoleOrg(roles []*v1.Role) string {
	for _, role := range roles {
		if role.GetRoleType() == "primary" {
			return role.GetOrg().GetSourcedId()
		}
	}
	return ""
}

// requireSessionDates rejects a session whose dates are missing or inverted.
//
// startDate and endDate are both required by the JSON Schema, and the database
// carries a CHECK that end_date >= start_date. Checking here names the field
// that is wrong, where the constraint violation would surface only as
// failed_precondition with a constraint name the caller cannot act on.
func requireSessionDates(session *v1.AcademicSessionWrite) error {
	start, end := session.GetStartDate(), session.GetEndDate()
	if start == nil {
		return fmt.Errorf("%w: start_date is required", errInvalid)
	}
	if end == nil {
		return fmt.Errorf("%w: end_date is required", errInvalid)
	}

	// Both must be real calendar dates before they can be ordered. Without this
	// a start of 2026-02-31 would pass the comparison, then be dropped by
	// storageDate, then fail the NOT NULL constraint — surfacing as a constraint
	// name rather than as the field the caller got wrong.
	if _, _, _, valid := calendarDate(start); !valid {
		return fmt.Errorf("%w: start_date %d-%02d-%02d is not a calendar date",
			errInvalid, start.GetYear(), start.GetMonth(), start.GetDay())
	}
	if _, _, _, valid := calendarDate(end); !valid {
		return fmt.Errorf("%w: end_date %d-%02d-%02d is not a calendar date",
			errInvalid, end.GetYear(), end.GetMonth(), end.GetDay())
	}

	// Compared as a (year, month, day) tuple, which is now safe because both are
	// known to be real dates.
	startKey := [3]int32{start.GetYear(), start.GetMonth(), start.GetDay()}
	endKey := [3]int32{end.GetYear(), end.GetMonth(), end.GetDay()}
	if endKey[0] < startKey[0] ||
		(endKey[0] == startKey[0] && endKey[1] < startKey[1]) ||
		(endKey[0] == startKey[0] && endKey[1] == startKey[1] && endKey[2] < startKey[2]) {
		return fmt.Errorf(
			"%w: end_date %d-%02d-%02d is before start_date %d-%02d-%02d",
			errInvalid, endKey[0], endKey[1], endKey[2], startKey[0], startKey[1], startKey[2])
	}
	return nil
}
