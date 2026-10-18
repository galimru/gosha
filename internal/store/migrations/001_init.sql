CREATE TABLE runs (
    id             INTEGER PRIMARY KEY,
    project        TEXT    NOT NULL,
    ticket_id      TEXT    NOT NULL,
    mode           TEXT    NOT NULL,
    branch         TEXT    NOT NULL DEFAULT '',
    base_branch    TEXT    NOT NULL DEFAULT '',
    pr_number      INTEGER NOT NULL DEFAULT 0,
    pr_url         TEXT    NOT NULL DEFAULT '',
    step           TEXT    NOT NULL DEFAULT '',
    review_round   INTEGER NOT NULL DEFAULT 0,
    check_round    INTEGER NOT NULL DEFAULT 0,
    impl_session   TEXT    NOT NULL DEFAULT '',
    -- Why the run last went Blocked, while it's blocked, and when.
    blocked_reason TEXT    NOT NULL DEFAULT '',
    blocked_at     TEXT    NOT NULL DEFAULT '',
    -- Agent passes so far, numbering transcripts runs/<ticket>/<n>-<role>.jsonl.
    agent_runs     INTEGER NOT NULL DEFAULT 0,
    -- The head the last review was run on, and the GitHub review posted for it,
    -- so a crash after posting doesn't post the same review twice.
    reviewed_sha   TEXT    NOT NULL DEFAULT '',
    review_id      INTEGER NOT NULL DEFAULT 0,
    -- Path to the last reviewer result, read back by the address step.
    review_result  TEXT    NOT NULL DEFAULT '',
    -- The implementer's last address summary, shown to the next reviewer.
    last_reply     TEXT    NOT NULL DEFAULT '',
    -- The head the last checks verdict was for, so a resumed run doesn't count
    -- the same failing head as a second check round.
    checked_sha    TEXT    NOT NULL DEFAULT '',
    -- Path to the CI feedback saved for checked_sha, read back by the fix step.
    checks_result  TEXT    NOT NULL DEFAULT '',
    -- The merge commit, set as soon as GitHub merged the PR, so a crash before
    -- cleanup isn't mistaken for a merge by hand.
    merge_sha      TEXT    NOT NULL DEFAULT '',
    -- What the Delivered comment reports, as JSON kept by the pipeline, so a
    -- delivery resumed after a crash still has it, and a follow-up can show
    -- what was shipped.
    report         TEXT    NOT NULL DEFAULT '',
    outcome        TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL,
    finished_at    TEXT
);

CREATE INDEX runs_open ON runs (project, ticket_id) WHERE finished_at IS NULL;

CREATE TABLE comment_cursors (
    project          TEXT NOT NULL,
    ticket_id        TEXT NOT NULL,
    last_comment_id  TEXT NOT NULL,
    last_comment_at  TEXT NOT NULL,
    PRIMARY KEY (project, ticket_id)
);

CREATE TABLE project_state (
    project     TEXT PRIMARY KEY,
    status      TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    notion      INTEGER NOT NULL DEFAULT 0,
    commit_sha  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL
);

-- Daemon-wide values: the gosha version that last migrated the schema, the
-- usage-limit pause, the last digest date.
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
