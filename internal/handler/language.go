package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/i18n"
	"github.com/barancezayirli/dsforms/internal/urlsafe"
)

// Language stores the browser's language choice and returns to the page that
// offered the switcher. It is public: the login page needs it before anyone has
// a session.
//
// The destination is checked with urlsafe.RelativePath, the same guard the login
// `next` parameter uses. A switcher that accepted an absolute URL would be an
// open redirect on a public route.
func (b *Base) Language(w http.ResponseWriter, r *http.Request) {
	lang := i18n.Normalize(r.URL.Query().Get("lang"))
	if !lang.Valid() {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	next := r.URL.Query().Get("next")
	if !urlsafe.RelativePath(next) {
		next = "/admin"
	}

	http.SetCookie(w, &http.Cookie{
		Name:     i18n.CookieName,
		Value:    string(lang),
		Path:     "/",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(b.BaseURL, "https://"),
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}
