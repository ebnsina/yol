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

-- name: DeleteService :exec
DELETE FROM services WHERE id = $1 AND org_id = $2;
