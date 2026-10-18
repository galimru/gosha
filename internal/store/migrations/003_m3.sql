-- The head the last review was run on, and the GitHub review posted for it,
-- so a crash after posting doesn't post the same review twice.
ALTER TABLE runs ADD COLUMN reviewed_sha  TEXT    NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN review_id     INTEGER NOT NULL DEFAULT 0;
-- Path to the last reviewer result, read back by the address step.
ALTER TABLE runs ADD COLUMN review_result TEXT    NOT NULL DEFAULT '';
-- Time of the newest human PR feedback already given to the implementer.
ALTER TABLE runs ADD COLUMN pr_cursor     TEXT    NOT NULL DEFAULT '';
-- The implementer's last address summary, shown to the next reviewer.
ALTER TABLE runs ADD COLUMN last_reply    TEXT    NOT NULL DEFAULT '';
