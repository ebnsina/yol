-- +goose Up

-- Somebody gives us access to their repositories by installing our application on GitHub. What
-- comes back is an installation identifier, and this is the record of which organization it belongs
-- to. Nothing here can read a repository on its own: reading needs a token minted for one hour.
CREATE TABLE github_installations (
    id     uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,

    -- GitHub's own identifier for the installation. Unique across everyone, because an
    -- installation belongs to one account and that account is in one organization here.
    external_id text NOT NULL UNIQUE,
    -- Whose account it is, shown so somebody can tell two installations apart.
    account text NOT NULL,

    connected_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Set when GitHub tells us the installation was removed, so a project can explain why it
    -- stopped deploying rather than simply failing.
    revoked_at timestamptz
);

CREATE INDEX github_installations_org_idx ON github_installations (org_id);

ALTER TABLE github_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE github_installations FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON github_installations
    USING (org_id IS NOT DISTINCT FROM current_org_id());

-- A push arrives from GitHub with no organization in scope: all it carries is a repository and a
-- branch. Working out what that should deploy therefore has to happen before any scope is set, so
-- it goes through a function rather than a scoped query, exactly as sessions and enrollment do.
--
-- Only what a deploy needs comes back, and only for a project whose repository matches and whose
-- installation has not been revoked.
-- +goose StatementBegin
CREATE FUNCTION find_deploy_targets(p_repo_external_id text, p_branch text)
RETURNS TABLE (
    org_id         uuid,
    project_id     uuid,
    environment_id uuid,
    service_id     uuid,
    server_id      uuid,
    repo_full_name text,
    installation_id text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT p.org_id, p.id, e.id, s.id, e.server_id, p.repo_full_name, p.repo_installation_id
    FROM projects p
    JOIN environments e ON e.project_id = p.id
    JOIN services s ON s.env_id = e.id AND s.kind = 'app'
    JOIN github_installations i ON i.external_id = p.repo_installation_id AND i.org_id = p.org_id
    WHERE p.repo_provider = 'github'
      AND p.repo_external_id = p_repo_external_id
      AND e.branch = p_branch
      AND i.revoked_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION find_deploy_targets(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION find_deploy_targets(text, text) TO yol_app;

-- An installation being removed also arrives with no organization in scope, and marking it is what
-- lets a project say why it stopped deploying instead of failing on the next push.
-- +goose StatementBegin
CREATE FUNCTION revoke_installation(p_external_id text)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    UPDATE github_installations SET revoked_at = now()
    WHERE external_id = p_external_id AND revoked_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION revoke_installation(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION revoke_installation(text) TO yol_app;

-- +goose Down
DROP FUNCTION revoke_installation(text);
DROP FUNCTION find_deploy_targets(text, text);
DROP TABLE github_installations;
