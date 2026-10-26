-- name: InsertContact :one
INSERT INTO contacts (name, parent, helper, agent, profile, last_active)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetContact :one
SELECT
    id,
    name,
    agent,
    profile,
    last_active
FROM contacts
WHERE id = ?;

-- name: GetContactByName :one
SELECT
    id,
    name,
    agent,
    profile,
    last_active
FROM contacts
WHERE name = ?;

-- name: ListContacts :many
SELECT
    id,
    name,
    agent,
    profile,
    last_active
FROM contacts
WHERE parent = 0 AND helper = 0
ORDER BY last_active DESC, id DESC
LIMIT ?;

-- name: RenameContact :execrows
UPDATE contacts
SET name = ?
WHERE id = ?;

-- name: TouchContact :exec
UPDATE contacts
SET last_active = ?
WHERE id = ?;

-- name: CountContacts :one
SELECT count(*) FROM contacts;
