-- Freeze the report boundary even when a late reply shares the terminal millisecond.
ALTER TABLE helper_tasks ADD COLUMN terminal_seq INTEGER NOT NULL DEFAULT 0;
