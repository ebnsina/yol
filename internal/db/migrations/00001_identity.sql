-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

-- Applied to every tenant-scoped table so a missing WHERE clause cannot cross orgs.
CREATE OR REPLACE FUNCTION current_org_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT nullif(current_setting('app.org_id', true), '')::uuid
$$;

-- Lets a signed-in user read their own membership rows across organizations, which is
-- how "my organizations" works without opening tenant data.
CREATE OR REPLACE FUNCTION current_user_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT nullif(current_setting('app.user_id', true), '')::uuid
$$;

CREATE TABLE users (
    id                uuid PRIMARY KEY,
    email             citext NOT NULL UNIQUE,
    name              text NOT NULL,
    password_hash     text NOT NULL,
    email_verified_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE organizations (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    slug       citext NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TYPE membership_role AS ENUM ('owner', 'admin', 'member', 'viewer');

CREATE TABLE memberships (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       membership_role NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id)
);

CREATE INDEX memberships_user_id_idx ON memberships (user_id);

-- Sessions store only a hash, so a database leak cannot be replayed as a login.
CREATE TABLE sessions (
    token_hash   bytea PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_agent   text NOT NULL,
    ip           inet,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE invitations (
    id            uuid PRIMARY KEY,
    org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email         citext NOT NULL,
    role          membership_role NOT NULL,
    token_hash    bytea NOT NULL UNIQUE,
    invited_by_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    accepted_at   timestamptz
);

CREATE UNIQUE INDEX invitations_pending_idx
    ON invitations (org_id, email)
    WHERE accepted_at IS NULL;

CREATE TABLE audit_log (
    id            uuid PRIMARY KEY,
    org_id        uuid REFERENCES organizations(id) ON DELETE CASCADE,
    actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    action        text NOT NULL,
    target_type   text NOT NULL,
    target_id     uuid,
    metadata      jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip            inet,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_org_created_idx ON audit_log (org_id, created_at DESC);

-- FORCE is required because the application connects as the table owner, which would
-- otherwise bypass the policy. A single omitted org filter then returns nothing.
ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON memberships
    USING (org_id IS NOT DISTINCT FROM current_org_id());

-- SELECT only on purpose. A writable version would let a user insert themselves into any
-- organization, because permissive policies are combined with OR.
CREATE POLICY own_membership ON memberships FOR SELECT
    USING (user_id IS NOT DISTINCT FROM current_user_id());

ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON invitations
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON audit_log
    USING (org_id IS NOT DISTINCT FROM current_org_id());

-- +goose Down
DROP TABLE audit_log;
DROP TABLE invitations;
DROP TABLE sessions;
DROP TABLE memberships;
DROP TABLE organizations;
DROP TABLE users;
DROP TYPE membership_role;
DROP FUNCTION current_user_id();
DROP FUNCTION current_org_id();
