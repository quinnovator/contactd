-- name: MaxSeq :one
SELECT cast(coalesce(max(seq), 0) AS INTEGER) AS max_seq
FROM events
WHERE contact = ?;

-- name: TailEvents :many
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE contact = ?
ORDER BY seq DESC
LIMIT ?;

-- name: EventsAfter :many
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE contact = ? AND seq > ?
ORDER BY seq ASC
LIMIT ?;

-- name: EventsRange :many
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE contact = ? AND seq >= ? AND seq <= ?
ORDER BY seq ASC
LIMIT ?;

-- name: GetEvent :one
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE contact = ? AND seq = ?;

-- name: MessagesAfter :many
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE contact = ? AND seq > ? AND kind IN (1, 2)
ORDER BY seq ASC
LIMIT ?;

-- name: LatestOfKind :many
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE contact = ? AND kind = ?
ORDER BY seq DESC
LIMIT ?;
