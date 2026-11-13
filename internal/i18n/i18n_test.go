package i18n

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want Lang
	}{
		{"en", English},
		{"EN", English},
		{" ru ", Russian},
		{"ru-RU", Russian},
		{"en_US", English},
		{"de", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFromRequestPrefersExplicitChoice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		query    string
		cookie   string
		fallback string
		accept   string
		want     Lang
	}{
		{
			name:  "query wins over cookie and fallback",
			query: "ru", cookie: "en", fallback: "en", accept: "en",
			want: Russian,
		},
		{
			name:   "cookie wins over fallback",
			cookie: "ru", fallback: "en",
			want: Russian,
		},
		{
			name:     "a configured language beats the browser",
			fallback: "en", accept: "ru-RU,ru;q=0.9",
			want: English,
		},
		{
			name:     "auto follows the browser and honours q-values",
			fallback: "auto", accept: "en;q=0.2,ru-RU;q=0.9",
			want: Russian,
		},
		{
			name:     "auto falls back to English",
			fallback: "auto", accept: "de-DE,de;q=0.9",
			want: English,
		},
		{
			name:     "a malformed q-value does not select the language",
			fallback: "auto", accept: "ru;q=0",
			want: English,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/admin?lang="+tt.query, nil)
			if tt.accept != "" {
				req.Header.Set("Accept-Language", tt.accept)
			}
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: CookieName, Value: tt.cookie})
			}
			if got := FromRequest(req, tt.fallback); got != tt.want {
				t.Errorf("FromRequest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTranslationAndPlural(t *testing.T) {
	t.Parallel()

	if got := T(Russian, "Forms"); got != "Формы" {
		t.Errorf("T(Russian, Forms) = %q", got)
	}
	if got := T(English, "Forms"); got != "Forms" {
		t.Errorf("T(English, Forms) = %q", got)
	}
	if got := T(Russian, "Restored to %s as unread.", "Contact"); got != "Восстановлена в «Contact» как непрочитанная." {
		t.Errorf("T with an argument = %q", got)
	}
	if got := T(Russian, "not in the catalogue"); got != "not in the catalogue" {
		t.Errorf("a missing key = %q, want the source text", got)
	}

	tests := []struct {
		n    int
		want string
	}{
		{1, "1 форма"},
		{2, "2 формы"},
		{5, "5 форм"},
		{11, "11 форм"},
		{21, "21 форма"},
	}
	for _, tt := range tests {
		if got := Plural(Russian, tt.n, "form"); got != tt.want {
			t.Errorf("Plural(Russian, %d, form) = %q, want %q", tt.n, got, tt.want)
		}
	}
	if got := Plural(English, 2, "form"); got != "2 forms" {
		t.Errorf("Plural(English, 2, form) = %q", got)
	}
}

// TestEveryRequestedTranslationExists is the guard that keeps the Russian UI
// from silently falling back to English one phrase at a time. It walks the real
// templates and handler files rather than a list, so a new screen is covered the
// moment it calls T.
func TestEveryRequestedTranslationExists(t *testing.T) {
	t.Parallel()

	type source struct {
		glob    string
		pattern *regexp.Regexp
	}
	sources := []source{
		{
			glob:    filepath.Join("..", "..", "templates", "*.html"),
			pattern: regexp.MustCompile(`\{\{(?:\$|\.)\.?T\s+"((?:[^"\\]|\\.)*)"`),
		},
		{
			glob:    filepath.Join("..", "handler", "*.go"),
			pattern: regexp.MustCompile(`(?:h\.T|data\.T)\(\s*(?:r,\s*)?"((?:[^"\\]|\\.)*)"`),
		},
	}

	var missing []string
	checked := 0
	for _, src := range sources {
		files, err := filepath.Glob(src.glob)
		if err != nil {
			t.Fatalf("globbing %s: %v", src.glob, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			for _, m := range src.pattern.FindAllStringSubmatch(string(body), -1) {
				checked++
				key := m[1]
				if _, ok := catalog[Russian][key]; !ok {
					missing = append(missing, filepath.Base(file)+": "+key)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no translations found; the matcher is broken")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("Russian catalogue is missing %d key(s):\n%s\n\n"+
			"A missing key renders English in an otherwise Russian page.",
			len(missing), strings.Join(missing, "\n"))
	}
}
