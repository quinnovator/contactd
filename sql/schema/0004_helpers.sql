-- A helper contact works for an A2A task, not for a person. Like a sub-agent
-- it is left out of the contacts a person sees.
ALTER TABLE contacts ADD COLUMN helper INTEGER NOT NULL DEFAULT 0;

-- One row per A2A task on the helper agent (the server side). The state is
-- authoritative here; reports and history are read from the helper
-- contact's log.
CREATE TABLE helper_tasks (
    id TEXT PRIMARY KEY,
    context_id TEXT NOT NULL,
    contact INTEGER NOT NULL DEFAULT 0, -- 0 for a task rejected before a contact was made
    title TEXT NOT NULL,
    state TEXT NOT NULL,
    status_message TEXT NOT NULL DEFAULT '',
    status_ts INTEGER NOT NULL,
    every_ms INTEGER NOT NULL,
    until_ts INTEGER NOT NULL,
    max_checks INTEGER NOT NULL,
    next_due INTEGER NOT NULL DEFAULT 0,
    checks INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    last_check_at INTEGER NOT NULL DEFAULT 0,
    last_outcome TEXT NOT NULL DEFAULT '',
    first_seq INTEGER NOT NULL DEFAULT 0,
    pushed_seq INTEGER NOT NULL DEFAULT 0,
    pushed_state TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE INDEX helper_tasks_due ON helper_tasks (next_due)
WHERE next_due > 0;

CREATE INDEX helper_tasks_contact ON helper_tasks (contact);

CREATE INDEX helper_tasks_listing ON helper_tasks (created_at DESC, id DESC);

CREATE TABLE helper_push_configs (
    task_id TEXT NOT NULL,
    id TEXT NOT NULL,
    url TEXT NOT NULL,
    token TEXT NOT NULL DEFAULT '',
    auth_scheme TEXT NOT NULL DEFAULT '',
    auth_credentials TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (task_id, id)
) WITHOUT ROWID;

-- One row per helper a contact started (the client side). handle is what the
-- contact and its person call it.
CREATE TABLE remote_helpers (
    handle INTEGER PRIMARY KEY,
    parent INTEGER NOT NULL,
    agent_url TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    context_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    state TEXT NOT NULL,
    status_message TEXT NOT NULL DEFAULT '',
    helper_contact INTEGER NOT NULL DEFAULT 0,
    every_ms INTEGER NOT NULL,
    until_ts INTEGER NOT NULL,
    next_check INTEGER NOT NULL DEFAULT 0,
    checks INTEGER NOT NULL DEFAULT 0,
    reports INTEGER NOT NULL DEFAULT 0,
    last_check_at INTEGER NOT NULL DEFAULT 0,
    last_report_at INTEGER NOT NULL DEFAULT 0,
    last_outcome TEXT NOT NULL DEFAULT '',
    push_token TEXT NOT NULL UNIQUE,
    push_secret TEXT NOT NULL,
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    -- The last state the parent was given a notice for. Separate from state,
    -- which a GetTask refresh may advance before the push arrives.
    notified_state TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX remote_helpers_parent ON remote_helpers (parent, created_at DESC);

CREATE INDEX remote_helpers_task ON remote_helpers (task_id);

-- Artifacts a requester has already turned into notices: pushes are
-- delivered at least once, and this makes them count once.
CREATE TABLE remote_helper_artifacts (
    handle INTEGER NOT NULL,
    artifact_id TEXT NOT NULL,
    PRIMARY KEY (handle, artifact_id)
) WITHOUT ROWID;
