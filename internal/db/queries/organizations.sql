-- name: CreateOrganization :one
INSERT INTO organizations (id, name, slug)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOrganizationByID :one
SELECT * FROM organizations WHERE id = $1;

-- name: GetOrganizationBySlug :one
SELECT * FROM organizations WHERE slug = $1;

-- name: RenameOrganization :one
UPDATE organizations SET name = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- Joins through memberships so a user only ever sees organizations they belong to.
-- name: ListOrganizationsForUser :many
SELECT o.*, m.role
FROM organizations o
JOIN memberships m ON m.org_id = o.id
WHERE m.user_id = $1
ORDER BY o.created_at;

-- name: GetMembershipForUser :one
SELECT * FROM memberships WHERE org_id = $1 AND user_id = $2;

-- name: CreateMembership :one
INSERT INTO memberships (id, org_id, user_id, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListMembers :many
SELECT m.*, u.email, u.name
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.org_id = $1
ORDER BY m.created_at;

-- name: UpdateMemberRole :one
UPDATE memberships SET role = $3, updated_at = now()
WHERE org_id = $1 AND user_id = $2
RETURNING *;

-- name: DeleteMembership :exec
DELETE FROM memberships WHERE org_id = $1 AND user_id = $2;

-- name: CountOwners :one
SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'owner';
