-- name: CreateInvitation :one
INSERT INTO invitations (id, org_id, email, role, token_hash, invited_by_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListPendingInvitations :many
SELECT * FROM invitations
WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > now()
ORDER BY created_at;

-- name: AcceptInvitation :exec
UPDATE invitations SET accepted_at = now() WHERE id = $1;

-- name: DeleteInvitation :exec
DELETE FROM invitations WHERE id = $1 AND org_id = $2;
