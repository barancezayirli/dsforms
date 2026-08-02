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
