package oneroster

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

// The inverse of core.go's toProto* functions: wire form to storage form, for
// the curation writes. Pure, like their counterparts.
//
// Each returns the fields a guarded write needs; the caller supplies the subject
// and the write timestamp, which are not properties of the entity.

// optText converts an optional proto string to a nullable column, preserving the
// distinction between absent and empty that a plain string would lose.
func optText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// refText takes the sourced id out of a GUIDRef for storage as a foreign key. A
// nil ref becomes NULL.
func refText(sourcedID string, present bool) pgtype.Text {
	if !present || sourcedID == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: sourcedID, Valid: true}
}

// optBool converts an optional proto bool to a nullable column.
func optBool(b *bool) pgtype.Bool {
	if b == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *b, Valid: true}
}

// storageDate converts google.type.Date to a DATE column.
//
// Returns NULL for a nil date and for one that is not a real calendar date,
// rather than letting time.Date normalise it — a plausible wrong date is worse
// than an absent one, because nobody notices it.
func storageDate(d *date.Date) pgtype.Date {
	if d == nil {
		return pgtype.Date{}
	}

	year, month, day, valid := calendarDate(d)
	if !valid {
		return pgtype.Date{}
	}
	return pgtype.Date{
		Time:  time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC),
		Valid: true,
	}
}

// calendarDate reports whether d names a real date, returning its components.
//
// A range check on each component is not enough: 31 is a legal day and 2 is a
// legal month, but 2026-02-31 is not a date. time.Date would normalise it to
// 2026-03-03 and return no error, so the only reliable test is to construct the
// value and see whether it came back unchanged. That also gets leap years right
// without a table.
//
// A fuzz target found the missing check here, having been given exactly that
// seed — see FuzzStorageDateNeverShiftsADate.
func calendarDate(d *date.Date) (year, month, day int, valid bool) {
	year, month, day = int(d.GetYear()), int(d.GetMonth()), int(d.GetDay())

	if year < minProtoYear || year > maxProtoYear ||
		month < 1 || month > 12 ||
		day < 1 || day > 31 {
		return 0, 0, 0, false
	}

	normalised := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if normalised.Year() != year ||
		int(normalised.Month()) != month ||
		normalised.Day() != day {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

// storageMetadata encodes the extension bag for a JSONB column.
//
// A nil struct becomes nil rather than the JSON literal "null", so the column is
// SQL NULL and round-trips back through metadata() as absent.
func storageMetadata(s *structpb.Struct) []byte {
	if s == nil {
		return nil
	}
	raw, err := protojson.Marshal(s)
	if err != nil {
		// structpb.Struct is always marshalable; a failure here would mean a
		// corrupt value, and dropping it matches how metadata() treats one on
		// the way out.
		return nil
	}
	return raw
}

// storageStrings normalises a repeated string field for a NOT NULL TEXT[]
// column, which cannot take nil.
func storageStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// ── Per-entity field sets ─────────────────────────────────────────────────────

// orgStorage is an Org's storable fields.
type orgStorage struct {
	Status           string
	DateLastModified pgtype.Timestamptz
	Metadata         []byte
	Name             string
	Type             string
	Identifier       string
	ParentSourcedID  pgtype.Text
	ParentHref       pgtype.Text
}

func toStorageOrg(org *v1.OrgWrite, now time.Time) orgStorage {
	parent := org.GetParent()
	return orgStorage{
		Status:           checkedStatus(org.GetStatus()),
		DateLastModified: writeTimestamp(now),
		Metadata:         storageMetadata(org.GetMetadata()),
		Name:             org.GetName(),
		Type:             org.GetType(),
		Identifier:       org.GetIdentifier(),
		ParentSourcedID:  refText(parent.GetSourcedId(), parent != nil),
		ParentHref:       refText(parent.GetHref(), parent != nil),
	}
}

// courseStorage is a Course's storable fields.
type courseStorage struct {
	Status              string
	DateLastModified    pgtype.Timestamptz
	Metadata            []byte
	Title               string
	CourseCode          string
	Grades              []string
	Subjects            []string
	SubjectCodes        []string
	OrgSourcedID        pgtype.Text
	OrgHref             pgtype.Text
	SchoolYearSourcedID pgtype.Text
	SchoolYearHref      pgtype.Text
}

func toStorageCourse(course *v1.CourseWrite, now time.Time) courseStorage {
	org, year := course.GetOrg(), course.GetSchoolYear()
	return courseStorage{
		Status:              checkedStatus(course.GetStatus()),
		DateLastModified:    writeTimestamp(now),
		Metadata:            storageMetadata(course.GetMetadata()),
		Title:               course.GetTitle(),
		CourseCode:          course.GetCourseCode(),
		Grades:              storageStrings(course.GetGrades()),
		Subjects:            storageStrings(course.GetSubjects()),
		SubjectCodes:        storageStrings(course.GetSubjectCodes()),
		OrgSourcedID:        refText(org.GetSourcedId(), org != nil),
		OrgHref:             refText(org.GetHref(), org != nil),
		SchoolYearSourcedID: refText(year.GetSourcedId(), year != nil),
		SchoolYearHref:      refText(year.GetHref(), year != nil),
	}
}

// classStorage is a Class's storable fields.
type classStorage struct {
	Status           string
	DateLastModified pgtype.Timestamptz
	Metadata         []byte
	Title            string
	ClassCode        pgtype.Text
	ClassType        pgtype.Text
	Location         pgtype.Text
	Grades           []string
	Subjects         []string
	SubjectCodes     []string
	Periods          []string
	CourseSourcedID  string
	CourseHref       pgtype.Text
	SchoolSourcedID  string
	SchoolHref       pgtype.Text
}

func toStorageClass(class *v1.ClassWrite, now time.Time) classStorage {
	course, school := class.GetCourse(), class.GetSchool()
	return classStorage{
		Status:           checkedStatus(class.GetStatus()),
		DateLastModified: writeTimestamp(now),
		Metadata:         storageMetadata(class.GetMetadata()),
		Title:            class.GetTitle(),
		ClassCode:        optText(class.ClassCode),
		ClassType:        optText(class.ClassType),
		Location:         optText(class.Location),
		Grades:           storageStrings(class.GetGrades()),
		Subjects:         storageStrings(class.GetSubjects()),
		SubjectCodes:     storageStrings(class.GetSubjectCodes()),
		Periods:          storageStrings(class.GetPeriods()),
		CourseSourcedID:  course.GetSourcedId(),
		CourseHref:       refText(course.GetHref(), course != nil),
		SchoolSourcedID:  school.GetSourcedId(),
		SchoolHref:       refText(school.GetHref(), school != nil),
	}
}

// enrollmentStorage is an Enrollment's storable fields.
type enrollmentStorage struct {
	Status           string
	DateLastModified pgtype.Timestamptz
	Metadata         []byte
	UserSourcedID    string
	UserHref         pgtype.Text
	ClassSourcedID   string
	ClassHref        pgtype.Text
	SchoolSourcedID  string
	SchoolHref       pgtype.Text
	Role             string
	IsPrimary        pgtype.Bool
	BeginDate        pgtype.Date
	EndDate          pgtype.Date
}

func toStorageEnrollment(enrollment *v1.EnrollmentWrite, now time.Time) enrollmentStorage {
	user, class, school := enrollment.GetUser(), enrollment.GetClass(), enrollment.GetSchool()
	return enrollmentStorage{
		Status:           checkedStatus(enrollment.GetStatus()),
		DateLastModified: writeTimestamp(now),
		Metadata:         storageMetadata(enrollment.GetMetadata()),
		UserSourcedID:    user.GetSourcedId(),
		UserHref:         refText(user.GetHref(), user != nil),
		ClassSourcedID:   class.GetSourcedId(),
		ClassHref:        refText(class.GetHref(), class != nil),
		SchoolSourcedID:  school.GetSourcedId(),
		SchoolHref:       refText(school.GetHref(), school != nil),
		Role:             enrollment.GetRole(),
		IsPrimary:        optBool(enrollment.Primary),
		BeginDate:        storageDate(enrollment.GetBeginDate()),
		EndDate:          storageDate(enrollment.GetEndDate()),
	}
}

// userStorage is a User's storable fields.
//
// Password is absent deliberately: toProtoUser never serves it, so a curator
// never receives one to send back, and accepting it here would let an update
// that echoed a response blank the column.
type userStorage struct {
	Status               string
	DateLastModified     pgtype.Timestamptz
	Metadata             []byte
	UserMasterIdentifier pgtype.Text
	Username             pgtype.Text
	EnabledUser          bool
	GivenName            string
	FamilyName           string
	MiddleName           pgtype.Text
	PreferredFirstName   pgtype.Text
	PreferredLastName    pgtype.Text
	PreferredMiddleName  pgtype.Text
	Pronouns             pgtype.Text
	Grades               []string
	Identifier           pgtype.Text
	Email                pgtype.Text
	Sms                  pgtype.Text
	Phone                pgtype.Text
	PrimaryOrgSourcedID  pgtype.Text
	PrimaryOrgHref       pgtype.Text
}

func toStorageUser(user *v1.UserWrite, now time.Time) userStorage {
	org := user.GetPrimaryOrg()
	return userStorage{
		Status:               checkedStatus(user.GetStatus()),
		DateLastModified:     writeTimestamp(now),
		Metadata:             storageMetadata(user.GetMetadata()),
		UserMasterIdentifier: optText(user.UserMasterIdentifier),
		Username:             optText(user.Username),
		EnabledUser:          user.GetEnabledUser(),
		GivenName:            user.GetGivenName(),
		FamilyName:           user.GetFamilyName(),
		MiddleName:           optText(user.MiddleName),
		PreferredFirstName:   optText(user.PreferredFirstName),
		PreferredLastName:    optText(user.PreferredLastName),
		PreferredMiddleName:  optText(user.PreferredMiddleName),
		Pronouns:             optText(user.Pronouns),
		Grades:               storageStrings(user.GetGrades()),
		Identifier:           optText(user.Identifier),
		Email:                optText(user.Email),
		Sms:                  optText(user.Sms),
		Phone:                optText(user.Phone),
		PrimaryOrgSourcedID:  refText(org.GetSourcedId(), org != nil),
		PrimaryOrgHref:       refText(org.GetHref(), org != nil),
	}
}

// academicSessionStorage is an AcademicSession's storable fields.
type academicSessionStorage struct {
	Status           string
	DateLastModified pgtype.Timestamptz
	Metadata         []byte
	Title            string
	StartDate        pgtype.Date
	EndDate          pgtype.Date
	Type             string
	SchoolYear       string
	ParentSourcedID  pgtype.Text
	ParentHref       pgtype.Text
}

func toStorageAcademicSession(session *v1.AcademicSessionWrite, now time.Time) academicSessionStorage {
	parent := session.GetParent()
	return academicSessionStorage{
		Status:           checkedStatus(session.GetStatus()),
		DateLastModified: writeTimestamp(now),
		Metadata:         storageMetadata(session.GetMetadata()),
		Title:            session.GetTitle(),
		StartDate:        storageDate(session.GetStartDate()),
		EndDate:          storageDate(session.GetEndDate()),
		Type:             session.GetType(),
		SchoolYear:       session.GetSchoolYear(),
		ParentSourcedID:  refText(parent.GetSourcedId(), parent != nil),
		ParentHref:       refText(parent.GetHref(), parent != nil),
	}
}
