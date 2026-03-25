package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

const CookieName = "dsforms_session"

type contextKey string

const userContextKey contextKey = "user"

// UserStore is the interface the auth package needs from the store.
type UserStore interface {
	GetUserByID(id string) (store.User, error)
}

// createSessionValue builds the signed cookie value at the given time.
// It is unexported so tests can inject arbitrary timestamps for expiry testing.
func createSessionValue(userID, secretKey string, t time.Time) string {
	timestamp := strconv.FormatInt(t.Unix(), 10)
	payload := userID + ":" + timestamp
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.URLEncoding.EncodeToString([]byte(payload + ":" + sig))
}

// CreateSessionCookie creates a signed session cookie for the given user.
func CreateSessionCookie(userID, secretKey, baseURL string) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    createSessionValue(userID, secretKey, time.Now()),
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(baseURL, "https"),
	}
}

// ValidateSession reads and validates the session cookie from the request.
// It returns the userID and true if the session is valid and unexpired.
func ValidateSession(r *http.Request, secretKey string) (userID string, ok bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return "", false
	}

	decoded, err := base64.URLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return "", false
	}

	// Expected format: userID:timestamp:signature
	// userID itself may contain ":" so split from the right using last two ":"
	raw := string(decoded)
	lastColon := strings.LastIndex(raw, ":")
	if lastColon < 0 {
		return "", false
	}
	sig := raw[lastColon+1:]
	rest := raw[:lastColon]

	secondLastColon := strings.LastIndex(rest, ":")
	if secondLastColon < 0 {
		return "", false
	}
	timestamp := rest[secondLastColon+1:]
	uid := rest[:secondLastColon]

	// Recompute HMAC over userID:timestamp
	payload := fmt.Sprintf("%s:%s", uid, timestamp)
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", false
	}

	// Parse timestamp and check expiry (30 days)
	unixSec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return "", false
	}
	issued := time.Unix(unixSec, 0)
	if time.Since(issued) >= 30*24*time.Hour {
		return "", false
	}

	return uid, true
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

// RequireAuth is middleware that enforces authentication.
// On failure it redirects to /admin/login.
func RequireAuth(us UserStore, secretKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, ok := ValidateSession(r, secretKey)
			if !ok {
				http.Redirect(w, r, "/admin/login", http.StatusFound)
				return
			}

			user, err := us.GetUserByID(userID)
			if err != nil {
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
