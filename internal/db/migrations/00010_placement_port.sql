-- +goose Up

-- Where a version is reached, recorded when it is placed rather than read from the service later.
--
-- Routing read the service's current health port, so changing that setting re-pointed traffic at a
-- port the version already serving was never listening on: the app broke before any deploy. A port
-- belongs to the version that was rolled out with it, exactly as its container name does.
ALTER TABLE placements ADD COLUMN port integer NOT NULL DEFAULT 80;
ALTER TABLE placements ADD COLUMN health_path text;

-- +goose Down
ALTER TABLE placements DROP COLUMN health_path;
ALTER TABLE placements DROP COLUMN port;
