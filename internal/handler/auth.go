package handler

import (
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/ratelimit"
	"github.com/youruser/dsforms/internal/store"
)

// AuthHandler handles login and logout.
type AuthHandler struct {
	Store      *store.Store
	SecretKey  string
	BaseURL    string
	LoginGuard *ratelimit.LoginGuard
	Templates  map[string]*template.Template
}

// LoginData holds data passed to the login template.
type LoginData struct {
	LoginError bool
}

// LoginPage renders the login form.
// It reads the ?error=1 query param and sets LoginError accordingly.
func (h *AuthHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	data := LoginData{
		LoginError: r.URL.Query().Get("error") == "1",
	}
	if err := h.Templates["login.html"].Execute(w, data); err != nil {
		log.Printf("login template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// LoginSubmit processes a login form submission.
// Flow:
//  1. Extract IP via ExtractIP(r)
//  2. If LoginGuard.IsLocked(ip) → 429
//  3. Parse form: username, password
//  4. Call store.CheckPassword(username, password)
//  5. On failure: guard.RecordFailure(ip), redirect to /admin/login?error=1
//  6. On success: guard.RecordSuccess(ip), create session cookie, redirect to /admin/forms
func (h *AuthHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := ExtractIP(r)

	if h.LoginGuard.IsLocked(ip) {
		http.Error(w, "Too many failed attempts. Try again in 15 minutes.", http.StatusTooManyRequests)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	user, err := h.Store.CheckPassword(username, password)
	if err != nil {
		h.LoginGuard.RecordFailure(ip)
		http.Redirect(w, r, "/admin/login?error=1", http.StatusFound)
		return
	}

	h.LoginGuard.RecordSuccess(ip)
	token, err := h.Store.CreateSession(user.ID, 30*24*time.Hour)
	if err != nil {
		log.Printf("login: failed to create session: %v", err)
		http.Redirect(w, r, "/admin/login?error=1", http.StatusFound)
		return
	}
	cookie := auth.CreateSessionCookie(token, h.BaseURL)
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/admin/forms", http.StatusFound)
}

// Logout clears the session cookie and redirects to the login page.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	token, _ := auth.GetSessionToken(r)
	if token != "" {
		h.Store.DeleteSession(token)
	}
	http.SetCookie(w, auth.ClearSessionCookie())
	http.Redirect(w, r, "/admin/login", http.StatusFound)
}
