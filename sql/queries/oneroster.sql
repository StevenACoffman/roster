-- OneRoster rostering queries.
--
-- Upserts rather than inserts throughout: a roster arrives as a full snapshot
-- from the source system, so re-importing must converge rather than conflict.

-- ── Org ───────────────────────────────────────────────────────────────────────

-- name: UpsertOrg :one
INSERT INTO org (
    sourced_id, status, date_last_modified, metadata, name, type, identifier,
    parent_sourced_id, parent_href
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (sourced_id) DO UPDATE SET
    status             = EXCLUDED.status,
    date_last_modified = EXCLUDED.date_last_modified,
    metadata           = EXCLUDED.metadata,
    name               = EXCLUDED.name,
    type               = EXCLUDED.type,
    identifier         = EXCLUDED.identifier,
    parent_sourced_id  = EXCLUDED.parent_sourced_id,
    parent_href        = EXCLUDED.parent_href
RETURNING *;

-- name: UpsertAcademicSession :one
INSERT INTO academic_session (
    sourced_id, status, date_last_modified, metadata, title, start_date,
    end_date, type, school_year, parent_sourced_id, parent_href
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (sourced_id) DO UPDATE SET
    status             = EXCLUDED.status,
    date_last_modified = EXCLUDED.date_last_modified,
    metadata           = EXCLUDED.metadata,
    title              = EXCLUDED.title,
    start_date         = EXCLUDED.start_date,
    end_date           = EXCLUDED.end_date,
    type               = EXCLUDED.type,
    school_year        = EXCLUDED.school_year,
    parent_sourced_id  = EXCLUDED.parent_sourced_id,
    parent_href        = EXCLUDED.parent_href
RETURNING *;

-- name: UpsertCourse :one
INSERT INTO course (
    sourced_id, status, date_last_modified, metadata, title, course_code,
    grades, subjects, subject_codes, org_sourced_id, org_href,
    school_year_sourced_id, school_year_href
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (sourced_id) DO UPDATE SET
    status                 = EXCLUDED.status,
    date_last_modified     = EXCLUDED.date_last_modified,
    metadata               = EXCLUDED.metadata,
    title                  = EXCLUDED.title,
    course_code            = EXCLUDED.course_code,
    grades                 = EXCLUDED.grades,
    subjects               = EXCLUDED.subjects,
    subject_codes          = EXCLUDED.subject_codes,
    org_sourced_id         = EXCLUDED.org_sourced_id,
    org_href               = EXCLUDED.org_href,
    school_year_sourced_id = EXCLUDED.school_year_sourced_id,
    school_year_href       = EXCLUDED.school_year_href
RETURNING *;

-- name: UpsertClass :one
INSERT INTO class (
    sourced_id, status, date_last_modified, metadata, title, class_code,
    class_type, location, grades, subjects, subject_codes, periods,
    course_sourced_id, course_href, school_sourced_id, school_href
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (sourced_id) DO UPDATE SET
    status             = EXCLUDED.status,
    date_last_modified = EXCLUDED.date_last_modified,
    metadata           = EXCLUDED.metadata,
    title              = EXCLUDED.title,
    class_code         = EXCLUDED.class_code,
    class_type         = EXCLUDED.class_type,
    location           = EXCLUDED.location,
    grades             = EXCLUDED.grades,
    subjects           = EXCLUDED.subjects,
    subject_codes      = EXCLUDED.subject_codes,
    periods            = EXCLUDED.periods,
    course_sourced_id  = EXCLUDED.course_sourced_id,
    course_href        = EXCLUDED.course_href,
    school_sourced_id  = EXCLUDED.school_sourced_id,
    school_href        = EXCLUDED.school_href
RETURNING *;

-- name: ReplaceClassTerms :exec
-- Terms arrive as an ordered list, inserted in one round trip. WITH ORDINALITY
-- supplies the position, so the caller passes parallel arrays and no ordinal
-- array; hrefs are subscripted by that position. Single-argument unnest because
-- sqlc's catalog does not carry the multi-argument form.
INSERT INTO class_term (class_sourced_id, academic_session_sourced_id, academic_session_href, ordinal)
SELECT $1, term.sourced_id, ($3::text[])[term.ord], (term.ord - 1)::int
FROM unnest($2::text[]) WITH ORDINALITY AS term(sourced_id, ord)
ON CONFLICT (class_sourced_id, academic_session_sourced_id) DO UPDATE SET
    academic_session_href = EXCLUDED.academic_session_href,
    ordinal               = EXCLUDED.ordinal;

-- name: DeleteClassTerms :exec
DELETE FROM class_term WHERE class_sourced_id = $1;

-- name: UpsertUser :one
INSERT INTO oneroster_user (
    sourced_id, status, date_last_modified, metadata, user_master_identifier,
    username, enabled_user, given_name, family_name, middle_name,
    preferred_first_name, preferred_last_name, preferred_middle_name, pronouns,
    grades, identifier, email, sms, phone, password,
    primary_org_sourced_id, primary_org_href
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20, $21, $22
)
ON CONFLICT (sourced_id) DO UPDATE SET
    status                 = EXCLUDED.status,
    date_last_modified     = EXCLUDED.date_last_modified,
    metadata               = EXCLUDED.metadata,
    user_master_identifier = EXCLUDED.user_master_identifier,
    username               = EXCLUDED.username,
    enabled_user           = EXCLUDED.enabled_user,
    given_name             = EXCLUDED.given_name,
    family_name            = EXCLUDED.family_name,
    middle_name            = EXCLUDED.middle_name,
    preferred_first_name   = EXCLUDED.preferred_first_name,
    preferred_last_name    = EXCLUDED.preferred_last_name,
    preferred_middle_name  = EXCLUDED.preferred_middle_name,
    pronouns               = EXCLUDED.pronouns,
    grades                 = EXCLUDED.grades,
    identifier             = EXCLUDED.identifier,
    email                  = EXCLUDED.email,
    sms                    = EXCLUDED.sms,
    phone                  = EXCLUDED.phone,
    password               = EXCLUDED.password,
    primary_org_sourced_id = EXCLUDED.primary_org_sourced_id,
    primary_org_href       = EXCLUDED.primary_org_href
RETURNING *;

-- name: ReplaceUserAgents :exec
INSERT INTO user_agent (user_sourced_id, agent_sourced_id, agent_href, ordinal)
SELECT $1, agent.sourced_id, ($3::text[])[agent.ord], (agent.ord - 1)::int
FROM unnest($2::text[]) WITH ORDINALITY AS agent(sourced_id, ord)
ON CONFLICT (user_sourced_id, agent_sourced_id) DO UPDATE SET
    agent_href = EXCLUDED.agent_href,
    ordinal    = EXCLUDED.ordinal;

-- name: DeleteUserAgents :exec
DELETE FROM user_agent WHERE user_sourced_id = $1;

-- name: InsertUserIdentifier :one
INSERT INTO user_identifier (user_sourced_id, type, identifier, ordinal)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: DeleteUserIdentifiers :exec
DELETE FROM user_identifier WHERE user_sourced_id = $1;

-- name: InsertUserRole :one
INSERT INTO user_role (
    user_sourced_id, user_href, role_type, role, org_sourced_id, org_href,
    begin_date, end_date, user_profile, ordinal
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: DeleteUserRoles :exec
DELETE FROM user_role WHERE user_sourced_id = $1;

-- name: UpsertUserProfile :one
INSERT INTO user_profile (
    profile_id, user_sourced_id, profile_type, vendor_id, application_id,
    description, ordinal
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (profile_id) DO UPDATE SET
    user_sourced_id = EXCLUDED.user_sourced_id,
    profile_type    = EXCLUDED.profile_type,
    vendor_id       = EXCLUDED.vendor_id,
    application_id  = EXCLUDED.application_id,
    description     = EXCLUDED.description,
    ordinal         = EXCLUDED.ordinal
RETURNING *;

-- name: DeleteUserProfiles :exec
DELETE FROM user_profile WHERE user_sourced_id = $1;

-- name: InsertCredential :one
INSERT INTO credential (profile_id, type, username, password, ordinal)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpsertDemographics :one
INSERT INTO demographics (
    sourced_id, status, date_last_modified, metadata, birth_date, sex,
    american_indian_or_alaska_native, asian, black_or_african_american,
    native_hawaiian_or_other_pacific_islander, white,
    demographic_race_two_or_more_races, hispanic_or_latino_ethnicity,
    country_of_birth_code, state_of_birth_abbreviation, city_of_birth,
    public_school_residence_status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
)
ON CONFLICT (sourced_id) DO UPDATE SET
    status                                    = EXCLUDED.status,
    date_last_modified                        = EXCLUDED.date_last_modified,
    metadata                                  = EXCLUDED.metadata,
    birth_date                                = EXCLUDED.birth_date,
    sex                                       = EXCLUDED.sex,
    american_indian_or_alaska_native          = EXCLUDED.american_indian_or_alaska_native,
    asian                                     = EXCLUDED.asian,
    black_or_african_american                 = EXCLUDED.black_or_african_american,
    native_hawaiian_or_other_pacific_islander = EXCLUDED.native_hawaiian_or_other_pacific_islander,
    white                                     = EXCLUDED.white,
    demographic_race_two_or_more_races        = EXCLUDED.demographic_race_two_or_more_races,
    hispanic_or_latino_ethnicity              = EXCLUDED.hispanic_or_latino_ethnicity,
    country_of_birth_code                     = EXCLUDED.country_of_birth_code,
    state_of_birth_abbreviation               = EXCLUDED.state_of_birth_abbreviation,
    city_of_birth                             = EXCLUDED.city_of_birth,
    public_school_residence_status            = EXCLUDED.public_school_residence_status
RETURNING *;

-- name: UpsertEnrollment :one
INSERT INTO enrollment (
    sourced_id, status, date_last_modified, metadata, user_sourced_id,
    user_href, class_sourced_id, class_href, school_sourced_id, school_href,
    role, is_primary, begin_date, end_date
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (sourced_id) DO UPDATE SET
    status             = EXCLUDED.status,
    date_last_modified = EXCLUDED.date_last_modified,
    metadata           = EXCLUDED.metadata,
    user_sourced_id    = EXCLUDED.user_sourced_id,
    user_href          = EXCLUDED.user_href,
    class_sourced_id   = EXCLUDED.class_sourced_id,
    class_href         = EXCLUDED.class_href,
    school_sourced_id  = EXCLUDED.school_sourced_id,
    school_href        = EXCLUDED.school_href,
    role               = EXCLUDED.role,
    is_primary         = EXCLUDED.is_primary,
    begin_date         = EXCLUDED.begin_date,
    end_date           = EXCLUDED.end_date
RETURNING *;

-- name: UpsertResource :one
INSERT INTO resource (
    sourced_id, status, date_last_modified, metadata, title, roles,
    importance, vendor_resource_id, vendor_id, application_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (sourced_id) DO UPDATE SET
    status             = EXCLUDED.status,
    date_last_modified = EXCLUDED.date_last_modified,
    metadata           = EXCLUDED.metadata,
    title              = EXCLUDED.title,
    roles              = EXCLUDED.roles,
    importance         = EXCLUDED.importance,
    vendor_resource_id = EXCLUDED.vendor_resource_id,
    vendor_id          = EXCLUDED.vendor_id,
    application_id     = EXCLUDED.application_id
RETURNING *;

