package spam

import (
	"reflect"
	"strings"
	"testing"
)

// TestDetailSignals asserts the exact []Signal for each rule in isolation, plus
// the multi-signal ordering contract. Order is part of the API: the quarantine
// breakdown renders signals top-to-bottom in the order Detail returns them, and
// Go map iteration is randomized, so Detail must sort field keys itself.
func TestDetailSignals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		data        map[string]string
		wantScore   int
		wantSignals []Signal
	}{
		{
			name:        "clean submission has no signals",
			data:        map[string]string{"name": "Jane Doe", "message": "Hello, I loved your work."},
			wantScore:   0,
			wantSignals: nil,
		},
		{
			name:      "markup link",
			data:      map[string]string{"message": "[url=http://x.com]click[/url]"},
			wantScore: 6,
			wantSignals: []Signal{
				{Rule: "markup", Field: "message", Match: "[url=", Weight: 6},
			},
		},
		{
			name:      "repeated markup marker is one signal with multiplied weight",
			data:      map[string]string{"message": "<a href=1><a href=2>"},
			wantScore: 12,
			wantSignals: []Signal{
				{Rule: "markup", Field: "message", Match: "<a href", Weight: 12},
			},
		},
		{
			name:      "sql probe",
			data:      map[string]string{"message": "admin' or 1=1 -- -"},
			wantScore: 12,
			wantSignals: []Signal{
				{Rule: "sql", Field: "message", Match: "' or 1=1", Weight: 6},
				{Rule: "sql", Field: "message", Match: "-- -", Weight: 6},
			},
		},
		{
			name:      "keyword hit",
			data:      map[string]string{"message": "buy backlinks now"},
			wantScore: 5,
			wantSignals: []Signal{
				{Rule: "keyword", Field: "message", Match: "backlinks", Weight: 5},
			},
		},
		{
			name:      "keyword match reports the marker lowercased regardless of input case",
			data:      map[string]string{"message": "Best CASINO in town"},
			wantScore: 5,
			wantSignals: []Signal{
				{Rule: "keyword", Field: "message", Match: "casino", Weight: 5},
			},
		},
		{
			name:      "url inside a name field",
			data:      map[string]string{"name": "https://cheap-pills.example"},
			wantScore: 4,
			wantSignals: []Signal{
				{Rule: "url_in_name", Field: "name", Match: "https://cheap-pills.example", Weight: 4},
			},
		},
		{
			name:      "extra links is one whole-submission signal",
			data:      map[string]string{"message": "https://a.com and https://b.com and https://c.com"},
			wantScore: 4,
			wantSignals: []Signal{
				{Rule: "extra_links", Field: "", Match: "", Weight: 4},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score, signals := Detail(tt.data)
			if score != tt.wantScore {
				t.Errorf("Detail() score = %d, want %d", score, tt.wantScore)
			}
			if !reflect.DeepEqual(signals, tt.wantSignals) {
				t.Errorf("Detail() signals =\n  %+v\nwant\n  %+v", signals, tt.wantSignals)
			}
		})
	}
}

// TestDetailGibberishCapturesOriginalCaseToken pins the one signal whose Match
// is lifted from the submission rather than from a fixed marker list. Gibberish
// is deliberately checked against the original-case value (lowercasing destroys
// the case-transition signal), so the captured token must keep its casing.
func TestDetailGibberishCapturesOriginalCaseToken(t *testing.T) {
	t.Parallel()
	score, signals := Detail(map[string]string{"message": "hello xKqZjWmB world"})
	if score != gibberishWeight {
		t.Fatalf("score = %d, want %d", score, gibberishWeight)
	}
	if len(signals) != 1 {
		t.Fatalf("got %d signals, want 1: %+v", len(signals), signals)
	}
	if signals[0].Rule != "gibberish" {
		t.Errorf("Rule = %q, want %q", signals[0].Rule, "gibberish")
	}
	if signals[0].Match != "xKqZjWmB" {
		t.Errorf("Match = %q, want the original-case token %q", signals[0].Match, "xKqZjWmB")
	}
}

// TestDetailFieldOrderIsDeterministic is the regression guard for D9. Go
// randomizes map iteration, so a Detail that ranged over data directly would
// pass this only intermittently. Run with -count to shake it out.
func TestDetailFieldOrderIsDeterministic(t *testing.T) {
	t.Parallel()
	data := map[string]string{
		"zeta":    "buy viagra",
		"alpha":   "[url=http://x.com]x[/url]",
		"message": "union select 1",
	}

	_, first := Detail(data)
	for i := 0; i < 50; i++ {
		_, again := Detail(data)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("Detail() is not deterministic across calls:\n  %+v\n  %+v", first, again)
		}
	}

	wantFields := []string{"alpha", "message", "zeta"}
	gotFields := make([]string, 0, len(first))
	for _, s := range first {
		gotFields = append(gotFields, s.Field)
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Errorf("signal fields = %v, want them sorted %v", gotFields, wantFields)
	}
}

// TestDetailRuleOrderWithinField pins the within-field emission order, which
// mirrors the order Score has always evaluated rules in.
func TestDetailRuleOrderWithinField(t *testing.T) {
	t.Parallel()
	// A single name field that trips markup, sql, keyword and url_in_name at once.
	_, signals := Detail(map[string]string{
		"name": "<a href=http://x.com>casino</a> union select xKqZjWmB",
	})

	want := []string{"markup", "sql", "keyword", "url_in_name", "gibberish"}
	got := make([]string, 0, len(signals))
	for _, s := range signals {
		got = append(got, s.Rule)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rule order = %v, want %v", got, want)
	}
}

// TestDetailExtraLinksSortsLast asserts the whole-submission signal is appended
// after every per-field signal, whatever the field names sort to.
func TestDetailExtraLinksSortsLast(t *testing.T) {
	t.Parallel()
	_, signals := Detail(map[string]string{
		"aaa": "https://a.com https://b.com",
		"zzz": "buy backlinks",
	})
	if len(signals) == 0 {
		t.Fatal("want signals, got none")
	}
	last := signals[len(signals)-1]
	if last.Rule != "extra_links" {
		t.Errorf("last signal = %q, want extra_links (got all: %+v)", last.Rule, signals)
	}
	if last.Field != "" {
		t.Errorf("extra_links Field = %q, want \"\" (whole-submission rule)", last.Field)
	}
}

// TestDetailWeightsSumToScore is the invariant the quarantine meter depends on:
// the breakdown must add up to the number shown next to it, or the panel is
// lying about why the submission was held.
func TestDetailWeightsSumToScore(t *testing.T) {
	t.Parallel()
	cases := []map[string]string{
		{"name": "Jane Doe", "message": "Hello"},
		{"message": "[url=http://x.com]x[/url] casino"},
		{"name": "http://x.com", "message": "https://a.com https://b.com union select"},
		{"message": "xKqZjWmB qWxZvKpL buy viagra now"},
		{"a": "<a href=1>", "b": "<a href=2>", "c": "-- -"},
	}
	for _, data := range cases {
		score, signals := Detail(data)
		sum := 0
		for _, s := range signals {
			sum += s.Weight
		}
		if sum != score {
			t.Errorf("Detail(%v): weights sum to %d but score is %d (signals %+v)", data, sum, score, signals)
		}
	}
}

// TestDetailTruncatesMatch keeps an attacker from stuffing the quarantine
// breakdown — and the spam_signals table — with an unbounded blob.
func TestDetailTruncatesMatch(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 500)
	_, signals := Detail(map[string]string{"name": "https://x.example/" + long})
	if len(signals) == 0 {
		t.Fatal("want a url_in_name signal, got none")
	}
	for _, s := range signals {
		if n := len([]rune(s.Match)); n > maxMatchRunes {
			t.Errorf("%s Match is %d runes, want <= %d", s.Rule, n, maxMatchRunes)
		}
	}
}

// TestScoreMatchesDetail is the wrapper contract: reducing Score to a Detail
// wrapper must not move any existing score. spam_test.go covers the values
// themselves; this covers the two staying in step.
func TestScoreMatchesDetail(t *testing.T) {
	t.Parallel()
	cases := []map[string]string{
		{"name": "Jane Doe", "message": "Hello, I loved your work."},
		{"message": "See https://example.com for details"},
		{"message": "https://a.com and https://b.com"},
		{"message": "[url=http://x.com]click[/url]"},
		{"message": "buy backlinks now"},
		{"name": "http://spam.example", "message": "casino unsubscribe"},
		{"message": "admin' or '1'='1 -- -"},
	}
	for _, data := range cases {
		want, _ := Detail(data)
		if got := Score(data); got != want {
			t.Errorf("Score(%v) = %d, Detail score = %d", data, got, want)
		}
	}
}

// TestIsSpamUsesDefaultThreshold guards the exported constant replacing the old
// private one — IsSpam must keep comparing against 6.
func TestIsSpamUsesDefaultThreshold(t *testing.T) {
	t.Parallel()
	if DefaultThreshold != 6 {
		t.Errorf("DefaultThreshold = %d, want 6 (the documented default)", DefaultThreshold)
	}
	if !IsSpam(map[string]string{"message": "[url=http://x.com]x[/url]"}) {
		t.Error("a markup link scores DefaultThreshold and must still be spam")
	}
	if IsSpam(map[string]string{"message": "buy backlinks now"}) {
		t.Error("a lone keyword scores below DefaultThreshold and must not be spam")
	}
}
