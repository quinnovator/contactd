-- Notices a push has accepted and its requesting contact has not yet logged.
-- A webhook commits a row here with its dedupe and cursor updates, and
-- answers at once; delivery to the contact waits for any reply in flight.
CREATE TABLE remote_helper_inbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    handle INTEGER NOT NULL,
    parent INTEGER NOT NULL,
    notice TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX remote_helper_inbox_parent ON remote_helper_inbox (parent, id);

-- Push every status change, including a return to the state last pushed,
-- such as a second question after an answer. Rows already pushed keep that.
ALTER TABLE helper_tasks ADD COLUMN pushed_status_ts INTEGER NOT NULL DEFAULT 0;

UPDATE helper_tasks SET pushed_status_ts = status_ts
WHERE pushed_state = state;

-- The messageId of the brief that created a task, so a retried brief is
-- answered with that task instead of starting a second one.
ALTER TABLE helper_tasks ADD COLUMN message_id TEXT NOT NULL DEFAULT '';

-- The scheduler's, outbox's and capacity checks' scans by state.
CREATE INDEX helper_tasks_state ON helper_tasks (state, next_due);

CREATE INDEX helper_tasks_unpushed ON helper_tasks (id)
WHERE pushed_state != state OR pushed_status_ts != status_ts;

CREATE INDEX helper_tasks_message ON helper_tasks (message_id)
WHERE message_id != '';
