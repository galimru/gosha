-- The head the last checks verdict was for, so a resumed run doesn't count
-- the same failing head as a second check round.
ALTER TABLE runs ADD COLUMN checked_sha   TEXT NOT NULL DEFAULT '';
-- Path to the CI feedback saved for checked_sha, read back by the fix step.
ALTER TABLE runs ADD COLUMN checks_result TEXT NOT NULL DEFAULT '';
-- The merge commit, set as soon as GitHub merged the PR, so a crash before
-- cleanup isn't mistaken for a merge by hand.
ALTER TABLE runs ADD COLUMN merge_sha     TEXT NOT NULL DEFAULT '';
