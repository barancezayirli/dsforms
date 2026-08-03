package spam

import "testing"

func TestIsGibberishToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "short token exempt regardless of shape", token: "LLC", want: false},
		{name: "short real word exempt", token: "IBM", want: false},
		{name: "random mixed-case string flagged", token: "AvAJQuWVbvzQPBGpngyOW", want: true},
		{name: "zero-vowel random string flagged", token: "Gfngfxr", want: true},
		{name: "low-vowel random string flagged", token: "Viqymx", want: true},
		{name: "real title-case word not flagged", token: "Ecthogb", want: false},
		{name: "real surname from consonant-heavy language not flagged alone", token: "Troskot", want: false},
		{name: "real word with normal vowel ratio not flagged", token: "Mateusz", want: false},
		{name: "vowel-light real brand name not flagged", token: "Dropbox", want: false},
		{name: "lowercase real word not flagged", token: "backlinks", want: false},
		{name: "ALLCAPS real word not flagged", token: "UNSUBSCRIBE", want: false},
		// Accented Latin vowels must count as vowels — the letter denominator
		// already counts them, so an ASCII-only vowel set pushes the ratio down
		// and flags ordinary Polish/Turkish/Czech/Vietnamese words.
		{name: "polish word with ogonek vowel not flagged", token: "proszę", want: false},
		{name: "polish word with acute vowel not flagged", token: "wycenę", want: false},
		{name: "czech name with acute and hacek not flagged", token: "Dvořák", want: false},
		{name: "turkish name with dotless i not flagged", token: "Yılmaz", want: false},
		{name: "hungarian word with umlaut vowels not flagged", token: "Törökország", want: false},
		{name: "vietnamese name with stacked diacritics not flagged", token: "Nguyễn", want: false},
		{name: "ALLCAPS accented surname not flagged (uppercase vowels lowercased)", token: "ÖZTÜRK", want: false},
		{name: "vietnamese word with horn vowels not flagged", token: "Phượng", want: false},
		// The length gate must count runes, not bytes: a 5-letter accented word
		// is 8 bytes in UTF-8 and must still be exempt as "too short to judge".
		{name: "five-rune accented token exempt by rune count", token: "Şükrü", want: false},
		{name: "five-rune accented token exempt by rune count (vietnamese)", token: "Hương", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isGibberishToken(tt.token); got != tt.want {
				t.Errorf("isGibberishToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestFieldHasGibberish(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "empty value", value: "", want: false},
		{name: "normal sentence", value: "Hello, I loved your work.", want: false},
		{name: "one gibberish token among real words", value: "Loved your Gfngfxr work", want: true},
		{name: "two-word gibberish name", value: "Gfngfxr Viqymx", want: true},
		{name: "single long gibberish token", value: "AvAJQuWVbvzQPBGpngyOW", want: true},
		{name: "real name with consonant-heavy surname", value: "Mateusz Sobczyk", want: true},
		{name: "cyrillic message not flagged (non-latin protected)", value: "Проктологическое заболевание", want: false},
		{name: "url token skipped but gibberish detected", value: "visit http://spam.com Gfngfxr", want: true},
		{name: "polish message not flagged", value: "Dzień dobry, proszę o wycenę.", want: false},
		{name: "turkish company not flagged", value: "Yılmaz Tekstil", want: false},
		{name: "turkish name not flagged", value: "Şükrü Yılmaz", want: false},
		{name: "vietnamese name not flagged", value: "Nguyễn Thị Hương", want: false},
		{name: "vietnamese message not flagged", value: "Xin chào, tôi muốn hỏi giá.", want: false},
		// Email addresses are structurally low-vowel (local part + domain glued
		// together) and duplicate whatever signal the name/company field already
		// carries — skipped like URL-shaped tokens so one signal can't score twice.
		{name: "email token skipped", value: "m.sobczyk@sobczyk.pl", want: false},
		{name: "short-domain email token skipped", value: "hr@bkstrm.co", want: false},
		{name: "email token skipped but gibberish elsewhere still detected", value: "hr@bkstrm.co Gfngfxr", want: true},
		// URL exemption must be case-insensitive like every other check here.
		{name: "capitalized url token skipped", value: "Https://MySite.io/Portfolio", want: false},
		{name: "uppercase www token skipped", value: "WWW.Bkstrm.co", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := fieldHasGibberish(tt.value); got != tt.want {
				t.Errorf("fieldHasGibberish(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestIsURLShapedToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "http scheme detected", token: "http://example.com", want: true},
		{name: "https scheme detected", token: "https://example.com", want: true},
		{name: "www domain detected", token: "www.example.com", want: true},
		{name: "www with protocol detected", token: "https://www.example.com", want: true},
		{name: "plain word not detected", token: "example", want: false},
		{name: "random gibberish not detected", token: "Gfngfxr", want: false},
		{name: "href attribute contains http", token: "href=\"https://x.com\">click</a>", want: true},
		{name: "mailto link not detected (no http/www)", token: "mailto:user@example.com", want: false},
		// Mobile keyboards autocapitalize sentence starts — matching must be
		// case-insensitive like the rest of the package.
		{name: "capitalized scheme detected", token: "Https://MySite.io/Portfolio", want: true},
		{name: "uppercase www detected", token: "WWW.Bkstrm.co", want: true},
		{name: "all-uppercase url detected", token: "HTTP://EXAMPLE.COM", want: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isURLShapedToken(tt.token); got != tt.want {
				t.Errorf("isURLShapedToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestIsEmailShapedToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "plain address", token: "user@example.com", want: true},
		{name: "dotted local part", token: "t.u.kilo.v.i.n.2.46@gmail.com", want: true},
		{name: "surname-derived address", token: "m.sobczyk@sobczyk.pl", want: true},
		{name: "uppercase address", token: "M.Sobczyk@Sobczyk.PL", want: true},
		{name: "mailto prefixed address", token: "mailto:user@example.com", want: true},
		{name: "trailing punctuation tolerated", token: "user@example.com,", want: true},
		{name: "no at sign", token: "Gfngfxr", want: false},
		{name: "no dot in domain", token: "user@localhost", want: false},
		{name: "two at signs", token: "a@b@c.com", want: false},
		{name: "empty local part", token: "@example.com", want: false},
		{name: "empty domain", token: "user@", want: false},
		{name: "at sign embedded in gibberish without domain dot", token: "AvAJQuWV@bvzQPBGpngyOW", want: false},
		{name: "empty token", token: "", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isEmailShapedToken(tt.token); got != tt.want {
				t.Errorf("isEmailShapedToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestIsTokenTooNonLatin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "pure cyrillic exempt", token: "москве", want: true},
		{name: "pure cyrillic word exempt", token: "Проктологическое", want: true},
		{name: "pure CJK exempt", token: "中文", want: true},
		{name: "pure arabic exempt", token: "العربية", want: true},
		{name: "pure latin not exempt", token: "example", want: false},
		{name: "latin with diacritics not exempt", token: "naïve", want: false},
		{name: "latin with diacritics not exempt (french)", token: "Français", want: false},
		{name: "mostly latin (cyrillic in path) not exempt", token: "href=\"https://cyrillic.ru\">", want: false},
		{name: "below 33% threshold latin (2 latin + 5 cyrillic ~28%) exempt", token: "abцдфгх", want: true},
		{name: "above 33% threshold latin (2 latin + 3 cyrillic ~40%) not exempt", token: "abмпр", want: false},
		{name: "short non-latin protected", token: "мр", want: true},
		{name: "short latin not protected", token: "ab", want: false},
		{name: "empty string not exempt", token: "", want: false},
		{name: "no letters (numbers/punctuation) not exempt", token: "123!@#", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isTokenTooNonLatin(tt.token); got != tt.want {
				t.Errorf("isTokenTooNonLatin(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}
