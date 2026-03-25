package store

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func mustNew(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) failed: %v", err)
	}
	return s
}

func TestNew(t *testing.T) {
	t.Parallel()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) error = %v", err)
	}
	if s == nil {
		t.Fatal("New(:memory:) returned nil Store")
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/test.db"
	s1, err := New(path)
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	_ = s1
	s2, err := New(path)
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	_ = s2
}

func TestDefaultUserSeeded(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin) error = %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("Username = %q, want %q", u.Username, "admin")
	}
	if !u.IsDefaultPassword {
		t.Error("IsDefaultPassword = false, want true")
	}
}

func TestDefaultUserNotReseeded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/test.db"
	s1, err := New(path)
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	u, _ := s1.GetUserByUsername("admin")
	_ = s1.UpdatePassword(u.ID, "newpass")

	s2, err := New(path)
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	u2, _ := s2.GetUserByUsername("admin")
	err = bcrypt.CompareHashAndPassword([]byte(u2.passwordHash), []byte("newpass"))
	if err != nil {
		t.Error("admin password was re-seeded, expected it to remain changed")
	}
}
