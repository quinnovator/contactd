-- A contact's name is what a person calls it. It is kept outside the agent
-- config, so naming or renaming a contact never changes a byte of its prompt.
ALTER TABLE contacts ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- A sub-agent is a contact too. Its parent is copied out of the agent config
-- so that listing the contacts a person talks to never reads a sub-agent.
ALTER TABLE contacts ADD COLUMN parent INTEGER NOT NULL DEFAULT 0;

UPDATE contacts
SET parent = coalesce(json_extract(agent, '$.parent'), 0);

-- Names are optional, and unique where given.
CREATE UNIQUE INDEX contacts_name ON contacts (name)
WHERE name <> '';

CREATE INDEX contacts_listing ON contacts (parent, last_active DESC);
