-- Curation writes: the statements behind the Create/Update/Delete RPCs.
--
-- Three invariants hold for every statement here.
--
--   1. Authorization is in the statement. An insert is
--      `INSERT ... SELECT ... WHERE EXISTS (<curating access>)`; an update or
--      delete carries the same predicate as a WHERE conjunct. A caller without
--      the grant affects zero rows, and the handler reports that as not_found —
--      the same answer an out-of-scope read gets, so a write cannot be used to
--      probe for an entity's existence.
--
--   2. Writing needs more than reading. The curating roles are roster_admin,
--      district_admin, school_admin and data_provider. roster_reader is absent,
--      so a read-only grant cannot write no matter which RPC it calls.
--
--   3. Updates are version-checked on date_last_modified. The caller sends the
--      value it read; a mismatch affects zero rows. Without this a nightly
--      import and a curator editing the same record would silently overwrite
--      each other, and the loser would never know.
--
-- The global-grant branch is spelled out separately in every predicate rather
-- than relying on auth_effective_access alone. That view expands a global grant
-- with `CROSS JOIN org`, so it yields no rows while org is empty — meaning a
-- global administrator could not create the very first org. The extra clause is
-- what makes bootstrapping possible.

-- name: CanCurateOrg :one
-- Whether this subject may write to the given org. Used to distinguish "you may
-- not" from "it does not exist" after a guarded write affects nothing.
SELECT EXISTS (
    SELECT 1 FROM auth_effective_access a
    WHERE a.subject = @subject::text
      AND a.org_sourced_id = @org_sourced_id::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
    UNION ALL
    SELECT 1
    FROM auth_grant g
    JOIN auth_principal p ON p.id = g.principal_id
    JOIN auth_role r ON r.name = g.role_name
    WHERE p.subject = @subject::text
      AND NOT p.disabled
      AND r.is_global
      AND g.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
      AND (g.expires_at IS NULL OR g.expires_at > NOW())
) AS allowed;

-- name: SubjectHoldsGlobalCuratingGrant :one
-- The bootstrap case: a global curator with no org to be scoped against, which
-- is the only way the first org can be created.
SELECT EXISTS (
    SELECT 1
    FROM auth_grant g
    JOIN auth_principal p ON p.id = g.principal_id
    JOIN auth_role r ON r.name = g.role_name
    WHERE p.subject = @subject::text
      AND NOT p.disabled
      AND r.is_global
      AND g.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
      AND (g.expires_at IS NULL OR g.expires_at > NOW())
) AS allowed;

-- ── Org ───────────────────────────────────────────────────────────────────────

-- name: CreateOrgGuarded :one
-- Scoped against the parent org: creating a school under a district is a change
-- to that district's subtree. A root org (no parent) therefore requires a global
-- curating grant, which is the bootstrap path.
INSERT INTO org (
    sourced_id, status, date_last_modified, metadata, name, type, identifier,
    parent_sourced_id, parent_href
)
SELECT @sourced_id::text, @status::text, @date_last_modified::timestamptz,
       @metadata::jsonb, @name::text, @type::text, @identifier::text,
       sqlc.narg(parent_sourced_id)::text, sqlc.narg(parent_href)::text
WHERE EXISTS (
    SELECT 1 FROM auth_effective_access a
    WHERE a.subject = @subject::text
      AND a.org_sourced_id = COALESCE(sqlc.narg(parent_sourced_id)::text, '')
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
) OR EXISTS (
    SELECT 1
    FROM auth_grant g
    JOIN auth_principal p ON p.id = g.principal_id
    JOIN auth_role r ON r.name = g.role_name
    WHERE p.subject = @subject::text
      AND NOT p.disabled
      AND r.is_global
      AND g.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
      AND (g.expires_at IS NULL OR g.expires_at > NOW())
)
RETURNING *;

-- name: UpdateOrgGuarded :one
UPDATE org SET
    status             = @status::text,
    date_last_modified = @date_last_modified::timestamptz,
    metadata           = @metadata::jsonb,
    name               = @name::text,
    type               = @type::text,
    identifier         = @identifier::text,
    parent_sourced_id  = sqlc.narg(parent_sourced_id)::text,
    parent_href        = sqlc.narg(parent_href)::text
WHERE sourced_id = @sourced_id::text
  AND date_last_modified = @expected_date_last_modified::timestamptz
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = org.sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- name: DeleteOrgGuarded :one
-- OneRoster deletion is a status flag, not a DELETE: consumers may remove such
-- records but are not obliged to, and a hard delete here would cascade through
-- every class and enrollment beneath the org.
UPDATE org SET
    status             = 'tobedeleted',
    date_last_modified = @date_last_modified::timestamptz
WHERE sourced_id = @sourced_id::text
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = org.sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- ── Course ────────────────────────────────────────────────────────────────────

-- name: CreateCourseGuarded :one
INSERT INTO course (
    sourced_id, status, date_last_modified, metadata, title, course_code,
    grades, subjects, subject_codes, org_sourced_id, org_href,
    school_year_sourced_id, school_year_href
)
SELECT @sourced_id::text, @status::text, @date_last_modified::timestamptz,
       @metadata::jsonb, @title::text, @course_code::text,
       @grades::text[], @subjects::text[], @subject_codes::text[],
       sqlc.narg(org_sourced_id)::text, sqlc.narg(org_href)::text,
       sqlc.narg(school_year_sourced_id)::text, sqlc.narg(school_year_href)::text
WHERE EXISTS (
    SELECT 1 FROM auth_effective_access a
    WHERE a.subject = @subject::text
      AND a.org_sourced_id = COALESCE(sqlc.narg(org_sourced_id)::text, '')
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
)
RETURNING *;

-- name: UpdateCourseGuarded :one
UPDATE course SET
    status                 = @status::text,
    date_last_modified     = @date_last_modified::timestamptz,
    metadata               = @metadata::jsonb,
    title                  = @title::text,
    course_code            = @course_code::text,
    grades                 = @grades::text[],
    subjects               = @subjects::text[],
    subject_codes          = @subject_codes::text[],
    org_sourced_id         = sqlc.narg(org_sourced_id)::text,
    org_href               = sqlc.narg(org_href)::text,
    school_year_sourced_id = sqlc.narg(school_year_sourced_id)::text,
    school_year_href       = sqlc.narg(school_year_href)::text
WHERE sourced_id = @sourced_id::text
  AND date_last_modified = @expected_date_last_modified::timestamptz
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = course.org_sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- name: DeleteCourseGuarded :one
UPDATE course SET
    status             = 'tobedeleted',
    date_last_modified = @date_last_modified::timestamptz
WHERE sourced_id = @sourced_id::text
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = course.org_sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- ── Class ─────────────────────────────────────────────────────────────────────

-- name: CreateClassGuarded :one
-- Scoped on school_sourced_id, which is NOT NULL for a class, so there is no
-- bootstrap case here: a class always belongs to a school that already exists.
INSERT INTO class (
    sourced_id, status, date_last_modified, metadata, title, class_code,
    class_type, location, grades, subjects, subject_codes, periods,
    course_sourced_id, course_href, school_sourced_id, school_href
)
SELECT @sourced_id::text, @status::text, @date_last_modified::timestamptz,
       @metadata::jsonb, @title::text, sqlc.narg(class_code)::text,
       sqlc.narg(class_type)::text, sqlc.narg(location)::text,
       @grades::text[], @subjects::text[], @subject_codes::text[], @periods::text[],
       @course_sourced_id::text, sqlc.narg(course_href)::text,
       @school_sourced_id::text, sqlc.narg(school_href)::text
WHERE EXISTS (
    SELECT 1 FROM auth_effective_access a
    WHERE a.subject = @subject::text
      AND a.org_sourced_id = @school_sourced_id::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
)
RETURNING *;

-- name: UpdateClassGuarded :one
UPDATE class SET
    status             = @status::text,
    date_last_modified = @date_last_modified::timestamptz,
    metadata           = @metadata::jsonb,
    title              = @title::text,
    class_code         = sqlc.narg(class_code)::text,
    class_type         = sqlc.narg(class_type)::text,
    location           = sqlc.narg(location)::text,
    grades             = @grades::text[],
    subjects           = @subjects::text[],
    subject_codes      = @subject_codes::text[],
    periods            = @periods::text[],
    course_sourced_id  = @course_sourced_id::text,
    course_href        = sqlc.narg(course_href)::text,
    school_sourced_id  = @school_sourced_id::text,
    school_href        = sqlc.narg(school_href)::text
WHERE sourced_id = @sourced_id::text
  AND date_last_modified = @expected_date_last_modified::timestamptz
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = class.school_sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- name: DeleteClassGuarded :one
UPDATE class SET
    status             = 'tobedeleted',
    date_last_modified = @date_last_modified::timestamptz
WHERE sourced_id = @sourced_id::text
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = class.school_sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- ── Enrollment ────────────────────────────────────────────────────────────────

-- name: CreateEnrollmentGuarded :one
INSERT INTO enrollment (
    sourced_id, status, date_last_modified, metadata, user_sourced_id,
    user_href, class_sourced_id, class_href, school_sourced_id, school_href,
    role, is_primary, begin_date, end_date
)
SELECT @sourced_id::text, @status::text, @date_last_modified::timestamptz,
       @metadata::jsonb, @user_sourced_id::text, sqlc.narg(user_href)::text,
       @class_sourced_id::text, sqlc.narg(class_href)::text,
       @school_sourced_id::text, sqlc.narg(school_href)::text,
       @role::text, sqlc.narg(is_primary)::boolean,
       sqlc.narg(begin_date)::date, sqlc.narg(end_date)::date
WHERE EXISTS (
    SELECT 1 FROM auth_effective_access a
    WHERE a.subject = @subject::text
      AND a.org_sourced_id = @school_sourced_id::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
)
RETURNING *;

-- name: UpdateEnrollmentGuarded :one
UPDATE enrollment SET
    status             = @status::text,
    date_last_modified = @date_last_modified::timestamptz,
    metadata           = @metadata::jsonb,
    user_sourced_id    = @user_sourced_id::text,
    user_href          = sqlc.narg(user_href)::text,
    class_sourced_id   = @class_sourced_id::text,
    class_href         = sqlc.narg(class_href)::text,
    school_sourced_id  = @school_sourced_id::text,
    school_href        = sqlc.narg(school_href)::text,
    role               = @role::text,
    is_primary         = sqlc.narg(is_primary)::boolean,
    begin_date         = sqlc.narg(begin_date)::date,
    end_date           = sqlc.narg(end_date)::date
WHERE sourced_id = @sourced_id::text
  AND date_last_modified = @expected_date_last_modified::timestamptz
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = enrollment.school_sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- name: DeleteEnrollmentGuarded :one
UPDATE enrollment SET
    status             = 'tobedeleted',
    date_last_modified = @date_last_modified::timestamptz
WHERE sourced_id = @sourced_id::text
  AND EXISTS (
      SELECT 1 FROM auth_effective_access a
      WHERE a.subject = @subject::text
        AND a.org_sourced_id = enrollment.school_sourced_id
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- ── User ──────────────────────────────────────────────────────────────────────

-- name: CreateUserGuarded :one
-- Scoped on the org named in the request rather than on the user's roles, which
-- do not exist yet at creation time. The handler passes the primary org.
INSERT INTO oneroster_user (
    sourced_id, status, date_last_modified, metadata, user_master_identifier,
    username, enabled_user, given_name, family_name, middle_name,
    preferred_first_name, preferred_last_name, preferred_middle_name, pronouns,
    grades, identifier, email, sms, phone, password,
    primary_org_sourced_id, primary_org_href
)
SELECT @sourced_id::text, @status::text, @date_last_modified::timestamptz,
       @metadata::jsonb, sqlc.narg(user_master_identifier)::text,
       sqlc.narg(username)::text, @enabled_user::boolean,
       @given_name::text, @family_name::text, sqlc.narg(middle_name)::text,
       sqlc.narg(preferred_first_name)::text, sqlc.narg(preferred_last_name)::text,
       sqlc.narg(preferred_middle_name)::text, sqlc.narg(pronouns)::text,
       @grades::text[], sqlc.narg(identifier)::text, sqlc.narg(email)::text,
       sqlc.narg(sms)::text, sqlc.narg(phone)::text, sqlc.narg(password)::text,
       sqlc.narg(primary_org_sourced_id)::text, sqlc.narg(primary_org_href)::text
WHERE EXISTS (
    SELECT 1 FROM auth_effective_access a
    WHERE a.subject = @subject::text
      AND a.org_sourced_id = @scope_org_sourced_id::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
)
RETURNING *;

-- name: UpdateUserGuarded :one
-- Scoped through the user's roles, matching how reads scope them: a user is
-- visible where they hold a role, and is writable in the same places.
UPDATE oneroster_user SET
    status                 = @status::text,
    date_last_modified     = @date_last_modified::timestamptz,
    metadata               = @metadata::jsonb,
    user_master_identifier = sqlc.narg(user_master_identifier)::text,
    username               = sqlc.narg(username)::text,
    enabled_user           = @enabled_user::boolean,
    given_name             = @given_name::text,
    family_name            = @family_name::text,
    middle_name            = sqlc.narg(middle_name)::text,
    preferred_first_name   = sqlc.narg(preferred_first_name)::text,
    preferred_last_name    = sqlc.narg(preferred_last_name)::text,
    preferred_middle_name  = sqlc.narg(preferred_middle_name)::text,
    pronouns               = sqlc.narg(pronouns)::text,
    grades                 = @grades::text[],
    identifier             = sqlc.narg(identifier)::text,
    email                  = sqlc.narg(email)::text,
    sms                    = sqlc.narg(sms)::text,
    phone                  = sqlc.narg(phone)::text,
    primary_org_sourced_id = sqlc.narg(primary_org_sourced_id)::text,
    primary_org_href       = sqlc.narg(primary_org_href)::text
WHERE sourced_id = @sourced_id::text
  AND date_last_modified = @expected_date_last_modified::timestamptz
  AND EXISTS (
      SELECT 1
      FROM user_role ur
      JOIN auth_effective_access a ON a.org_sourced_id = ur.org_sourced_id
      WHERE ur.user_sourced_id = oneroster_user.sourced_id
        AND a.subject = @subject::text
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- name: DeleteUserGuarded :one
UPDATE oneroster_user SET
    status             = 'tobedeleted',
    date_last_modified = @date_last_modified::timestamptz
WHERE sourced_id = @sourced_id::text
  AND EXISTS (
      SELECT 1
      FROM user_role ur
      JOIN auth_effective_access a ON a.org_sourced_id = ur.org_sourced_id
      WHERE ur.user_sourced_id = oneroster_user.sourced_id
        AND a.subject = @subject::text
        AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
  )
RETURNING *;

-- ── Visibility probes ─────────────────────────────────────────────────────────
-- After a guarded write affects no rows, these say whether the row was invisible
-- (not_found) or whether the version moved (aborted). Without them the two are
-- indistinguishable, and a curator whose edit lost a race would be told the
-- record does not exist.

-- name: OrgIsVisibleForCuration :one
SELECT EXISTS (
    SELECT 1 FROM org o
    JOIN auth_effective_access a ON a.org_sourced_id = o.sourced_id
    WHERE o.sourced_id = @sourced_id::text
      AND a.subject = @subject::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
) AS visible;

-- name: CourseIsVisibleForCuration :one
SELECT EXISTS (
    SELECT 1 FROM course c
    JOIN auth_effective_access a ON a.org_sourced_id = c.org_sourced_id
    WHERE c.sourced_id = @sourced_id::text
      AND a.subject = @subject::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
) AS visible;

-- name: ClassIsVisibleForCuration :one
SELECT EXISTS (
    SELECT 1 FROM class c
    JOIN auth_effective_access a ON a.org_sourced_id = c.school_sourced_id
    WHERE c.sourced_id = @sourced_id::text
      AND a.subject = @subject::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
) AS visible;

-- name: EnrollmentIsVisibleForCuration :one
SELECT EXISTS (
    SELECT 1 FROM enrollment e
    JOIN auth_effective_access a ON a.org_sourced_id = e.school_sourced_id
    WHERE e.sourced_id = @sourced_id::text
      AND a.subject = @subject::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
) AS visible;

-- name: UserIsVisibleForCuration :one
SELECT EXISTS (
    SELECT 1 FROM oneroster_user u
    JOIN user_role ur ON ur.user_sourced_id = u.sourced_id
    JOIN auth_effective_access a ON a.org_sourced_id = ur.org_sourced_id
    WHERE u.sourced_id = @sourced_id::text
      AND a.subject = @subject::text
      AND a.role_name IN ('roster_admin', 'district_admin', 'school_admin', 'data_provider')
) AS visible;

-- ── AcademicSession (roster_admin only) ───────────────────────────────────────
--
-- Sessions are the one rostering entity with no owning org. The OneRoster JSON
-- Schema gives AcademicSessionDType no org reference at all: its parent and
-- children chain only to other sessions, and schoolYear is a "YYYY" label, not
-- a reference. Classes and courses point AT a session, so one session is
-- reachable from every org whose classes use it — a union, not a scope.
--
-- There is therefore nothing for an org-scoped predicate to check, and a
-- brand-new session has no classes pointing at it in any case. These statements
-- require a live, deployment-wide roster_admin grant instead. A district_admin
-- cannot create a school year, because a school year is not a district's to own.
--
-- Unlike the org-scoped writes, a caller without the grant gets zero rows and
-- the handler reports permission_denied rather than not_found: sessions are
-- readable by any principal holding any grant, so their existence is not a
-- secret and there is nothing to protect by pretending otherwise.

-- name: SubjectIsGlobalRosterAdmin :one
SELECT EXISTS (
    SELECT 1
    FROM auth_grant g
    JOIN auth_principal p ON p.id = g.principal_id
    JOIN auth_role r ON r.name = g.role_name
    WHERE p.subject = @subject::text
      AND NOT p.disabled
      AND r.is_global
      AND g.role_name = 'roster_admin'
      AND (g.expires_at IS NULL OR g.expires_at > NOW())
) AS allowed;

-- name: CreateAcademicSessionGuarded :one
INSERT INTO academic_session (
    sourced_id, status, date_last_modified, metadata, title, start_date,
    end_date, type, school_year, parent_sourced_id, parent_href
)
SELECT @sourced_id::text, @status::text, @date_last_modified::timestamptz,
       @metadata::jsonb, @title::text, @start_date::date, @end_date::date,
       @type::text, @school_year::text,
       sqlc.narg(parent_sourced_id)::text, sqlc.narg(parent_href)::text
WHERE EXISTS (
    SELECT 1
    FROM auth_grant g
    JOIN auth_principal p ON p.id = g.principal_id
    JOIN auth_role r ON r.name = g.role_name
    WHERE p.subject = @subject::text
      AND NOT p.disabled
      AND r.is_global
      AND g.role_name = 'roster_admin'
      AND (g.expires_at IS NULL OR g.expires_at > NOW())
)
RETURNING *;

-- name: UpdateAcademicSessionGuarded :one
UPDATE academic_session SET
    status             = @status::text,
    date_last_modified = @date_last_modified::timestamptz,
    metadata           = @metadata::jsonb,
    title              = @title::text,
    start_date         = @start_date::date,
    end_date           = @end_date::date,
    type               = @type::text,
    school_year        = @school_year::text,
    parent_sourced_id  = sqlc.narg(parent_sourced_id)::text,
    parent_href        = sqlc.narg(parent_href)::text
WHERE sourced_id = @sourced_id::text
  AND date_last_modified = @expected_date_last_modified::timestamptz
  AND EXISTS (
      SELECT 1
      FROM auth_grant g
      JOIN auth_principal p ON p.id = g.principal_id
      JOIN auth_role r ON r.name = g.role_name
      WHERE p.subject = @subject::text
        AND NOT p.disabled
        AND r.is_global
        AND g.role_name = 'roster_admin'
        AND (g.expires_at IS NULL OR g.expires_at > NOW())
  )
RETURNING *;

-- name: DeleteAcademicSessionGuarded :one
UPDATE academic_session SET
    status             = 'tobedeleted',
    date_last_modified = @date_last_modified::timestamptz
WHERE sourced_id = @sourced_id::text
  AND EXISTS (
      SELECT 1
      FROM auth_grant g
      JOIN auth_principal p ON p.id = g.principal_id
      JOIN auth_role r ON r.name = g.role_name
      WHERE p.subject = @subject::text
        AND NOT p.disabled
        AND r.is_global
        AND g.role_name = 'roster_admin'
        AND (g.expires_at IS NULL OR g.expires_at > NOW())
  )
RETURNING *;

-- name: AcademicSessionExists :one
-- After a guarded write affects nothing and the caller IS a roster_admin, this
-- says whether the row was missing (not_found) or its version moved (aborted).
SELECT EXISTS (
    SELECT 1 FROM academic_session WHERE sourced_id = @sourced_id::text
) AS present;
