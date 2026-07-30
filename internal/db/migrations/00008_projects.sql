-- +goose Up

CREATE TABLE projects (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name       text NOT NULL,
    slug       citext NOT NULL,

    -- Where the code comes from. Null until a repository is connected, because a project can
    -- exist before its source does.
    repo_provider text CHECK (repo_provider IN ('github')),
    repo_full_name text,
    repo_external_id text,
    repo_installation_id text,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (org_id, slug)
);

CREATE INDEX projects_org_idx ON projects (org_id, created_at DESC);

-- An environment is one running copy of a project: production, staging, or anything else. Each
-- is pointed at a server, which may be the same one or a different one.
CREATE TABLE environments (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    server_id  uuid REFERENCES servers(id) ON DELETE SET NULL,

    name       citext NOT NULL,
    branch     text NOT NULL,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (project_id, name)
);

CREATE INDEX environments_server_idx ON environments (server_id);

CREATE TYPE service_kind AS ENUM (
    'app', 'postgres', 'mysql', 'redis', 'clickhouse', 'sqlite', 'srs', 'mediamtx'
);

-- A service is one thing running inside an environment: the application itself, or something it
-- depends on. New kinds are added by adding a recipe, never by changing the agent.
CREATE TABLE services (
    id     uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    env_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,

    name text NOT NULL,
    kind service_kind NOT NULL,

    -- How the agent decides a new container is actually serving before traffic moves to it.
    -- Without this a deploy is an outage with extra steps.
    health_path text,
    health_port integer,

    -- Always set, so one runaway process cannot take a whole server down.
    memory_limit_bytes bigint NOT NULL DEFAULT 536870912,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (env_id, name)
);

CREATE TYPE deployment_status AS ENUM (
    'queued', 'building', 'deploying', 'live', 'failed', 'superseded'
);

CREATE TABLE deployments (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,

    status deployment_status NOT NULL DEFAULT 'queued',

    -- What was built. The reference is kept after the deployment is replaced, so rolling back
    -- reuses the image rather than building it again.
    commit_sha  text,
    commit_ref  text,
    image_ref   text,

    -- Plain language, shown as-is when something goes wrong.
    failure_reason text,

    -- Set when this deployment took over from another, for showing history and rolling back.
    replaced_deployment_id uuid REFERENCES deployments(id) ON DELETE SET NULL,

    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz
);

CREATE INDEX deployments_service_idx ON deployments (service_id, created_at DESC);
-- At most one deployment is live for a service at a time.
CREATE UNIQUE INDEX deployments_live_idx ON deployments (service_id) WHERE status = 'live';

-- Where a deployment is actually running. One row today; several once a service spans machines,
-- which is why it is a table rather than a column on the deployment.
CREATE TABLE placements (
    id            uuid PRIMARY KEY,
    org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    deployment_id uuid NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    server_id     uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,

    container_name text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),

    UNIQUE (server_id, container_name)
);

CREATE INDEX placements_deployment_idx ON placements (deployment_id);

-- Environment variables, encrypted at rest. Values are never returned to a client once set.
CREATE TABLE env_vars (
    id     uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    env_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,

    name   text NOT NULL,
    value  bytea NOT NULL,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (env_id, name)
);

-- The control plane owns each server's usable ports, so two projects on one machine cannot
-- collide. Rows are released when the service that held them goes.
CREATE TABLE port_allocations (
    id        uuid PRIMARY KEY,
    org_id    uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,

    port       integer NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    service_id uuid REFERENCES services(id) ON DELETE CASCADE,
    purpose    text NOT NULL,

    created_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (server_id, port)
);

CREATE INDEX port_allocations_service_idx ON port_allocations (service_id);

-- A hostname pointed at a service. Verified before it is enabled, so a certificate is never
-- requested for a name the user does not control.
CREATE TABLE domains (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,

    hostname citext NOT NULL UNIQUE,
    -- True for the subdomain we hand out, which needs no verifying because we own the parent.
    ours     boolean NOT NULL DEFAULT false,

    verified_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX domains_service_idx ON domains (service_id);

-- Build and deploy output, kept briefly so a failure can be read after the fact rather than
-- only while watching.
CREATE TABLE deployment_logs (
    id            uuid PRIMARY KEY,
    org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    deployment_id uuid NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,

    stream     text NOT NULL,
    text       text NOT NULL,
    at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX deployment_logs_idx ON deployment_logs (deployment_id, at);

-- Every table below an organization enforces isolation, without exception. A test walks the
-- schema and fails if a new table appears without it.
ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON projects
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE environments ENABLE ROW LEVEL SECURITY;
ALTER TABLE environments FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON environments
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE services ENABLE ROW LEVEL SECURITY;
ALTER TABLE services FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON services
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE deployments ENABLE ROW LEVEL SECURITY;
ALTER TABLE deployments FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deployments
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE placements ENABLE ROW LEVEL SECURITY;
ALTER TABLE placements FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON placements
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE env_vars ENABLE ROW LEVEL SECURITY;
ALTER TABLE env_vars FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON env_vars
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE port_allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE port_allocations FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON port_allocations
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE domains FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON domains
    USING (org_id IS NOT DISTINCT FROM current_org_id());

ALTER TABLE deployment_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE deployment_logs FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deployment_logs
    USING (org_id IS NOT DISTINCT FROM current_org_id());

-- Caddy asks before issuing a certificate, so a customer's server cannot be made to request one
-- for a name nobody here controls. It asks before any organization is in scope, so this is the
-- sanctioned way past the policies, matching how sessions and enrollment already work.
-- +goose StatementBegin
CREATE FUNCTION is_hostname_allowed(p_hostname citext)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT EXISTS (
        SELECT 1 FROM domains
        WHERE hostname = p_hostname
          AND (verified_at IS NOT NULL OR ours)
    )
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION is_hostname_allowed(citext) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION is_hostname_allowed(citext) TO yol_app;

-- +goose Down
DROP FUNCTION is_hostname_allowed(citext);
DROP TABLE deployment_logs;
DROP TABLE domains;
DROP TABLE port_allocations;
DROP TABLE env_vars;
DROP TABLE placements;
DROP TABLE deployments;
DROP TABLE services;
DROP TABLE environments;
DROP TABLE projects;
DROP TYPE deployment_status;
DROP TYPE service_kind;
