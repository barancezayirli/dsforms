package auth

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/youruser/dsforms/internal/store"
)

const CookieName = "dsforms_session"

type contextKey string

const userContextKey contextKey = "user"

// SessionStore is the interface the auth package needs from the store.
type SessionStore interface {
	GetSession(token string) (userID string, err error)
	GetUserByID(id string) (store.User, error)
}

// CreateSessionCookie wraps a session token in an HTTP cookie.
func CreateSessionCookie(token, baseURL string) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(baseURL, "https"),
	}
}

// GetSessionToken reads the session token from the request cookie.
func GetSessionToken(r *http.Request) (string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// ClearSessionCookie returns a cookie that instructs the browser to delete the session.
func ClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	}
}

// RequireAuth validates the session token via DB lookup.
func RequireAuth(ss SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := GetSessionToken(r)
			if !ok {
				http.Redirect(w, r, "/admin/login", http.StatusFound)
				return
			}
			userID, err := ss.GetSession(token)
			if err != nil {
				http.SetCookie(w, ClearSessionCookie())
				http.Redirect(w, r, "/admin/login", http.StatusFound)
				return
			}
			user, err := ss.GetUserByID(userID)
			if err != nil {
				log.Printf("auth: failed to load user %s: %v", userID, err)
				http.SetCookie(w, ClearSessionCookie())
				http.Redirect(w, r, "/admin/login", http.StatusFound)
				return
			}
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userContextKey).(store.User)
	return u, ok
}
