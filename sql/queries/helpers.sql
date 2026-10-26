-- name: InsertHelperTask :exec
INSERT INTO helper_tasks (
    id,
    context_id,
    contact,
    title,
    state,
    status_message,
    status_ts,
    every_ms,
    until_ts,
    max_checks,
    next_due,
    checks,
    failures,
    last_check_at,
    last_outcome,
    first_seq,
    pushed_seq,
    pushed_state,
    created_at
)
VALUES (
    @id,
    @context_id,
    @contact,
    @title,
    @state,
    @status_message,
    @status_ts,
    @every_ms,
    @until_ts,
    @max_checks,
    @next_due,
    @checks,
    @failures,
    @last_check_at,
    @last_outcome,
    @first_seq,
    @pushed_seq,
    @pushed_state,
    @created_at
);

-- name: UpdateHelperTask :exec
UPDATE helper_tasks
SET
    title = @title,
    state = @state,
    status_message = @status_message,
    status_ts = @status_ts,
    every_ms = @every_ms,
    until_ts = @until_ts,
    max_checks = @max_checks,
    next_due = @next_due,
    checks = @checks,
    failures = @failures,
    last_check_at = @last_check_at,
    last_outcome = @last_outcome,
    first_seq = @first_seq,
    pushed_seq = @pushed_seq,
    pushed_state = @pushed_state
WHERE id = @id;

-- name: GetHelperTask :one
SELECT
    id,
    context_id,
    contact,
    title,
    state,
    status_message,
    status_ts,
    every_ms,
    until_ts,
    max_checks,
    next_due,
    checks,
    failures,
    last_check_at,
    last_outcome,
    first_seq,
    pushed_seq,
    pushed_state,
    created_at
FROM helper_tasks
WHERE id = ?;

-- name: GetHelperTaskByContact :one
SELECT
    id,
    context_id,
    contact,
    title,
    state,
    status_message,
    status_ts,
    every_ms,
    until_ts,
    max_checks,
    next_due,
    checks,
    failures,
    last_check_at,
    last_outcome,
    first_seq,
    pushed_seq,
    pushed_state,
    created_at
FROM helper_tasks
WHERE contact = ? AND contact != 0;

-- name: DueHelperTasks :many
SELECT
    id,
    context_id,
    contact,
    title,
    state,
    status_message,
    status_ts,
    every_ms,
    until_ts,
    max_checks,
    next_due,
    checks,
    failures,
    last_check_at,
    last_outcome,
    first_seq,
    pushed_seq,
    pushed_state,
    created_at
FROM helper_tasks
WHERE
    (state = 'TASK_STATE_WORKING' AND next_due > 0 AND (next_due <= @now OR until_ts <= @now))
    OR (state = 'TASK_STATE_INPUT_REQUIRED' AND until_ts <= @now)
ORDER BY next_due, id LIMIT @page;

-- name: NextHelperDue :one
SELECT cast(coalesce(min(due), 0) AS INTEGER) AS due FROM (
    SELECT min(next_due, until_ts) AS due FROM helper_tasks
    WHERE state = 'TASK_STATE_WORKING' AND next_due > 0
    UNION ALL
    SELECT until_ts AS due FROM helper_tasks
    WHERE state = 'TASK_STATE_INPUT_REQUIRED'
);

-- name: PendingHelperTasks :many
SELECT
    id,
    context_id,
    contact,
    title,
    state,
    status_message,
    status_ts,
    every_ms,
    until_ts,
    max_checks,
    next_due,
    checks,
    failures,
    last_check_at,
    last_outcome,
    first_seq,
    pushed_seq,
    pushed_state,
    created_at
FROM helper_tasks
WHERE
    state IN ('TASK_STATE_SUBMITTED', 'TASK_STATE_WORKING', 'TASK_STATE_INPUT_REQUIRED')
    OR pushed_state != state
ORDER BY created_at, id;

-- name: ListHelperTasks :many
SELECT
    id,
    context_id,
    contact,
    title,
    state,
    status_message,
    status_ts,
    every_ms,
    until_ts,
    max_checks,
    next_due,
    checks,
    failures,
    last_check_at,
    last_outcome,
    first_seq,
    pushed_seq,
    pushed_state,
    created_at
FROM helper_tasks
WHERE
    (@context_id = '' OR context_id = @context_id)
    AND (@state = '' OR state = @state)
    AND (@after = 0 OR status_ts >= @after)
    AND (
        @cursor_id = '' OR created_at < @cursor_ts OR (created_at = @cursor_ts AND id < @cursor_id)
    )
ORDER BY created_at DESC, id DESC LIMIT @page;

-- name: CountHelperTasks :one
SELECT count(*) FROM helper_tasks
WHERE
    (@context_id = '' OR context_id = @context_id)
    AND (@state = '' OR state = @state)
    AND (@after = 0 OR status_ts >= @after);

-- name: CountActiveHelperTasks :one
SELECT count(*) FROM helper_tasks
WHERE state IN ('TASK_STATE_SUBMITTED', 'TASK_STATE_WORKING', 'TASK_STATE_INPUT_REQUIRED');

-- name: PutPushConfig :exec
INSERT INTO helper_push_configs (
    task_id,
    id,
    url,
    token,
    auth_scheme,
    auth_credentials,
    created_at
)
VALUES (
    @task_id,
    @id,
    @url,
    @token,
    @auth_scheme,
    @auth_credentials,
    @created_at
)
ON CONFLICT (task_id, id) DO UPDATE SET
    url = excluded.url,
    token = excluded.token,
    auth_scheme = excluded.auth_scheme,
    auth_credentials = excluded.auth_credentials;

-- name: GetPushConfig :one
SELECT
    task_id,
    id,
    url,
    token,
    auth_scheme,
    auth_credentials,
    created_at
FROM helper_push_configs
WHERE task_id = ? AND id = ?;

-- name: ListPushConfigs :many
SELECT
    task_id,
    id,
    url,
    token,
    auth_scheme,
    auth_credentials,
    created_at
FROM helper_push_configs
WHERE task_id = ?
ORDER BY id;

-- name: DeletePushConfig :exec
DELETE FROM helper_push_configs
WHERE task_id = ? AND id = ?;
