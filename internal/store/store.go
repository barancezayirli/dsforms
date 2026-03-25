package store

import (
	"database/sql"
	"time"
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

// New opens a SQLite database and runs migrations.
func New(path string) (*Store, error) {
	return nil, nil
}

// GetUserByUsername looks up a user by username.
func (s *Store) GetUserByUsername(username string) (User, error) {
	return User{}, nil
}

// GetUserByID looks up a user by ID.
func (s *Store) GetUserByID(id string) (User, error) {
	return User{}, nil
}

// ListUsers returns all users.
func (s *Store) ListUsers() ([]User, error) {
	return nil, nil
}

// CreateUser creates a new user with a bcrypt-hashed password.
func (s *Store) CreateUser(username, password string) error {
	return nil
}

// UpdatePassword updates a user's password and clears IsDefaultPassword.
func (s *Store) UpdatePassword(userID, newPassword string) error {
	return nil
}

// DeleteUser deletes a user. Fails if it's the last remaining user.
func (s *Store) DeleteUser(id string) error {
	return nil
}

// HasDefaultPassword checks if a user still has the default password.
func (s *Store) HasDefaultPassword(userID string) (bool, error) {
	return false, nil
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
