-- Subject-scoped rostering reads: every query the RPC service issues.
--
-- Two invariants hold for every query in this file, and there is deliberately no
-- unscoped variant of any of them — an unscoped read would be the easier call to
-- reach for, and reaching for it would silently disable authorization.
--
--   1. Authorization is in the query. Each read joins auth_effective_access on
--      the entity's owning org and filters by the calling subject, so the
--      database never materializes a row the caller may not see. Filtering in Go
--      after an unscoped read would move that decision into the handler, where a
--      missed branch leaks data.
--
--   2. Paging is keyset, not offset. `sourced_id` is the primary key and
--      therefore a total order, so `sourced_id > @after_sourced_id` resumes
--      exactly where the previous page stopped. Rosters are rewritten by bulk
--      imports; under OFFSET that traffic makes pages skip and repeat rows.
--      An empty @after_sourced_id starts at the first page, because '' sorts
--      before every non-empty identifier.
--
-- DISTINCT throughout: a principal holding two roles over the same org matches
-- auth_effective_access twice, which would otherwise duplicate the entity.
--
-- Entities whose owning org is NULL (a course with no org) match no row in
-- auth_effective_access and are therefore invisible to everyone. That is
-- intentional — an unowned row has no scope to check, so it fails closed.

-- ── Org ───────────────────────────────────────────────────────────────────────

-- name: ListOrgsForSubject :many
SELECT DISTINCT o.*
FROM org o
JOIN auth_effective_access a ON a.org_sourced_id = o.sourced_id
WHERE a.subject = @subject::text
  AND o.status <> 'tobedeleted'
  AND o.sourced_id > @after_sourced_id::text
ORDER BY o.sourced_id
LIMIT @page_size::int;

-- name: ListSchoolsForSubject :many
SELECT DISTINCT o.*
FROM org o
JOIN auth_effective_access a ON a.org_sourced_id = o.sourced_id
WHERE a.subject = @subject::text
  AND o.type = 'school'
  AND o.status <> 'tobedeleted'
  AND o.sourced_id > @after_sourced_id::text
ORDER BY o.sourced_id
LIMIT @page_size::int;

-- name: GetOrgForSubject :one
SELECT DISTINCT o.*
FROM org o
JOIN auth_effective_access a ON a.org_sourced_id = o.sourced_id
WHERE a.subject = @subject::text
  AND o.sourced_id = @sourced_id::text;

-- ── AcademicSession ───────────────────────────────────────────────────────────
-- Academic sessions carry no org: a school year is a property of the deployment,
-- not of any one district. They are therefore readable by any principal holding
-- any live grant, and the EXISTS below is that check. Scoping them per-org would
-- require inventing an ownership edge the OneRoster model does not define.

-- name: ListAcademicSessionsForSubject :many
SELECT s.*
FROM academic_session s
WHERE EXISTS (SELECT 1 FROM auth_effective_access a WHERE a.subject = @subject::text)
  AND s.status <> 'tobedeleted'
  AND (@session_type::text = '' OR s.type = @session_type::text)
  AND s.sourced_id > @after_sourced_id::text
ORDER BY s.sourced_id
LIMIT @page_size::int;

-- name: GetAcademicSessionForSubject :one
SELECT s.*
FROM academic_session s
WHERE EXISTS (SELECT 1 FROM auth_effective_access a WHERE a.subject = @subject::text)
  AND s.sourced_id = @sourced_id::text;

-- ── Course ────────────────────────────────────────────────────────────────────

-- name: ListCoursesForSubject :many
SELECT DISTINCT c.*
FROM course c
JOIN auth_effective_access a ON a.org_sourced_id = c.org_sourced_id
WHERE a.subject = @subject::text
  AND c.status <> 'tobedeleted'
  AND (@org_sourced_id::text = '' OR c.org_sourced_id IN (
        SELECT descendant_sourced_id FROM org_closure
        WHERE ancestor_sourced_id = @org_sourced_id::text))
  AND c.sourced_id > @after_sourced_id::text
ORDER BY c.sourced_id
LIMIT @page_size::int;

-- name: GetCourseForSubject :one
SELECT DISTINCT c.*
FROM course c
JOIN auth_effective_access a ON a.org_sourced_id = c.org_sourced_id
WHERE a.subject = @subject::text
  AND c.sourced_id = @sourced_id::text;

-- ── Class ─────────────────────────────────────────────────────────────────────

-- name: ListClassesForSubject :many
SELECT DISTINCT c.*
FROM class c
JOIN auth_effective_access a ON a.org_sourced_id = c.school_sourced_id
WHERE a.subject = @subject::text
  AND c.status <> 'tobedeleted'
  AND (@org_sourced_id::text = '' OR c.school_sourced_id IN (
        SELECT descendant_sourced_id FROM org_closure
        WHERE ancestor_sourced_id = @org_sourced_id::text))
  AND c.sourced_id > @after_sourced_id::text
ORDER BY c.sourced_id
LIMIT @page_size::int;

-- name: GetClassForSubject :one
SELECT DISTINCT c.*
FROM class c
JOIN auth_effective_access a ON a.org_sourced_id = c.school_sourced_id
WHERE a.subject = @subject::text
  AND c.sourced_id = @sourced_id::text;

-- name: ListClassTermsForClasses :many
-- Association load for a page of classes, batched by class id rather than issued
-- once per class: PostgreSQL is a remote server, so N+1 here would be N round
-- trips. Returns the terms of every requested class at once; the caller groups
-- them by class_sourced_id.
SELECT ct.class_sourced_id, ct.ordinal, ct.academic_session_href, s.*
FROM class_term ct
JOIN academic_session s ON s.sourced_id = ct.academic_session_sourced_id
WHERE ct.class_sourced_id = ANY(@class_sourced_ids::text[])
ORDER BY ct.class_sourced_id, ct.ordinal;

-- ── User ──────────────────────────────────────────────────────────────────────
-- A user is scoped through the orgs their roles name, not through primary_org:
-- primary_org is nullable and a user may legitimately hold roles at several
-- schools, so the role edge is the one that decides visibility.

-- name: ListUsersForSubject :many
SELECT DISTINCT u.*
FROM oneroster_user u
JOIN user_role r ON r.user_sourced_id = u.sourced_id
JOIN auth_effective_access a ON a.org_sourced_id = r.org_sourced_id
WHERE a.subject = @subject::text
  AND u.status <> 'tobedeleted'
  AND (@role::text = '' OR r.role = @role::text)
  AND (@org_sourced_id::text = '' OR r.org_sourced_id IN (
        SELECT descendant_sourced_id FROM org_closure
        WHERE ancestor_sourced_id = @org_sourced_id::text))
  AND u.sourced_id > @after_sourced_id::text
ORDER BY u.sourced_id
LIMIT @page_size::int;

-- name: GetUserForSubject :one
SELECT DISTINCT u.*
FROM oneroster_user u
JOIN user_role r ON r.user_sourced_id = u.sourced_id
JOIN auth_effective_access a ON a.org_sourced_id = r.org_sourced_id
WHERE a.subject = @subject::text
  AND u.sourced_id = @sourced_id::text;

-- name: ListUserRolesForUsers :many
-- Batched association load for a page of users; see ListClassTermsForClasses.
SELECT * FROM user_role
WHERE user_sourced_id = ANY(@user_sourced_ids::text[])
ORDER BY user_sourced_id, ordinal;

-- name: ListUserIdentifiersForUsers :many
SELECT * FROM user_identifier
WHERE user_sourced_id = ANY(@user_sourced_ids::text[])
ORDER BY user_sourced_id, ordinal;

-- name: ListUserAgentsForUsers :many
SELECT user_sourced_id, agent_sourced_id, agent_href, ordinal
FROM user_agent
WHERE user_sourced_id = ANY(@user_sourced_ids::text[])
ORDER BY user_sourced_id, ordinal;

-- ── Enrollment ────────────────────────────────────────────────────────────────

-- name: ListEnrollmentsForSubject :many
SELECT DISTINCT e.*
FROM enrollment e
JOIN auth_effective_access a ON a.org_sourced_id = e.school_sourced_id
WHERE a.subject = @subject::text
  AND e.status <> 'tobedeleted'
  AND (@org_sourced_id::text = '' OR e.school_sourced_id IN (
        SELECT descendant_sourced_id FROM org_closure
        WHERE ancestor_sourced_id = @org_sourced_id::text))
  AND e.sourced_id > @after_sourced_id::text
ORDER BY e.sourced_id
LIMIT @page_size::int;

-- name: GetEnrollmentForSubject :one
SELECT DISTINCT e.*
FROM enrollment e
JOIN auth_effective_access a ON a.org_sourced_id = e.school_sourced_id
WHERE a.subject = @subject::text
  AND e.sourced_id = @sourced_id::text;

-- ── Demographics ──────────────────────────────────────────────────────────────
-- Demographics share the user's sourced_id, so visibility follows the user's.

-- name: ListDemographicsForSubject :many
SELECT DISTINCT d.*
FROM demographics d
JOIN user_role r ON r.user_sourced_id = d.sourced_id
JOIN auth_effective_access a ON a.org_sourced_id = r.org_sourced_id
WHERE a.subject = @subject::text
  AND d.status <> 'tobedeleted'
  AND (@org_sourced_id::text = '' OR r.org_sourced_id IN (
        SELECT descendant_sourced_id FROM org_closure
        WHERE ancestor_sourced_id = @org_sourced_id::text))
  AND d.sourced_id > @after_sourced_id::text
ORDER BY d.sourced_id
LIMIT @page_size::int;

-- name: GetDemographicsForSubject :one
SELECT DISTINCT d.*
FROM demographics d
JOIN user_role r ON r.user_sourced_id = d.sourced_id
JOIN auth_effective_access a ON a.org_sourced_id = r.org_sourced_id
WHERE a.subject = @subject::text
  AND d.sourced_id = @sourced_id::text;
