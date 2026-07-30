-- +goose Up
-- Background jobs live in their own schema for two reasons. The job library owns the shape
-- of its tables, and those tables are not tenant data, so keeping them out of public lets the
-- rule that every table in public enforces tenant isolation stay absolute and testable.
CREATE SCHEMA IF NOT EXISTS jobs;

GRANT USAGE ON SCHEMA jobs TO yol_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA jobs
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO yol_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA jobs
    GRANT USAGE, SELECT ON SEQUENCES TO yol_app;

-- +goose Down
DROP SCHEMA IF EXISTS jobs CASCADE;
