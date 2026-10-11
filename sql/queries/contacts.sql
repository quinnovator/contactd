-- name: InsertContact :one
INSERT INTO contacts (agent, profile, last_active)
VALUES (?, ?, ?)
RETURNING id;

-- name: GetContact :one
SELECT
    id,
    agent,
    profile,
    last_active
FROM contacts
WHERE id = ?;

-- name: TouchContact :exec
UPDATE contacts
SET last_active = ?
WHERE id = ?;

-- name: CountContacts :one
SELECT count(*) FROM contacts;
