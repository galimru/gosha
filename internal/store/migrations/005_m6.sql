-- What the Delivered comment reports (summary, review rounds, checks, diff,
-- deploy), as JSON kept by the pipeline, so a delivery resumed after a
-- crash still has it, and a follow-up can show what was shipped.
ALTER TABLE runs ADD COLUMN report TEXT NOT NULL DEFAULT '';
