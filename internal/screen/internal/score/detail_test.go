package score

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
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
				{Check: "markup", Field: "message", Match: "[url=", Weight: 6},
			},
		},
		{
			name:      "repeated markup marker is one signal with multiplied weight",
			data:      map[string]string{"message": "<a href=1><a href=2>"},
			wantScore: 12,
			wantSignals: []Signal{
				{Check: "markup", Field: "message", Match: "<a href", Weight: 12},
			},
		},
		{
			name:      "sql probe",
			data:      map[string]string{"message": "admin' or 1=1 -- -"},
			wantScore: 12,
			wantSignals: []Signal{
				{Check: "sql", Field: "message", Match: "' or 1=1", Weight: 6},
				{Check: "sql", Field: "message", Match: "-- -", Weight: 6},
			},
		},
		{
			name:      "keyword hit",
			data:      map[string]string{"message": "buy backlinks now"},
			wantScore: 5,
			wantSignals: []Signal{
				{Check: "keyword", Field: "message", Match: "backlinks", Weight: 5},
			},
		},
		{
			name:      "keyword match reports the marker lowercased regardless of input case",
			data:      map[string]string{"message": "Best CASINO in town"},
			wantScore: 5,
			wantSignals: []Signal{
				{Check: "keyword", Field: "message", Match: "casino", Weight: 5},
			},
		},
		{
			name:      "url inside a name field",
			data:      map[string]string{"name": "https://cheap-pills.example"},
			wantScore: 4,
			wantSignals: []Signal{
				{Check: "url_in_name", Field: "name", Match: "https://cheap-pills.example", Weight: 4},
			},
		},
		{
			name:      "extra links is one whole-submission signal",
			data:      map[string]string{"message": "https://a.com and https://b.com and https://c.com"},
			wantScore: 4,
			wantSignals: []Signal{
				{Check: "extra_links", Field: "", Match: "", Weight: 4},
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
	if signals[0].Check != CheckGibberish {
		t.Errorf("Check = %q, want %q", signals[0].Check, CheckGibberish)
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

	want := []Check{CheckMarkup, CheckSQL, CheckKeyword, CheckURLInName, CheckGibberish}
	got := make([]Check, 0, len(signals))
	for _, s := range signals {
		got = append(got, s.Check)
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
	if last.Check != CheckExtraLinks {
		t.Errorf("last signal = %q, want extra_links (got all: %+v)", last.Check, signals)
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
			t.Errorf("%s Match is %d runes, want <= %d", s.Check, n, maxMatchRunes)
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

// TestDetailWithCustomKeywords covers the operator-supplied keyword list from
// internal/filter. Custom keywords score exactly like built-in ones — the point
// of feeding them through the scorer rather than short-circuiting is that a
// single keyword still never holds a submission on its own.
func TestDetailWithCustomKeywords(t *testing.T) {
	t.Parallel()

	data := map[string]string{"message": "join our telegram pump group today"}

	if score, signals := Detail(data); score != 0 || signals != nil {
		t.Fatalf("baseline: Detail() = %d, %+v; want a clean submission", score, signals)
	}

	score, signals := DetailWith(data, []string{"telegram pump"})
	if score != keywordWeight {
		t.Errorf("score = %d, want %d", score, keywordWeight)
	}
	want := []Signal{{Check: "keyword", Field: "message", Match: "telegram pump", Weight: keywordWeight}}
	if !reflect.DeepEqual(signals, want) {
		t.Errorf("signals = %+v, want %+v", signals, want)
	}

	// Below the default threshold, so a custom keyword alone must not hold.
	if score >= DefaultThreshold {
		t.Errorf("a lone custom keyword scores %d, which is at or above the threshold %d — "+
			"custom keywords must pile up, not drop on their own", score, DefaultThreshold)
	}
}

// A custom keyword duplicating a built-in one must not score twice.
func TestDetailWithCustomKeywordsDeduplicates(t *testing.T) {
	t.Parallel()
	score, signals := DetailWith(map[string]string{"message": "buy backlinks"}, []string{"backlinks", "backlinks"})
	if score != keywordWeight {
		t.Errorf("score = %d, want %d (one hit, not three)", score, keywordWeight)
	}
	if len(signals) != 1 {
		t.Errorf("got %d signals, want 1: %+v", len(signals), signals)
	}
}

func TestDetailWithNilKeywordsMatchesDetail(t *testing.T) {
	t.Parallel()
	data := map[string]string{"name": "http://x.example", "message": "casino [url=http://y]z[/url]"}
	wantScore, wantSignals := Detail(data)
	gotScore, gotSignals := DetailWith(data, nil)
	if gotScore != wantScore || !reflect.DeepEqual(gotSignals, wantSignals) {
		t.Errorf("DetailWith(data, nil) disagrees with Detail(data):\n  %d %+v\n  %d %+v",
			gotScore, gotSignals, wantScore, wantSignals)
	}
}

// TestAllChecksIsComplete keeps AllChecks honest.
//
// AllChecks exists so callers stop restating the rule set, but it is itself a
// second copy of the const block — so a hand-written expected count here would
// just move the staleness one line over. Go cannot enumerate a type's constants
// at runtime, so this parses the package's own source and derives the list. A
// new Check constant that nobody adds to AllChecks fails, with no number for
// anyone to forget to bump.
func TestAllChecksIsComplete(t *testing.T) {
	t.Parallel()

	declared := checkConstantsInSource(t)
	if len(declared) == 0 {
		t.Fatal("found no Check constants in the source; this test is asserting nothing")
	}

	listed := map[Check]bool{}
	for _, r := range AllChecks {
		if listed[r] {
			t.Errorf("AllChecks lists %q twice", r)
		}
		listed[r] = true
	}

	for name, value := range declared {
		if !listed[value] {
			t.Errorf("constant %s (%q) is missing from AllChecks", name, value)
		}
	}
	if len(AllChecks) != len(declared) {
		t.Errorf("AllChecks has %d entries, source declares %d constants", len(AllChecks), len(declared))
	}
}

// checkConstantsInSource returns every `X Check = "y"` constant declared in this
// package, by name and value.
func checkConstantsInSource(t *testing.T) map[string]Check {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parsing package source: %v", err)
	}

	out := map[string]Check{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				var lastType string
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					// Within a const block the type carries down to entries that
					// omit it, so remember the most recent one.
					if id, ok := vs.Type.(*ast.Ident); ok {
						lastType = id.Name
					}
					if lastType != "Check" {
						continue
					}
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						value, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatalf("unquoting %s: %v", name.Name, err)
						}
						out[name.Name] = Check(value)
					}
				}
			}
		}
	}
	return out
}

// TestDetailEmitsOnlyDeclaredRules is the behavioural half: whatever the scorer
// actually produces must be a declared rule, derived from running it rather
// than from a literal.
func TestDetailEmitsOnlyDeclaredRules(t *testing.T) {
	t.Parallel()

	listed := map[Check]bool{}
	for _, r := range AllChecks {
		listed[r] = true
	}

	emitted := map[Check]bool{}
	for _, data := range []map[string]string{
		{"message": "<a href=x>casino</a> http://a.example http://b.example"},
		{"message": "union select 1"},
		{"name": "http://spam.example"},
		{"comment": "xkcdqwrtplm zzzxqjvbn"},
	} {
		_, signals := Detail(data)
		for _, sig := range signals {
			emitted[sig.Check] = true
		}
	}
	if len(emitted) == 0 {
		t.Fatal("the fixtures produced no signals; this test is asserting nothing")
	}
	for rule := range emitted {
		if !listed[rule] {
			t.Errorf("Detail emits %q but AllChecks does not list it", rule)
		}
	}
}
