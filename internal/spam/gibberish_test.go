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
