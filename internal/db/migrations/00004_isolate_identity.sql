-- +goose Up
-- Closes the remaining gaps: organizations, users and sessions were readable with no scope,
-- so one missing WHERE clause could have listed every organization or every email address.
--
-- Operations that happen BEFORE identity is established cannot be expressed as a policy,
-- because there is no current user yet to compare against. Those get one narrow
-- SECURITY DEFINER function each, authorized by presenting a secret (a token) or by the
-- credential check itself. Everything after identity is established relies on policies.
--
-- Production note: the role owning these functions must be a table owner but NOT a
-- superuser. Owning the tables is enough to bypass the policies; superuser is not required
-- and makes a mistake here far more damaging.

ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;

-- Reachable while acting inside the organization.
CREATE POLICY org_current_scope ON organizations
    USING (id IS NOT DISTINCT FROM current_org_id());

-- Reachable when looking one up by slug before any organization is chosen. SELECT only:
-- a writable version would let a member alter any organization they belong to regardless
-- of the scope the request was made in.
CREATE POLICY org_member_read ON organizations FOR SELECT
    USING (EXISTS (
        SELECT 1 FROM memberships m
        WHERE m.org_id = organizations.id AND m.user_id = current_user_id()
    ));

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;

CREATE POLICY user_self ON users
    USING (id IS NOT DISTINCT FROM current_user_id());

-- Members of the current organization are visible to each other, which is what the member
-- list needs, and nothing wider.
CREATE POLICY user_org_peer ON users FOR SELECT
    USING (EXISTS (
        SELECT 1 FROM memberships m
        WHERE m.org_id = current_org_id() AND m.user_id = users.id
    ));

-- Signing up happens before there is a current user. Permitting the insert leaks nothing;
-- reading is what the policies above restrict.
CREATE POLICY user_signup ON users FOR INSERT WITH CHECK (true);

ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE ROW LEVEL SECURITY;

CREATE POLICY session_own ON sessions
    USING (user_id IS NOT DISTINCT FROM current_user_id());

-- A session is created immediately after credentials are verified, before the session it
-- is creating can identify anyone.
CREATE POLICY session_create ON sessions FOR INSERT WITH CHECK (true);

-- Verifying a password: the caller has presented an address and a password, and the
-- password is checked by the application against the hash returned here.
-- +goose StatementBegin
CREATE FUNCTION find_user_for_login(p_email citext)
RETURNS TABLE (
    user_id             uuid,
    user_email          citext,
    user_name           text,
    user_password_hash  text,
    user_verified_at    timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT id, email, name, password_hash, email_verified_at
    FROM users
    WHERE email = p_email
$$;
-- +goose StatementEnd

-- Establishing identity from a session token. Also records activity, but only when the
-- stored value is stale, so a busy client does not cause a write on every request.
-- +goose StatementBegin
CREATE FUNCTION authenticate_session(p_token_hash bytea)
RETURNS TABLE (
    user_id           uuid,
    user_email        citext,
    user_name         text,
    user_verified_at  timestamptz,
    session_expires_at timestamptz
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    UPDATE sessions s
    SET last_seen_at = now()
    WHERE s.token_hash = p_token_hash
      AND s.expires_at > now()
      AND s.last_seen_at < now() - interval '1 minute';

    RETURN QUERY
    SELECT u.id, u.email, u.name, u.email_verified_at, s.expires_at
    FROM sessions s
    JOIN users u ON u.id = s.user_id
    WHERE s.token_hash = p_token_hash
      AND s.expires_at > now();
END
$$;
-- +goose StatementEnd

-- Signing out. Holding the token is what authorizes discarding it.
-- +goose StatementBegin
CREATE FUNCTION delete_session(p_token_hash bytea)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    DELETE FROM sessions WHERE token_hash = p_token_hash
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION find_user_for_login(citext) FROM PUBLIC;
REVOKE ALL ON FUNCTION authenticate_session(bytea) FROM PUBLIC;
REVOKE ALL ON FUNCTION delete_session(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION find_user_for_login(citext) TO yol_app;
GRANT EXECUTE ON FUNCTION authenticate_session(bytea) TO yol_app;
GRANT EXECUTE ON FUNCTION delete_session(bytea) TO yol_app;

-- +goose Down
DROP FUNCTION delete_session(bytea);
DROP FUNCTION authenticate_session(bytea);
DROP FUNCTION find_user_for_login(citext);

DROP POLICY session_create ON sessions;
DROP POLICY session_own ON sessions;
ALTER TABLE sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sessions DISABLE ROW LEVEL SECURITY;

DROP POLICY user_signup ON users;
DROP POLICY user_org_peer ON users;
DROP POLICY user_self ON users;
ALTER TABLE users NO FORCE ROW LEVEL SECURITY;
ALTER TABLE users DISABLE ROW LEVEL SECURITY;

DROP POLICY org_member_read ON organizations;
DROP POLICY org_current_scope ON organizations;
ALTER TABLE organizations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE organizations DISABLE ROW LEVEL SECURITY;
