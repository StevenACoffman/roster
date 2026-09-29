-- Authorization: who may read and manage which rosters.
--
-- This is deliberately separate from OneRoster's own Role table (user_role in
-- 001). The two answer different questions:
--
--   user_role      what the source system says a person IS within an org
--                  ("Ana is a teacher at Lincoln High") — roster data, replaced
--                  wholesale on every import.
--   auth_grant     what a caller of this service may DO, and over which orgs
--                  ("Ana may write rosters for the Springfield district") —
--                  operator-managed, and must survive an import.
--
-- Conflating them would mean a district's nightly feed could silently grant or
-- revoke access to this service.
--
-- Scope is an org subtree, not a single org: a grant on a district covers every
-- school beneath it, resolved through the org_closure view from 001. That is why
-- these tables reference org rather than duplicating a school list.

-- +goose Up
-- +goose StatementBegin

-- ── Principals ────────────────────────────────────────────────────────────────
-- An authenticated caller. `subject` is whatever the authentication layer
-- establishes — an OIDC sub, or the identifier attached to an API token — and is
-- the only join key between a request and this table.
CREATE TABLE IF NOT EXISTS auth_principal (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject                   TEXT        NOT NULL UNIQUE,
    kind                      TEXT        NOT NULL CHECK (kind IN ('person', 'service')),
    email                     TEXT,
    display_name              TEXT,
    -- Optional link to the roster. A district administrator who is also a User in
    -- the roster gets one; a nightly import service account does not. ON DELETE
    -- SET NULL, because losing the roster row must not delete the principal and
    -- with it their grants.
    oneroster_user_sourced_id TEXT        REFERENCES oneroster_user (sourced_id) ON DELETE SET NULL,
    disabled                  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    modified_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_auth_principal_oneroster_user
    ON auth_principal (oneroster_user_sourced_id);
CREATE INDEX IF NOT EXISTS idx_auth_principal_email ON auth_principal (email);

-- ── Roles ─────────────────────────────────────────────────────────────────────
-- The service's own role vocabulary. A row here is a name the RPC policy can
-- require; the policy itself (which procedure needs which role) stays in
-- configuration, so changing it does not need a migration.
CREATE TABLE IF NOT EXISTS auth_role (
    name        TEXT PRIMARY KEY,
    description TEXT    NOT NULL DEFAULT '',
    -- A global role's grants ignore org scope entirely. Reserved for operators.
    is_global   BOOLEAN NOT NULL DEFAULT FALSE
);

INSERT INTO auth_role (name, description, is_global) VALUES
    ('roster_admin',   'Full read and write access across every org.', TRUE),
    ('district_admin', 'Read and write rosters for the scoped org and everything beneath it.', FALSE),
    ('school_admin',   'Read and write rosters for the scoped school.', FALSE),
    ('data_provider',  'Write-only: may import rosters into the scoped org subtree.', FALSE),
    ('roster_reader',  'Read-only access to the scoped org subtree.', FALSE)
ON CONFLICT (name) DO NOTHING;

-- ── Grants ────────────────────────────────────────────────────────────────────
-- The authorization relationship: this principal holds this role over this org
-- subtree. A NULL org_sourced_id means deployment-wide, which only makes sense
-- for a global role — enforced by the trigger below rather than a CHECK, because
-- a CHECK cannot consult auth_role.
CREATE TABLE IF NOT EXISTS auth_grant (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    principal_id   UUID        NOT NULL REFERENCES auth_principal (id) ON DELETE CASCADE,
    role_name      TEXT        NOT NULL REFERENCES auth_role (name) ON DELETE RESTRICT,
    org_sourced_id TEXT        REFERENCES org (sourced_id) ON DELETE CASCADE,
    -- Who granted this, for the audit question "why does this person have access?"
    granted_by     UUID        REFERENCES auth_principal (id) ON DELETE SET NULL,
    granted_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- NULL never expires. A set value is enforced at read time, not by a job.
    expires_at     TIMESTAMPTZ,
    -- NULLS NOT DISTINCT so a principal cannot hold the same global role twice;
    -- without it PostgreSQL treats every NULL org as a different row.
    CONSTRAINT auth_grant_unique UNIQUE NULLS NOT DISTINCT (principal_id, role_name, org_sourced_id)
);

CREATE INDEX IF NOT EXISTS idx_auth_grant_principal ON auth_grant (principal_id);
CREATE INDEX IF NOT EXISTS idx_auth_grant_org ON auth_grant (org_sourced_id);
CREATE INDEX IF NOT EXISTS idx_auth_grant_role ON auth_grant (role_name);

-- A non-global role must name an org, or it would silently grant everything.
CREATE OR REPLACE FUNCTION auth_grant_check_scope() RETURNS TRIGGER AS $$
DECLARE
    role_is_global BOOLEAN;
BEGIN
    SELECT is_global INTO role_is_global FROM auth_role WHERE name = NEW.role_name;
    IF NOT role_is_global AND NEW.org_sourced_id IS NULL THEN
        RAISE EXCEPTION 'role % is not global and requires org_sourced_id', NEW.role_name
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER auth_grant_scope_trigger
    BEFORE INSERT OR UPDATE ON auth_grant
    FOR EACH ROW EXECUTE FUNCTION auth_grant_check_scope();

-- ── API tokens ────────────────────────────────────────────────────────────────
-- Only the hash is stored, so a dump of this table cannot be replayed against
-- the service. A token is presented once at creation and never recoverable.
CREATE TABLE IF NOT EXISTS auth_api_token (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    principal_id UUID        NOT NULL REFERENCES auth_principal (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    token_hash   BYTEA       NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_auth_api_token_principal ON auth_api_token (principal_id);

-- ── Effective access ──────────────────────────────────────────────────────────
-- One row per (principal, role, org) the principal actually holds right now,
-- with the subtree already expanded and expired or disabled rows removed. This
-- is the view the authorization interceptor asks, so that "may this caller touch
-- this school?" is a single indexed lookup rather than a recursive query per
-- request.
CREATE OR REPLACE VIEW auth_effective_access AS
-- Scoped grants, expanded down the org tree.
SELECT p.id                       AS principal_id,
       p.subject                  AS subject,
       g.role_name                AS role_name,
       closure.descendant_sourced_id AS org_sourced_id,
       FALSE                      AS is_global
FROM auth_grant g
JOIN auth_principal p ON p.id = g.principal_id
JOIN auth_role r ON r.name = g.role_name
JOIN org_closure closure ON closure.ancestor_sourced_id = g.org_sourced_id
WHERE NOT p.disabled
  AND (g.expires_at IS NULL OR g.expires_at > NOW())
  AND NOT r.is_global
UNION
-- Global grants: every org, and valid even before any org exists.
SELECT p.id      AS principal_id,
       p.subject AS subject,
       g.role_name,
       o.sourced_id AS org_sourced_id,
       TRUE      AS is_global
FROM auth_grant g
JOIN auth_principal p ON p.id = g.principal_id
JOIN auth_role r ON r.name = g.role_name
CROSS JOIN org o
WHERE NOT p.disabled
  AND (g.expires_at IS NULL OR g.expires_at > NOW())
  AND r.is_global;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW IF EXISTS auth_effective_access;
DROP TRIGGER IF EXISTS auth_grant_scope_trigger ON auth_grant;
DROP FUNCTION IF EXISTS auth_grant_check_scope();
DROP TABLE IF EXISTS auth_api_token;
DROP TABLE IF EXISTS auth_grant;
DROP TABLE IF EXISTS auth_role;
DROP TABLE IF EXISTS auth_principal;
-- +goose StatementEnd
