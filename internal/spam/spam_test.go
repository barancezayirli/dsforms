package spam

import "testing"

func TestScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data map[string]string
		want int
	}{
		{
			name: "ham no links",
			data: map[string]string{"name": "Jane Doe", "message": "Hello, I loved your work."},
			want: 0,
		},
		{
			name: "ham one link",
			data: map[string]string{"message": "See https://example.com for details"},
			want: 0,
		},
		{
			name: "two links scores per link past first",
			data: map[string]string{"message": "https://a.com and https://b.com"},
			want: 2,
		},
		{
			name: "scheme plus www counts as one link",
			data: map[string]string{"message": "visit https://www.example.com"},
			want: 0,
		},
		{
			name: "markup link",
			data: map[string]string{"message": "[url=http://x.com]click[/url]"},
			want: 5,
		},
		{
			name: "single keyword",
			data: map[string]string{"message": "buy backlinks now"},
			want: 5,
		},
		{
			name: "two keywords",
			data: map[string]string{"message": "casino and forex deals"},
			want: 10,
		},
		{
			name: "three links plus keyword",
			data: map[string]string{"message": "forex http://a.com http://b.com http://c.com"},
			want: 9,
		},
		{
			name: "url in name field",
			data: map[string]string{"name": "http://spam.com", "message": "hi"},
			want: 4,
		},
		{
			name: "case insensitive keyword",
			data: map[string]string{"message": "Cheap BACKLINKS"},
			want: 5,
		},
		{
			name: "markup link plus extra plain link",
			data: map[string]string{"message": "[url=http://x.com]hi[/url] also http://y.com"},
			want: 7,
		},
		{
			name: "url in mixed-case name field",
			data: map[string]string{"Name": "http://spam.com", "message": "hi"},
			want: 4,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Score(tt.data); got != tt.want {
				t.Errorf("Score() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsSpam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data map[string]string
		want bool
	}{
		{
			name: "below threshold (score 5) kept",
			data: map[string]string{"message": "[url=http://x.com]click[/url]"},
			want: false,
		},
		{
			name: "at threshold (score 6) dropped",
			data: map[string]string{"message": "http://a.com http://b.com http://c.com http://d.com"},
			want: true,
		},
		{
			name: "well over threshold dropped",
			data: map[string]string{"message": "casino and forex deals"},
			want: true,
		},
		{
			name: "clean message kept",
			data: map[string]string{"name": "Jane", "message": "Loved the talk, thanks!"},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSpam(tt.data); got != tt.want {
				t.Errorf("IsSpam() = %v, want %v", got, tt.want)
			}
		})
	}
}
