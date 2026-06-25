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
			name: "bbcode markup link alone is instant drop",
			data: map[string]string{"message": "[url=http://x.com]click[/url]"},
			want: 6,
		},
		{
			name: "html anchor markup alone is instant drop",
			data: map[string]string{"message": "<a href=\"http://x.com\">click</a>"},
			want: 6,
		},
		{
			name: "bare bbcode url marker is instant drop",
			data: map[string]string{"message": "[url]http://x.com[/url]"},
			want: 6,
		},
		{
			name: "bbcode link marker is instant drop",
			data: map[string]string{"message": "[link]http://x.com[/link]"},
			want: 6,
		},
		{
			name: "single keyword",
			data: map[string]string{"message": "buy backlinks now"},
			want: 5,
		},
		{
			name: "bare forex no longer matches (substring narrowed)",
			data: map[string]string{"message": "we offer the best forex deals around"},
			want: 0,
		},
		{
			name: "forex trading keyword matches",
			data: map[string]string{"message": "join our forex trading group"},
			want: 5,
		},
		{
			name: "forex signals keyword matches",
			data: map[string]string{"message": "daily forex signals here"},
			want: 5,
		},
		{
			name: "multi-word keyword",
			data: map[string]string{"message": "buy seo service packages"},
			want: 5,
		},
		{
			name: "unsubscribe keyword",
			data: map[string]string{"message": "click here to unsubscribe"},
			want: 5,
		},
		{
			name: "two keywords",
			data: map[string]string{"message": "casino and viagra deals"},
			want: 10,
		},
		{
			name: "three links plus keyword",
			data: map[string]string{"message": "casino http://a.com http://b.com http://c.com"},
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
			want: 8,
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
			name: "below threshold (single keyword, score 5) kept",
			data: map[string]string{"message": "buy backlinks now"},
			want: false,
		},
		{
			name: "genuine unsubscribe request alone kept",
			data: map[string]string{"message": "Please unsubscribe me from your newsletter"},
			want: false,
		},
		{
			name: "url in name field alone (score 4) kept",
			data: map[string]string{"name": "http://spam.com", "message": "hi"},
			want: false,
		},
		{
			name: "markup link alone dropped",
			data: map[string]string{"message": "<a href=\"http://x.com\">hi</a>"},
			want: true,
		},
		{
			name: "four links at threshold dropped",
			data: map[string]string{"message": "http://a.com http://b.com http://c.com http://d.com"},
			want: true,
		},
		{
			name: "two keywords well over threshold dropped",
			data: map[string]string{"message": "casino and viagra deals"},
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

// TestRealSpamSamples pins the filter against spam actually captured in
// production. Each must be dropped; these are the regression guard for the
// 2026-06-25 retune (see the design doc revision). The originals scored
// 7/4/5 on the pre-retune filter — two of three slipped through.
func TestRealSpamSamples(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		data      map[string]string
		wantScore int
	}{
		{
			name: "automisto24 html anchor plus repeated url",
			data: map[string]string{"message": "Here to explore discussions, exchange ideas, and gain fresh perspectives throughout the journey. I like learning from different perspectives and contributing whenever I can. Here is my site-<a href=\"https://automisto24.com.ua/\">AutoMisto24</a> https://automisto24.com.ua/ sample"},
			wantScore: 8,
		},
		{
			name: "jayrn marketersmentor three links plus unsubscribe",
			data: map[string]string{"message": "Hi, it’s Jayrn. Watch it here: https://marketersmentor.com/referral-system.php?refer=openapps.pro To multiplying your leverage, Jayrn My Blog: https://www.jayrn.com Unsubscribe: https://marketersmentor.com/unsubscribe.php?d=openapps.pro gzlrmkmdswesheytngrwediuoipqzm"},
			wantScore: 9,
		},
		{
			name: "russian proctology single html anchor punycode",
			data: map[string]string{"message": "Проктологическое заболевание — это повсеместная патология. <a href=\"https://xn----etbhrcdtbbfidklik5p.xn--p1ai/service/hemorrhoid/\">удаление геморроя цена в москве</a> Ранняя помощь даёт возможность."},
			wantScore: 6,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Score(tt.data); got != tt.wantScore {
				t.Errorf("Score() = %d, want %d", got, tt.wantScore)
			}
			if !IsSpam(tt.data) {
				t.Errorf("IsSpam() = false, want true (real spam must be dropped)")
			}
		})
	}
}
