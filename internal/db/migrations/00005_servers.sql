-- +goose Up
CREATE TYPE server_mode AS ENUM ('managed', 'watch');

-- How web traffic reaches apps on this server. Left null until the question is answered,
-- which only happens once we know whether ports 80 and 443 are already taken.
CREATE TYPE routing_mode AS ENUM ('takeover', 'behind_proxy');

CREATE TYPE server_status AS ENUM (
    'pending',      -- created, nothing done to it yet
    'surveying',    -- reading what is already there, changing nothing
    'awaiting_choice', -- survey found a conflict and needs an answer
    'installing',   -- making changes
    'online',       -- agent is connected
    'offline',      -- agent has not been heard from
    'failed'        -- setup did not complete
);

CREATE TABLE servers (
    id           uuid PRIMARY KEY,
    org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name         text NOT NULL,
    mode         server_mode NOT NULL DEFAULT 'managed',
    status       server_status NOT NULL DEFAULT 'pending',
    routing_mode routing_mode,

    host         text NOT NULL,
    ssh_port     integer NOT NULL DEFAULT 22,
    ssh_user     text NOT NULL DEFAULT 'root',

    -- Encrypted at rest. A password is deleted the moment setup finishes, because holding
    -- other people's server passwords is the worst thing to be breached with.
    ssh_secret        bytea,
    ssh_secret_kind   text CHECK (ssh_secret_kind IN ('key', 'password')),

    -- Only the hash is stored, so a database leak yields no usable agent credential.
    agent_token_hash  bytea UNIQUE,
    agent_version     text,
    agent_last_seen_at timestamptz,

    -- Reported by the survey and refreshed by the agent.
    os_name      text,
    os_version   text,
    arch         text,
    kernel       text,
    cpu_count    integer,
    memory_bytes bigint,
    docker_version text,

    -- Set when setup fails, in the same plain language the interface shows.
    failure_reason text,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    UNIQUE (org_id, host, ssh_port)
);

CREATE INDEX servers_org_idx ON servers (org_id, created_at DESC);

-- Progress of a long-running setup, streamed to the client so a five-minute wait is not a
-- silent spinner. Append-only.
CREATE TABLE server_events (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    server_id  uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    step       text NOT NULL,
    message    text NOT NULL,
    level      text NOT NULL DEFAULT 'info' CHECK (level IN ('info', 'warning', 'error')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX server_events_server_idx ON server_events (server_id, created_at);

CREATE TYPE discovered_kind AS ENUM (
    'container', 'image', 'volume', 'service', 'port', 'database'
);

-- Everything found on a server, whether we created it or not. This is what lets the
-- interface answer "what is on this box?" instead of pretending it is empty.
CREATE TABLE discovered_resources (
    id          uuid PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    server_id   uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    kind        discovered_kind NOT NULL,

    -- Identifier on the machine: container name, unit name, port number, volume name.
    external_id text NOT NULL,
    name        text NOT NULL,
    status      text,
    image       text,
    version     text,
    ports       integer[] NOT NULL DEFAULT '{}',
    size_bytes  bigint,

    -- True when we created it. Everything else is reported but never touched.
    managed     boolean NOT NULL DEFAULT false,

    -- Set when the user asks us to manage something that already existed. Recorded here
    -- rather than by labelling their container, because Docker labels cannot be added to an
    -- existing container without recreating it.
    adopted_at  timestamptz,
    -- Distinguishes a genuinely adopted container from one recreated with the same name.
    adopted_container_created_at timestamptz,

    details     jsonb NOT NULL DEFAULT '{}'::jsonb,

    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),

    UNIQUE (server_id, kind, external_id)
);

CREATE INDEX discovered_resources_server_idx ON discovered_resources (server_id, kind);
CREATE INDEX discovered_resources_adopted_idx ON discovered_resources (server_id) WHERE adopted_at IS NOT NULL;

ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON servers
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE server_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_events FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON server_events
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE discovered_resources ENABLE ROW LEVEL SECURITY;
ALTER TABLE discovered_resources FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON discovered_resources
    USING (org_id IS NOT DISTINCT FROM current_org_id());

-- The agent authenticates with a token and must reach its own server row before any
-- organization is in scope, so this is the one sanctioned path, matching how sessions and
-- invitations are handled.
-- +goose StatementBegin
CREATE FUNCTION authenticate_agent(p_token_hash bytea)
RETURNS TABLE (
    server_id     uuid,
    server_org_id uuid,
    server_mode   server_mode,
    server_status server_status
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT id, org_id, mode, status FROM servers WHERE agent_token_hash = p_token_hash
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION authenticate_agent(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION authenticate_agent(bytea) TO yol_app;

-- +goose Down
DROP FUNCTION authenticate_agent(bytea);
DROP TABLE discovered_resources;
DROP TABLE server_events;
DROP TABLE servers;
DROP TYPE discovered_kind;
DROP TYPE server_status;
DROP TYPE routing_mode;
DROP TYPE server_mode;
