package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/barancezayirli/dsforms/internal/i18n"
)

func TestLanguageSwitcherSetsCookieAndReturnsToThePage(t *testing.T) {
	t.Parallel()

	b := &Base{BaseURL: "https://forms.example.com"}
	req := httptest.NewRequest(http.MethodGet, "/admin/language?lang=ru&next=%2Fadmin%2Fquarantine%3Fpage%3D2", nil)
	w := httptest.NewRecorder()

	b.Language(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/quarantine?page=2" {
		t.Errorf("Location = %q, want the page that offered the switcher", loc)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want one language preference", len(cookies))
	}
	c := cookies[0]
	if c.Name != i18n.CookieName || c.Value != "ru" {
		t.Errorf("cookie = %s=%s, want %s=ru", c.Name, c.Value, i18n.CookieName)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags = HttpOnly %v, SameSite %v", c.HttpOnly, c.SameSite)
	}
	if !c.Secure {
		t.Error("cookie is not Secure on an https BASE_URL")
	}
	if c.Path != "/" {
		t.Errorf("cookie Path = %q, want /", c.Path)
	}
}

func TestLanguageSwitcherRefusesAnOffSiteReturn(t *testing.T) {
	t.Parallel()

	b := &Base{}
	req := httptest.NewRequest(http.MethodGet, "/admin/language?lang=ru&next=https://evil.example/", nil)
	w := httptest.NewRecorder()

	b.Language(w, req)

	if loc := w.Header().Get("Location"); loc != "/admin" {
		t.Errorf("Location = %q, want /admin — a public route must not be an open redirect", loc)
	}
}

func TestShellUsesTheCookieLanguage(t *testing.T) {
	t.Parallel()

	b := &Base{SecretKey: "test-secret", DefaultLanguage: "en"}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: i18n.CookieName, Value: "ru"})
	w := httptest.NewRecorder()

	data := b.Shell(w, req, "Home", "home")

	if data.Lang != i18n.Russian {
		t.Fatalf("Lang = %q, want ru", data.Lang)
	}
	if data.Title != "Главная" {
		t.Errorf("Title = %q, want the translated title", data.Title)
	}
	if !strings.Contains(data.Crumb, "Обзор") {
		t.Errorf("Crumb = %q, want the translated nav group", data.Crumb)
	}
	if got := data.T("Forms"); got != "Формы" {
		t.Errorf("T(Forms) = %q", got)
	}
}
