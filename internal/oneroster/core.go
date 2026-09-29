// Package oneroster serves the OneRoster v1.2 rostering model over Connect-RPC.
//
// The package is split along the functional-core/imperative-shell line: this
// file and cursor.go are pure, and handler.go is the only place that touches the
// database. Nothing in this file performs I/O or reads the clock, so every
// decision it makes is testable by passing a value and comparing the result.
package oneroster

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/internal/db"
)

// errInvalid marks a validation failure — the only error class the core
// produces. The shell maps it onto connect.CodeInvalidArgument, so a caller sees
// their own mistake rather than an internal error.
var errInvalid = errors.New("invalid argument")

// Paging bounds. A request asking for zero gets defaultPageSize; one asking for
// more than maxPageSize is clamped rather than refused, because a client that
// asks for too much wants as much as it can get, and refusing would make the
// limit a breaking change to discover.
const (
	defaultPageSize int32 = 100
	maxPageSize     int32 = 1000
)

// Bounds for a google.type.Date year. PostgreSQL's DATE spans 4713 BC to
// 5874897 AD, far wider than the int32 field, so the conversion is guarded.
const (
	minProtoYear = 1
	maxProtoYear = 9999
)

// OneRoster status values. The spec closes this vocabulary, unlike the type and
// role vocabularies which admit an "ext:" escape.
const (
	statusActive      = "active"
	statusToBeDeleted = "tobedeleted"
)

// Academic session types, used to back the /terms and /gradingPeriods methods as
// filters over one table.
const (
	sessionTypeTerm          = "term"
	sessionTypeGradingPeriod = "gradingPeriod"
)

// OneRoster roles that get their own list methods.
const (
	roleStudent = "student"
	roleTeacher = "teacher"
)

// The "type" discriminator OneRoster puts in every GUIDRef. These are the
// spec's values and are what the protovalidate `const:` constraints enforce.
const (
	guidRefOrg             = "org"
	guidRefAcademicSession = "academicSession"
	guidRefCourse          = "course"
	guidRefClass           = "class"
	guidRefUser            = "user"
)

// The collection path segment for each type, used to synthesize an href.
//
// Spelled out rather than derived from the type name: OneRoster's collections
// are not the type name plus "s" — "class" pluralises to "classes", not
// "classs" — and an href a client cannot dereference is worse than none.
const (
	collectionOrgs             = "orgs"
	collectionAcademicSessions = "academicSessions"
	collectionCourses          = "courses"
	collectionClasses          = "classes"
	collectionUsers            = "users"
)

// pageBounds resolves a list request's paging inputs.
//
// Returns the row limit to ask the database for and the keyset cursor to resume
// from. The limit is one larger than the caller's page size: the extra row is
// how the shell learns whether a further page exists without issuing a COUNT.
//
// Requires: pageSize >= 0 (protovalidate enforces this at the transport edge).
// Ensures:  limit is in [defaultPageSize+1, maxPageSize+1]; a malformed token
//
//	returns an error wrapping errInvalid.
func pageBounds(pageSize int32, pageToken string) (limit int32, after string, err error) {
	switch {
	case pageSize <= 0:
		pageSize = defaultPageSize
	case pageSize > maxPageSize:
		pageSize = maxPageSize
	}

	after, err = decodeCursor(pageToken)
	if err != nil {
		return 0, "", err
	}

	return pageSize + 1, after, nil
}

// paginate trims the sentinel row that pageBounds asked for and derives the next
// page token from the last row actually returned.
//
// The sentinel is the only evidence that another page exists, so it must be
// dropped here rather than reaching the response. Returns an empty token on the
// final page, which is what ends a client's iteration.
func paginate[T any](rows []T, limit int32, sourcedID func(T) string) (page []T, nextToken string) {
	if len(rows) < int(limit) {
		return rows, ""
	}
	page = rows[:limit-1]
	return page, encodeCursor(sourcedID(page[len(page)-1]))
}

// ── Scalar conversions ────────────────────────────────────────────────────────

// textPtr unwraps a nullable column into an optional proto field, preserving the
// difference between NULL and the empty string that text() discards.
func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

func boolPtr(b pgtype.Bool) *bool {
	if !b.Valid {
		return nil
	}
	v := b.Bool
	return &v
}

// timestamp converts a stored modification time. An invalid value yields nil
// rather than the zero instant, so a caller can tell "not set" from 1970.
func timestamp(t pgtype.Timestamptz) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time.UTC())
}

// protoDate converts a DATE column to google.type.Date, which carries no time
// zone — the right type for a birth date or a term boundary, where the calendar
// day is the fact and the instant is not.
func protoDate(d pgtype.Date) *date.Date {
	if !d.Valid {
		return nil
	}
	year, rawMonth, day := d.Time.Date()
	month := int(rawMonth)
	// Range-checked rather than converted blindly. PostgreSQL's DATE spans a far
	// wider range than google.type.Date's int32 fields, so an out-of-range value
	// means the row is corrupt — returning nil surfaces that, where a silent
	// truncation would serve a plausible wrong date.
	if year < minProtoYear || year > maxProtoYear ||
		month < 1 || month > 12 ||
		day < 1 || day > 31 {
		return nil
	}
	return &date.Date{Year: int32(year), Month: int32(month), Day: int32(day)}
}

// metadata decodes the JSONB extension bag.
//
// A malformed value is dropped rather than failing the read: metadata is an
// opaque vendor extension, and refusing to serve a roster because one record's
// extension bag is unparseable trades a total outage for a partial one.
func metadata(raw []byte) *structpb.Struct {
	if len(raw) == 0 {
		return nil
	}
	var s structpb.Struct
	if err := protojson.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return &s
}

// ── GUIDRef builders ──────────────────────────────────────────────────────────
//
// OneRoster represents every association as a GUIDRef: a sourced id, a type
// discriminator, and an href the client can dereference. The stored form is an
// id column plus an href column, so these rebuild the wire shape. Each returns
// nil for an absent association, which proto3 renders as an omitted field.

func orgRef(id, href pgtype.Text) *v1.OrgGUIDRef {
	if !id.Valid || id.String == "" {
		return nil
	}
	return &v1.OrgGUIDRef{
		SourcedId: id.String,
		Href:      refHref(href, collectionOrgs, id.String),
		Type:      guidRefOrg,
	}
}

func orgRefRequired(id string, href pgtype.Text) *v1.OrgGUIDRef {
	return &v1.OrgGUIDRef{
		SourcedId: id,
		Href:      refHref(href, collectionOrgs, id),
		Type:      guidRefOrg,
	}
}

func sessionRef(id, href pgtype.Text) *v1.AcadSessionGUIDRef {
	if !id.Valid || id.String == "" {
		return nil
	}
	return &v1.AcadSessionGUIDRef{
		SourcedId: id.String,
		Href:      refHref(href, collectionAcademicSessions, id.String),
		Type:      guidRefAcademicSession,
	}
}

// sessionRefRequired is sessionRef for a non-nullable column, where an absent
// reference is not representable.
func sessionRefRequired(id string, href pgtype.Text) *v1.AcadSessionGUIDRef {
	return &v1.AcadSessionGUIDRef{
		SourcedId: id,
		Href:      refHref(href, collectionAcademicSessions, id),
		Type:      guidRefAcademicSession,
	}
}

func courseRef(id string, href pgtype.Text) *v1.CourseGUIDRef {
	return &v1.CourseGUIDRef{
		SourcedId: id,
		Href:      refHref(href, collectionCourses, id),
		Type:      guidRefCourse,
	}
}

func classRef(id string, href pgtype.Text) *v1.ClassGUIDRef {
	return &v1.ClassGUIDRef{
		SourcedId: id,
		Href:      refHref(href, collectionClasses, id),
		Type:      guidRefClass,
	}
}

func userRef(id string, href pgtype.Text) *v1.UserGUIDRef {
	return &v1.UserGUIDRef{
		SourcedId: id,
		Href:      refHref(href, collectionUsers, id),
		Type:      guidRefUser,
	}
}

// refHref returns the stored href, or synthesizes one from the spec's
// collection path when the source system supplied none.
//
// Synthesizing beats returning empty: href is a required field in the OneRoster
// schema, so an empty one produces a response that fails the consumer's own
// validation. collection must be the plural path segment, not the type name.
func refHref(href pgtype.Text, collection, sourcedID string) string {
	if href.Valid && href.String != "" {
		return href.String
	}
	return "/ims/oneroster/rostering/v1p2/" + collection + "/" + sourcedID
}

// ── Entity conversions ────────────────────────────────────────────────────────
//
// One function per entity, each a total function from a stored row to its wire
// form. Associations that live in other tables (a class's terms, a user's roles)
// are attached by the shell after a batched load, because a pure function cannot
// fetch them and an N+1 per row is what the batch exists to avoid.

func toProtoOrg(row db.Org) *v1.Org {
	return &v1.Org{
		SourcedId:        row.SourcedID,
		Status:           checkedStatus(row.Status),
		DateLastModified: timestamp(row.DateLastModified),
		Metadata:         metadata(row.Metadata),
		Name:             row.Name,
		Type:             row.Type,
		Identifier:       row.Identifier,
		Parent:           orgRef(row.ParentSourcedID, row.ParentHref),
	}
}

func toProtoAcademicSession(row db.AcademicSession) *v1.AcademicSession {
	return &v1.AcademicSession{
		SourcedId:        row.SourcedID,
		Status:           checkedStatus(row.Status),
		DateLastModified: timestamp(row.DateLastModified),
		Metadata:         metadata(row.Metadata),
		Title:            row.Title,
		StartDate:        protoDate(row.StartDate),
		EndDate:          protoDate(row.EndDate),
		Type:             row.Type,
		SchoolYear:       row.SchoolYear,
		Parent:           sessionRef(row.ParentSourcedID, row.ParentHref),
	}
}

func toProtoCourse(row db.Course) *v1.Course {
	return &v1.Course{
		SourcedId:        row.SourcedID,
		Status:           checkedStatus(row.Status),
		DateLastModified: timestamp(row.DateLastModified),
		Metadata:         metadata(row.Metadata),
		Title:            row.Title,
		CourseCode:       row.CourseCode,
		Grades:           row.Grades,
		Subjects:         row.Subjects,
		SubjectCodes:     row.SubjectCodes,
		Org:              orgRef(row.OrgSourcedID, row.OrgHref),
		SchoolYear:       sessionRef(row.SchoolYearSourcedID, row.SchoolYearHref),
	}
}

func toProtoClass(row db.Class) *v1.Class {
	return &v1.Class{
		SourcedId:        row.SourcedID,
		Status:           checkedStatus(row.Status),
		DateLastModified: timestamp(row.DateLastModified),
		Metadata:         metadata(row.Metadata),
		Title:            row.Title,
		ClassCode:        textPtr(row.ClassCode),
		ClassType:        textPtr(row.ClassType),
		Location:         textPtr(row.Location),
		Grades:           row.Grades,
		Subjects:         row.Subjects,
		SubjectCodes:     row.SubjectCodes,
		Periods:          row.Periods,
		Course:           courseRef(row.CourseSourcedID, row.CourseHref),
		School:           orgRefRequired(row.SchoolSourcedID, row.SchoolHref),
	}
}

func toProtoUser(row db.OnerosterUser) *v1.User {
	return &v1.User{
		SourcedId:            row.SourcedID,
		Status:               checkedStatus(row.Status),
		DateLastModified:     timestamp(row.DateLastModified),
		Metadata:             metadata(row.Metadata),
		UserMasterIdentifier: textPtr(row.UserMasterIdentifier),
		Username:             textPtr(row.Username),
		EnabledUser:          row.EnabledUser,
		GivenName:            row.GivenName,
		FamilyName:           row.FamilyName,
		MiddleName:           textPtr(row.MiddleName),
		PreferredFirstName:   textPtr(row.PreferredFirstName),
		PreferredLastName:    textPtr(row.PreferredLastName),
		PreferredMiddleName:  textPtr(row.PreferredMiddleName),
		Pronouns:             textPtr(row.Pronouns),
		Grades:               row.Grades,
		Identifier:           textPtr(row.Identifier),
		Email:                textPtr(row.Email),
		Sms:                  textPtr(row.Sms),
		Phone:                textPtr(row.Phone),
		PrimaryOrg:           orgRef(row.PrimaryOrgSourcedID, row.PrimaryOrgHref),
		// Password is deliberately not copied. The column exists so a capture of
		// a source system round-trips; serving it would hand every reader of the
		// roster every user's credential.
	}
}

func toProtoEnrollment(row db.Enrollment) *v1.Enrollment {
	return &v1.Enrollment{
		SourcedId:        row.SourcedID,
		Status:           checkedStatus(row.Status),
		DateLastModified: timestamp(row.DateLastModified),
		Metadata:         metadata(row.Metadata),
		User:             userRef(row.UserSourcedID, row.UserHref),
		Class:            classRef(row.ClassSourcedID, row.ClassHref),
		School:           orgRefRequired(row.SchoolSourcedID, row.SchoolHref),
		Role:             row.Role,
		Primary:          boolPtr(row.IsPrimary),
		BeginDate:        protoDate(row.BeginDate),
		EndDate:          protoDate(row.EndDate),
	}
}

func toProtoDemographics(row db.Demographic) *v1.Demographics {
	return &v1.Demographics{
		SourcedId:                            row.SourcedID,
		Status:                               checkedStatus(row.Status),
		DateLastModified:                     timestamp(row.DateLastModified),
		Metadata:                             metadata(row.Metadata),
		BirthDate:                            protoDate(row.BirthDate),
		Sex:                                  textPtr(row.Sex),
		AmericanIndianOrAlaskaNative:         boolPtr(row.AmericanIndianOrAlaskaNative),
		Asian:                                boolPtr(row.Asian),
		BlackOrAfricanAmerican:               boolPtr(row.BlackOrAfricanAmerican),
		NativeHawaiianOrOtherPacificIslander: boolPtr(row.NativeHawaiianOrOtherPacificIslander),
		White:                                boolPtr(row.White),
		DemographicRaceTwoOrMoreRaces:        boolPtr(row.DemographicRaceTwoOrMoreRaces),
		HispanicOrLatinoEthnicity:            boolPtr(row.HispanicOrLatinoEthnicity),
		CountryOfBirthCode:                   textPtr(row.CountryOfBirthCode),
		StateOfBirthAbbreviation:             textPtr(row.StateOfBirthAbbreviation),
		CityOfBirth:                          textPtr(row.CityOfBirth),
		PublicSchoolResidenceStatus:          textPtr(row.PublicSchoolResidenceStatus),
	}
}

func toProtoRole(row db.UserRole) *v1.Role {
	return &v1.Role{
		RoleType:    checkedRoleType(row.RoleType),
		Role:        row.Role,
		Org:         orgRefRequired(row.OrgSourcedID, row.OrgHref),
		BeginDate:   protoDate(row.BeginDate),
		EndDate:     protoDate(row.EndDate),
		UserProfile: textPtr(row.UserProfile),
	}
}

func toProtoUserID(row db.UserIdentifier) *v1.UserId {
	return &v1.UserId{
		Type:       row.Type,
		Identifier: row.Identifier,
	}
}

// ── Closed vocabulary validation ──────────────────────────────────────────────
//
// status, role_type and every GUIDRef type are plain strings on the wire, so
// OneRoster's own spelling ("active", not ORG_STATUS_ACTIVE) reaches the client.
// Losing the protobuf enum means losing the compiler's guarantee that only
// declared values exist, so the vocabulary is enforced in two places instead:
//
//   inbound   protovalidate, from the `in:`/`const:` constraints on the proto
//             fields, rejecting a bad value before any handler runs;
//   outbound  the functions below, applied to every value read from the
//             database on its way to the wire.
//
// The database already CHECK-constrains these columns, so the outbound pass is
// genuinely defensive: it only fires when the constraint and this code disagree
// — a dropped constraint, a restore from an older dump, or a write that reached
// the table some other way.

// Valid values for the closed vocabularies, mirroring both the CHECK constraints
// in sql/schema/001_oneroster.sql and the protovalidate constraints in the proto
// files. All three must agree; these are the copy the serializer enforces.
var (
	validStatuses  = []string{statusActive, statusToBeDeleted}
	validRoleTypes = []string{"primary", "secondary"}
)

// checkedStatus returns status when it is one the API defines, and the empty
// string otherwise.
//
// Empty is deliberate: proto3 omits an empty string, so an unrecognized value
// disappears from the response rather than being passed through to a client that
// trusts the declared vocabulary, and rather than being coerced to a plausible
// neighbour. Coercing would be worse than either — mapping an unknown value to
// "active" could resurrect a record the source system had marked for deletion.
func checkedStatus(status string) string {
	return checkedVocabulary(status, validStatuses)
}

// checkedRoleType is checkedStatus for Role.role_type.
func checkedRoleType(roleType string) string {
	return checkedVocabulary(roleType, validRoleTypes)
}

// checkedVocabulary returns value when it appears in allowed, empty otherwise.
func checkedVocabulary(value string, allowed []string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return ""
}

// ── Request validation ────────────────────────────────────────────────────────

// requireSourcedID rejects a blank identifier before it reaches the database.
//
// protovalidate already enforces min_len on these fields, so this is the second
// line rather than the first; it exists because the handler is also reachable
// from tests and from any future caller that bypasses the interceptor.
func requireSourcedID(sourcedID string) (string, error) {
	trimmed := strings.TrimSpace(sourcedID)
	if trimmed == "" {
		return "", fmt.Errorf("%w: sourced_id cannot be blank", errInvalid)
	}
	return trimmed, nil
}
