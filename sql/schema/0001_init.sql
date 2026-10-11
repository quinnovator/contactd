CREATE TABLE contacts (
    id INTEGER PRIMARY KEY,
    agent TEXT NOT NULL,
    profile BLOB,
    last_active INTEGER NOT NULL
);

-- The key leads with the contact, so one contact's history is a single
-- sequential range scan.
CREATE TABLE events (
    contact INTEGER NOT NULL,
    seq INTEGER NOT NULL,
    ts INTEGER NOT NULL,
    kind INTEGER NOT NULL,
    body BLOB NOT NULL,
    PRIMARY KEY (contact, seq)
) WITHOUT ROWID;

-- One 384-d float32 vector per message event, written after embedding.
CREATE TABLE vectors (
    contact INTEGER NOT NULL,
    seq INTEGER NOT NULL,
    vec BLOB NOT NULL,
    PRIMARY KEY (contact, seq)
) WITHOUT ROWID;

-- Derived index; rebuildable from events + vectors at any time.
CREATE TABLE episodes (
    contact INTEGER NOT NULL,
    ep INTEGER NOT NULL,
    start_seq INTEGER NOT NULL,
    end_seq INTEGER NOT NULL,
    centroid BLOB NOT NULL,
    summary_seq INTEGER,
    PRIMARY KEY (contact, ep)
) WITHOUT ROWID;
