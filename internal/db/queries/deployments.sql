-- name: CreateDeployment :one
INSERT INTO deployments (id, org_id, service_id, commit_ref, commit_sha, status)
VALUES ($1, $2, $3, $4, $5, 'queued')
RETURNING *;

-- name: GetDeployment :one
SELECT * FROM deployments WHERE id = $1;

-- name: ListDeployments :many
SELECT * FROM deployments
WHERE service_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: GetLiveDeployment :one
SELECT * FROM deployments WHERE service_id = $1 AND status = 'live';

-- The status is cast explicitly because it is read more than once here, and the same placeholder
-- compared against text and against the enum cannot be given one type.
-- name: SetDeploymentStatus :exec
UPDATE deployments
SET status = sqlc.arg(status)::deployment_status,
    failure_reason = sqlc.arg(failure_reason),
    started_at = COALESCE(started_at,
        CASE WHEN sqlc.arg(status)::deployment_status = 'building' THEN now() END),
    finished_at = CASE
        WHEN sqlc.arg(status)::deployment_status IN ('live', 'failed') THEN now()
        ELSE finished_at END
WHERE id = sqlc.arg(id);

-- name: SetDeploymentImage :exec
UPDATE deployments SET image_ref = $2, commit_sha = COALESCE($3, commit_sha) WHERE id = $1;

-- Only one deployment is live at a time, so the previous one steps aside first.
-- name: SupersedePreviousDeployments :exec
UPDATE deployments
SET status = 'superseded', finished_at = COALESCE(finished_at, now())
WHERE service_id = $1 AND status = 'live' AND id <> $2;

-- name: SetDeploymentReplaced :exec
UPDATE deployments SET replaced_deployment_id = $2 WHERE id = $1;

-- The port and health path are captured here rather than read from the service later, so changing
-- either does not re-point traffic at a version that never listened on it.
-- name: CreatePlacement :one
INSERT INTO placements (id, org_id, deployment_id, server_id, container_name, port, health_path)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListPlacements :many
SELECT * FROM placements WHERE deployment_id = $1;

-- Every container that should be running on one server: the deployment serving each service, and
-- one going out alongside it. A version being rolled out has to be in the desired state or the
-- agent would never start it, and it cannot become live until the agent reports that it answers.
-- name: ListLivePlacementsForServer :many
SELECT pl.*, d.image_ref, d.status, d.id AS deployment_id, s.id AS service_id, s.name AS service_name,
       s.kind, s.memory_limit_bytes,
       e.id AS environment_id, e.name AS environment_name,
       p.id AS project_id, p.slug AS project_slug
FROM placements pl
JOIN deployments d ON d.id = pl.deployment_id
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.env_id
JOIN projects p ON p.id = e.project_id
WHERE pl.server_id = $1 AND d.status IN ('live', 'deploying')
ORDER BY p.slug, e.name, s.name, d.created_at;

-- name: AppendDeploymentLog :exec
INSERT INTO deployment_logs (id, org_id, deployment_id, stream, text)
VALUES ($1, $2, $3, $4, $5);

-- name: ListDeploymentLogs :many
SELECT * FROM deployment_logs
WHERE deployment_id = $1 AND at > $2
ORDER BY at
LIMIT $3;

-- name: SetEnvVar :exec
INSERT INTO env_vars (id, org_id, env_id, name, value)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (env_id, name) DO UPDATE
SET value = EXCLUDED.value, updated_at = now();

-- Names only, so a client can list them without values ever leaving the control plane.
-- name: ListEnvVarNames :many
SELECT name, updated_at FROM env_vars WHERE env_id = $1 ORDER BY name;

-- name: ListEnvVars :many
SELECT name, value FROM env_vars WHERE env_id = $1 ORDER BY name;

-- name: DeleteEnvVar :exec
DELETE FROM env_vars WHERE env_id = $1 AND name = $2;

-- Takes the lowest free port in the range, so allocations stay compact and predictable.
-- name: AllocatePort :one
INSERT INTO port_allocations (id, org_id, server_id, port, service_id, purpose)
SELECT $1, $2, $3, candidate, $4, $5
FROM generate_series(sqlc.arg(range_start)::int, sqlc.arg(range_end)::int) AS candidate
WHERE NOT EXISTS (
    SELECT 1 FROM port_allocations existing
    WHERE existing.server_id = $3 AND existing.port = candidate
)
ORDER BY candidate
LIMIT 1
RETURNING *;

-- name: ListPortAllocations :many
SELECT * FROM port_allocations WHERE server_id = $1 ORDER BY port;

-- name: GetServicePort :one
SELECT * FROM port_allocations WHERE service_id = $1 AND purpose = $2;

-- name: ReleaseServicePorts :exec
DELETE FROM port_allocations WHERE service_id = $1;

-- name: CreateDomain :one
INSERT INTO domains (id, org_id, service_id, hostname, ours, verified_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListDomains :many
SELECT * FROM domains WHERE service_id = $1 ORDER BY ours DESC, hostname;

-- name: MarkDomainVerified :exec
UPDATE domains SET verified_at = now() WHERE id = $1 AND org_id = $2;

-- name: DeleteDomain :exec
DELETE FROM domains WHERE id = $1 AND org_id = $2;

-- Hostnames the router should answer for on one server.
-- name: ListDomainsForServer :many
SELECT d.hostname, d.service_id, s.health_port
FROM domains d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.env_id
WHERE e.server_id = $1 AND (d.verified_at IS NOT NULL OR d.ours)
ORDER BY d.hostname;

-- One hostname with the server it is meant to point at, which is what verification compares against.
-- name: GetDomain :one
SELECT d.*, e.server_id
FROM domains d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.env_id
WHERE d.id = $1;
