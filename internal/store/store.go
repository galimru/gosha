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
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the Gosha database.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path and migrates it.
func Open(ctx context.Context, path string) (*Store, error) {
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
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store version: %w", err)
	}
	if version > len(names) {
		return fmt.Errorf("store schema version %d is newer than this gosha (%d): upgrade gosha", version, len(names))
	}
	for i := version; i < len(names); i++ {
		body, err := migrations.ReadFile(names[i])
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
	return nil
}

const timeFormat = time.RFC3339Nano

func now() string { return time.Now().UTC().Format(timeFormat) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeFormat, s)
	return t
}

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
	Outcome     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CreateRun records a new open run and sets its ID.
func (s *Store) CreateRun(ctx context.Context, r *Run) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO runs
		(project, ticket_id, mode, branch, pr_number, step, review_round, check_round, impl_session, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Project, r.TicketID, r.Mode, r.Branch, r.PRNumber, r.Step, r.ReviewRound, r.CheckRound, r.ImplSession, ts, ts)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	r.ID, err = res.LastInsertId()
	r.CreatedAt, r.UpdatedAt = parseTime(ts), parseTime(ts)
	return err
}

// FinishRun closes a run with an outcome.
func (s *Store) FinishRun(ctx context.Context, id int64, outcome string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET outcome = ?, finished_at = ?, updated_at = ? WHERE id = ?`, outcome, ts, ts, id)
	return err
}

const runColumns = `id, project, ticket_id, mode, branch, pr_number, step, review_round, check_round, impl_session, outcome, created_at, updated_at`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var created, updated string
	err := row.Scan(&r.ID, &r.Project, &r.TicketID, &r.Mode, &r.Branch, &r.PRNumber, &r.Step,
		&r.ReviewRound, &r.CheckRound, &r.ImplSession, &r.Outcome, &created, &updated)
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
	Project   string
	Status    string
	Reason    string
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO project_state (project, status, reason, commit_sha, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (project) DO UPDATE
		SET status = excluded.status, reason = excluded.reason, commit_sha = excluded.commit_sha, updated_at = excluded.updated_at`,
		st.Project, st.Status, st.Reason, st.Commit, now())
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
	err := s.db.QueryRowContext(ctx, `SELECT project, status, reason, commit_sha, updated_at FROM project_state
		WHERE project = ?`, project).Scan(&st.Project, &st.Status, &st.Reason, &st.Commit, &at)
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
