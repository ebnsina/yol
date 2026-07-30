-- name: RecordAuditEvent :exec
INSERT INTO audit_log (id, org_id, actor_user_id, action, target_type, target_id, metadata, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListAuditEvents :many
SELECT * FROM audit_log
WHERE org_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
