-- name: UpsertVector :exec
INSERT INTO vectors (contact, seq, vec)
VALUES (?, ?, ?)
ON CONFLICT (contact, seq) DO UPDATE SET vec = excluded.vec;

-- name: VectorsRange :many
SELECT
    seq,
    vec
FROM vectors
WHERE contact = ? AND seq >= ? AND seq <= ?
ORDER BY seq ASC;
