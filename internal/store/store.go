// Package store implements lgit's SQLite persistence (schema v1 per
// docs/SPEC.md) using the pure-Go modernc.org/sqlite driver.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS pr (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	number         INTEGER NOT NULL UNIQUE,
	title          TEXT NOT NULL DEFAULT '',
	body           TEXT NOT NULL DEFAULT '',
	base_ref       TEXT NOT NULL,
	head_ref       TEXT NOT NULL,
	base_sha       TEXT NOT NULL,
	merge_base_sha TEXT NOT NULL,
	head_sha       TEXT NOT NULL,
	worktree_path  TEXT NOT NULL DEFAULT '',
	state          TEXT NOT NULL DEFAULT 'open'
		CHECK (state IN ('open','changes_requested','approved','merged','closed')),
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS review (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	pr_id      INTEGER NOT NULL REFERENCES pr(id),
	verdict    TEXT NOT NULL CHECK (verdict IN ('approve','request_changes','comment')),
	body       TEXT NOT NULL DEFAULT '',
	head_sha   TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS thread (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	pr_id      INTEGER NOT NULL REFERENCES pr(id),
	file_path  TEXT NOT NULL DEFAULT '',
	line       INTEGER NOT NULL DEFAULT 0,
	side       TEXT NOT NULL DEFAULT 'new' CHECK (side IN ('old','new')),
	anchor_sha TEXT NOT NULL DEFAULT '',
	resolved   INTEGER NOT NULL DEFAULT 0,
	outdated   INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS comment (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	thread_id  INTEGER NOT NULL REFERENCES thread(id),
	author     TEXT NOT NULL CHECK (author IN ('user','agent')),
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
`

// PR mirrors the pr table.
type PR struct {
	ID           int64  `json:"id"`
	Number       int64  `json:"number"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	BaseRef      string `json:"base_ref"`
	HeadRef      string `json:"head_ref"`
	BaseSHA      string `json:"base_sha"`
	MergeBaseSHA string `json:"merge_base_sha"`
	HeadSHA      string `json:"head_sha"`
	WorktreePath string `json:"worktree_path"`
	State        string `json:"state"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// Review mirrors the review table.
type Review struct {
	ID        int64  `json:"id"`
	PRID      int64  `json:"pr_id"`
	Verdict   string `json:"verdict"`
	Body      string `json:"body"`
	HeadSHA   string `json:"head_sha"`
	CreatedAt string `json:"created_at"`
}

// Thread mirrors the thread table; Comments is populated by queries.
type Thread struct {
	ID        int64     `json:"id"`
	PRID      int64     `json:"pr_id"`
	FilePath  string    `json:"file_path"`
	Line      int64     `json:"line"`
	Side      string    `json:"side"`
	AnchorSHA string    `json:"anchor_sha"`
	Resolved  bool      `json:"resolved"`
	Outdated  bool      `json:"outdated"`
	CreatedAt string    `json:"created_at"`
	Comments  []Comment `json:"comments"`
}

// Comment mirrors the comment table.
type Comment struct {
	ID        int64  `json:"id"`
	ThreadID  int64  `json:"thread_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// DBPath returns the lgit DB path under the given git common dir.
func DBPath(commonDir string) string {
	return filepath.Join(commonDir, "lgit", "lgit.db")
}

// Open opens (creating directories as needed) the DB at path and ensures the
// schema exists. Idempotent.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE review ADD COLUMN head_sha TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		db.Close()
		return nil, fmt.Errorf("migrating schema: %w", err)
	}
	return &Store{db: db}, nil
}

func isDuplicateColumn(err error) bool {
	return strings.Contains(err.Error(), "duplicate column name")
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// CreatePR inserts a PR, assigning the next sequential number. Returns the PR.
func (s *Store) CreatePR(pr PR) (*PR, error) {
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO pr (number, title, body, base_ref, head_ref, base_sha,
			merge_base_sha, head_sha, worktree_path, state, created_at, updated_at)
		VALUES ((SELECT COALESCE(MAX(number),0)+1 FROM pr), ?,?,?,?,?,?,?,?,?,?,?)`,
		pr.Title, pr.Body, pr.BaseRef, pr.HeadRef, pr.BaseSHA,
		pr.MergeBaseSHA, pr.HeadSHA, pr.WorktreePath, "open", ts, ts)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetPR(id)
}

const prCols = `id, number, title, body, base_ref, head_ref, base_sha,
	merge_base_sha, head_sha, worktree_path, state, created_at, updated_at`

func scanPR(row interface{ Scan(...any) error }) (*PR, error) {
	var p PR
	err := row.Scan(&p.ID, &p.Number, &p.Title, &p.Body, &p.BaseRef, &p.HeadRef,
		&p.BaseSHA, &p.MergeBaseSHA, &p.HeadSHA, &p.WorktreePath, &p.State,
		&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPR fetches a PR by id.
func (s *Store) GetPR(id int64) (*PR, error) {
	return scanPR(s.db.QueryRow(`SELECT `+prCols+` FROM pr WHERE id = ?`, id))
}

// GetPRByNumber fetches a PR by its user-facing number.
func (s *Store) GetPRByNumber(number int64) (*PR, error) {
	return scanPR(s.db.QueryRow(`SELECT `+prCols+` FROM pr WHERE number = ?`, number))
}

// ListPRs returns PRs, optionally filtered by state, newest first.
func (s *Store) ListPRs(state string) ([]PR, error) {
	q := `SELECT ` + prCols + ` FROM pr`
	var args []any
	if state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY number DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prs []PR
	for rows.Next() {
		p, err := scanPR(rows)
		if err != nil {
			return nil, err
		}
		prs = append(prs, *p)
	}
	return prs, rows.Err()
}

// UpdatePRState sets a PR's state.
func (s *Store) UpdatePRState(id int64, state string) error {
	res, err := s.db.Exec(`UPDATE pr SET state = ?, updated_at = ? WHERE id = ?`, state, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkPRReady flips a changes_requested PR back to open, signalling the
// agent has addressed feedback and it is the reviewer's turn again. It is
// idempotent on an already-open PR and errors on any other state so a caller
// cannot silently drop an approval or reopen a merged/closed PR. Returns the
// (possibly updated) PR.
func (s *Store) MarkPRReady(id int64) (*PR, error) {
	pr, err := s.GetPR(id)
	if err != nil {
		return nil, err
	}
	switch pr.State {
	case "open":
		return pr, nil
	case "changes_requested":
		if err := s.UpdatePRState(id, "open"); err != nil {
			return nil, err
		}
		return s.GetPR(id)
	default:
		return nil, fmt.Errorf("PR #%d is %s; only open or changes_requested PRs can be marked ready", pr.Number, pr.State)
	}
}

// UpdatePRRange refreshes a PR's snapshot SHAs (recomputed diff range).
func (s *Store) UpdatePRRange(id int64, baseSHA, mergeBaseSHA, headSHA string) error {
	_, err := s.db.Exec(`UPDATE pr SET base_sha = ?, merge_base_sha = ?, head_sha = ?, updated_at = ?
		WHERE id = ?`, baseSHA, mergeBaseSHA, headSHA, now(), id)
	return err
}

// CreateReview inserts a review row at the PR head SHA being reviewed.
func (s *Store) CreateReview(prID int64, verdict, body, headSHA string) (*Review, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO review (pr_id, verdict, body, head_sha, created_at) VALUES (?,?,?,?,?)`,
		prID, verdict, body, headSHA, ts)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Review{ID: id, PRID: prID, Verdict: verdict, Body: body, HeadSHA: headSHA, CreatedAt: ts}, nil
}

// LastReview returns the newest review row for a PR.
func (s *Store) LastReview(prID int64) (*Review, error) {
	var r Review
	err := s.db.QueryRow(`SELECT id, pr_id, verdict, body, head_sha, created_at
		FROM review WHERE pr_id = ? ORDER BY id DESC LIMIT 1`, prID).
		Scan(&r.ID, &r.PRID, &r.Verdict, &r.Body, &r.HeadSHA, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateThread inserts a thread plus its first comment, atomically.
func (s *Store) CreateThread(t Thread, firstAuthor, firstBody string) (*Thread, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO thread (pr_id, file_path, line, side, anchor_sha, resolved, outdated, created_at)
		VALUES (?,?,?,?,?,0,0,?)`, t.PRID, t.FilePath, t.Line, t.Side, t.AnchorSHA, ts)
	if err != nil {
		return nil, err
	}
	tid, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO comment (thread_id, author, body, created_at) VALUES (?,?,?,?)`,
		tid, firstAuthor, firstBody, ts); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetThread(tid)
}

// GetThread fetches a thread with its comments.
func (s *Store) GetThread(id int64) (*Thread, error) {
	var t Thread
	err := s.db.QueryRow(`SELECT id, pr_id, file_path, line, side, anchor_sha, resolved, outdated, created_at
		FROM thread WHERE id = ?`, id).
		Scan(&t.ID, &t.PRID, &t.FilePath, &t.Line, &t.Side, &t.AnchorSHA, &t.Resolved, &t.Outdated, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	comments, err := s.commentsFor(t.ID)
	if err != nil {
		return nil, err
	}
	t.Comments = comments
	return &t, nil
}

// ListThreads returns a PR's threads (with comments), oldest first. If
// unresolvedOnly is set, resolved threads are skipped.
func (s *Store) ListThreads(prID int64, unresolvedOnly bool) ([]Thread, error) {
	q := `SELECT id, pr_id, file_path, line, side, anchor_sha, resolved, outdated, created_at
		FROM thread WHERE pr_id = ?`
	if unresolvedOnly {
		q += ` AND resolved = 0`
	}
	q += ` ORDER BY id`
	rows, err := s.db.Query(q, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	threads := []Thread{}
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.PRID, &t.FilePath, &t.Line, &t.Side, &t.AnchorSHA,
			&t.Resolved, &t.Outdated, &t.CreatedAt); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range threads {
		comments, err := s.commentsFor(threads[i].ID)
		if err != nil {
			return nil, err
		}
		threads[i].Comments = comments
	}
	return threads, nil
}

func (s *Store) commentsFor(threadID int64) ([]Comment, error) {
	rows, err := s.db.Query(`SELECT id, thread_id, author, body, created_at
		FROM comment WHERE thread_id = ? ORDER BY id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.ThreadID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// AddComment appends a comment to a thread.
func (s *Store) AddComment(threadID int64, author, body string) (*Comment, error) {
	if _, err := s.GetThread(threadID); err != nil {
		return nil, err
	}
	ts := now()
	res, err := s.db.Exec(`INSERT INTO comment (thread_id, author, body, created_at) VALUES (?,?,?,?)`,
		threadID, author, body, ts)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Comment{ID: id, ThreadID: threadID, Author: author, Body: body, CreatedAt: ts}, nil
}

// SetThreadResolved marks a thread resolved/unresolved.
func (s *Store) SetThreadResolved(threadID int64, resolved bool) error {
	res, err := s.db.Exec(`UPDATE thread SET resolved = ? WHERE id = ?`, boolInt(resolved), threadID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetThreadOutdated marks whether an inline thread's anchor is stale relative
// to the current PR head.
func (s *Store) SetThreadOutdated(threadID int64, outdated bool) error {
	res, err := s.db.Exec(`UPDATE thread SET outdated = ? WHERE id = ?`, boolInt(outdated), threadID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
