package handler

import (
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

func setupUsers(t *testing.T) (*store.Store, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}

	// Inline test templates
	funcMap := TemplateFuncs()

	templates := make(map[string]*template.Template)

	// users.html
	baseTmpl := template.Must(template.New("base").Funcs(funcMap).Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	usersTmpl, _ := baseTmpl.Clone()
	template.Must(usersTmpl.New("content").Parse(
		`{{range .Users}}<span class="user">{{.Username}}</span>{{if .IsYou}}<span class="you">(you)</span>{{end}}{{end}}` +
			`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}`))
	templates["users.html"] = usersTmpl

	// users_new.html
	newTmpl, _ := baseTmpl.Clone()
	template.Must(newTmpl.New("content").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}<form>new user</form>`))
	templates["users_new.html"] = newTmpl

	// account.html
	acctTmpl, _ := baseTmpl.Clone()
	template.Must(acctTmpl.New("content").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}` +
			`<span class="username">{{.CurrentUser.Username}}</span>` +
			`{{if .CurrentUser.IsDefaultPassword}}<span class="default-pw">default</span>{{end}}`))
	templates["account.html"] = acctTmpl

	// For warning banner tests, we need a template that renders the base with the banner
	// The real base.html checks .CurrentUser.IsDefaultPassword
	// For tests, create a special dashboard-like template that shows the banner
	dashTmpl, _ := baseTmpl.Clone()
	template.Must(dashTmpl.New("content").Parse(
		`{{if .CurrentUser.IsDefaultPassword}}<div class="warn-banner">Default password</div>{{end}}<p>dashboard</p>`))
	templates["dashboard.html"] = dashTmpl

	uh := &UsersHandler{
		Store: s,
		Base: Base{
			Nav:       s,
			SecretKey: "test-secret-key-32-chars-long!!",
			BaseURL:   "https://example.com",
			Templates: templates,
		},
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/users", uh.ListUsers)
		r.Get("/admin/users/new", uh.NewUserPage)
		r.Post("/admin/users/new", uh.CreateUser)
		r.Post("/admin/users/{id}/delete", uh.DeleteUser)
		r.Get("/admin/account", uh.AccountPage)
		r.Post("/admin/account/password", uh.UpdatePassword)
	})

	return s, r
}

func doUserRequest(t *testing.T, s *store.Store, r *chi.Mux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	cookie := auth.CreateSessionCookie(token, "https://example.com")
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestListUsersReturns200(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	w := doUserRequest(t, s, r, "GET", "/admin/users", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "admin") {
		t.Error("admin not listed")
	}
}

func TestListUsersCurrentUserTag(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	w := doUserRequest(t, s, r, "GET", "/admin/users", "")
	if !strings.Contains(w.Body.String(), "(you)") {
		t.Error("(you) tag not shown")
	}
}

func TestNewUserPage(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	w := doUserRequest(t, s, r, "GET", "/admin/users/new", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestCreateUserValid(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	form := url.Values{"username": {"alice"}, "password": {"longenoughsecret"}, "confirm_password": {"longenoughsecret"}}
	w := doUserRequest(t, s, r, "POST", "/admin/users/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	users, _ := s.ListUsers()
	if len(users) != 2 {
		t.Errorf("users = %d, want 2", len(users))
	}
}

func TestCreateUserDuplicate(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Long enough to reach the duplicate check. With a 4-character password the
	// length guard fired first, so this test asserted "an error appeared" while
	// never exercising the duplicate path it is named for.
	pw := strings.Repeat("a", store.MinPasswordLength)
	form := url.Values{"username": {"admin"}, "password": {pw}, "confirm_password": {pw}}
	w := doUserRequest(t, s, r, "POST", "/admin/users/new", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render)", w.Code)
	}
	// The specific sentence, not merely "an error". The stub template renders
	// class="error" for every failure, so matching that proved only that
	// something went wrong — a mismatched-password error would have satisfied it
	// just as well as the duplicate this test is named for.
	if body := w.Body.String(); !strings.Contains(body, "Username already exists") {
		t.Errorf("page did not report a duplicate username; got:\n%s", body)
	}
}

func TestCreateUserMismatchedPasswords(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Both long enough to clear the length guard. With 5-character values this
	// passed only because the mismatch check happens to sit above the length
	// check; reorder them and it would have gone green while testing the wrong
	// branch — the defect that made two other tests here hollow.
	pw := strings.Repeat("a", store.MinPasswordLength)
	form := url.Values{"username": {"alice"}, "password": {pw + "1"}, "confirm_password": {pw + "2"}}
	w := doUserRequest(t, s, r, "POST", "/admin/users/new", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "Passwords do not match") {
		t.Errorf("page did not report mismatched passwords; got:\n%s", body)
	}
}

func TestCreateUserEmptyUsername(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Valid length, so the empty-username branch is what is being exercised.
	pw := strings.Repeat("a", store.MinPasswordLength)
	form := url.Values{"username": {""}, "password": {pw}, "confirm_password": {pw}}
	w := doUserRequest(t, s, r, "POST", "/admin/users/new", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestDeleteUser(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	_ = s.CreateUser("alice", "longenoughpassphrase")
	alice, _ := s.GetUserByUsername("alice")
	w := doUserRequest(t, s, r, "POST", "/admin/users/"+alice.ID+"/delete", "")
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	_, err := s.GetUserByUsername("alice")
	if err == nil {
		t.Error("alice still exists")
	}
}

func TestDeleteUserSelf(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	admin, _ := s.GetUserByUsername("admin")
	w := doUserRequest(t, s, r, "POST", "/admin/users/"+admin.ID+"/delete", "")
	// Should redirect with error, user still exists
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	_, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Error("admin was deleted (should not be)")
	}
}

func TestDeleteUserLast(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Only admin exists — cannot delete
	admin, _ := s.GetUserByUsername("admin")
	// Create a second user, login as them, then try to delete admin (the last-user check is in store)
	// Actually: admin IS the only user, trying to delete admin hits both self-check and last-user check
	// Let's create alice, then delete admin (not self, but last-user check still applies to store)
	_ = s.CreateUser("alice", "longenoughpassphrase")
	// Delete alice first
	alice, _ := s.GetUserByUsername("alice")
	doUserRequest(t, s, r, "POST", "/admin/users/"+alice.ID+"/delete", "")
	// Now try to delete admin (only user left) — self-check catches it first
	// This test is tricky. Let's just verify the store-level guard works
	err := s.DeleteUser(admin.ID)
	if err == nil {
		t.Error("should not be able to delete last user")
	}
}

func TestDeleteUserNotFound(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	w := doUserRequest(t, s, r, "POST", "/admin/users/nonexistent/delete", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestAccountPage(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	w := doUserRequest(t, s, r, "GET", "/admin/account", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "admin") {
		t.Error("username not shown")
	}
}

func TestUpdatePasswordValid(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	form := url.Values{"current_password": {"admin"}, "new_password": {"newpassphrase"}, "confirm_password": {"newpassphrase"}}
	w := doUserRequest(t, s, r, "POST", "/admin/account/password", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	// Verify new password works
	_, err := s.CheckPassword("admin", "newpassphrase")
	if err != nil {
		t.Errorf("new password doesn't work: %v", err)
	}
}

func TestUpdatePasswordWrongCurrent(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	form := url.Values{"current_password": {"wrong"}, "new_password": {"newpassphrase"}, "confirm_password": {"newpassphrase"}}
	w := doUserRequest(t, s, r, "POST", "/admin/account/password", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "error") {
		t.Error("error not shown")
	}
}

func TestUpdatePasswordMismatch(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Long enough that the length guard is not what rejects these.
	pw := strings.Repeat("a", store.MinPasswordLength)
	form := url.Values{"current_password": {"admin"}, "new_password": {pw + "1"}, "confirm_password": {pw + "2"}}
	w := doUserRequest(t, s, r, "POST", "/admin/account/password", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "do not match") {
		t.Errorf("page did not report mismatched passwords; got:\n%s", body)
	}
}

func TestUpdatePasswordClearsDefault(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Admin has IsDefaultPassword=true initially
	admin, _ := s.GetUserByUsername("admin")
	has, _ := s.HasDefaultPassword(admin.ID)
	if !has {
		t.Fatal("admin should have default password initially")
	}
	form := url.Values{"current_password": {"admin"}, "new_password": {"newpassphrase"}, "confirm_password": {"newpassphrase"}}
	doUserRequest(t, s, r, "POST", "/admin/account/password", form.Encode())
	has, _ = s.HasDefaultPassword(admin.ID)
	if has {
		t.Error("IsDefaultPassword still true after update")
	}
}

func TestUpdatePasswordInvalidatesSessions(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	admin, _ := s.GetUserByUsername("admin")
	// Create a session (simulating another device)
	otherToken, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	// Change password
	form := url.Values{"current_password": {"admin"}, "new_password": {"newpassphrase"}, "confirm_password": {"newpassphrase"}}
	doUserRequest(t, s, r, "POST", "/admin/account/password", form.Encode())
	// Other session should be invalid
	_, err := s.GetSession(otherToken)
	if err == nil {
		t.Error("other session still valid after password change")
	}
}

func TestWarnBannerVisibleWhenDefault(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Admin has default password — banner should show
	w := doUserRequest(t, s, r, "GET", "/admin/account", "")
	if !strings.Contains(w.Body.String(), "default") {
		t.Error("warning banner not visible with default password")
	}
}

func TestWarnBannerAbsentAfterUpdate(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	// Change password first
	form := url.Values{"current_password": {"admin"}, "new_password": {"newpassphrase"}, "confirm_password": {"newpassphrase"}}
	doUserRequest(t, s, r, "POST", "/admin/account/password", form.Encode())
	// Now re-login with new password and check account page
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	cookie := auth.CreateSessionCookie(token, "https://example.com")
	req := httptest.NewRequest("GET", "/admin/account", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	body := w.Body.String()
	if strings.Contains(body, "default-pw") {
		t.Error("warning banner still visible after password change")
	}
}

// TestCreateUserRejectsShortPassword and TestUpdatePasswordRejectsShortPassword
// cover the two handler-side length checks, which had no test at all.
//
// The store enforces the minimum regardless, so it is tempting to read these
// checks as belt-and-braces. They are not. If the handler check goes, the store
// returns ErrPasswordTooShort, which is not a UNIQUE-constraint error, so
// CreateUser's error branch falls through to http.Error(500) — a short password
// becomes "internal error" instead of a sentence telling the operator what to
// do. The account path has the same shape. What these pin is the message, which
// is the only reason the duplicated check exists.
func TestCreateUserRejectsShortPassword(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	short := strings.Repeat("a", store.MinPasswordLength-1)
	form := url.Values{"username": {"alice"}, "password": {short}, "confirm_password": {short}}
	w := doUserRequest(t, s, r, "POST", "/admin/users/new", form.Encode())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render with the reason, not a 500)", w.Code)
	}
	want := fmt.Sprintf("at least %d characters", store.MinPasswordLength)
	if body := w.Body.String(); !strings.Contains(body, want) {
		t.Errorf("page did not state the minimum; want %q, got:\n%s", want, body)
	}
	if users, _ := s.ListUsers(); len(users) != 1 {
		t.Errorf("users = %d, want 1 — the account must not have been created", len(users))
	}
}

func TestUpdatePasswordRejectsShortPassword(t *testing.T) {
	t.Parallel()
	s, r := setupUsers(t)
	short := strings.Repeat("a", store.MinPasswordLength-1)
	form := url.Values{"current_password": {"admin"}, "new_password": {short}, "confirm_password": {short}}
	w := doUserRequest(t, s, r, "POST", "/admin/account/password", form.Encode())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render with the reason, not a 500)", w.Code)
	}
	want := fmt.Sprintf("at least %d characters", store.MinPasswordLength)
	if body := w.Body.String(); !strings.Contains(body, want) {
		t.Errorf("page did not state the minimum; want %q, got:\n%s", want, body)
	}
	// The old password must still work: a rejected change that quietly took
	// effect would be worse than the 500 this guards against.
	if _, err := s.CheckPassword("admin", "admin"); err != nil {
		t.Errorf("original password stopped working after a rejected change: %v", err)
	}
}
