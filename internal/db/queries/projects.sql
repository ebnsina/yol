-- name: CreateProject :one
INSERT INTO projects (id, org_id, name, slug, repo_provider, repo_full_name, repo_external_id, repo_installation_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1;

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE org_id = $1 AND slug = $2;

-- name: ListProjects :many
SELECT * FROM projects WHERE org_id = $1 ORDER BY created_at DESC;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = $1 AND org_id = $2;

-- name: CreateEnvironment :one
INSERT INTO environments (id, org_id, project_id, server_id, name, branch)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetEnvironment :one
SELECT * FROM environments WHERE id = $1;

-- name: ListEnvironments :many
SELECT * FROM environments WHERE project_id = $1 ORDER BY name;

-- Used to decide what a push should deploy.
-- name: FindEnvironmentsForBranch :many
SELECT e.* FROM environments e
JOIN projects p ON p.id = e.project_id
WHERE p.repo_external_id = $1 AND e.branch = $2;

-- name: SetEnvironmentServer :exec
UPDATE environments SET server_id = $2, updated_at = now() WHERE id = $1;

-- name: SetEnvironmentBranch :exec
UPDATE environments SET branch = $2, updated_at = now() WHERE id = $1;

-- name: CreateService :one
INSERT INTO services (id, org_id, env_id, name, kind, health_path, health_port, memory_limit_bytes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetService :one
SELECT * FROM services WHERE id = $1;

-- name: ListServices :many
SELECT * FROM services WHERE env_id = $1 ORDER BY kind, name;

-- Everything the agent on one server needs to run, across every project placed there.
-- name: ListServicesForServer :many
SELECT s.*, e.id AS environment_id, e.name AS environment_name, p.slug AS project_slug
FROM services s
JOIN environments e ON e.id = s.env_id
JOIN projects p ON p.id = e.project_id
WHERE e.server_id = $1
ORDER BY p.slug, e.name, s.name;

-- name: UpdateService :one
UPDATE services
SET health_path = $2, health_port = $3, memory_limit_bytes = $4, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteService :exec
DELETE FROM services WHERE id = $1 AND org_id = $2;

-- name: CreateInstallation :one
INSERT INTO github_installations (id, org_id, external_id, account, connected_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (external_id) DO UPDATE
SET account = EXCLUDED.account, revoked_at = NULL
RETURNING *;

-- name: ListInstallations :many
SELECT * FROM github_installations
WHERE org_id = $1 AND revoked_at IS NULL
ORDER BY account;

-- name: GetInstallation :one
SELECT * FROM github_installations WHERE org_id = $1 AND external_id = $2 AND revoked_at IS NULL;

-- name: DeleteInstallation :exec
DELETE FROM github_installations WHERE org_id = $1 AND external_id = $2;

-- name: SetProjectRepository :one
UPDATE projects
SET repo_provider = $2, repo_full_name = $3, repo_external_id = $4, repo_installation_id = $5,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: RevokeInstallation :exec
SELECT revoke_installation($1);

-- Everything a deploy of one environment needs, in one read. Scoped by row level security like
-- every other query here, unlike the pre-identity lookup a push goes through.
-- name: GetDeployTarget :one
SELECT p.org_id, p.id AS project_id, e.id AS environment_id, s.id AS service_id, e.server_id,
       e.branch, p.repo_full_name, p.repo_installation_id
FROM environments e
JOIN projects p ON p.id = e.project_id
JOIN services s ON s.env_id = e.id AND s.kind = 'app'
WHERE e.id = $1;
