-- No RETURNING for the same reason as CreateUser: the session being created is what will
-- identify the caller, so it cannot yet be read back.
-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, user_agent, ip, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- Reading and discarding a session by token goes through authenticate_session and
-- delete_session, because a token must be resolved before there is a current user for the
-- tenant policies to compare against.

-- name: ListSessionsForUser :many
SELECT token_hash, user_agent, ip, created_at, last_seen_at, expires_at
FROM sessions
WHERE user_id = $1 AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();
