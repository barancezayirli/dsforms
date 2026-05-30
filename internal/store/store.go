package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"golang.org/x/crypto/bcrypt"
)

// Store wraps the SQLite database connection.
type Store struct {
	db *sql.DB
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
    created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS submissions (
    id          TEXT PRIMARY KEY,
    form_id     TEXT NOT NULL REFERENCES forms(id) ON DELETE CASCADE,
    data        TEXT NOT NULL,
    ip          TEXT NOT NULL DEFAULT '',
    read        INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_submissions_form_id ON submissions(form_id);
CREATE INDEX IF NOT EXISTS idx_submissions_read ON submissions(read);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

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
func runAlterMigrations(db *sql.DB) error {
	alters := []string{
		"ALTER TABLE forms ADD COLUMN webhook_url TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE forms ADD COLUMN webhook_format TEXT NOT NULL DEFAULT ''",
	}
	for _, q := range alters {
		_, err := db.Exec(q)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("alter migration: %w", err)
		}
	}
	return nil
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

	if err := runMigrations(db); err != nil {
		return nil, err
	}
	if err := runAlterMigrations(db); err != nil {
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
	return s.db.Close()
}

// DB returns the underlying *sql.DB for backup operations.
func (s *Store) DB() *sql.DB {
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
	err := s.db.QueryRow(
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
	err := s.db.QueryRow(
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
	rows, err := s.db.Query(
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

// CreateUser creates a new user with a bcrypt-hashed password.
func (s *Store) CreateUser(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	id := uuid.New().String()
	_, err = s.db.Exec(
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
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	result, err := s.db.Exec(
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
	tx, err := s.db.Begin()
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
	err := s.db.QueryRow(
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
	_, err := s.db.Exec(
		"INSERT INTO forms (id, name, email_to, redirect, webhook_url, webhook_format) VALUES (?, ?, ?, ?, ?, ?)",
		f.ID, f.Name, f.EmailTo, f.Redirect, f.WebhookURL, f.WebhookFormat,
	)
	if err != nil {
		return fmt.Errorf("create form: %w", err)
	}
	return nil
}

// GetForm returns a form by ID.
func (s *Store) GetForm(id string) (Form, error) {
	var f Form
	err := s.db.QueryRow(
		"SELECT id, name, email_to, redirect, webhook_url, webhook_format, created_at FROM forms WHERE id = ?",
		id,
	).Scan(&f.ID, &f.Name, &f.EmailTo, &f.Redirect, &f.WebhookURL, &f.WebhookFormat, &f.CreatedAt)
	if err != nil {
		return Form{}, fmt.Errorf("get form: %w", err)
	}
	return f, nil
}

// ListForms returns all forms with unread counts.
func (s *Store) ListForms() ([]FormSummary, error) {
	rows, err := s.db.Query(`
		SELECT f.id, f.name, f.email_to, f.redirect, f.webhook_url, f.webhook_format, f.created_at,
		       COUNT(CASE WHEN s.read = 0 THEN 1 END) as unread_count
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
		if err := rows.Scan(&fs.ID, &fs.Name, &fs.EmailTo, &fs.Redirect, &fs.WebhookURL, &fs.WebhookFormat, &fs.CreatedAt, &fs.UnreadCount); err != nil {
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
	_, err := s.db.Exec(
		"UPDATE forms SET name = ?, email_to = ?, redirect = ?, webhook_url = ?, webhook_format = ? WHERE id = ?",
		f.Name, f.EmailTo, f.Redirect, f.WebhookURL, f.WebhookFormat, f.ID,
	)
	if err != nil {
		return fmt.Errorf("update form: %w", err)
	}
	return nil
}

// DeleteForm deletes a form and its submissions.
// Returns sql.ErrNoRows if no form with the given ID exists.
func (s *Store) DeleteForm(id string) error {
	result, err := s.db.Exec("DELETE FROM forms WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete form: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delete form: %w", sql.ErrNoRows)
	}
	return nil
}

// CreateSubmission creates a new submission.
func (s *Store) CreateSubmission(sub Submission) error {
	_, err := s.db.Exec(
		"INSERT INTO submissions (id, form_id, data, ip) VALUES (?, ?, ?, ?)",
		sub.ID, sub.FormID, sub.RawData, sub.IP,
	)
	if err != nil {
		return fmt.Errorf("create submission: %w", err)
	}
	return nil
}

// ListSubmissions returns all submissions for a form.
func (s *Store) ListSubmissions(formID string) ([]Submission, error) {
	rows, err := s.db.Query(
		"SELECT id, form_id, data, ip, read, created_at FROM submissions WHERE form_id = ? ORDER BY created_at DESC",
		formID,
	)
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}
	defer rows.Close()

	var subs []Submission
	for rows.Next() {
		var sub Submission
		var rawData string
		var readInt int
		if err := rows.Scan(&sub.ID, &sub.FormID, &rawData, &sub.IP, &readInt, &sub.CreatedAt); err != nil {
			return nil, fmt.Errorf("list submissions: %w", err)
		}
		sub.RawData = rawData
		sub.Read = readInt == 1
		if err := json.Unmarshal([]byte(rawData), &sub.Data); err != nil {
			log.Printf("warning: failed to unmarshal submission %s data: %v", sub.ID, err)
			sub.Data = map[string]string{"_raw": rawData}
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}
	return subs, nil
}

// MarkRead marks a submission as read.
func (s *Store) MarkRead(submissionID string) error {
	_, err := s.db.Exec("UPDATE submissions SET read = 1 WHERE id = ?", submissionID)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// CountAllSubmissions returns the total count of all submissions across all forms.
func (s *Store) CountAllSubmissions() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM submissions").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count all submissions: %w", err)
	}
	return count, nil
}

// MarkAllRead marks all submissions for a form as read.
func (s *Store) MarkAllRead(formID string) error {
	_, err := s.db.Exec("UPDATE submissions SET read = 1 WHERE form_id = ?", formID)
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}

// DeleteSubmission deletes a submission.
func (s *Store) DeleteSubmission(id string) error {
	_, err := s.db.Exec("DELETE FROM submissions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete submission: %w", err)
	}
	return nil
}

// GetSubmission returns a single submission by ID.
func (s *Store) GetSubmission(id string) (Submission, error) {
	var sub Submission
	var rawData string
	var readInt int
	err := s.db.QueryRow(
		"SELECT id, form_id, data, ip, read, created_at FROM submissions WHERE id = ?", id,
	).Scan(&sub.ID, &sub.FormID, &rawData, &sub.IP, &readInt, &sub.CreatedAt)
	if err != nil {
		return Submission{}, fmt.Errorf("get submission: %w", err)
	}
	sub.RawData = rawData
	sub.Read = readInt == 1
	if err := json.Unmarshal([]byte(rawData), &sub.Data); err != nil {
		log.Printf("warning: failed to unmarshal submission %s data: %v", sub.ID, err)
		sub.Data = map[string]string{"_raw": rawData}
	}
	return sub, nil
}

// ListSubmissionsPaged returns a page of submissions for a form.
func (s *Store) ListSubmissionsPaged(formID string, limit, offset int) ([]Submission, error) {
	rows, err := s.db.Query(
		"SELECT id, form_id, data, ip, read, created_at FROM submissions WHERE form_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?",
		formID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list submissions paged: %w", err)
	}
	defer rows.Close()

	var subs []Submission
	for rows.Next() {
		var sub Submission
		var rawData string
		var readInt int
		if err := rows.Scan(&sub.ID, &sub.FormID, &rawData, &sub.IP, &readInt, &sub.CreatedAt); err != nil {
			return nil, fmt.Errorf("list submissions paged: %w", err)
		}
		sub.RawData = rawData
		sub.Read = readInt == 1
		if err := json.Unmarshal([]byte(rawData), &sub.Data); err != nil {
			log.Printf("warning: failed to unmarshal submission %s data: %v", sub.ID, err)
			sub.Data = map[string]string{"_raw": rawData}
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list submissions paged: %w", err)
	}
	return subs, nil
}

// CountSubmissions returns the total number of submissions for a form.
func (s *Store) CountSubmissions(formID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM submissions WHERE form_id = ?", formID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count submissions: %w", err)
	}
	return count, nil
}

// UnreadCount returns the number of unread submissions for a form.
func (s *Store) UnreadCount(formID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM submissions WHERE form_id = ? AND read = 0",
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
	expiresAt := time.Now().Add(expiry)
	_, err := s.db.Exec(
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
	err := s.db.QueryRow(
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
	_, err := s.db.Exec("DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions removes all sessions for the given user.
func (s *Store) DeleteUserSessions(userID string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// CleanExpiredSessions removes all expired sessions from the database.
func (s *Store) CleanExpiredSessions() error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
	if err != nil {
		return fmt.Errorf("clean expired sessions: %w", err)
	}
	return nil
}

// CreateWaitlist creates a new waitlist.
func (s *Store) CreateWaitlist(wl Waitlist) error {
	_, err := s.db.Exec(
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
	err := s.db.QueryRow(
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
	rows, err := s.db.Query(`
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
// Returns sql.ErrNoRows if no waitlist with the given ID exists.
func (s *Store) UpdateWaitlist(wl Waitlist) error {
	result, err := s.db.Exec(
		"UPDATE waitlists SET name = ?, redirect = ?, confirm_subject = ?, confirm_body = ? WHERE id = ?",
		wl.Name, wl.Redirect, wl.ConfirmSubject, wl.ConfirmBody, wl.ID,
	)
	if err != nil {
		return fmt.Errorf("update waitlist: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("update waitlist: %w", sql.ErrNoRows)
	}
	return nil
}

// DeleteWaitlist deletes a waitlist and its entries/broadcasts (cascade).
// Returns sql.ErrNoRows if no waitlist with the given ID exists.
func (s *Store) DeleteWaitlist(id string) error {
	result, err := s.db.Exec("DELETE FROM waitlists WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete waitlist: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delete waitlist: %w", sql.ErrNoRows)
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
	res, err := s.db.Exec(
		`INSERT INTO waitlist_entries (id, waitlist_id, email, data, ip)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(waitlist_id, email) DO NOTHING`,
		e.ID, e.WaitlistID, e.Email, rawData, e.IP,
	)
	if err != nil {
		return 0, false, fmt.Errorf("create entry: %w", err)
	}
	n, _ := res.RowsAffected()
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
	err := s.db.QueryRow(`
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
	err := s.db.QueryRow("SELECT COUNT(*) FROM waitlist_entries WHERE waitlist_id = ?", waitlistID).Scan(&n)
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
	rows, err := s.db.Query(entrySelectWithPosition+" LIMIT ? OFFSET ?", waitlistID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list entries paged: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ListEntries returns all entries for a waitlist (newest first) with positions.
func (s *Store) ListEntries(waitlistID string) ([]WaitlistEntry, error) {
	rows, err := s.db.Query(entrySelectWithPosition, waitlistID)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// DeleteEntry deletes a single waitlist entry by ID.
func (s *Store) DeleteEntry(id string) error {
	_, err := s.db.Exec("DELETE FROM waitlist_entries WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	return nil
}

// CreateBroadcast inserts a broadcast (always created with status 'sending';
// the b.Status field is ignored) plus one pending delivery per email, atomically.
func (s *Store) CreateBroadcast(b Broadcast, emails []string) error {
	tx, err := s.db.Begin()
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
	err := s.db.QueryRow(
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
	err = s.db.QueryRow(`
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
	rows, err := s.db.Query(`
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
	rows, err := s.db.Query(
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
	_, err := s.db.Exec(
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
	_, err := s.db.Exec(`
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
	err := s.db.QueryRow(
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
	rows, err := s.db.Query("SELECT id FROM broadcasts WHERE status = 'sending'")
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
	_, err := s.db.Exec("UPDATE broadcasts SET status = 'done' WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("mark broadcast done: %w", err)
	}
	return nil
}
