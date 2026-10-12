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

-- kinds is a bit set: bit k is set when events of kind k are wanted.
-- name: EventsAfterOfKinds :many
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE
    contact = @contact
    AND seq > @after
    AND (cast(@kinds AS INTEGER) >> kind) & 1 = 1
ORDER BY seq ASC
LIMIT @page;

-- Newest first: the caller reverses the page into log order.
-- name: EventsBeforeOfKinds :many
SELECT
    contact,
    seq,
    ts,
    kind,
    body
FROM events
WHERE
    contact = @contact
    AND seq < @before
    AND (cast(@kinds AS INTEGER) >> kind) & 1 = 1
ORDER BY seq DESC
LIMIT @page;

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
