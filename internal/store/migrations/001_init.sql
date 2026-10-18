CREATE TABLE runs (
    id            INTEGER PRIMARY KEY,
    project       TEXT    NOT NULL,
    ticket_id     TEXT    NOT NULL,
    mode          TEXT    NOT NULL,
    branch        TEXT    NOT NULL DEFAULT '',
    pr_number     INTEGER NOT NULL DEFAULT 0,
    step          TEXT    NOT NULL DEFAULT '',
    review_round  INTEGER NOT NULL DEFAULT 0,
    check_round   INTEGER NOT NULL DEFAULT 0,
    impl_session  TEXT    NOT NULL DEFAULT '',
    outcome       TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    finished_at   TEXT
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
    commit_sha  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL
);
