package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// ErrNotFound is what every lookup here returns when the row does not exist.
//
// It names a contract that used to be unwritten. Handlers tested against
// ErrNotFound directly, which was a private arrangement between two concrete
// types in one binary — and then the handler interfaces made the store
// pluggable, so "returns a database/sql sentinel" became a load-bearing term of
// eleven published interfaces that stated it nowhere. An implementation that did
// not happen to use database/sql would turn every 404 into a 500.
//
// It is ErrNotFound rather than a wrapper, so nothing changes today: existing
// errors.Is checks keep matching, and callers that already had the sentinel
// still work. What it buys is a name the store owns, in the package whose
// contract it is. If the store ever stops returning the raw sentinel, this is
// the one place that changes.
var ErrNotFound = sql.ErrNoRows

// Store wraps the SQLite database connection.
type Store struct {
	// mu guards db, which Reopen replaces while the server is serving. Read
	// through conn(); write only in Reopen.
	mu sync.RWMutex
	db *sql.DB
}

// execQuerier is the subset of *sql.DB the migration helpers need.
type execQuerier interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}

// User represents a user account.
type User struct {
	ID                string
	Username          string
	IsDefaultPassword bool
	CreatedAt         time.Time
	passwordHash      string
}

// Form represents a form endpoint.
type Form struct {
	ID            string
	Name          string
	EmailTo       string
	Redirect      string
	WebhookURL    string
	WebhookFormat string
	CreatedAt     time.Time

	// SpamThreshold overrides the instance-wide threshold for this form.
	// 0 means inherit — not "hold everything", which is why it is not nullable.
	SpamThreshold int
}

// FormSummary is a Form with its unread submission count.
type FormSummary struct {
	Form
	UnreadCount int
}

// Submission represents a form submission.
type Submission struct {
	ID        string
	FormID    string
	Data      map[string]string
	RawData   string // raw JSON stored in the DB; Data is its decoded form
	IP        string
	Read      bool
	CreatedAt time.Time

	// Quarantine state. Always populated *on a read*: every read path selects
	// heldColumns and scans through scanHeld, so these mean the same thing
	// whichever function returned the value. Guarded by
	// TestEverySubmissionReadUsesTheSharedColumnList rather than by this comment.
	//
	// Write paths carry them too, now. CreateSubmission writes SpamScore and
	// HeldThreshold straight from the struct — an accepted submission has a real
	// score, and printing zero for it was the UI stating a number the submission
	// never had. CreateHeldSubmission still takes them as parameters because it
	// also writes the signal rows. A fixture that leaves them zero is saying the
	// message scored nothing, which for a fixture is true.
	//
	// Signals are deliberately NOT stored for accepted submissions. The reader
	// distinguishes "was held, then restored" from "passed" by whether any signal
	// rows exist, so writing them for every sub-threshold hit would make ordinary
	// submissions claim they had been quarantined — a worse statement than the
	// one being fixed, and one that would need a new column to undo.
	//
	// That is deliberate, and it is the alternative to splitting this into
	// separate held and accepted types. The two are one row and one lifecycle —
	// RestoreSubmission turns one into the other with a single UPDATE — so a
	// split would need a conversion, and a conversion is where fields get
	// dropped. Populating every column removes the invalid state instead of
	// renaming it.
	//
	// These are non-zero on a *restored* submission, which is the case that
	// makes the invariant load-bearing: the score and threshold are kept as
	// evidence of a false positive, so a partial read reports score 0 for a row
	// the database says scored 11. That exact mismatch shipped once already.
	// Notified likewise defaults to 1 in the schema, so a partial read claims an
	// accepted submission was never notified.
	IsHeld    bool
	SpamScore int
	// HeldThreshold is the threshold actually applied, whether or not the
	// submission was held — an accepted one is judged against a bar too, and the
	// reader shows the score against it.
	HeldThreshold int
	Notified      bool
}

// Waitlist represents an email-keyed signup list.
type Waitlist struct {
	ID             string
	Name           string
	Redirect       string
	ConfirmSubject string
	ConfirmBody    string
	CreatedAt      time.Time
}

// WaitlistSummary is a Waitlist with its entry count.
type WaitlistSummary struct {
	Waitlist
	EntryCount int
}

// WaitlistEntry represents one person on a waitlist. Email is the identity;
// any extra submitted fields are stored as JSON in Data.
type WaitlistEntry struct {
	ID         string
	WaitlistID string
	Email      string
	Data       map[string]string
	RawData    string // raw JSON stored in the DB; Data is its decoded form
	IP         string
	Position   int // computed signup rank; set only by ListEntries/ListEntriesPaged. CreateEntry returns position separately, not via this field.
	CreatedAt  time.Time
}

// Broadcast represents one bulk message to a waitlist.
type Broadcast struct {
	ID         string
	WaitlistID string
	Subject    string
	Body       string
	Status     string // "sending" | "done"
	CreatedAt  time.Time
}

// IsSending reports whether the broadcast is still being delivered.
func (b Broadcast) IsSending() bool { return b.Status == BroadcastStatusSending }

// BroadcastSummary is a Broadcast with per-status delivery counts.
type BroadcastSummary struct {
	Broadcast
	Total   int
	Sent    int
	Failed  int
	Pending int
}

// Delivery represents one recipient's delivery within a broadcast.
type Delivery struct {
	ID          string
	BroadcastID string
	Email       string
	Status      string // "pending" | "sent" | "failed"
	Error       string
	Attempts    int
	UpdatedAt   time.Time
}

// Broadcast and delivery status values.
const (
	BroadcastStatusSending = "sending"
	BroadcastStatusDone    = "done"

	DeliveryStatusPending = "pending"
	DeliveryStatusSent    = "sent"
	DeliveryStatusFailed  = "failed"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id                  TEXT PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE,
    password            TEXT NOT NULL,
    is_default_password INTEGER NOT NULL DEFAULT 1,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS forms (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    email_to       TEXT NOT NULL DEFAULT '',
    redirect       TEXT NOT NULL DEFAULT '',
    webhook_url    TEXT NOT NULL DEFAULT '',
    webhook_format TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    -- Per-form spam sensitivity. 0 means inherit the instance default, not a
    -- literal threshold of zero, which would hold every submission.
    spam_threshold INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS submissions (
    id             TEXT PRIMARY KEY,
    form_id        TEXT NOT NULL REFERENCES forms(id) ON DELETE CASCADE,
    data           TEXT NOT NULL,
    ip             TEXT NOT NULL DEFAULT '',
    read           INTEGER NOT NULL DEFAULT 0,
    created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    -- Quarantine. A held submission is stored but withheld from the form's
    -- inbox until an operator restores or deletes it. notified stays 0 while
    -- held so a restore can send the notification that was withheld.
    is_held        INTEGER NOT NULL DEFAULT 0,
    spam_score     INTEGER NOT NULL DEFAULT 0,
    held_threshold INTEGER NOT NULL DEFAULT 0,
    held_at        DATETIME NOT NULL DEFAULT '',
    notified       INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_submissions_form_id ON submissions(form_id);
CREATE INDEX IF NOT EXISTS idx_submissions_read ON submissions(read);
CREATE INDEX IF NOT EXISTS idx_submissions_created_at ON submissions(created_at);

-- spam_signals records why one submission was held: which rule fired, on which
-- field, on what text, and for how many points.
--
-- It is a stored record, not a re-computation. The weights and keyword list in
-- internal/screen can be retuned and the threshold is operator-configurable, so
-- re-scoring an old submission at review time would show a reviewer a reason
-- that was never actually applied to it.
--
-- match_text is attacker-supplied: truncated on the way in and escaped on the
-- way out by html/template. It is not named "match" because MATCH is a SQLite
-- operator and the bare word would need quoting at every use site.
CREATE TABLE IF NOT EXISTS spam_signals (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    rule          TEXT NOT NULL,
    field         TEXT NOT NULL DEFAULT '',
    match_text    TEXT NOT NULL DEFAULT '',
    weight        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_spam_signals_submission_id ON spam_signals(submission_id);

-- filter_rules are the operator's explicit overrides of the scoring filter:
-- allow entries skip scoring entirely, block entries hold on arrival whatever
-- the score, and keyword entries extend the built-in list at the usual weight.
CREATE TABLE IF NOT EXISTS filter_rules (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL CHECK(kind IN ('block','allow')),
    type       TEXT NOT NULL CHECK(type IN ('email','domain','ip','cidr','keyword')),
    value      TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    hits       INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(kind, type, value)
);
CREATE INDEX IF NOT EXISTS idx_filter_rules_kind ON filter_rules(kind);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

-- api_tokens are the credentials an MCP client presents. One row is one
-- long-lived bearer token belonging to one user.
--
-- Only the hash is stored, exactly as for sessions: the raw token is returned
-- once at creation and is unrecoverable afterwards, so a database read is not a
-- set of live credentials.
--
-- The ON DELETE CASCADE is the point of binding a token to a user rather than to
-- the instance. Removing someone's account removes their access in the same
-- statement; an instance-wide token would outlive its owner with nobody to
-- answer for what it did.
--
-- expires_at is '' for a token that never expires, matching held_at above rather
-- than introducing the schema's first nullable column. Read the warning on
-- GetAPIToken before writing any comparison against it.
CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL UNIQUE,
    scopes       TEXT NOT NULL DEFAULT '',
    -- form_ids bounds the token to a set of forms, comma-separated. Empty means
    -- every form: that is what the column holds for tokens minted before this
    -- existed, and an upgrade must not silently revoke live credentials. Read
    -- it through APIToken.Scope, which is the one place that reading is made.
    form_ids     TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    last_used_at DATETIME NOT NULL DEFAULT '',
    expires_at   DATETIME NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);

CREATE TABLE IF NOT EXISTS waitlists (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    redirect        TEXT NOT NULL DEFAULT '',
    confirm_subject TEXT NOT NULL DEFAULT '',
    confirm_body    TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS waitlist_entries (
    id          TEXT PRIMARY KEY,
    waitlist_id TEXT NOT NULL REFERENCES waitlists(id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    data        TEXT NOT NULL DEFAULT '{}',
    ip          TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(waitlist_id, email)
);
CREATE INDEX IF NOT EXISTS idx_waitlist_entries_waitlist_id ON waitlist_entries(waitlist_id);

CREATE TABLE IF NOT EXISTS broadcasts (
    id          TEXT PRIMARY KEY,
    waitlist_id TEXT NOT NULL REFERENCES waitlists(id) ON DELETE CASCADE,
    subject     TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'sending' CHECK(status IN ('sending','done')),
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_broadcasts_waitlist_id ON broadcasts(waitlist_id);

CREATE TABLE IF NOT EXISTS deliveries (
    id           TEXT PRIMARY KEY,
    broadcast_id TEXT NOT NULL REFERENCES broadcasts(id) ON DELETE CASCADE,
    email        TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sent','failed')),
    error        TEXT NOT NULL DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    updated_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_deliveries_status ON deliveries(status);
CREATE INDEX IF NOT EXISTS idx_deliveries_broadcast_id ON deliveries(broadcast_id);
`

// runMigrations applies the schema to db. Safe to call on an existing DB (idempotent).
func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// runAlterMigrations adds columns to existing tables. Only "duplicate column"
// errors are ignored; all other errors are returned.
//
// Every column here is also declared in the CREATE TABLE above, so a fresh
// database gets it from the schema and this pass is a no-op; an existing
// database gets it from the ALTER. That double declaration is the established
// pattern in this file (see forms.webhook_url) and is what keeps migrations
// idempotent without a version table.
func runAlterMigrations(db *sql.DB) error {
	alters := []string{
		"ALTER TABLE forms ADD COLUMN webhook_url TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE forms ADD COLUMN webhook_format TEXT NOT NULL DEFAULT ''",
		// Per-form spam sensitivity. 0 means inherit the instance default
		// rather than NULL, matching the no-nullable-columns style of the rest
		// of the schema.
		"ALTER TABLE forms ADD COLUMN spam_threshold INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE submissions ADD COLUMN is_held INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE submissions ADD COLUMN spam_score INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE submissions ADD COLUMN held_threshold INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE submissions ADD COLUMN held_at DATETIME NOT NULL DEFAULT ''",
		"ALTER TABLE submissions ADD COLUMN notified INTEGER NOT NULL DEFAULT 1",
		// Empty is every form, so existing tokens keep the access they have.
		"ALTER TABLE api_tokens ADD COLUMN form_ids TEXT NOT NULL DEFAULT ''",
	}
	for _, q := range alters {
		_, err := db.Exec(q)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("alter migration: %w", err)
		}
	}

	// Indexes over columns the ALTERs above may have just introduced. They
	// cannot live in the schema constant: that runs before this function, so on
	// an upgrade from a pre-quarantine database the column would not yet exist.
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_submissions_form_held ON submissions(form_id, is_held)",
		"CREATE INDEX IF NOT EXISTS idx_submissions_held ON submissions(is_held)",
	}
	for _, q := range indexes {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("index migration: %w", err)
		}
	}
	return nil
}

// runSearchMigrations creates the FTS5 index and its triggers, then brings the
// index into step with the table. Separate from runMigrations because the
// triggers reference submissions columns that runAlterMigrations may have only
// just added.
func runSearchMigrations(db execQuerier) error {
	if _, err := db.Exec(searchSchema); err != nil {
		return fmt.Errorf("search migrations: %w", err)
	}
	return syncSearchIndex(db)
}

// New opens a SQLite database and runs migrations.
func New(path string) (*Store, error) {
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		// Use file URI for in-memory so pragmas apply correctly.
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if path == ":memory:" {
		// database/sql pools connections, and every new connection to an
		// in-memory database gets its own private, empty one. A second
		// connection would therefore see no tables at all — "no such table:
		// submissions" from a perfectly valid query. Capping the pool at one
		// keeps every caller on the same database.
		//
		// This only affects tests; the file-backed path keeps WAL concurrency.
		db.SetMaxOpenConns(1)
	}

	if err := runMigrations(db); err != nil {
		return nil, err
	}
	if err := runAlterMigrations(db); err != nil {
		return nil, err
	}
	if err := runSearchMigrations(db); err != nil {
		return nil, err
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}

	if count == 0 {
		hash, err := bcrypt.GenerateFromPassword([]byte("admin"), 12)
		if err != nil {
			return nil, fmt.Errorf("hash default password: %w", err)
		}
		id := uuid.New().String()
		_, err = db.Exec(
			"INSERT INTO users (id, username, password, is_default_password) VALUES (?, ?, ?, 1)",
			id, "admin", string(hash),
		)
		if err != nil {
			return nil, fmt.Errorf("seed default user: %w", err)
		}
	}

	var hasDefault int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE is_default_password = 1").Scan(&hasDefault); err == nil && hasDefault > 0 {
		log.Println("⚠  WARNING: Default admin credentials are active (admin/admin). Change your password immediately at /admin/users.")
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.conn().Close()
}

// DB returns the underlying *sql.DB for backup operations.
func (s *Store) DB() *sql.DB {
	return s.conn()
}

// conn reads the current handle under the lock.
//
// Every access to s.db goes through here, because Reopen replaces it while the
// server is live. Reading the field directly is a data race in the plain Go
// sense — verified with -race between Reopen's write and a concurrent read — and
// the /healthz probe turned it from a coincidence into something exercised every
// few seconds rather than only when a request happened to overlap a restore.
//
// The lock covers the pointer read, not the query that follows. A request that
// takes the handle immediately before a restore swaps it will use the old one
// and get "sql: database is closed" — an honest error, and unavoidable when the
// database is being replaced underneath live traffic. What it will not do is
// read a half-written pointer.
func (s *Store) conn() *sql.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

// Reopen opens a new database at path, confirms it is healthy, then closes the
// old connection. If the old connection was already closed (e.g. by Import),
// that close error is intentionally ignored.
func (s *Store) Reopen(path string) error {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	newDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("reopen: open new db: %w", err)
	}
	if err := newDB.Ping(); err != nil {
		newDB.Close()
		return fmt.Errorf("reopen: ping: %w", err)
	}
	if err := runMigrations(newDB); err != nil {
		newDB.Close()
		return fmt.Errorf("reopen: %w", err)
	}
	if err := runAlterMigrations(newDB); err != nil {
		newDB.Close()
		return fmt.Errorf("reopen: %w", err)
	}
	// A restored backup arrives carrying whatever search index that file had —
	// possibly none, if it predates this feature — so the index is resynced on
	// reopen as well as on a normal open.
	if err := runSearchMigrations(newDB); err != nil {
		newDB.Close()
		return fmt.Errorf("reopen: %w", err)
	}
	// Swap under the write lock, so no reader can observe the field mid-change.
	s.mu.Lock()
	defer s.mu.Unlock()
	// Close old connection; ignore error — may already be closed by Import.
	if s.db != nil {
		s.db.Close()
	}
	s.db = newDB
	return nil
}

// GetUserByUsername looks up a user by username.
func (s *Store) GetUserByUsername(username string) (User, error) {
	var u User
	var isDefault int
	err := s.conn().QueryRow(
		"SELECT id, username, password, is_default_password, created_at FROM users WHERE username = ?",
		username,
	).Scan(&u.ID, &u.Username, &u.passwordHash, &isDefault, &u.CreatedAt)
	if err != nil {
		return User{}, fmt.Errorf("user not found: %w", err)
	}
	u.IsDefaultPassword = isDefault == 1
	return u, nil
}

// GetUserByID looks up a user by ID.
func (s *Store) GetUserByID(id string) (User, error) {
	var u User
	var isDefault int
	err := s.conn().QueryRow(
		"SELECT id, username, password, is_default_password, created_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Username, &u.passwordHash, &isDefault, &u.CreatedAt)
	if err != nil {
		return User{}, fmt.Errorf("get user by id: %w", err)
	}
	u.IsDefaultPassword = isDefault == 1
	return u, nil
}

// ListUsers returns all users.
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.conn().Query(
		"SELECT id, username, password, is_default_password, created_at FROM users ORDER BY created_at",
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var isDefault int
		if err := rows.Scan(&u.ID, &u.Username, &u.passwordHash, &isDefault, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		u.IsDefaultPassword = isDefault == 1
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// MinPasswordLength is the shortest password this instance will store.
//
// Enforced on write, in the store, because that is where all four *operator*
// ways of setting a password converge: the admin form, the account page,
// `dsforms user add` and `dsforms user set-password`. There was no check on any
// of them, so an account could be created with an EMPTY password and would then
// log in normally with full admin rights — verified against a running instance
// before this was added.
//
// The default-admin seed is a fifth path and does not converge here: it hashes
// and INSERTs directly, so it is exempt by construction rather than by
// permission. That is deliberate — see the note on login below — but it means
// "every path that sets a password is checked" is false as stated, and the
// exemption is worth knowing about before adding a sixth.
//
// Twelve, not eight, because the markup said twelve first and the two never
// agreed: the Nocturne port put "minimum 12 characters" into account.html and
// users_new.html on 2026-09-09, a day before any minimum existed in code, and
// when one arrived it was 8. Both numbers were picked a day apart, so neither
// is a long-standing promise — but 12 is the one operators were shown, and
// raising the check is the direction that does not weaken anything. Neither
// page states it now; both call minPassword, which reads this.
//
// Deliberately not enforced on login. Existing accounts may hold shorter
// passwords, and rejecting them at the door would lock people out of their own
// data to fix a problem they cannot then log in to fix. Note this is not only
// an upgrade concern: the seed below writes a 5-character password, so a fresh
// install starts with one too.
const MinPasswordLength = 12

// ErrPasswordTooShort is returned by CreateUser and UpdatePassword.
var ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)

// CreateUser creates a new user with a bcrypt-hashed password.
func (s *Store) CreateUser(username, password string) error {
	if len(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	id := uuid.New().String()
	_, err = s.conn().Exec(
		"INSERT INTO users (id, username, password, is_default_password) VALUES (?, ?, ?, 0)",
		id, username, string(hash),
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// UpdatePassword updates a user's password and clears IsDefaultPassword.
func (s *Store) UpdatePassword(userID, newPassword string) error {
	if len(newPassword) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	result, err := s.conn().Exec(
		"UPDATE users SET password = ?, is_default_password = 0 WHERE id = ?",
		string(hash), userID,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("update password: user not found")
	}
	return nil
}

// DeleteUser deletes a user. Fails if it's the last remaining user.
func (s *Store) DeleteUser(id string) error {
	tx, err := s.conn().Begin()
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if count <= 1 {
		return fmt.Errorf("cannot delete the last user")
	}
	result, err := tx.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delete user: user not found")
	}
	return tx.Commit()
}

// HasDefaultPassword checks if a user still has the default password.
func (s *Store) HasDefaultPassword(userID string) (bool, error) {
	var isDefault int
	err := s.conn().QueryRow(
		"SELECT is_default_password FROM users WHERE id = ?",
		userID,
	).Scan(&isDefault)
	if err != nil {
		return false, fmt.Errorf("has default password: %w", err)
	}
	return isDefault == 1, nil
}

// CheckPassword verifies a plaintext password against the stored hash for a user.
func (s *Store) CheckPassword(username, password string) (User, error) {
	u, err := s.GetUserByUsername(username)
	if err != nil {
		return User{}, fmt.Errorf("check password: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.passwordHash), []byte(password)); err != nil {
		return User{}, fmt.Errorf("check password: %w", err)
	}
	return u, nil
}

// CreateForm creates a new form.
func (s *Store) CreateForm(f Form) error {
	_, err := s.conn().Exec(
		"INSERT INTO forms (id, name, email_to, redirect, webhook_url, webhook_format, spam_threshold) VALUES (?, ?, ?, ?, ?, ?, ?)",
		f.ID, f.Name, f.EmailTo, f.Redirect, f.WebhookURL, f.WebhookFormat, f.SpamThreshold,
	)
	if err != nil {
		return fmt.Errorf("create form: %w", err)
	}
	return nil
}

// GetForm returns a form by ID.
func (s *Store) GetForm(id string) (Form, error) {
	var f Form
	err := s.conn().QueryRow(
		"SELECT id, name, email_to, redirect, webhook_url, webhook_format, created_at, spam_threshold FROM forms WHERE id = ?",
		id,
	).Scan(&f.ID, &f.Name, &f.EmailTo, &f.Redirect, &f.WebhookURL, &f.WebhookFormat, &f.CreatedAt, &f.SpamThreshold)
	if err != nil {
		return Form{}, fmt.Errorf("get form: %w", err)
	}
	return f, nil
}

// ListForms returns all forms with unread counts.
func (s *Store) ListForms() ([]FormSummary, error) {
	rows, err := s.conn().Query(`
		SELECT f.id, f.name, f.email_to, f.redirect, f.webhook_url, f.webhook_format, f.created_at, f.spam_threshold,
		       COUNT(CASE WHEN s.read = 0 AND s.is_held = 0 THEN 1 END) as unread_count
		FROM forms f
		LEFT JOIN submissions s ON s.form_id = f.id
		GROUP BY f.id
		ORDER BY f.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list forms: %w", err)
	}
	defer rows.Close()

	var forms []FormSummary
	for rows.Next() {
		var fs FormSummary
		if err := rows.Scan(&fs.ID, &fs.Name, &fs.EmailTo, &fs.Redirect, &fs.WebhookURL, &fs.WebhookFormat, &fs.CreatedAt, &fs.SpamThreshold, &fs.UnreadCount); err != nil {
			return nil, fmt.Errorf("list forms: %w", err)
		}
		forms = append(forms, fs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list forms: %w", err)
	}
	return forms, nil
}

// UpdateForm updates a form's fields.
func (s *Store) UpdateForm(f Form) error {
	_, err := s.conn().Exec(
		"UPDATE forms SET name = ?, email_to = ?, redirect = ?, webhook_url = ?, webhook_format = ?, spam_threshold = ? WHERE id = ?",
		f.Name, f.EmailTo, f.Redirect, f.WebhookURL, f.WebhookFormat, f.SpamThreshold, f.ID,
	)
	if err != nil {
		return fmt.Errorf("update form: %w", err)
	}
	return nil
}

// DeleteForm deletes a form and its submissions.
// Returns ErrNotFound if no form with the given ID exists.
func (s *Store) DeleteForm(id string) error {
	result, err := s.conn().Exec("DELETE FROM forms WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete form: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delete form: %w", ErrNotFound)
	}
	return nil
}

// sqliteTime is the text layout SQLite's own datetime('now') produces. Times
// written from Go must use it: created_at columns are TEXT, so ORDER BY compares
// them as strings and a different layout (RFC3339's "T" separator, say) would
// sort inconsistently against rows written by the column default.
const sqliteTime = "2006-01-02 15:04:05"

// sqliteTimestamp renders t in the layout SQLite's own datetime('now') produces.
//
// Every timestamp written from Go goes through this. Handing the driver a
// time.Time instead stringifies it with an offset ("2026-09-09T17:22:49.9-07:00"
// or "… +0000 UTC" depending on the value), which date() and datetime() cannot
// parse — and since these columns are TEXT, a range comparison against a
// differently-formatted value is a string comparison that is silently
// meaningless. That has been the cause of three separate bugs here.
//
// The .UTC() is unconditional rather than left to the caller. Several call sites
// were correct only because the value could be traced back to a time.Now().UTC()
// a few lines up, which is not a property anyone should have to re-derive.
func sqliteTimestamp(t time.Time) string {
	return t.UTC().Format(sqliteTime)
}

// CreateSubmission creates a new submission. created_at is written explicitly so
// the caller's Submission carries the same timestamp as the stored row — the
// notification email formats its Date header from the in-memory struct, which
// left to the column default would be a zero time.Time. A zero CreatedAt falls
// back to now rather than writing a year 0001 row.
func (s *Store) CreateSubmission(sub Submission) error {
	createdAt := sub.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	// SpamScore and HeldThreshold are written from the struct rather than taken
	// as parameters, which is what CreateHeldSubmission does. This function has
	// one production caller and a long tail of test fixtures that legitimately
	// pass zero, so a signature change would be almost entirely churn — and the
	// struct already carries both fields for reads. Writing them here is what
	// makes them mean the same thing in both directions.
	//
	// The cost is that the two writers now honour the same fields under opposite
	// conventions: this one reads sub.SpamScore, CreateHeldSubmission takes it as
	// a parameter and ignores the field. Nothing in the types says so, which is
	// why both doc comments do.
	_, err := s.conn().Exec(
		"INSERT INTO submissions (id, form_id, data, ip, created_at, spam_score, held_threshold) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?)",
		sub.ID, sub.FormID, sub.RawData, sub.IP, sqliteTimestamp(createdAt),
		sub.SpamScore, sub.HeldThreshold,
	)
	if err != nil {
		return fmt.Errorf("create submission: %w", err)
	}
	return nil
}

// ListSubmissions returns all submissions for a form.
func (s *Store) ListSubmissions(formID string) ([]Submission, error) {
	return s.querySubmissions("list submissions",
		"SELECT "+heldColumns+" FROM submissions WHERE form_id = ? AND is_held = 0 ORDER BY created_at DESC, id",
		formID)
}

// MarkRead marks a submission as read.
func (s *Store) MarkRead(submissionID string) error {
	_, err := s.conn().Exec("UPDATE submissions SET read = 1 WHERE id = ?", submissionID)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// MarkUnread puts a submission back in the unread state.
//
// The inverse of MarkRead, and it exists because a client that can only ever
// mark things read can never undo a misclick. The unread badge counts this
// column, so the two stay in step by construction.
func (s *Store) MarkUnread(submissionID string) error {
	_, err := s.conn().Exec("UPDATE submissions SET read = 0 WHERE id = ?", submissionID)
	if err != nil {
		return fmt.Errorf("mark unread: %w", err)
	}
	return nil
}

// ReadFilter selects which of an inbox's submissions a listing returns.
//
// A defined type rather than a bool, because the question has three answers and
// a bool can only carry two. It arrived as `unreadOnly bool`, and the third case
// was then handled by filtering the returned page in the caller — which thins a
// page that LIMIT and OFFSET have already chosen, so a form whose read
// submissions all sit behind a screenful of unread ones reports having none.
type ReadFilter string

const (
	// ReadAny returns read and unread alike.
	ReadAny ReadFilter = "all"
	// ReadUnread returns only what nobody has opened.
	ReadUnread ReadFilter = "unread"
	// ReadRead returns only what somebody has.
	ReadRead ReadFilter = "read"
)

// clause returns the SQL this filter adds, and whether it is a filter this build
// understands.
//
// Every case is named and the default refuses rather than widening to "all":
// a value we cannot interpret must not quietly return more rows than the caller
// asked for, which for an unrecognised status would mean handing back the whole
// inbox to someone who asked for a subset of it.
func (f ReadFilter) clause() (string, bool) {
	switch f {
	case ReadAny:
		return "", true
	case ReadUnread:
		return " AND read = 0", true
	case ReadRead:
		return " AND read = 1", true
	default:
		return "", false
	}
}

// ListSubmissionsFiltered returns a page of accepted submissions, optionally
// narrowed to one form and to a read state.
//
// It exists because neither existing listing answers "what have I not read?"
// across forms: ListSubmissions and ListSubmissionsPaged are both scoped to a
// single form and neither filters on read. An empty formID means every form.
//
// Held submissions are excluded, like every other inbox read — quarantine is
// reviewed on its own screen, and a message awaiting a spam decision is not
// something anyone has failed to read.
//
// The read state is applied here rather than by the caller, and that is the
// whole point of the type: filtering a returned page is filtering rows that
// LIMIT and OFFSET already chose, so the answer depends on how many rows of the
// other kind happened to sort ahead of them.
//
// The query is built by appending fixed clause strings and binding every value,
// never by interpolating one: the only input that reaches the SQL text is a
// constant chosen by the switch above.
func (s *Store) ListSubmissionsFiltered(formID string, read ReadFilter, limit, offset int) ([]Submission, error) {
	readClause, ok := read.clause()
	if !ok {
		return nil, fmt.Errorf("list submissions filtered: unknown read filter %q", read)
	}

	query := "SELECT " + heldColumns + " FROM submissions WHERE is_held = 0"
	var args []any
	if formID != "" {
		query += " AND form_id = ?"
		args = append(args, formID)
	}
	query += readClause
	// Tie-broken by id: created_at alone is not stable, and submissions arriving
	// in the same second would let a LIMIT/OFFSET page repeat or skip a row.
	query += " ORDER BY created_at DESC, id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	return s.querySubmissions("list submissions filtered", query, args...)
}

// CountAllSubmissions returns the total count of all submissions across all forms.
func (s *Store) CountAllSubmissions() (int, error) {
	var count int
	err := s.conn().QueryRow("SELECT COUNT(*) FROM submissions WHERE is_held = 0").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count all submissions: %w", err)
	}
	return count, nil
}

// MarkAllRead marks all submissions for a form as read.
func (s *Store) MarkAllRead(formID string) error {
	_, err := s.conn().Exec("UPDATE submissions SET read = 1 WHERE form_id = ? AND is_held = 0", formID)
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}

// DeleteSubmission deletes a submission.
func (s *Store) DeleteSubmission(id string) error {
	_, err := s.conn().Exec("DELETE FROM submissions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete submission: %w", err)
	}
	return nil
}

// DeleteSubmissions deletes multiple submissions belonging to formID. IDs
// belonging to a different form are silently ignored. A nil/empty ids
// deletes nothing.
func (s *Store) DeleteSubmissions(formID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, formID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := "DELETE FROM submissions WHERE form_id = ? AND id IN (" + strings.Join(placeholders, ",") + ")"
	if _, err := s.conn().Exec(query, args...); err != nil {
		return fmt.Errorf("delete submissions: %w", err)
	}
	return nil
}

// GetSubmission returns a single submission by ID.
func (s *Store) GetSubmission(id string) (Submission, error) {
	// Reads the quarantine columns too, via the same helpers the held-submission
	// queries use. It previously selected only the pre-quarantine columns, which
	// left SpamScore and IsHeld at zero on every read — and the submission
	// reader renders SpamScore beside the stored signal breakdown, so a restored
	// submission showed "score 0" above weights summing to 11. A handler guard
	// written against IsHeld would likewise have been a silent no-op.
	sub, err := scanHeld(s.conn().QueryRow("SELECT "+heldColumns+" FROM submissions WHERE id = ?", id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Callers distinguish "no such submission" from a real failure.
			return Submission{}, err
		}
		return Submission{}, fmt.Errorf("get submission: %w", err)
	}
	return sub, nil
}

// ListSubmissionsPaged returns a page of submissions for a form.
func (s *Store) ListSubmissionsPaged(formID string, limit, offset int) ([]Submission, error) {
	return s.querySubmissions("list submissions paged",
		"SELECT "+heldColumns+" FROM submissions WHERE form_id = ? AND is_held = 0 ORDER BY created_at DESC, id LIMIT ? OFFSET ?",
		formID, limit, offset)
}

// CountSubmissions returns the total number of submissions for a form.
func (s *Store) CountSubmissions(formID string) (int, error) {
	var count int
	err := s.conn().QueryRow("SELECT COUNT(*) FROM submissions WHERE form_id = ? AND is_held = 0", formID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count submissions: %w", err)
	}
	return count, nil
}

// UnreadCount returns the number of unread submissions for a form.
func (s *Store) UnreadCount(formID string) (int, error) {
	var count int
	err := s.conn().QueryRow(
		"SELECT COUNT(*) FROM submissions WHERE form_id = ? AND read = 0 AND is_held = 0",
		formID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("unread count: %w", err)
	}
	return count, nil
}

// hashToken returns the hex-encoded SHA-256 hash of the given token.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateSession creates a new session for the given user and returns the raw token.
func (s *Store) CreateSession(userID string, expiry time.Duration) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("create session: userID must not be empty")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	token := hex.EncodeToString(b)
	tokenHash := hashToken(token)
	// Formatted, not bound as a time.Time. The driver stringifies one with an
	// offset ("2026-09-09T17:22:21.69-07:00"), which datetime('now') — UTC, no
	// offset — neither parses nor compares against correctly, so expires_at is
	// read as a plain string that sorts by the wrong digits.
	expiresAt := sqliteTimestamp(time.Now().Add(expiry))
	_, err := s.conn().Exec(
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
		tokenHash, userID, expiresAt,
	)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// GetSession returns the userID for the given raw token if it exists and is not expired.
func (s *Store) GetSession(token string) (string, error) {
	tokenHash := hashToken(token)
	var userID string
	err := s.conn().QueryRow(
		"SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > datetime('now')",
		tokenHash,
	).Scan(&userID)
	if err != nil {
		return "", fmt.Errorf("get session: %w", err)
	}
	return userID, nil
}

// DeleteSession removes the session with the given raw token.
func (s *Store) DeleteSession(token string) error {
	tokenHash := hashToken(token)
	_, err := s.conn().Exec("DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions removes all sessions for the given user.
func (s *Store) DeleteUserSessions(userID string) error {
	_, err := s.conn().Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// CleanExpiredSessions removes all expired sessions from the database.
func (s *Store) CleanExpiredSessions() error {
	_, err := s.conn().Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
	if err != nil {
		return fmt.Errorf("clean expired sessions: %w", err)
	}
	return nil
}

// CreateWaitlist creates a new waitlist.
func (s *Store) CreateWaitlist(wl Waitlist) error {
	_, err := s.conn().Exec(
		"INSERT INTO waitlists (id, name, redirect, confirm_subject, confirm_body) VALUES (?, ?, ?, ?, ?)",
		wl.ID, wl.Name, wl.Redirect, wl.ConfirmSubject, wl.ConfirmBody,
	)
	if err != nil {
		return fmt.Errorf("create waitlist: %w", err)
	}
	return nil
}

// GetWaitlist returns a waitlist by ID.
func (s *Store) GetWaitlist(id string) (Waitlist, error) {
	var wl Waitlist
	err := s.conn().QueryRow(
		"SELECT id, name, redirect, confirm_subject, confirm_body, created_at FROM waitlists WHERE id = ?",
		id,
	).Scan(&wl.ID, &wl.Name, &wl.Redirect, &wl.ConfirmSubject, &wl.ConfirmBody, &wl.CreatedAt)
	if err != nil {
		return Waitlist{}, fmt.Errorf("get waitlist: %w", err)
	}
	return wl, nil
}

// ListWaitlists returns all waitlists with entry counts.
func (s *Store) ListWaitlists() ([]WaitlistSummary, error) {
	rows, err := s.conn().Query(`
		SELECT w.id, w.name, w.redirect, w.confirm_subject, w.confirm_body, w.created_at,
		       COUNT(e.id) AS entry_count
		FROM waitlists w
		LEFT JOIN waitlist_entries e ON e.waitlist_id = w.id
		GROUP BY w.id
		ORDER BY w.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list waitlists: %w", err)
	}
	defer rows.Close()

	var out []WaitlistSummary
	for rows.Next() {
		var ws WaitlistSummary
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Redirect, &ws.ConfirmSubject, &ws.ConfirmBody, &ws.CreatedAt, &ws.EntryCount); err != nil {
			return nil, fmt.Errorf("list waitlists: %w", err)
		}
		out = append(out, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list waitlists: %w", err)
	}
	return out, nil
}

// UpdateWaitlist updates a waitlist's editable fields.
// Returns ErrNotFound if no waitlist with the given ID exists.
func (s *Store) UpdateWaitlist(wl Waitlist) error {
	result, err := s.conn().Exec(
		"UPDATE waitlists SET name = ?, redirect = ?, confirm_subject = ?, confirm_body = ? WHERE id = ?",
		wl.Name, wl.Redirect, wl.ConfirmSubject, wl.ConfirmBody, wl.ID,
	)
	if err != nil {
		return fmt.Errorf("update waitlist: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("update waitlist: %w", ErrNotFound)
	}
	return nil
}

// DeleteWaitlist deletes a waitlist and its entries/broadcasts (cascade).
// Returns ErrNotFound if no waitlist with the given ID exists.
func (s *Store) DeleteWaitlist(id string) error {
	result, err := s.conn().Exec("DELETE FROM waitlists WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete waitlist: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delete waitlist: %w", ErrNotFound)
	}
	return nil
}

// CreateEntry inserts a waitlist entry, deduping by (waitlist_id, email).
// Returns the entry's signup position and whether the email was already present.
func (s *Store) CreateEntry(e WaitlistEntry) (position int, alreadyJoined bool, err error) {
	if e.Email == "" {
		return 0, false, fmt.Errorf("create entry: email must not be empty")
	}
	rawData := e.RawData
	if rawData == "" {
		rawData = "{}"
	}
	res, err := s.conn().Exec(
		`INSERT INTO waitlist_entries (id, waitlist_id, email, data, ip)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(waitlist_id, email) DO NOTHING`,
		e.ID, e.WaitlistID, e.Email, rawData, e.IP,
	)
	if err != nil {
		return 0, false, fmt.Errorf("create entry: %w", err)
	}
	// Not discarded. This is the one RowsAffected in this file whose zero value
	// is a *claim* rather than an error: n == 0 means the ON CONFLICT fired, and
	// the caller turns that into "you are already on the list". A swallowed
	// error would tell a first-time signup they had already joined, which is
	// both wrong and unarguable from their side.
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("create entry: rows affected: %w", err)
	}
	alreadyJoined = n == 0

	position, err = s.entryPosition(e.WaitlistID, e.Email)
	if err != nil {
		return 0, alreadyJoined, fmt.Errorf("create entry: %w", err)
	}
	return position, alreadyJoined, nil
}

// entryPosition returns the signup rank (1-based) of an email within a waitlist.
func (s *Store) entryPosition(waitlistID, email string) (int, error) {
	var pos int
	err := s.conn().QueryRow(`
		SELECT COUNT(*) FROM waitlist_entries
		WHERE waitlist_id = ?
		  AND rowid <= (SELECT rowid FROM waitlist_entries WHERE waitlist_id = ? AND email = ?)
	`, waitlistID, waitlistID, email).Scan(&pos)
	if err != nil {
		return 0, fmt.Errorf("entry position: %w", err)
	}
	return pos, nil
}

// CountEntries returns the number of entries on a waitlist.
func (s *Store) CountEntries(waitlistID string) (int, error) {
	var n int
	err := s.conn().QueryRow("SELECT COUNT(*) FROM waitlist_entries WHERE waitlist_id = ?", waitlistID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count entries: %w", err)
	}
	return n, nil
}

// scanEntries reads entry rows (with a trailing position column) into structs.
func scanEntries(rows *sql.Rows) ([]WaitlistEntry, error) {
	var out []WaitlistEntry
	for rows.Next() {
		var e WaitlistEntry
		var rawData string
		if err := rows.Scan(&e.ID, &e.WaitlistID, &e.Email, &rawData, &e.IP, &e.CreatedAt, &e.Position); err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		e.RawData = rawData
		if err := json.Unmarshal([]byte(rawData), &e.Data); err != nil {
			log.Printf("warning: failed to unmarshal entry %s data: %v", e.ID, err)
			e.Data = map[string]string{"_raw": rawData}
		}
		if e.Data == nil {
			e.Data = map[string]string{}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan entries: %w", err)
	}
	return out, nil
}

const entrySelectWithPosition = `
	SELECT e.id, e.waitlist_id, e.email, e.data, e.ip, e.created_at,
	       (SELECT COUNT(*) FROM waitlist_entries e2
	        WHERE e2.waitlist_id = e.waitlist_id AND e2.rowid <= e.rowid) AS position
	FROM waitlist_entries e
	WHERE e.waitlist_id = ?
	ORDER BY e.rowid DESC`

// ListEntriesPaged returns a page of entries (newest first) with positions.
func (s *Store) ListEntriesPaged(waitlistID string, limit, offset int) ([]WaitlistEntry, error) {
	rows, err := s.conn().Query(entrySelectWithPosition+" LIMIT ? OFFSET ?", waitlistID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list entries paged: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ListEntries returns all entries for a waitlist (newest first) with positions.
func (s *Store) ListEntries(waitlistID string) ([]WaitlistEntry, error) {
	rows, err := s.conn().Query(entrySelectWithPosition, waitlistID)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// DeleteEntry deletes a single waitlist entry scoped to its waitlist.
// Returns ErrNotFound if no matching entry exists.
func (s *Store) DeleteEntry(waitlistID, id string) error {
	result, err := s.conn().Exec("DELETE FROM waitlist_entries WHERE id = ? AND waitlist_id = ?", id, waitlistID)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delete entry: %w", ErrNotFound)
	}
	return nil
}

// CreateBroadcast inserts a broadcast (always created with status 'sending';
// the b.Status field is ignored) plus one pending delivery per email, atomically.
func (s *Store) CreateBroadcast(b Broadcast, emails []string) error {
	tx, err := s.conn().Begin()
	if err != nil {
		return fmt.Errorf("create broadcast: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		"INSERT INTO broadcasts (id, waitlist_id, subject, body, status) VALUES (?, ?, ?, ?, 'sending')",
		b.ID, b.WaitlistID, b.Subject, b.Body,
	)
	if err != nil {
		return fmt.Errorf("create broadcast: %w", err)
	}
	for _, email := range emails {
		_, err = tx.Exec(
			"INSERT INTO deliveries (id, broadcast_id, email) VALUES (?, ?, ?)",
			uuid.New().String(), b.ID, email,
		)
		if err != nil {
			return fmt.Errorf("create broadcast delivery: %w", err)
		}
	}
	return tx.Commit()
}

// GetBroadcast returns a broadcast by ID.
func (s *Store) GetBroadcast(id string) (Broadcast, error) {
	var b Broadcast
	err := s.conn().QueryRow(
		"SELECT id, waitlist_id, subject, body, status, created_at FROM broadcasts WHERE id = ?",
		id,
	).Scan(&b.ID, &b.WaitlistID, &b.Subject, &b.Body, &b.Status, &b.CreatedAt)
	if err != nil {
		return Broadcast{}, fmt.Errorf("get broadcast: %w", err)
	}
	return b, nil
}

// GetBroadcastSummary returns a broadcast with per-status delivery counts.
func (s *Store) GetBroadcastSummary(id string) (BroadcastSummary, error) {
	b, err := s.GetBroadcast(id)
	if err != nil {
		return BroadcastSummary{}, err
	}
	sum := BroadcastSummary{Broadcast: b}
	err = s.conn().QueryRow(`
		SELECT
			COUNT(*),
			COUNT(CASE WHEN status = 'sent' THEN 1 END),
			COUNT(CASE WHEN status = 'failed' THEN 1 END),
			COUNT(CASE WHEN status = 'pending' THEN 1 END)
		FROM deliveries WHERE broadcast_id = ?`, id,
	).Scan(&sum.Total, &sum.Sent, &sum.Failed, &sum.Pending)
	if err != nil {
		return BroadcastSummary{}, fmt.Errorf("get broadcast summary: %w", err)
	}
	return sum, nil
}

// ListBroadcasts returns all broadcasts for a waitlist (newest first) with counts.
func (s *Store) ListBroadcasts(waitlistID string) ([]BroadcastSummary, error) {
	rows, err := s.conn().Query(`
		SELECT b.id, b.waitlist_id, b.subject, b.body, b.status, b.created_at,
			COUNT(d.id),
			COUNT(CASE WHEN d.status = 'sent' THEN 1 END),
			COUNT(CASE WHEN d.status = 'failed' THEN 1 END),
			COUNT(CASE WHEN d.status = 'pending' THEN 1 END)
		FROM broadcasts b
		LEFT JOIN deliveries d ON d.broadcast_id = b.id
		WHERE b.waitlist_id = ?
		GROUP BY b.id
		ORDER BY b.created_at DESC`, waitlistID)
	if err != nil {
		return nil, fmt.Errorf("list broadcasts: %w", err)
	}
	defer rows.Close()

	var out []BroadcastSummary
	for rows.Next() {
		var sum BroadcastSummary
		if err := rows.Scan(&sum.ID, &sum.WaitlistID, &sum.Subject, &sum.Body, &sum.Status, &sum.CreatedAt,
			&sum.Total, &sum.Sent, &sum.Failed, &sum.Pending); err != nil {
			return nil, fmt.Errorf("list broadcasts: %w", err)
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list broadcasts: %w", err)
	}
	return out, nil
}

// NextPendingDeliveries returns up to limit pending deliveries (oldest first).
func (s *Store) NextPendingDeliveries(limit int) ([]Delivery, error) {
	rows, err := s.conn().Query(
		"SELECT id, broadcast_id, email, status, error, attempts, updated_at FROM deliveries WHERE status = 'pending' ORDER BY rowid LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("next pending deliveries: %w", err)
	}
	defer rows.Close()

	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.BroadcastID, &d.Email, &d.Status, &d.Error, &d.Attempts, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("next pending deliveries: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("next pending deliveries: %w", err)
	}
	return out, nil
}

// MarkDeliverySent marks a delivery as sent.
func (s *Store) MarkDeliverySent(id string) error {
	_, err := s.conn().Exec(
		"UPDATE deliveries SET status = 'sent', error = '', updated_at = datetime('now') WHERE id = ?",
		id,
	)
	if err != nil {
		return fmt.Errorf("mark delivery sent: %w", err)
	}
	return nil
}

// MarkDeliveryFailed records a failed attempt. The delivery stays 'pending'
// (eligible for retry) until attempts reach maxAttempts, then becomes 'failed'.
func (s *Store) MarkDeliveryFailed(id, errMsg string, maxAttempts int) error {
	_, err := s.conn().Exec(`
		UPDATE deliveries
		SET attempts = attempts + 1,
		    error = ?,
		    status = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END,
		    updated_at = datetime('now')
		WHERE id = ?`, errMsg, maxAttempts, id)
	if err != nil {
		return fmt.Errorf("mark delivery failed: %w", err)
	}
	return nil
}

// HasPendingDeliveries reports whether a broadcast still has pending deliveries.
func (s *Store) HasPendingDeliveries(broadcastID string) (bool, error) {
	var n int
	err := s.conn().QueryRow(
		"SELECT COUNT(*) FROM deliveries WHERE broadcast_id = ? AND status = 'pending'",
		broadcastID,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has pending deliveries: %w", err)
	}
	return n > 0, nil
}

// ListSendingBroadcasts returns the IDs of broadcasts still in 'sending' state.
func (s *Store) ListSendingBroadcasts() ([]string, error) {
	rows, err := s.conn().Query("SELECT id FROM broadcasts WHERE status = 'sending'")
	if err != nil {
		return nil, fmt.Errorf("list sending broadcasts: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list sending broadcasts: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sending broadcasts: %w", err)
	}
	return ids, nil
}

// MarkBroadcastDone marks a broadcast as done.
func (s *Store) MarkBroadcastDone(id string) error {
	_, err := s.conn().Exec("UPDATE broadcasts SET status = 'done' WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("mark broadcast done: %w", err)
	}
	return nil
}

// Initials returns up to two uppercase letters for the avatar in the sidebar
// and the submission reader. Falls back to "?" so the avatar circle is never
// empty — an empty circle reads as a rendering bug rather than a person.
func (u User) Initials() string {
	fields := strings.Fields(strings.ReplaceAll(u.Username, ".", " "))
	switch {
	case len(fields) == 0:
		return "?"
	case len(fields) == 1:
		r := []rune(fields[0])
		if len(r) == 1 {
			return strings.ToUpper(string(r[0]))
		}
		return strings.ToUpper(string(r[0:2]))
	default:
		return strings.ToUpper(string([]rune(fields[0])[0:1]) + string([]rune(fields[1])[0:1]))
	}
}

// decodeSubmissionData unmarshals a stored submission payload.
//
// A row whose JSON will not parse is surfaced under a "_raw" key rather than
// dropped: the payload is the whole value of a submission, and showing an
// operator something unreadable beats showing them nothing and no error. This
// was inlined identically at four call sites before it was extracted here.
func decodeSubmissionData(id, rawData string) map[string]string {
	var data map[string]string
	if err := json.Unmarshal([]byte(rawData), &data); err != nil {
		log.Printf("warning: failed to unmarshal submission %s data: %v", id, err)
		return map[string]string{"_raw": rawData}
	}
	return data
}
