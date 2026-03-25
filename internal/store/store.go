package store

import (
	"database/sql"
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
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=ON")
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
		log.Println("⚠  WARNING: Default admin credentials are active (admin/admin). Change your password immediately at /admin/users.")
	}

	return &Store{db: db}, nil
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
	_, err = s.db.Exec(
		"UPDATE users SET password = ?, is_default_password = 0 WHERE id = ?",
		string(hash), userID,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// DeleteUser deletes a user. Fails if it's the last remaining user.
func (s *Store) DeleteUser(id string) error {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if count <= 1 {
		return fmt.Errorf("cannot delete the last user")
	}
	_, err := s.db.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
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

// CreateForm creates a new form.
func (s *Store) CreateForm(f Form) error {
	return nil
}

// GetForm returns a form by ID.
func (s *Store) GetForm(id string) (Form, error) {
	return Form{}, nil
}

// ListForms returns all forms with unread counts.
func (s *Store) ListForms() ([]FormSummary, error) {
	return nil, nil
}

// UpdateForm updates a form's fields.
func (s *Store) UpdateForm(f Form) error {
	return nil
}

// DeleteForm deletes a form and its submissions.
func (s *Store) DeleteForm(id string) error {
	return nil
}

// CreateSubmission creates a new submission.
func (s *Store) CreateSubmission(sub Submission) error {
	return nil
}

// ListSubmissions returns all submissions for a form.
func (s *Store) ListSubmissions(formID string) ([]Submission, error) {
	return nil, nil
}

// MarkRead marks a submission as read.
func (s *Store) MarkRead(submissionID string) error {
	return nil
}

// MarkAllRead marks all submissions for a form as read.
func (s *Store) MarkAllRead(formID string) error {
	return nil
}

// DeleteSubmission deletes a submission.
func (s *Store) DeleteSubmission(id string) error {
	return nil
}

// UnreadCount returns the number of unread submissions for a form.
func (s *Store) UnreadCount(formID string) (int, error) {
	return 0, nil
}
