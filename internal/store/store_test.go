package store

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

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
	if err := bcrypt.CompareHashAndPassword([]byte(u.passwordHash), []byte("plaintext")); err != nil {
		t.Errorf("bcrypt verify failed: %v", err)
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

func TestCreateFormGetFormRoundTrip(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := Form{
		ID:      "form-1",
		Name:    "Contact",
		EmailTo: "me@example.com",
	}
	if err := s.CreateForm(f); err != nil {
		t.Fatalf("CreateForm error = %v", err)
	}
	got, err := s.GetForm("form-1")
	if err != nil {
		t.Fatalf("GetForm error = %v", err)
	}
	if got.Name != "Contact" {
		t.Errorf("Name = %q, want %q", got.Name, "Contact")
	}
	if got.EmailTo != "me@example.com" {
		t.Errorf("EmailTo = %q, want %q", got.EmailTo, "me@example.com")
	}
}

func TestListFormsWithUnreadCount(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := Form{ID: "form-1", Name: "Contact", EmailTo: "me@example.com"}
	_ = s.CreateForm(f)
	_ = s.CreateSubmission(Submission{ID: "sub-1", FormID: "form-1", RawData: `{"name":"Alice"}`})
	_ = s.CreateSubmission(Submission{ID: "sub-2", FormID: "form-1", RawData: `{"name":"Bob"}`})
	_ = s.MarkRead("sub-1")

	forms, err := s.ListForms()
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(forms) != 1 {
		t.Fatalf("len = %d, want 1", len(forms))
	}
	if forms[0].UnreadCount != 1 {
		t.Errorf("UnreadCount = %d, want 1", forms[0].UnreadCount)
	}
}

func TestUpdateForm(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := Form{ID: "form-1", Name: "Old", EmailTo: "old@example.com"}
	_ = s.CreateForm(f)
	f.Name = "New"
	f.EmailTo = "new@example.com"
	f.Redirect = "https://example.com/thanks"
	if err := s.UpdateForm(f); err != nil {
		t.Fatalf("error = %v", err)
	}
	got, _ := s.GetForm("form-1")
	if got.Name != "New" {
		t.Errorf("Name = %q, want %q", got.Name, "New")
	}
	if got.Redirect != "https://example.com/thanks" {
		t.Errorf("Redirect = %q, want %q", got.Redirect, "https://example.com/thanks")
	}
}

func TestDeleteFormCascades(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := Form{ID: "form-1", Name: "Contact", EmailTo: "me@example.com"}
	_ = s.CreateForm(f)
	_ = s.CreateSubmission(Submission{ID: "sub-1", FormID: "form-1", RawData: `{"a":"b"}`})
	if err := s.DeleteForm("form-1"); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, _ := s.ListSubmissions("form-1")
	if len(subs) != 0 {
		t.Errorf("submissions len = %d, want 0 after cascade delete", len(subs))
	}
}

func TestCreateSubmissionListSubmissions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	sub := Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice","email":"a@b.com"}`, IP: "1.2.3.4"}
	if err := s.CreateSubmission(sub); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("len = %d, want 1", len(subs))
	}
	if subs[0].Data["name"] != "Alice" {
		t.Errorf("Data[name] = %q, want %q", subs[0].Data["name"], "Alice")
	}
	if subs[0].IP != "1.2.3.4" {
		t.Errorf("IP = %q, want %q", subs[0].IP, "1.2.3.4")
	}
	if subs[0].Read {
		t.Error("Read = true, want false for new submission")
	}
}

func TestMarkRead(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	if err := s.MarkRead("s1"); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, _ := s.ListSubmissions("f1")
	if !subs[0].Read {
		t.Error("Read = false, want true after MarkRead")
	}
}

func TestMarkAllRead(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s2", FormID: "f1", RawData: `{}`})
	if err := s.MarkAllRead("f1"); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, _ := s.ListSubmissions("f1")
	for _, sub := range subs {
		if !sub.Read {
			t.Errorf("submission %s Read = false, want true", sub.ID)
		}
	}
}

func TestDeleteSubmission(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	if err := s.DeleteSubmission("s1"); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 0 {
		t.Errorf("len = %d, want 0", len(subs))
	}
}

func TestDeleteSubmissions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s2", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s3", FormID: "f1", RawData: `{}`})
	if err := s.DeleteSubmissions("f1", []string{"s1", "s3"}); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 1 || subs[0].ID != "s2" {
		t.Errorf("subs = %+v, want only s2 remaining", subs)
	}
}

func TestDeleteSubmissionsScopedToForm(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	_ = s.CreateForm(Form{ID: "f2", Name: "D", EmailTo: "m@e.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s2", FormID: "f2", RawData: `{}`})
	// Requesting s2's ID under f1's scope must not delete it.
	if err := s.DeleteSubmissions("f1", []string{"s1", "s2"}); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, _ := s.ListSubmissions("f2")
	if len(subs) != 1 {
		t.Errorf("f2 subs = %d, want 1 (cross-form delete must not happen)", len(subs))
	}
}

func TestDeleteSubmissionsEmpty(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	if err := s.DeleteSubmissions("f1", nil); err != nil {
		t.Fatalf("error = %v", err)
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 1 {
		t.Errorf("subs = %d, want 1 (empty ids must delete nothing)", len(subs))
	}
}

func TestUnreadCount(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s2", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s3", FormID: "f1", RawData: `{}`})

	count, err := s.UnreadCount("f1")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if count != 3 {
		t.Errorf("UnreadCount = %d, want 3", count)
	}

	_ = s.MarkRead("s1")
	count, _ = s.UnreadCount("f1")
	if count != 2 {
		t.Errorf("UnreadCount after read = %d, want 2", count)
	}

	_ = s.DeleteSubmission("s2")
	count, _ = s.UnreadCount("f1")
	if count != 1 {
		t.Errorf("UnreadCount after delete = %d, want 1", count)
	}
}

func TestListSubmissionsEmpty(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("len = %d, want 0", len(subs))
	}
}

func TestGetUserByUsernameNotFound(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_, err := s.GetUserByUsername("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent username, got nil")
	}
}

func TestGetUserByIDNotFound(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_, err := s.GetUserByID("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent user ID, got nil")
	}
}

func TestGetFormNotFound(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_, err := s.GetForm("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent form ID, got nil")
	}
}

func TestCreateSubmissionInvalidFormID(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	err := s.CreateSubmission(Submission{ID: "s1", FormID: "nonexistent", RawData: `{}`})
	if err == nil {
		t.Fatal("expected foreign key error for nonexistent form_id, got nil")
	}
}

func TestCountAllSubmissions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s2", FormID: "f1", RawData: `{}`})
	count, err := s.CountAllSubmissions()
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestGetSubmission(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice"}`, IP: "1.2.3.4"})
	sub, err := s.GetSubmission("s1")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if sub.Data["name"] != "Alice" {
		t.Errorf("Data[name] = %q, want Alice", sub.Data["name"])
	}
	if sub.IP != "1.2.3.4" {
		t.Errorf("IP = %q, want 1.2.3.4", sub.IP)
	}
}

func TestGetSubmissionNotFound(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_, err := s.GetSubmission("nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListSubmissionsPaged(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	for i := 1; i <= 5; i++ {
		_ = s.CreateSubmission(Submission{
			ID:      fmt.Sprintf("s%d", i),
			FormID:  "f1",
			RawData: fmt.Sprintf(`{"n":"%d"}`, i),
		})
	}

	tests := []struct {
		name   string
		limit  int
		offset int
		want   int
	}{
		{"first page of 3", 3, 0, 3},
		{"second page of 3", 3, 3, 2},
		{"page beyond end", 3, 10, 0},
		{"full page", 5, 0, 5},
		{"limit 1", 1, 0, 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			subs, err := s.ListSubmissionsPaged("f1", tt.limit, tt.offset)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(subs) != tt.want {
				t.Errorf("len = %d, want %d", len(subs), tt.want)
			}
		})
	}
}

func TestListSubmissionsPagedEmpty(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
	subs, err := s.ListSubmissionsPaged("f1", 20, 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("len = %d, want 0", len(subs))
	}
}

func TestCountSubmissions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})

	count, err := s.CountSubmissions("f1")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}

	_ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s2", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(Submission{ID: "s3", FormID: "f1", RawData: `{}`})

	count, err = s.CountSubmissions("f1")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestCheckPassword(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	// Default admin/admin should work
	u, err := s.CheckPassword("admin", "admin")
	if err != nil {
		t.Fatalf("CheckPassword error = %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("Username = %q, want admin", u.Username)
	}

	// Wrong password should fail
	_, err = s.CheckPassword("admin", "wrongpass")
	if err == nil {
		t.Fatal("expected error for wrong password, got nil")
	}

	// Wrong username should fail
	_, err = s.CheckPassword("nonexistent", "admin")
	if err == nil {
		t.Fatal("expected error for nonexistent user, got nil")
	}
}

func TestCreateSession(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	token, err := s.CreateSession(admin.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(token) != 64 {
		t.Errorf("token len = %d, want 64", len(token))
	}
}

func TestGetSessionValid(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	userID, err := s.GetSession(token)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if userID != admin.ID {
		t.Errorf("userID = %q, want %q", userID, admin.ID)
	}
}

func TestGetSessionExpired(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, -1*time.Hour)
	_, err := s.GetSession(token)
	if err == nil {
		t.Fatal("expected error for expired session")
	}
}

func TestGetSessionNotFound(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_, err := s.GetSession("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error for nonexistent token")
	}
}

func TestDeleteSession(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	if err := s.DeleteSession(token); err != nil {
		t.Fatalf("error = %v", err)
	}
	_, err := s.GetSession(token)
	if err == nil {
		t.Fatal("session still valid after delete")
	}
}

func TestDeleteUserSessions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	token1, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	token2, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	if err := s.DeleteUserSessions(admin.ID); err != nil {
		t.Fatalf("error = %v", err)
	}
	// Both original sessions should be gone
	if _, err := s.GetSession(token1); err == nil {
		t.Error("token1 still valid after DeleteUserSessions")
	}
	if _, err := s.GetSession(token2); err == nil {
		t.Error("token2 still valid after DeleteUserSessions")
	}
}

func TestCleanExpiredSessions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	admin, _ := s.GetUserByUsername("admin")
	s.CreateSession(admin.ID, -1*time.Hour) // expired
	validToken, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	if err := s.CleanExpiredSessions(); err != nil {
		t.Fatalf("error = %v", err)
	}
	if _, err := s.GetSession(validToken); err != nil {
		t.Fatalf("valid session gone after cleanup: %v", err)
	}
}

func TestCreateSessionEmptyUserID(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	_, err := s.CreateSession("", 30*24*time.Hour)
	if err == nil {
		t.Fatal("expected error for empty userID")
	}
}

func TestDB(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	if s.DB() == nil {
		t.Fatal("DB() returned nil")
	}
}

func TestReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pathA := dir + "/a.db"
	pathB := dir + "/b.db"

	sA, err := New(pathA)
	if err != nil {
		t.Fatalf("New(pathA) error: %v", err)
	}
	_ = sA.CreateForm(Form{ID: "f1", Name: "Test", EmailTo: "a@b.com"})

	// Create a second DB at path B (fresh, no forms)
	sB, err := New(pathB)
	if err != nil {
		t.Fatalf("New(pathB) error: %v", err)
	}
	sB.Close()

	// Reopen store A at path B — old data should be gone
	if err := sA.Reopen(pathB); err != nil {
		t.Fatalf("Reopen error: %v", err)
	}

	forms, _ := sA.ListForms()
	if len(forms) != 0 {
		t.Errorf("forms = %d after reopen to fresh DB, want 0", len(forms))
	}
}

func TestReopenRunsMigrations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pathA := dir + "/a.db"
	pathB := dir + "/b.db"

	sA, _ := New(pathA)

	sB, _ := New(pathB)
	sB.Close()

	if err := sA.Reopen(pathB); err != nil {
		t.Fatalf("Reopen error: %v", err)
	}

	// Should be able to create users (migrations ran)
	if err := sA.CreateUser("test", "pass"); err != nil {
		t.Errorf("CreateUser after reopen failed: %v", err)
	}
}

func TestCreateFormWithWebhook(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := Form{
		ID:            "form-wh",
		Name:          "Webhook Form",
		EmailTo:       "me@example.com",
		WebhookURL:    "https://hooks.slack.com/services/T00/B00/xxx",
		WebhookFormat: "slack",
	}
	if err := s.CreateForm(f); err != nil {
		t.Fatalf("CreateForm error = %v", err)
	}
	got, err := s.GetForm("form-wh")
	if err != nil {
		t.Fatalf("GetForm error = %v", err)
	}
	if got.WebhookURL != "https://hooks.slack.com/services/T00/B00/xxx" {
		t.Errorf("WebhookURL = %q, want webhook URL", got.WebhookURL)
	}
	if got.WebhookFormat != "slack" {
		t.Errorf("WebhookFormat = %q, want slack", got.WebhookFormat)
	}
}

func TestUpdateFormWebhookFields(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := Form{ID: "form-wh2", Name: "Test", EmailTo: "a@b.com"}
	_ = s.CreateForm(f)
	f.WebhookURL = "https://discord.com/api/webhooks/123/abc"
	f.WebhookFormat = "discord"
	if err := s.UpdateForm(f); err != nil {
		t.Fatalf("UpdateForm error = %v", err)
	}
	got, _ := s.GetForm("form-wh2")
	if got.WebhookURL != "https://discord.com/api/webhooks/123/abc" {
		t.Errorf("WebhookURL = %q, want discord URL", got.WebhookURL)
	}
	if got.WebhookFormat != "discord" {
		t.Errorf("WebhookFormat = %q, want discord", got.WebhookFormat)
	}
}

func TestGetFormDefaultWebhookEmpty(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := Form{ID: "form-no-wh", Name: "NoWH", EmailTo: "a@b.com"}
	_ = s.CreateForm(f)
	got, _ := s.GetForm("form-no-wh")
	if got.WebhookURL != "" {
		t.Errorf("WebhookURL = %q, want empty", got.WebhookURL)
	}
	if got.WebhookFormat != "" {
		t.Errorf("WebhookFormat = %q, want empty", got.WebhookFormat)
	}
}

func TestWaitlistCRUD(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	wl := Waitlist{
		ID:             "wl-1",
		Name:           "Launch List",
		Redirect:       "https://site.com/joined",
		ConfirmSubject: "You're in!",
		ConfirmBody:    "Hi {{email}}, you are #{{position}}.",
	}
	if err := s.CreateWaitlist(wl); err != nil {
		t.Fatalf("CreateWaitlist error = %v", err)
	}

	got, err := s.GetWaitlist("wl-1")
	if err != nil {
		t.Fatalf("GetWaitlist error = %v", err)
	}
	if got.Name != "Launch List" || got.Redirect != "https://site.com/joined" ||
		got.ConfirmSubject != "You're in!" || got.ConfirmBody != "Hi {{email}}, you are #{{position}}." {
		t.Errorf("GetWaitlist = %+v, want all fields round-tripped", got)
	}

	wl.Name = "Renamed"
	if err := s.UpdateWaitlist(wl); err != nil {
		t.Fatalf("UpdateWaitlist error = %v", err)
	}
	got, _ = s.GetWaitlist("wl-1")
	if got.Name != "Renamed" {
		t.Errorf("after update Name = %q, want Renamed", got.Name)
	}

	list, err := s.ListWaitlists()
	if err != nil {
		t.Fatalf("ListWaitlists error = %v", err)
	}
	if len(list) != 1 || list[0].EntryCount != 0 {
		t.Errorf("ListWaitlists = %+v, want 1 item with EntryCount 0", list)
	}

	if err := s.DeleteWaitlist("wl-1"); err != nil {
		t.Fatalf("DeleteWaitlist error = %v", err)
	}
	if _, err := s.GetWaitlist("wl-1"); err == nil {
		t.Error("GetWaitlist after delete: want error, got nil")
	}
}

func TestDeleteWaitlistNotFound(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	err := s.DeleteWaitlist("missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteWaitlist(missing) error = %v, want sql.ErrNoRows", err)
	}
}

func TestUpdateWaitlistNotFound(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	err := s.UpdateWaitlist(Waitlist{ID: "missing", Name: "X"})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("UpdateWaitlist(missing) error = %v, want sql.ErrNoRows", err)
	}
}

func seedWaitlist(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateWaitlist(Waitlist{ID: "wl", Name: "WL"}); err != nil {
		t.Fatalf("seed waitlist: %v", err)
	}
}

func TestCreateEntryDedupAndPosition(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)

	pos, already, err := s.CreateEntry(WaitlistEntry{
		ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{"name":"Al"}`, IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("CreateEntry e1 error = %v", err)
	}
	if pos != 1 || already {
		t.Errorf("first entry: pos=%d already=%v, want 1/false", pos, already)
	}

	pos2, already2, err := s.CreateEntry(WaitlistEntry{
		ID: "e2", WaitlistID: "wl", Email: "b@x.com", RawData: `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEntry e2 error = %v", err)
	}
	if pos2 != 2 || already2 {
		t.Errorf("second entry: pos=%d already=%v, want 2/false", pos2, already2)
	}

	// Duplicate email → no new row, alreadyJoined true, original position returned.
	posDup, alreadyDup, err := s.CreateEntry(WaitlistEntry{
		ID: "e3", WaitlistID: "wl", Email: "a@x.com", RawData: `{"name":"changed"}`,
	})
	if err != nil {
		t.Fatalf("CreateEntry dup error = %v", err)
	}
	if posDup != 1 || !alreadyDup {
		t.Errorf("dup entry: pos=%d already=%v, want 1/true", posDup, alreadyDup)
	}

	n, err := s.CountEntries("wl")
	if err != nil {
		t.Fatalf("CountEntries error = %v", err)
	}
	if n != 2 {
		t.Errorf("CountEntries = %d, want 2", n)
	}
}

func TestListEntriesPagedWithPosition(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	for i, email := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		if _, _, err := s.CreateEntry(WaitlistEntry{
			ID: fmt.Sprintf("e%d", i), WaitlistID: "wl", Email: email, RawData: `{}`,
		}); err != nil {
			t.Fatalf("seed entry %s: %v", email, err)
		}
	}

	entries, err := s.ListEntriesPaged("wl", 10, 0)
	if err != nil {
		t.Fatalf("ListEntriesPaged error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	// Newest first; c@x.com is position 3.
	if entries[0].Email != "c@x.com" || entries[0].Position != 3 {
		t.Errorf("entries[0] = %s pos %d, want c@x.com pos 3", entries[0].Email, entries[0].Position)
	}
	if entries[0].Data == nil {
		t.Error("Data should be a non-nil map")
	}
}

func TestDeleteEntry(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	if _, _, err := s.CreateEntry(WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{}`}); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	if err := s.DeleteEntry("wl", "e1"); err != nil {
		t.Fatalf("DeleteEntry error = %v", err)
	}
	n, _ := s.CountEntries("wl")
	if n != 0 {
		t.Errorf("CountEntries after delete = %d, want 0", n)
	}
}

func TestDeleteEntryWrongWaitlist(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	if _, _, err := s.CreateEntry(WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{}`}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Deleting under the wrong waitlist must not delete and must report ErrNoRows.
	if err := s.DeleteEntry("other", "e1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteEntry wrong waitlist err = %v, want sql.ErrNoRows", err)
	}
	if n, _ := s.CountEntries("wl"); n != 1 {
		t.Errorf("entry should remain; count = %d, want 1", n)
	}
}

func TestListEntriesAll(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	for i, email := range []string{"a@x.com", "b@x.com"} {
		if _, _, err := s.CreateEntry(WaitlistEntry{
			ID: fmt.Sprintf("le%d", i), WaitlistID: "wl", Email: email, RawData: `{}`,
		}); err != nil {
			t.Fatalf("seed entry %s: %v", email, err)
		}
	}
	entries, err := s.ListEntries("wl")
	if err != nil {
		t.Fatalf("ListEntries error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListEntries returned %d, want 2", len(entries))
	}
	// Newest first, with positions.
	if entries[0].Email != "b@x.com" || entries[0].Position != 2 {
		t.Errorf("entries[0] = %s pos %d, want b@x.com pos 2", entries[0].Email, entries[0].Position)
	}
}

func TestBroadcastAndDeliveries(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)

	b := Broadcast{ID: "b1", WaitlistID: "wl", Subject: "Launch", Body: "We are live"}
	if err := s.CreateBroadcast(b, []string{"a@x.com", "b@x.com"}); err != nil {
		t.Fatalf("CreateBroadcast error = %v", err)
	}

	got, err := s.GetBroadcast("b1")
	if err != nil {
		t.Fatalf("GetBroadcast error = %v", err)
	}
	if got.Status != "sending" || got.Subject != "Launch" {
		t.Errorf("GetBroadcast = %+v, want status sending subject Launch", got)
	}

	pending, err := s.NextPendingDeliveries(10)
	if err != nil {
		t.Fatalf("NextPendingDeliveries error = %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}

	if err := s.MarkDeliverySent(pending[0].ID); err != nil {
		t.Fatalf("MarkDeliverySent error = %v", err)
	}
	if err := s.MarkDeliveryFailed(pending[1].ID, "smtp 550", 3); err != nil {
		t.Fatalf("MarkDeliveryFailed error = %v", err)
	}

	hasPending, _ := s.HasPendingDeliveries("b1")
	if !hasPending {
		t.Error("HasPendingDeliveries = false, want true (failed-but-under-cap stays pending)")
	}

	again, _ := s.NextPendingDeliveries(10)
	_ = s.MarkDeliveryFailed(again[0].ID, "smtp 550", 3) // attempts 2
	last, _ := s.NextPendingDeliveries(10)
	_ = s.MarkDeliveryFailed(last[0].ID, "smtp 550", 3) // attempts 3 → failed
	hasPending, _ = s.HasPendingDeliveries("b1")
	if hasPending {
		t.Error("HasPendingDeliveries = true, want false after reaching attempt cap")
	}

	sum, err := s.GetBroadcastSummary("b1")
	if err != nil {
		t.Fatalf("GetBroadcastSummary error = %v", err)
	}
	if sum.Total != 2 || sum.Sent != 1 || sum.Failed != 1 || sum.Pending != 0 {
		t.Errorf("summary = %+v, want total2 sent1 failed1 pending0", sum)
	}

	if err := s.MarkBroadcastDone("b1"); err != nil {
		t.Fatalf("MarkBroadcastDone error = %v", err)
	}
	sending, _ := s.ListSendingBroadcasts()
	if len(sending) != 0 {
		t.Errorf("ListSendingBroadcasts = %v, want empty", sending)
	}
}

func TestListBroadcasts(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	if err := s.CreateBroadcast(Broadcast{ID: "b1", WaitlistID: "wl", Subject: "S", Body: "B"}, []string{"a@x.com"}); err != nil {
		t.Fatalf("CreateBroadcast error = %v", err)
	}
	list, err := s.ListBroadcasts("wl")
	if err != nil {
		t.Fatalf("ListBroadcasts error = %v", err)
	}
	if len(list) != 1 || list[0].Subject != "S" || list[0].Total != 1 || list[0].Pending != 1 {
		t.Errorf("ListBroadcasts = %+v, want one summary total1 pending1", list)
	}
}

func TestCreateEntryRejectsEmptyEmail(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	_, _, err := s.CreateEntry(WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "", RawData: `{}`})
	if err == nil {
		t.Error("CreateEntry with empty email: want error, got nil")
	}
}

func TestListWaitlistsCountsEntries(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	for i, e := range []string{"a@x.com", "b@x.com"} {
		if _, _, err := s.CreateEntry(WaitlistEntry{ID: fmt.Sprintf("e%d", i), WaitlistID: "wl", Email: e, RawData: `{}`}); err != nil {
			t.Fatalf("seed %s: %v", e, err)
		}
	}
	list, err := s.ListWaitlists()
	if err != nil {
		t.Fatalf("ListWaitlists: %v", err)
	}
	if len(list) != 1 || list[0].EntryCount != 2 {
		t.Errorf("EntryCount = %+v, want 2", list)
	}
}

func TestDeleteWaitlistCascades(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedWaitlist(t, s)
	if _, _, err := s.CreateEntry(WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{}`}); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	if err := s.CreateBroadcast(Broadcast{ID: "b1", WaitlistID: "wl", Subject: "S", Body: "B"}, []string{"a@x.com"}); err != nil {
		t.Fatalf("seed broadcast: %v", err)
	}
	if err := s.DeleteWaitlist("wl"); err != nil {
		t.Fatalf("DeleteWaitlist: %v", err)
	}
	if n, _ := s.CountEntries("wl"); n != 0 {
		t.Errorf("entries after cascade = %d, want 0", n)
	}
	if list, _ := s.ListBroadcasts("wl"); len(list) != 0 {
		t.Errorf("broadcasts after cascade = %d, want 0", len(list))
	}
}

func TestBroadcastIsSending(t *testing.T) {
	t.Parallel()
	if !(Broadcast{Status: BroadcastStatusSending}).IsSending() {
		t.Error("IsSending should be true for sending status")
	}
	if (Broadcast{Status: BroadcastStatusDone}).IsSending() {
		t.Error("IsSending should be false for done status")
	}
}
