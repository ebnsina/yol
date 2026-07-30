-- Local development only. The application must never connect as a superuser, because
-- superusers bypass row level security and tenant isolation silently stops working.
-- Production provisions this role with a real password; migration 00002 grants it rights.
CREATE ROLE yol_app WITH LOGIN PASSWORD 'yol_app'
    NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
