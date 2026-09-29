-- OneRoster v1.2 rostering entities.
--
-- Mapping rules from the protobuf definitions in proto/oneroster/v1p2/v1:
--
--   string sourced_id              -> TEXT PRIMARY KEY (an opaque GUID, not a UUID)
--   google.protobuf.Timestamp      -> TIMESTAMPTZ
--   google.type.Date               -> DATE
--   google.protobuf.Struct         -> JSONB
--   repeated string                -> TEXT[]
--   *Status enum                   -> TEXT + CHECK (closed vocabulary)
--   open "ext:" vocabularies       -> TEXT + CHECK allowing the ext: escape
--   singular *GUIDRef              -> <name>_sourced_id FK + <name>_href
--   repeated *GUIDRef              -> ordered junction table
--
-- Closed enums use CHECK rather than a PostgreSQL ENUM type deliberately: the
-- values cross the wire as the strings below, and CHECK avoids ALTER TYPE when
-- the spec adds a value.

-- +goose Up
-- +goose StatementBegin

-- ── Org ───────────────────────────────────────────────────────────────────────
-- Self-referencing: a school's parent is a district, whose parent is a state.
-- The "children" repeated GUIDRef is the inverse of this edge and is derived,
-- never stored, so the two can never disagree.
CREATE TABLE IF NOT EXISTS org (
    sourced_id         TEXT PRIMARY KEY,
    status             TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified TIMESTAMPTZ NOT NULL,
    metadata           JSONB,
    name               TEXT        NOT NULL,
    type               TEXT        NOT NULL CHECK (
                           type IN ('department', 'district', 'local', 'national', 'school', 'state')
                           OR type ~ '^ext:[a-zA-Z0-9._-]+$'
                       ),
    identifier         TEXT        NOT NULL,
    parent_sourced_id  TEXT        REFERENCES org (sourced_id) ON DELETE SET NULL,
    parent_href        TEXT
);

CREATE INDEX IF NOT EXISTS idx_org_parent ON org (parent_sourced_id);
CREATE INDEX IF NOT EXISTS idx_org_type ON org (type);
CREATE INDEX IF NOT EXISTS idx_org_status ON org (status);

-- ── AcademicSession ───────────────────────────────────────────────────────────
-- Also self-referencing: a grading period's parent is a term, whose parent is a
-- school year.
CREATE TABLE IF NOT EXISTS academic_session (
    sourced_id         TEXT PRIMARY KEY,
    status             TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified TIMESTAMPTZ NOT NULL,
    metadata           JSONB,
    title              TEXT        NOT NULL,
    start_date         DATE        NOT NULL,
    end_date           DATE        NOT NULL,
    type               TEXT        NOT NULL CHECK (
                           type IN ('gradingPeriod', 'semester', 'schoolYear', 'term')
                           OR type ~ '^ext:[a-zA-Z0-9._-]+$'
                       ),
    school_year        TEXT        NOT NULL,
    parent_sourced_id  TEXT        REFERENCES academic_session (sourced_id) ON DELETE SET NULL,
    parent_href        TEXT,
    CONSTRAINT academic_session_dates CHECK (end_date >= start_date)
);

CREATE INDEX IF NOT EXISTS idx_academic_session_parent ON academic_session (parent_sourced_id);
CREATE INDEX IF NOT EXISTS idx_academic_session_school_year ON academic_session (school_year);

-- ── Course ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS course (
    sourced_id             TEXT PRIMARY KEY,
    status                 TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified     TIMESTAMPTZ NOT NULL,
    metadata               JSONB,
    title                  TEXT        NOT NULL,
    course_code            TEXT        NOT NULL,
    grades                 TEXT[]      NOT NULL DEFAULT '{}',
    subjects               TEXT[]      NOT NULL DEFAULT '{}',
    subject_codes          TEXT[]      NOT NULL DEFAULT '{}',
    org_sourced_id         TEXT        REFERENCES org (sourced_id) ON DELETE SET NULL,
    org_href               TEXT,
    school_year_sourced_id TEXT        REFERENCES academic_session (sourced_id) ON DELETE SET NULL,
    school_year_href       TEXT
);

CREATE INDEX IF NOT EXISTS idx_course_org ON course (org_sourced_id);
CREATE INDEX IF NOT EXISTS idx_course_school_year ON course (school_year_sourced_id);

-- ── Class ─────────────────────────────────────────────────────────────────────
-- course and school are required by the proto, so both FKs are NOT NULL and
-- RESTRICT: deleting a course out from under its classes would orphan every
-- enrollment hanging off them.
CREATE TABLE IF NOT EXISTS class (
    sourced_id         TEXT PRIMARY KEY,
    status             TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified TIMESTAMPTZ NOT NULL,
    metadata           JSONB,
    title              TEXT        NOT NULL,
    class_code         TEXT,
    class_type         TEXT        CHECK (
                           class_type IS NULL
                           OR class_type IN ('homeroom', 'scheduled')
                           OR class_type ~ '^ext:[a-zA-Z0-9._-]+$'
                       ),
    location           TEXT,
    grades             TEXT[]      NOT NULL DEFAULT '{}',
    subjects           TEXT[]      NOT NULL DEFAULT '{}',
    subject_codes      TEXT[]      NOT NULL DEFAULT '{}',
    periods            TEXT[]      NOT NULL DEFAULT '{}',
    course_sourced_id  TEXT        NOT NULL REFERENCES course (sourced_id) ON DELETE RESTRICT,
    course_href        TEXT,
    school_sourced_id  TEXT        NOT NULL REFERENCES org (sourced_id) ON DELETE RESTRICT,
    school_href        TEXT
);

CREATE INDEX IF NOT EXISTS idx_class_course ON class (course_sourced_id);
CREATE INDEX IF NOT EXISTS idx_class_school ON class (school_sourced_id);
CREATE INDEX IF NOT EXISTS idx_class_status ON class (status);

-- class.terms is a required repeated AcadSessionGUIDRef. ordinal preserves the
-- order the source system sent, which repeated fields are defined to have.
CREATE TABLE IF NOT EXISTS class_term (
    class_sourced_id            TEXT NOT NULL REFERENCES class (sourced_id) ON DELETE CASCADE,
    academic_session_sourced_id TEXT NOT NULL REFERENCES academic_session (sourced_id) ON DELETE RESTRICT,
    academic_session_href       TEXT,
    ordinal                     INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (class_sourced_id, academic_session_sourced_id)
);

CREATE INDEX IF NOT EXISTS idx_class_term_session ON class_term (academic_session_sourced_id);

-- ── User ──────────────────────────────────────────────────────────────────────
-- Named oneroster_user because USER is a reserved word in PostgreSQL and an
-- unquoted `user` resolves to the session user function.
CREATE TABLE IF NOT EXISTS oneroster_user (
    sourced_id             TEXT PRIMARY KEY,
    status                 TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified     TIMESTAMPTZ NOT NULL,
    metadata               JSONB,
    user_master_identifier TEXT,
    username               TEXT,
    enabled_user           BOOLEAN     NOT NULL,
    given_name             TEXT        NOT NULL,
    family_name            TEXT        NOT NULL,
    middle_name            TEXT,
    preferred_first_name   TEXT,
    preferred_last_name    TEXT,
    preferred_middle_name  TEXT,
    pronouns               TEXT,
    grades                 TEXT[]      NOT NULL DEFAULT '{}',
    identifier             TEXT,
    email                  TEXT,
    sms                    TEXT,
    phone                  TEXT,
    -- The proto carries a password field. It is stored only so a capture of a
    -- source system round-trips; nothing in this service authenticates against
    -- it. Service credentials live in auth_api_token.
    password               TEXT,
    primary_org_sourced_id TEXT        REFERENCES org (sourced_id) ON DELETE SET NULL,
    primary_org_href       TEXT
);

CREATE INDEX IF NOT EXISTS idx_oneroster_user_primary_org ON oneroster_user (primary_org_sourced_id);
CREATE INDEX IF NOT EXISTS idx_oneroster_user_username ON oneroster_user (username);
CREATE INDEX IF NOT EXISTS idx_oneroster_user_email ON oneroster_user (email);
CREATE INDEX IF NOT EXISTS idx_oneroster_user_status ON oneroster_user (status);

-- user.agents: the guardian/parent/relative edge between two users.
CREATE TABLE IF NOT EXISTS user_agent (
    user_sourced_id  TEXT NOT NULL REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    agent_sourced_id TEXT NOT NULL REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    agent_href       TEXT,
    ordinal          INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (user_sourced_id, agent_sourced_id),
    CONSTRAINT user_agent_not_self CHECK (user_sourced_id <> agent_sourced_id)
);

CREATE INDEX IF NOT EXISTS idx_user_agent_agent ON user_agent (agent_sourced_id);

-- user.user_ids: identifiers this user carries in other systems.
CREATE TABLE IF NOT EXISTS user_identifier (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_sourced_id TEXT NOT NULL REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    type            TEXT NOT NULL,
    identifier      TEXT NOT NULL,
    ordinal         INT  NOT NULL DEFAULT 0
);

-- ── Role ──────────────────────────────────────────────────────────────────────
-- OneRoster's Role: what a user is *within an org*, as reported by the source
-- system. This is roster data, not an authorization decision — see
-- 002_authorization.sql for who may manage the roster.
-- Named user_role because ROLE is a reserved word in PostgreSQL.
CREATE TABLE IF NOT EXISTS user_role (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_sourced_id TEXT NOT NULL REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    user_href       TEXT,
    role_type       TEXT NOT NULL CHECK (role_type IN ('primary', 'secondary')),
    role            TEXT NOT NULL CHECK (
                        role IN ('aide', 'counselor', 'districtAdministrator', 'guardian',
                                 'parent', 'principal', 'proctor', 'relative',
                                 'siteAdministrator', 'student', 'systemAdministrator', 'teacher')
                        OR role ~ '^ext:[a-zA-Z0-9._-]+$'
                    ),
    org_sourced_id  TEXT NOT NULL REFERENCES org (sourced_id) ON DELETE CASCADE,
    org_href        TEXT,
    begin_date      DATE,
    end_date        DATE,
    user_profile    TEXT,
    ordinal         INT  NOT NULL DEFAULT 0,
    CONSTRAINT user_role_dates CHECK (end_date IS NULL OR begin_date IS NULL OR end_date >= begin_date)
);

CREATE INDEX IF NOT EXISTS idx_user_role_user ON user_role (user_sourced_id);
CREATE INDEX IF NOT EXISTS idx_user_role_org ON user_role (org_sourced_id);
CREATE INDEX IF NOT EXISTS idx_user_role_role ON user_role (role);
-- A user has exactly one primary role; secondary roles are unconstrained in count.
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_role_one_primary
    ON user_role (user_sourced_id)
    WHERE role_type = 'primary';

-- ── UserProfile / Credential ──────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_profile (
    profile_id      TEXT PRIMARY KEY,
    user_sourced_id TEXT NOT NULL REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    profile_type    TEXT NOT NULL,
    vendor_id       TEXT NOT NULL,
    application_id  TEXT,
    description     TEXT,
    ordinal         INT  NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_user_profile_user ON user_profile (user_sourced_id);

CREATE TABLE IF NOT EXISTS credential (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES user_profile (profile_id) ON DELETE CASCADE,
    type       TEXT NOT NULL,
    username   TEXT NOT NULL,
    password   TEXT,
    ordinal    INT  NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_credential_profile ON credential (profile_id);

-- ── Demographics ──────────────────────────────────────────────────────────────
-- Shares the user's sourced_id: OneRoster links the two implicitly rather than
-- through a field, so the PK is also the FK.
CREATE TABLE IF NOT EXISTS demographics (
    sourced_id                             TEXT PRIMARY KEY
                                               REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    status                                 TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified                     TIMESTAMPTZ NOT NULL,
    metadata                               JSONB,
    birth_date                             DATE,
    sex                                    TEXT        CHECK (
                                               sex IS NULL
                                               OR sex IN ('male', 'female', 'unspecified', 'other')
                                               OR sex ~ '^ext:[a-zA-Z0-9._-]+$'
                                           ),
    american_indian_or_alaska_native       BOOLEAN,
    asian                                  BOOLEAN,
    black_or_african_american              BOOLEAN,
    native_hawaiian_or_other_pacific_islander BOOLEAN,
    white                                  BOOLEAN,
    demographic_race_two_or_more_races     BOOLEAN,
    hispanic_or_latino_ethnicity           BOOLEAN,
    country_of_birth_code                  TEXT,
    state_of_birth_abbreviation            TEXT,
    city_of_birth                          TEXT,
    public_school_residence_status         TEXT
);

-- ── Enrollment ────────────────────────────────────────────────────────────────
-- The join that makes a roster a roster: this user, in this class, at this
-- school, in this role.
CREATE TABLE IF NOT EXISTS enrollment (
    sourced_id         TEXT PRIMARY KEY,
    status             TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified TIMESTAMPTZ NOT NULL,
    metadata           JSONB,
    user_sourced_id    TEXT        NOT NULL REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    user_href          TEXT,
    class_sourced_id   TEXT        NOT NULL REFERENCES class (sourced_id) ON DELETE CASCADE,
    class_href         TEXT,
    school_sourced_id  TEXT        NOT NULL REFERENCES org (sourced_id) ON DELETE RESTRICT,
    school_href        TEXT,
    role               TEXT        NOT NULL CHECK (
                           role IN ('administrator', 'proctor', 'student', 'teacher')
                           OR role ~ '^ext:[a-zA-Z0-9._-]+$'
                       ),
    -- "primary" is reserved in PostgreSQL.
    is_primary         BOOLEAN,
    begin_date         DATE,
    end_date           DATE,
    CONSTRAINT enrollment_dates CHECK (end_date IS NULL OR begin_date IS NULL OR end_date >= begin_date)
);

CREATE INDEX IF NOT EXISTS idx_enrollment_user ON enrollment (user_sourced_id);
CREATE INDEX IF NOT EXISTS idx_enrollment_class ON enrollment (class_sourced_id);
CREATE INDEX IF NOT EXISTS idx_enrollment_school ON enrollment (school_sourced_id);
CREATE INDEX IF NOT EXISTS idx_enrollment_role ON enrollment (role);
-- The same user cannot hold the same role in the same class twice.
CREATE UNIQUE INDEX IF NOT EXISTS idx_enrollment_unique_member
    ON enrollment (class_sourced_id, user_sourced_id, role);

-- ── Resource ──────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS resource (
    sourced_id          TEXT PRIMARY KEY,
    status              TEXT        NOT NULL CHECK (status IN ('active', 'tobedeleted')),
    date_last_modified  TIMESTAMPTZ NOT NULL,
    metadata            JSONB,
    title               TEXT        NOT NULL,
    roles               TEXT[]      NOT NULL DEFAULT '{}',
    importance          TEXT        CHECK (importance IS NULL OR importance IN ('primary', 'secondary')),
    vendor_resource_id  TEXT,
    vendor_id           TEXT,
    application_id      TEXT
);

-- course.resources, class.resources and user.resources are three repeated
-- ResourceGUIDRef fields over one resource table, so each gets its own junction.
CREATE TABLE IF NOT EXISTS course_resource (
    course_sourced_id   TEXT NOT NULL REFERENCES course (sourced_id) ON DELETE CASCADE,
    resource_sourced_id TEXT NOT NULL REFERENCES resource (sourced_id) ON DELETE CASCADE,
    resource_href       TEXT,
    ordinal             INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (course_sourced_id, resource_sourced_id)
);

CREATE TABLE IF NOT EXISTS class_resource (
    class_sourced_id    TEXT NOT NULL REFERENCES class (sourced_id) ON DELETE CASCADE,
    resource_sourced_id TEXT NOT NULL REFERENCES resource (sourced_id) ON DELETE CASCADE,
    resource_href       TEXT,
    ordinal             INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (class_sourced_id, resource_sourced_id)
);

CREATE TABLE IF NOT EXISTS user_resource (
    user_sourced_id     TEXT NOT NULL REFERENCES oneroster_user (sourced_id) ON DELETE CASCADE,
    resource_sourced_id TEXT NOT NULL REFERENCES resource (sourced_id) ON DELETE CASCADE,
    resource_href       TEXT,
    ordinal             INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (user_sourced_id, resource_sourced_id)
);

-- ── Org ancestry ──────────────────────────────────────────────────────────────
-- Every org paired with each of its descendants, itself at depth 0. This is what
-- turns "grant scoped to the district" into "these schools", and it is used by
-- both the rostering reads and the authorization checks in 002.
--
-- The depth cap makes a cycle in the parent edge terminate rather than spin. The
-- data should never contain one; the view should not be the thing that discovers
-- it by hanging.
CREATE OR REPLACE VIEW org_closure AS
WITH RECURSIVE walk AS (
    SELECT sourced_id AS ancestor_sourced_id,
           sourced_id AS descendant_sourced_id,
           0          AS depth
    FROM org
    UNION ALL
    SELECT w.ancestor_sourced_id,
           child.sourced_id,
           w.depth + 1
    FROM walk w
    JOIN org child ON child.parent_sourced_id = w.descendant_sourced_id
    WHERE w.depth < 32
)
SELECT ancestor_sourced_id, descendant_sourced_id, depth FROM walk;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW IF EXISTS org_closure;
DROP TABLE IF EXISTS user_resource;
DROP TABLE IF EXISTS class_resource;
DROP TABLE IF EXISTS course_resource;
DROP TABLE IF EXISTS resource;
DROP TABLE IF EXISTS enrollment;
DROP TABLE IF EXISTS demographics;
DROP TABLE IF EXISTS credential;
DROP TABLE IF EXISTS user_profile;
DROP TABLE IF EXISTS user_role;
DROP TABLE IF EXISTS user_identifier;
DROP TABLE IF EXISTS user_agent;
DROP TABLE IF EXISTS oneroster_user;
DROP TABLE IF EXISTS class_term;
DROP TABLE IF EXISTS class;
DROP TABLE IF EXISTS course;
DROP TABLE IF EXISTS academic_session;
DROP TABLE IF EXISTS org;
-- +goose StatementEnd
