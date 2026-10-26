-- name: InsertRemoteHelper :one
INSERT INTO remote_helpers (
    parent,
    agent_url,
    task_id,
    context_id,
    title,
    state,
    status_message,
    helper_contact,
    every_ms,
    until_ts,
    next_check,
    checks,
    reports,
    last_check_at,
    last_report_at,
    last_outcome,
    push_token,
    push_secret,
    cancel_requested,
    notified_state,
    created_at,
    updated_at
)
VALUES (

    @parent,
    @agent_url,
    @task_id,
    @context_id,
    @title,
    @state,
    @status_message,
    @helper_contact,
    @every_ms,
    @until_ts,
    @next_check,
    @checks,
    @reports,
    @last_check_at,
    @last_report_at,
    @last_outcome,
    @push_token,
    @push_secret,
    @cancel_requested,
    @notified_state,
    @created_at,
    @updated_at

) RETURNING handle;

-- name: UpdateRemoteHelper :exec
UPDATE remote_helpers
SET
    task_id = @task_id,
    context_id = @context_id,
    title = @title,
    state = @state,
    status_message = @status_message,
    helper_contact = @helper_contact,
    every_ms = @every_ms,
    until_ts = @until_ts,
    next_check = @next_check,
    checks = @checks,
    reports = @reports,
    last_check_at = @last_check_at,
    last_report_at = @last_report_at,
    last_outcome = @last_outcome,
    cancel_requested = @cancel_requested,
    notified_state = @notified_state,
    updated_at = @updated_at
WHERE handle = @handle;

-- name: GetRemoteHelper :one
SELECT
    handle,
    parent,
    agent_url,
    task_id,
    context_id,
    title,
    state,
    status_message,
    helper_contact,
    every_ms,
    until_ts,
    next_check,
    checks,
    reports,
    last_check_at,
    last_report_at,
    last_outcome,
    push_token,
    push_secret,
    cancel_requested,
    notified_state,
    created_at,
    updated_at
FROM remote_helpers
WHERE handle = ?;

-- name: GetRemoteHelperByToken :one
SELECT
    handle,
    parent,
    agent_url,
    task_id,
    context_id,
    title,
    state,
    status_message,
    helper_contact,
    every_ms,
    until_ts,
    next_check,
    checks,
    reports,
    last_check_at,
    last_report_at,
    last_outcome,
    push_token,
    push_secret,
    cancel_requested,
    notified_state,
    created_at,
    updated_at
FROM remote_helpers
WHERE push_token = ?;

-- name: GetRemoteHelperByTask :one
SELECT
    handle,
    parent,
    agent_url,
    task_id,
    context_id,
    title,
    state,
    status_message,
    helper_contact,
    every_ms,
    until_ts,
    next_check,
    checks,
    reports,
    last_check_at,
    last_report_at,
    last_outcome,
    push_token,
    push_secret,
    cancel_requested,
    notified_state,
    created_at,
    updated_at
FROM remote_helpers
WHERE task_id = ? AND task_id != '';

-- name: ListRemoteHelpers :many
SELECT
    handle,
    parent,
    agent_url,
    task_id,
    context_id,
    title,
    state,
    status_message,
    helper_contact,
    every_ms,
    until_ts,
    next_check,
    checks,
    reports,
    last_check_at,
    last_report_at,
    last_outcome,
    push_token,
    push_secret,
    cancel_requested,
    notified_state,
    created_at,
    updated_at
FROM remote_helpers
WHERE parent = ?
ORDER BY
    state IN ('TASK_STATE_SUBMITTED', 'TASK_STATE_WORKING', 'TASK_STATE_INPUT_REQUIRED') DESC,
    created_at DESC,
    handle DESC
LIMIT ?;

-- name: StrandedRemoteHelpers :many
SELECT
    handle,
    parent,
    agent_url,
    task_id,
    context_id,
    title,
    state,
    status_message,
    helper_contact,
    every_ms,
    until_ts,
    next_check,
    checks,
    reports,
    last_check_at,
    last_report_at,
    last_outcome,
    push_token,
    push_secret,
    cancel_requested,
    notified_state,
    created_at,
    updated_at
FROM remote_helpers
WHERE state = 'TASK_STATE_SUBMITTED' AND task_id = ''
ORDER BY handle;

-- name: DeleteRemoteHelper :exec
DELETE FROM remote_helpers
WHERE handle = ?;

-- name: CountActiveRemoteHelpers :one
SELECT count(*)
FROM remote_helpers
WHERE
    parent = ?
    AND state IN ('TASK_STATE_SUBMITTED', 'TASK_STATE_WORKING', 'TASK_STATE_INPUT_REQUIRED');

-- name: MarkArtifactSeen :execrows
INSERT OR IGNORE INTO remote_helper_artifacts (handle, artifact_id) VALUES (
    ?,
    ?
);

-- name: InsertHelperInbox :exec
INSERT INTO remote_helper_inbox (handle, parent, notice, created_at) VALUES (
    ?,
    ?,
    ?,
    ?
);

-- name: HelperInboxParents :many
SELECT DISTINCT parent FROM remote_helper_inbox
ORDER BY parent;

-- name: HelperInbox :many
SELECT
    id,
    handle,
    parent,
    notice,
    created_at
FROM remote_helper_inbox
WHERE parent = ?
ORDER BY id
LIMIT ?;

-- name: DeleteHelperInbox :exec
DELETE FROM remote_helper_inbox
WHERE id = ?;
