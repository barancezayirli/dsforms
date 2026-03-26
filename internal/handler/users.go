package handler

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/flash"
	"github.com/youruser/dsforms/internal/store"
)

// UsersHandler handles user management pages.
type UsersHandler struct {
	Store     *store.Store
	SecretKey string
	BaseURL   string
	Templates map[string]*template.Template
}

// UserWithYou embeds store.User and adds an IsYou flag for list display.
type UserWithYou struct {
	store.User
	IsYou bool
}

type usersListData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Users       []UserWithYou
	Error       string
}

type usersNewData struct {
	Title        string
	Active       string
	CurrentUser  store.User
	Flash        *FlashData
	Error        string
	FormUsername string
}

type accountData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Error       string
}

// ListUsers renders the user list page.
func (h *UsersHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	currentUser, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	users, err := h.Store.ListUsers()
	if err != nil {
		log.Printf("list users error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	usersWithYou := make([]UserWithYou, len(users))
	for i, u := range users {
		usersWithYou[i] = UserWithYou{
			User:  u,
			IsYou: u.ID == currentUser.ID,
		}
	}

	data := usersListData{
		Title:       "Users",
		Active:      "users",
		CurrentUser: currentUser,
		Flash:       newFlash(flashType, flashMsg),
		Users:       usersWithYou,
	}

	if err := h.Templates["users.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("users template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// NewUserPage renders the new user form page.
func (h *UsersHandler) NewUserPage(w http.ResponseWriter, r *http.Request) {
	currentUser, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	data := usersNewData{
		Title:       "New User",
		Active:      "users",
		CurrentUser: currentUser,
		Flash:       newFlash(flashType, flashMsg),
	}

	if err := h.Templates["users_new.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("users_new template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// CreateUser handles POST to create a new user.
func (h *UsersHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	currentUser, _ := auth.UserFromContext(r.Context())

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	confirmPassword := r.FormValue("confirm_password")

	renderError := func(errMsg string) {
		data := usersNewData{
			Title:        "New User",
			Active:       "users",
			CurrentUser:  currentUser,
			Error:        errMsg,
			FormUsername: username,
		}
		if err := h.Templates["users_new.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("users_new template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}

	if username == "" {
		renderError("Username is required.")
		return
	}

	if password != confirmPassword {
		renderError("Passwords do not match.")
		return
	}

	if err := h.Store.CreateUser(username, password); err != nil {
		// Detect UNIQUE constraint violation as a duplicate username
		if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "duplicate") {
			renderError("Username already exists.")
			return
		}
		log.Printf("create user error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// DeleteUser handles POST to delete a user by ID.
func (h *UsersHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	currentUser, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	// Check the user exists first
	if _, err := h.Store.GetUserByID(id); err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Prevent self-deletion
	if id == currentUser.ID {
		flash.Set(w, h.SecretKey, "error", "You cannot delete your own account.")
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}

	if err := h.Store.DeleteUser(id); err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "cannot delete the last user") {
			flash.Set(w, h.SecretKey, "error", "Cannot delete the last user.")
			http.Redirect(w, r, "/admin/users", http.StatusFound)
			return
		}
		log.Printf("delete user %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	flash.Set(w, h.SecretKey, "success", "User deleted.")
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// AccountPage renders the current user's account/password page.
func (h *UsersHandler) AccountPage(w http.ResponseWriter, r *http.Request) {
	currentUser, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	data := accountData{
		Title:       "Account",
		Active:      "account",
		CurrentUser: currentUser,
		Flash:       newFlash(flashType, flashMsg),
	}

	if err := h.Templates["account.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("account template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// UpdatePassword handles POST to change the current user's password.
func (h *UsersHandler) UpdatePassword(w http.ResponseWriter, r *http.Request) {
	currentUser, _ := auth.UserFromContext(r.Context())

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	renderError := func(errMsg string) {
		data := accountData{
			Title:       "Account",
			Active:      "account",
			CurrentUser: currentUser,
			Error:       errMsg,
		}
		if err := h.Templates["account.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("account template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}

	if newPassword != confirmPassword {
		renderError("Passwords do not match.")
		return
	}

	if _, err := h.Store.CheckPassword(currentUser.Username, currentPassword); err != nil {
		renderError("Current password is incorrect.")
		return
	}

	if err := h.Store.UpdatePassword(currentUser.ID, newPassword); err != nil {
		log.Printf("update password for user %s error: %v", currentUser.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Invalidate all existing sessions
	if err := h.Store.DeleteUserSessions(currentUser.ID); err != nil {
		log.Printf("delete user sessions for %s error: %v", currentUser.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Create a new session for this browser
	newToken, err := h.Store.CreateSession(currentUser.ID, 30*24*time.Hour)
	if err != nil {
		log.Printf("create session after password change for %s error: %v", currentUser.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	cookie := auth.CreateSessionCookie(newToken, h.BaseURL)
	http.SetCookie(w, cookie)

	flash.Set(w, h.SecretKey, "success", "Password updated.")
	http.Redirect(w, r, "/admin/account", http.StatusFound)
}
