-- No RETURNING: signing up happens before there is a current user, so the new row is not
-- yet readable, and the caller already knows every value it supplied.
-- name: CreateUser :exec
INSERT INTO users (id, email, name, password_hash)
VALUES ($1, $2, $3, $4);

-- Signing in reads through find_user_for_login instead, because the tenant policies on
-- users cannot apply before the caller has been identified.

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;
