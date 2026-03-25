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

func TestCreateUserBcryptsPassword(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	err := s.CreateUser("alice", "plaintext")
	if err != nil {
		t.Fatalf("CreateUser error = %v", err)
	}
	u, _ := s.GetUserByUsername("alice")
	if u.passwordHash == "plaintext" {
		t.Error("password stored as plain text, expected bcrypt hash")
	}
}

func TestGetUserByUsername(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("Username = %q, want %q", u.Username, "admin")
	}
	if u.ID == "" {
		t.Error("ID is empty")
	}
}

func TestGetUserByID(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	u, err := s.GetUserByID(admin.ID)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("Username = %q, want %q", u.Username, "admin")
	}
}

func TestListUsers(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateUser("alice", "pass")
	users, err := s.ListUsers()
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(users) != 2 {
		t.Errorf("len = %d, want 2", len(users))
	}
}

func TestUpdatePassword(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	err := s.UpdatePassword(admin.ID, "newpass")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	updated, _ := s.GetUserByUsername("admin")
	if updated.IsDefaultPassword {
		t.Error("IsDefaultPassword = true, want false after update")
	}
}

func TestDeleteUserNonLast(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateUser("alice", "pass")
	alice, _ := s.GetUserByUsername("alice")
	err := s.DeleteUser(alice.ID)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	_, err = s.GetUserByUsername("alice")
	if err == nil {
		t.Error("expected error for deleted user, got nil")
	}
}

func TestDeleteUserLastFails(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	err := s.DeleteUser(admin.ID)
	if err == nil {
		t.Fatal("expected error deleting last user, got nil")
	}
}

func TestHasDefaultPassword(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")

	has, err := s.HasDefaultPassword(admin.ID)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !has {
		t.Error("HasDefaultPassword = false, want true on fresh DB")
	}

	_ = s.UpdatePassword(admin.ID, "newpass")
	has, _ = s.HasDefaultPassword(admin.ID)
	if has {
		t.Error("HasDefaultPassword = true, want false after password update")
	}
}

func TestCreateUserDuplicate(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	err := s.CreateUser("admin", "pass")
	if err == nil {
		t.Fatal("expected error creating duplicate username, got nil")
	}
}
