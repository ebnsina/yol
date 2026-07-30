-- +goose Up
-- An agent starts with a single-use token written to the server during setup, and trades it
-- for a lasting one. That keeps the short-lived secret out of the long-lived config file, and
-- means a token seen during setup cannot be replayed afterwards.
ALTER TABLE servers
    ADD COLUMN enrollment_token_hash bytea UNIQUE,
    ADD COLUMN enrollment_expires_at timestamptz;

-- Enrolling happens before the agent has any identity, so no organization is in scope and the
-- tenant policies cannot apply. This is the sanctioned way past that, matching how sessions
-- and invitations already work: it is reachable only by presenting the secret, affects at most
-- one row, and consumes the token as it goes so a second attempt finds nothing.
-- +goose StatementBegin
CREATE FUNCTION enroll_agent(p_enrollment_hash bytea, p_agent_hash bytea)
RETURNS TABLE (
    server_id     uuid,
    server_org_id uuid,
    server_mode   server_mode
)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    UPDATE servers
    SET agent_token_hash = p_agent_hash,
        enrollment_token_hash = NULL,
        enrollment_expires_at = NULL,
        updated_at = now()
    WHERE enrollment_token_hash = p_enrollment_hash
      AND enrollment_expires_at > now()
    RETURNING id, org_id, mode
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION enroll_agent(bytea, bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION enroll_agent(bytea, bytea) TO yol_app;

-- +goose Down
DROP FUNCTION enroll_agent(bytea, bytea);
ALTER TABLE servers
    DROP COLUMN enrollment_token_hash,
    DROP COLUMN enrollment_expires_at;
