-- +goose Up
-- The application connects as yol_app, never as the owner. Owners and superusers bypass
-- row level security, so using either for application queries disables tenant isolation.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'yol_app') THEN
        RAISE EXCEPTION 'role yol_app does not exist: create it with LOGIN NOSUPERUSER NOBYPASSRLS before migrating';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'yol_app' AND (rolsuper OR rolbypassrls)) THEN
        RAISE EXCEPTION 'role yol_app must be NOSUPERUSER and NOBYPASSRLS or row level security will not apply';
    END IF;
END $$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO yol_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO yol_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO yol_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM yol_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM yol_app;
REVOKE USAGE ON SCHEMA public FROM yol_app;
