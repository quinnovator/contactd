-- name: UpsertEpisode :exec
INSERT INTO episodes (contact, ep, start_seq, end_seq, centroid, summary_seq)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (contact, ep) DO UPDATE SET
    start_seq = excluded.start_seq,
    end_seq = excluded.end_seq,
    centroid = excluded.centroid,
    summary_seq = excluded.summary_seq;

-- name: Episodes :many
SELECT
    contact,
    ep,
    start_seq,
    end_seq,
    centroid,
    summary_seq
FROM episodes
WHERE contact = ?
ORDER BY ep ASC;

-- name: DeleteEpisodes :exec
DELETE FROM episodes
WHERE contact = ?;
