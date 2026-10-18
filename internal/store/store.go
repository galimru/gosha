// Package store persists runs, comment cursors and project state in SQLite
// (DESIGN §10).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var embedded embed.FS

// migrations is swapped in tests.
var migrations fs.FS = embedded

// Store is the Gosha database.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path and migrates it.
// version is the running gosha's version: it's recorded with every
// migration, names the backup taken before one, and is what a newer
// database tells the user to install.
func Open(ctx context.Context, path, version string) (*Store, error) {
	// Create the file ourselves so it's private from the start.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	f.Close()
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx, path, version); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// metaVersion is the meta key holding the gosha version that last migrated
// the schema.
const metaVersion = "gosha_version"

// migrate applies the migrations the database doesn't have yet. An existing
// database is copied to <path>.bak-<old version> first, and one newer than
// this binary is refused.
func (s *Store) migrate(ctx context.Context, path, version string) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	var schema int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return fmt.Errorf("store version: %w", err)
	}
	if schema == len(names) {
		return nil
	}
	if schema > len(names) {
		by, _ := s.Meta(ctx, metaVersion)
		if by == "" {
			by = "a newer version"
		}
		return fmt.Errorf("%s has schema %d, written by gosha %s; this gosha (%s) knows schema %d: install gosha %s or newer",
			path, schema, by, version, len(names), by)
	}
	if schema > 0 {
		if err := s.backup(ctx, path); err != nil {
			return err
		}
	}
	for i := schema; i < len(names); i++ {
		body, err := fs.ReadFile(migrations, names[i])
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate %s: %w", names[i], err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return s.SetMeta(ctx, metaVersion, version)
}

// backup writes a consistent copy of the database next to it, named after
// the gosha version that last migrated it.
func (s *Store) backup(ctx context.Context, path string) error {
	by, err := s.Meta(ctx, metaVersion)
	if err != nil {
		return err
	}
	if by == "" {
		by = "unknown"
	}
	dst := path + ".bak-" + by
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store backup: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("store backup: %w", err)
	}
	return os.Chmod(dst, 0o600)
}

// Meta returns a daemon-wide value, or "" if unset (or before the meta
// table exists).
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return "", nil
	}
	return v, err
}

// SetMeta stores a daemon-wide value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

const timeFormat = time.RFC3339Nano

func now() string { return time.Now().UTC().Format(timeFormat) }

// formatTime stores the zero time as "".
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeFormat)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeFormat, s)
	return t
}

// Run outcomes the store itself needs to know.
const (
	// OutcomeDelivered is a run Gosha merged and reported In Review.
	OutcomeDelivered = "delivered"
	// OutcomeMerged is a run whose PR a human merged.
	OutcomeMerged = "merged by hand"
)

// Run is one pickup of a ticket.
type Run struct {
	ID          int64
	Project     string
	TicketID    string
	Mode        string
	Branch      string
	PRNumber    int
	Step        string
	ReviewRound int
	CheckRound  int
	ImplSession string
	BaseBranch  string
	PRURL       string
	// BlockedReason is why the run last went Blocked, while it's blocked.
	BlockedReason string
	// AgentRuns counts agent passes, numbering the transcripts.
	AgentRuns int
	// ReviewedSHA is the head the last review ran on, and ReviewID the
	// GitHub review posted for it (0 until posted).
	ReviewedSHA string
	ReviewID    int64
	// ReviewResult is the path to the last reviewer result file.
	ReviewResult string
	// LastReply is the implementer's last address summary.
	LastReply string
	// CheckedSHA is the head the last checks verdict was for, and
	// ChecksResult the path to the CI feedback saved for it ("" when green).
	CheckedSHA   string
	ChecksResult string
	// MergeSHA is the merge commit once Gosha has merged the PR.
	MergeSHA string
	// Report is the pipeline's JSON record of what to report on delivery.
	Report    string
	Outcome   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateRun records a new open run and sets its ID.
func (s *Store) CreateRun(ctx context.Context, r *Run) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO runs
		(project, ticket_id, mode, branch, pr_number, step, review_round, check_round, impl_session,
		 base_branch, pr_url, blocked_reason, agent_runs, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Project, r.TicketID, r.Mode, r.Branch, r.PRNumber, r.Step, r.ReviewRound, r.CheckRound, r.ImplSession,
		r.BaseBranch, r.PRURL, r.BlockedReason, r.AgentRuns, ts, ts)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	r.ID, err = res.LastInsertId()
	r.CreatedAt, r.UpdatedAt = parseTime(ts), parseTime(ts)
	return err
}

// UpdateRun saves a run's mutable fields.
func (s *Store) UpdateRun(ctx context.Context, r *Run) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET mode = ?, branch = ?, pr_number = ?, step = ?, review_round = ?,
		check_round = ?, impl_session = ?, base_branch = ?, pr_url = ?, blocked_reason = ?, agent_runs = ?,
		reviewed_sha = ?, review_id = ?, review_result = ?, last_reply = ?,
		checked_sha = ?, checks_result = ?, merge_sha = ?, report = ?, updated_at = ?
		WHERE id = ?`,
		r.Mode, r.Branch, r.PRNumber, r.Step, r.ReviewRound, r.CheckRound, r.ImplSession,
		r.BaseBranch, r.PRURL, r.BlockedReason, r.AgentRuns,
		r.ReviewedSHA, r.ReviewID, r.ReviewResult, r.LastReply,
		r.CheckedSHA, r.ChecksResult, r.MergeSHA, r.Report, ts, r.ID)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	r.UpdatedAt = parseTime(ts)
	return nil
}

// FinishRun closes a run with an outcome.
func (s *Store) FinishRun(ctx context.Context, id int64, outcome string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET outcome = ?, finished_at = ?, updated_at = ? WHERE id = ?`, outcome, ts, ts, id)
	return err
}

const runColumns = `id, project, ticket_id, mode, branch, pr_number, step, review_round, check_round, impl_session,
	base_branch, pr_url, blocked_reason, agent_runs, reviewed_sha, review_id, review_result, last_reply,
	checked_sha, checks_result, merge_sha, report, outcome, created_at, updated_at`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var created, updated string
	err := row.Scan(&r.ID, &r.Project, &r.TicketID, &r.Mode, &r.Branch, &r.PRNumber, &r.Step,
		&r.ReviewRound, &r.CheckRound, &r.ImplSession, &r.BaseBranch, &r.PRURL, &r.BlockedReason, &r.AgentRuns,
		&r.ReviewedSHA, &r.ReviewID, &r.ReviewResult, &r.LastReply,
		&r.CheckedSHA, &r.ChecksResult, &r.MergeSHA, &r.Report, &r.Outcome, &created, &updated)
	if err != nil {
		return nil, err
	}
	r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
	return &r, nil
}

// OpenRun returns the unfinished run for a ticket, or nil.
func (s *Store) OpenRun(ctx context.Context, project, ticketID string) (*Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs
		WHERE project = ? AND ticket_id = ? AND finished_at IS NULL ORDER BY id DESC LIMIT 1`, project, ticketID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// LastRun returns a ticket's newest run, open or finished, or nil.
func (s *Store) LastRun(ctx context.Context, project, ticketID string) (*Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs
		WHERE project = ? AND ticket_id = ? ORDER BY id DESC LIMIT 1`, project, ticketID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// LastDelivered returns a ticket's newest run whose PR was merged, by Gosha
// or by hand, or nil: a later pickup of the ticket is a follow-up.
func (s *Store) LastDelivered(ctx context.Context, project, ticketID string) (*Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs
		WHERE project = ? AND ticket_id = ? AND outcome IN (?, ?) ORDER BY id DESC LIMIT 1`,
		project, ticketID, OutcomeDelivered, OutcomeMerged))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// OpenRuns lists every unfinished run, for status.
func (s *Store) OpenRuns(ctx context.Context) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE finished_at IS NULL ORDER BY project, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Branches lists every branch Gosha has used in a project, for the rule
// that it never takes over a branch it doesn't own (DESIGN §8.2).
func (s *Store) Branches(ctx context.Context, project string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT branch FROM runs WHERE project = ? AND branch != ''`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Cursor is the last ticket comment Gosha has seen.
type Cursor struct {
	CommentID string
	At        time.Time
}

// CommentCursor returns the cursor for a ticket, or nil if none.
func (s *Store) CommentCursor(ctx context.Context, project, ticketID string) (*Cursor, error) {
	var c Cursor
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT last_comment_id, last_comment_at FROM comment_cursors
		WHERE project = ? AND ticket_id = ?`, project, ticketID).Scan(&c.CommentID, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.At = parseTime(at)
	return &c, nil
}

// SetCommentCursor moves a ticket's cursor.
func (s *Store) SetCommentCursor(ctx context.Context, project, ticketID string, c Cursor) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO comment_cursors (project, ticket_id, last_comment_id, last_comment_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (project, ticket_id) DO UPDATE
		SET last_comment_id = excluded.last_comment_id, last_comment_at = excluded.last_comment_at`,
		project, ticketID, c.CommentID, c.At.UTC().Format(timeFormat))
	return err
}

// Project statuses.
const (
	StatusActive = "active"
	StatusPaused = "paused"
)

// ProjectState is a project's last known health.
type ProjectState struct {
	Project string
	Status  string
	Reason  string
	// Notion is set when the pause is over Notion access, which only a
	// pickup's context refresh can clear (DESIGN §6.2).
	Notion    bool
	Commit    string
	UpdatedAt time.Time
}

// SetProjectState records a project's state and reports whether the status
// changed, so callers notify only on transitions (DESIGN §5.2).
func (s *Store) SetProjectState(ctx context.Context, st ProjectState) (changed bool, err error) {
	prev, err := s.ProjectState(ctx, st.Project)
	if err != nil {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO project_state (project, status, reason, notion, commit_sha, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (project) DO UPDATE
		SET status = excluded.status, reason = excluded.reason, notion = excluded.notion,
		commit_sha = excluded.commit_sha, updated_at = excluded.updated_at`,
		st.Project, st.Status, st.Reason, st.Notion, st.Commit, now())
	if err != nil {
		return false, err
	}
	if prev == nil {
		return st.Status != StatusActive, nil
	}
	return prev.Status != st.Status || (st.Status == StatusPaused && prev.Reason != st.Reason), nil
}

// ProjectState returns a project's state, or nil if unknown.
func (s *Store) ProjectState(ctx context.Context, project string) (*ProjectState, error) {
	var st ProjectState
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT project, status, reason, notion, commit_sha, updated_at FROM project_state
		WHERE project = ?`, project).Scan(&st.Project, &st.Status, &st.Reason, &st.Notion, &st.Commit, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.UpdatedAt = parseTime(at)
	return &st, nil
}

// DeleteProjectState forgets a project's state (run history is kept).
func (s *Store) DeleteProjectState(ctx context.Context, project string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_state WHERE project = ?`, project)
	return err
}
