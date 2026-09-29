-- Authorization queries: who may manage which rosters.
--
-- The access checks read auth_effective_access, which already expands org
-- subtrees and filters expired grants and disabled principals, so a request path
-- never runs the recursive walk itself.

-- ── Principals ────────────────────────────────────────────────────────────────

-- name: UpsertPrincipal :one
INSERT INTO auth_principal (
    subject, kind, email, display_name, oneroster_user_sourced_id, disabled
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (subject) DO UPDATE SET
    kind                      = EXCLUDED.kind,
    email                     = EXCLUDED.email,
    display_name              = EXCLUDED.display_name,
    oneroster_user_sourced_id = EXCLUDED.oneroster_user_sourced_id,
    disabled                  = EXCLUDED.disabled,
    modified_at               = NOW()
RETURNING *;

-- name: GetPrincipalBySubject :one
SELECT * FROM auth_principal WHERE subject = $1;

-- name: GetPrincipal :one
SELECT * FROM auth_principal WHERE id = $1;

-- name: ListPrincipals :many
-- Keyset paged on subject, which is UNIQUE and therefore a total order. An empty
-- @after_subject starts at the first page.
SELECT * FROM auth_principal
WHERE subject > @after_subject::text
ORDER BY subject
LIMIT @page_size::int;

-- name: SetPrincipalDisabled :one
UPDATE auth_principal
SET disabled = $2, modified_at = NOW()
WHERE id = $1
RETURNING *;

-- ── Roles ─────────────────────────────────────────────────────────────────────

-- name: ListAuthRoles :many
SELECT * FROM auth_role ORDER BY name;

-- name: GetAuthRole :one
SELECT * FROM auth_role WHERE name = $1;

-- ── Grants ────────────────────────────────────────────────────────────────────

-- name: GrantRole :one
-- A repeat grant refreshes the expiry and the audit trail rather than erroring,
-- so re-running a provisioning script is safe.
INSERT INTO auth_grant (principal_id, role_name, org_sourced_id, granted_by, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (principal_id, role_name, org_sourced_id) DO UPDATE SET
    granted_by = EXCLUDED.granted_by,
    granted_at = NOW(),
    expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: RevokeGrant :exec
DELETE FROM auth_grant WHERE id = $1;

-- name: RevokeRoleForPrincipal :exec
DELETE FROM auth_grant
WHERE principal_id = $1
  AND role_name = $2
  AND org_sourced_id IS NOT DISTINCT FROM $3;

-- name: ListGrantsForPrincipal :many
SELECT g.*, r.is_global
FROM auth_grant g
JOIN auth_role r ON r.name = g.role_name
WHERE g.principal_id = $1
ORDER BY g.role_name, g.org_sourced_id;

-- name: ListGrantsForOrg :many
-- Answers "who has access to this school, and who gave it to them?" — including
-- grants inherited from an ancestor org, which is the part a plain lookup on
-- auth_grant would miss.
SELECT g.*, p.subject, p.email, closure.depth
FROM auth_grant g
JOIN auth_principal p ON p.id = g.principal_id
JOIN org_closure closure ON closure.ancestor_sourced_id = g.org_sourced_id
WHERE closure.descendant_sourced_id = $1
ORDER BY closure.depth, p.subject;

-- ── Access checks ─────────────────────────────────────────────────────────────

-- name: PrincipalHasRoleForOrg :one
-- The interceptor's question: may this subject act in this role on this org?
SELECT EXISTS (
    SELECT 1 FROM auth_effective_access
    WHERE subject = $1 AND role_name = $2 AND org_sourced_id = $3
) AS allowed;

-- name: PrincipalHasAnyRoleForOrg :one
SELECT EXISTS (
    SELECT 1 FROM auth_effective_access
    WHERE subject = $1 AND role_name = ANY($2::text[]) AND org_sourced_id = $3
) AS allowed;

-- name: PrincipalIsGlobalAdmin :one
-- Checked without reference to an org, so it answers correctly on an empty
-- database — where the cross join in auth_effective_access yields no rows.
SELECT EXISTS (
    SELECT 1
    FROM auth_grant g
    JOIN auth_principal p ON p.id = g.principal_id
    JOIN auth_role r ON r.name = g.role_name
    WHERE p.subject = $1
      AND NOT p.disabled
      AND r.is_global
      AND (g.expires_at IS NULL OR g.expires_at > NOW())
) AS allowed;

-- name: ListAccessibleOrgs :many
-- Every org this subject may touch in any of the given roles. Used to scope a
-- list response rather than filtering it per row.
SELECT DISTINCT o.*
FROM auth_effective_access a
JOIN org o ON o.sourced_id = a.org_sourced_id
WHERE a.subject = $1 AND a.role_name = ANY($2::text[])
ORDER BY o.sourced_id;

-- name: ListRolesForSubject :many
SELECT DISTINCT role_name FROM auth_effective_access
WHERE subject = $1
ORDER BY role_name;

-- ── API tokens ────────────────────────────────────────────────────────────────

-- name: CreateAPIToken :one
INSERT INTO auth_api_token (principal_id, name, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetPrincipalByTokenHash :one
-- The authentication path: hash the presented bearer token, look it up here. A
-- revoked, expired, or disabled row returns no principal.
SELECT p.*
FROM auth_api_token t
JOIN auth_principal p ON p.id = t.principal_id
WHERE t.token_hash = $1
  AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > NOW())
  AND NOT p.disabled;

-- name: TouchAPToken :exec
UPDATE auth_api_token SET last_used_at = NOW() WHERE token_hash = $1;

-- name: RevokeAPIToken :exec
UPDATE auth_api_token SET revoked_at = NOW() WHERE id = $1 AND revoked_at IS NULL;

-- name: ListAPITokensForPrincipal :many
-- token_hash is deliberately not selected: nothing outside authentication needs
-- it, and a listing is the likeliest thing to end up in a log.
SELECT id, principal_id, name, created_at, last_used_at, expires_at, revoked_at
FROM auth_api_token
WHERE principal_id = $1
ORDER BY created_at DESC;
