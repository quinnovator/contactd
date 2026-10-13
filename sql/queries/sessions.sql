-- name: VisitState :one
SELECT
    time_zone,
    last_spoke
FROM contacts
WHERE id = ?;

-- name: SessionIDs :many
SELECT session_id FROM sessions
WHERE contact = ?
ORDER BY session_id;

-- name: RememberSession :exec
INSERT INTO sessions (contact, session_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: SetTimeZone :exec
UPDATE contacts SET time_zone = ?
WHERE id = ?;

-- name: Spoke :exec
UPDATE contacts SET last_spoke = ?
WHERE id = ?;
