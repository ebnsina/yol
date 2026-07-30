-- +goose Up
-- Someone accepting an invitation is not yet a member, so the org policy would hide the
-- row from them. This is the only sanctioned way past that policy: it returns at most one
-- row, reachable only by presenting the secret token, and it decides nothing else.
-- +goose StatementBegin
CREATE FUNCTION find_invitation_by_token(p_token_hash bytea)
RETURNS TABLE (
    id         uuid,
    org_id     uuid,
    email      citext,
    role       membership_role,
    expires_at timestamptz,
    org_name   text,
    org_slug   citext
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT i.id, i.org_id, i.email, i.role, i.expires_at, o.name, o.slug
    FROM invitations i
    JOIN organizations o ON o.id = i.org_id
    WHERE i.token_hash = p_token_hash
      AND i.accepted_at IS NULL
      AND i.expires_at > now()
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION find_invitation_by_token(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION find_invitation_by_token(bytea) TO yol_app;

-- +goose Down
DROP FUNCTION find_invitation_by_token(bytea);
