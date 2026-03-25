package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
	ID        string
	Name      string
	EmailTo   string
	Redirect  string
	CreatedAt time.Time
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
	RawData   string
	IP        string
	Read      bool
	CreatedAt time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id                  TEXT PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE,
    password            TEXT NOT NULL,
    is_default_password INTEGER NOT NULL DEFAULT 1,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS forms (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    email_to    TEXT NOT NULL,
    redirect    TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
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
`

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

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
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
		"INSERT INTO forms (id, name, email_to, redirect) VALUES (?, ?, ?, ?)",
		f.ID, f.Name, f.EmailTo, f.Redirect,
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
		"SELECT id, name, email_to, redirect, created_at FROM forms WHERE id = ?",
		id,
	).Scan(&f.ID, &f.Name, &f.EmailTo, &f.Redirect, &f.CreatedAt)
	if err != nil {
		return Form{}, fmt.Errorf("get form: %w", err)
	}
	return f, nil
}

// ListForms returns all forms with unread counts.
func (s *Store) ListForms() ([]FormSummary, error) {
	rows, err := s.db.Query(`
		SELECT f.id, f.name, f.email_to, f.redirect, f.created_at,
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
		if err := rows.Scan(&fs.ID, &fs.Name, &fs.EmailTo, &fs.Redirect, &fs.CreatedAt, &fs.UnreadCount); err != nil {
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
		"UPDATE forms SET name = ?, email_to = ?, redirect = ? WHERE id = ?",
		f.Name, f.EmailTo, f.Redirect, f.ID,
	)
	if err != nil {
		return fmt.Errorf("update form: %w", err)
	}
	return nil
}

// DeleteForm deletes a form and its submissions.
func (s *Store) DeleteForm(id string) error {
	_, err := s.db.Exec("DELETE FROM forms WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete form: %w", err)
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
