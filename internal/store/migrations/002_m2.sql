ALTER TABLE runs ADD COLUMN base_branch    TEXT    NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN pr_url         TEXT    NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN blocked_reason TEXT    NOT NULL DEFAULT '';
-- Agent passes so far, numbering transcripts runs/<ticket>/<n>-<role>.jsonl.
ALTER TABLE runs ADD COLUMN agent_runs     INTEGER NOT NULL DEFAULT 0;
