package store

import (
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
