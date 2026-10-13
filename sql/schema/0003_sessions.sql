ALTER TABLE contacts ADD COLUMN time_zone TEXT NOT NULL DEFAULT '';
ALTER TABLE contacts ADD COLUMN last_spoke INTEGER NOT NULL DEFAULT 0;

UPDATE contacts
SET last_spoke = coalesce((
    SELECT max(events.ts)
    FROM events
    WHERE events.contact = contacts.id AND events.kind IN (1, 2)
), 0);

CREATE TABLE sessions (
    contact INTEGER NOT NULL,
    session_id TEXT NOT NULL,
    PRIMARY KEY (contact, session_id)
) WITHOUT ROWID;
