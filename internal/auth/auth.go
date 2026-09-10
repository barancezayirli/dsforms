package auth

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/barancezayirli/dsforms/internal/store"
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
			// A fragment request gets 401, not a redirect to the login page.
			//
			// fetch follows redirects transparently, and /admin/login answers
			// 200 with a full HTML document — so an expired session made r.ok
			// true and the login page was injected into the reader drawer,
			// where none of the close controls exist. 401 makes the failure a
			// failure, so the client can navigate deliberately.
			unauthorized := func() {
				if r.Header.Get("X-Fragment") != "" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, "/admin/login", http.StatusFound)
			}

			token, ok := GetSessionToken(r)
			if !ok {
				unauthorized()
				return
			}
			userID, err := ss.GetSession(token)
			if err != nil {
				http.SetCookie(w, ClearSessionCookie())
				unauthorized()
				return
			}
			user, err := ss.GetUserByID(userID)
			if err != nil {
				log.Printf("auth: failed to load user %s: %v", userID, err)
				http.SetCookie(w, ClearSessionCookie())
				unauthorized()
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

// WithUser returns a context with the user stored. Used by middleware and tests.
func WithUser(ctx context.Context, u store.User) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}
