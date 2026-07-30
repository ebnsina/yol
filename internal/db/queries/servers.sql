-- name: CreateServer :one
INSERT INTO servers (id, org_id, name, mode, host, ssh_port, ssh_user, ssh_secret, ssh_secret_kind)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetServer :one
SELECT * FROM servers WHERE id = $1;

-- name: ListServers :many
SELECT * FROM servers WHERE org_id = $1 ORDER BY created_at DESC;

-- name: UpdateServerStatus :exec
UPDATE servers
SET status = $2, failure_reason = $3, updated_at = now()
WHERE id = $1;

-- name: UpdateServerFacts :exec
UPDATE servers
SET os_name = $2, os_version = $3, arch = $4, kernel = $5,
    cpu_count = $6, memory_bytes = $7, docker_version = $8, updated_at = now()
WHERE id = $1;

-- name: SetServerRoutingMode :exec
UPDATE servers SET routing_mode = $2, updated_at = now() WHERE id = $1;

-- name: SetServerAgentToken :exec
UPDATE servers SET agent_token_hash = $2, updated_at = now() WHERE id = $1;

-- Called the moment setup finishes: we should not hold a customer's server password.
-- name: ClearServerPassword :exec
UPDATE servers
SET ssh_secret = NULL, ssh_secret_kind = NULL, updated_at = now()
WHERE id = $1 AND ssh_secret_kind = 'password';

-- name: TouchServerAgent :exec
UPDATE servers
SET agent_last_seen_at = now(), agent_version = $2, status = 'online', updated_at = now()
WHERE id = $1;

-- name: DeleteServer :exec
DELETE FROM servers WHERE id = $1 AND org_id = $2;

-- name: RecordServerEvent :one
INSERT INTO server_events (id, org_id, server_id, step, message, level)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListServerEvents :many
SELECT * FROM server_events
WHERE server_id = $1 AND created_at > $2
ORDER BY created_at;

-- Upsert so a repeated survey updates rather than duplicates, and first_seen_at is kept.
-- name: RecordDiscoveredResource :exec
INSERT INTO discovered_resources (
    id, org_id, server_id, kind, external_id, name, status, image, version, ports,
    size_bytes, managed, details, first_seen_at, last_seen_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now(), now())
ON CONFLICT (server_id, kind, external_id) DO UPDATE
SET name = EXCLUDED.name,
    status = EXCLUDED.status,
    image = EXCLUDED.image,
    version = EXCLUDED.version,
    ports = EXCLUDED.ports,
    size_bytes = EXCLUDED.size_bytes,
    managed = EXCLUDED.managed,
    details = EXCLUDED.details,
    last_seen_at = now();

-- name: ListDiscoveredResources :many
SELECT * FROM discovered_resources
WHERE server_id = $1
ORDER BY kind, name;

-- Anything not seen in the latest report has gone from the machine.
-- name: DeleteStaleDiscoveredResources :execrows
DELETE FROM discovered_resources
WHERE server_id = $1 AND last_seen_at < $2 AND adopted_at IS NULL;

-- name: AdoptDiscoveredResource :one
UPDATE discovered_resources
SET adopted_at = now(), adopted_container_created_at = $3
WHERE server_id = $1 AND id = $2 AND kind = 'container'
RETURNING *;

-- Releasing touches the container itself not at all.
-- name: ReleaseDiscoveredResource :exec
UPDATE discovered_resources
SET adopted_at = NULL, adopted_container_created_at = NULL
WHERE server_id = $1 AND id = $2;
