package handler

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/barancezayirli/dsforms/internal/screen"
)

// updateGolden re-records the golden file. Run with:
//
//	go test ./internal/handler -run TestDecideGolden -update
//
// It exists so an *intended* behaviour change shows up as a reviewable diff in
// the committed golden rather than as a silently edited expectation.
var updateGolden = flag.Bool("update", false, "re-record testdata/verdicts.golden")

// TestDecideGolden characterises the whole hold/accept decision.
//
// This is not a test of any one rule; it is a record of what the decision
// currently does across its entire input space, so that moving it into
// internal/screen can be proved to change nothing. The last three review rounds
// each verified their fixes locally and never against whole-system behaviour,
// which is how a bypass survived all three — every individual test stayed green
// while the composed decision was wrong.
//
// A diff in the golden means behaviour changed. During a refactor that is a
// stop, not a re-record.
func TestDecideGolden(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	for _, c := range goldenCases() {
		// A fresh Screener per case, primed so the repeat flag is exactly what
		// the case asks for: Decide records as it decides, so a shared tracker
		// would make every later case a repeat.
		sc := screen.New(16)
		in := screen.Input{FormID: "f", Fields: c.fields, IP: c.ip, Rules: c.rules, Threshold: c.threshold}
		for i := 0; i < c.prior; i++ {
			sc.Decide(in)
		}
		writeCase(&buf, c, sc.Decide(in))
	}

	path := filepath.Join("testdata", "verdicts.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		t.Logf("recorded %d cases to %s", len(goldenCases()), path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden (run with -update to record): %v", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		// Write the actual output beside the golden so the diff is inspectable
		// rather than buried in test output.
		actual := filepath.Join("testdata", "verdicts.actual")
		_ = os.WriteFile(actual, buf.Bytes(), 0o644)
		t.Fatalf("the decision changed.\n"+
			"  diff %s %s\n"+
			"If the change is intended, re-record with -update and review the diff "+
			"as part of the change. If it is not, this is the regression.", path, actual)
	}
}

type goldenCase struct {
	name      string
	fields    map[string]string
	ip        string
	rules     []screen.Rule
	threshold int

	// prior is how many submissions from this (form, IP) precede the one being
	// recorded. It is a count, not a bool, because Tracker.Seen's threshold is a
	// count: priming twice and measuring the third call samples 1 and 3 and never
	// 2, so changing that threshold from >= 3 to >= 2 altered real behaviour and
	// produced no golden diff at all. A boolean corpus cannot see a boundary.
	prior int
}

// writeCase renders one case deterministically. Fields are sorted because Go
// randomises map iteration, and a golden file that reorders itself is worthless.
func writeCase(buf *bytes.Buffer, c goldenCase, v screen.Verdict) {
	fmt.Fprintf(buf, "=== %s\n", c.name)

	keys := make([]string, 0, len(c.fields))
	for k := range c.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(buf, "  field %s = %q\n", k, c.fields[k])
	}
	if len(keys) == 0 {
		buf.WriteString("  field (none)\n")
	}

	fmt.Fprintf(buf, "  ip=%q threshold=%d prior=%d\n", c.ip, c.threshold, c.prior)
	for _, r := range c.rules {
		fmt.Fprintf(buf, "  rule %s/%s %q\n", r.Kind, r.Type, r.Value)
	}
	if len(c.rules) == 0 {
		buf.WriteString("  rule (none)\n")
	}

	action := "accept"
	if v.Hold {
		action = "HOLD"
	}
	fmt.Fprintf(buf, "  -> %s score=%d matchedRule=%q\n", action, v.Score, v.MatchedRuleID)
	for _, s := range v.Signals {
		fmt.Fprintf(buf, "     signal %s field=%q match=%q weight=%d\n", s.Check, s.Field, s.Match, s.Weight)
	}
	if len(v.Signals) == 0 {
		buf.WriteString("     signal (none)\n")
	}
	buf.WriteString("\n")
}

// fieldSets are the submission shapes worth characterising: one per content
// check, the multi-signal pile-ups, the degenerate inputs, and every form that
// was a live filter bypass in review rounds 1, 2 and 3.
func fieldSets() []struct {
	name   string
	fields map[string]string
} {
	return []struct {
		name   string
		fields map[string]string
	}{
		{"empty", map[string]string{}},
		{"clean", map[string]string{"email": "jane@real.com", "message": "Hello there"}},
		{"clean-no-email", map[string]string{"name": "Jane", "message": "Hello there"}},
		{"markup", map[string]string{"email": "a@x.com", "message": "<a href=http://x>hi</a>"}},
		{"bbcode", map[string]string{"email": "a@x.com", "message": "[url=http://x]hi[/url]"}},
		{"keyword-one", map[string]string{"email": "a@x.com", "message": "cheap viagra"}},
		{"keyword-many", map[string]string{"email": "a@x.com", "message": "casino backlinks crypto pump"}},
		{"sql-probe", map[string]string{"email": "a@x.com", "message": "' or 1=1 -- -"}},
		{"sql-union", map[string]string{"email": "a@x.com", "q": "union select 1,2,3"}},
		{"url-in-name", map[string]string{"name": "http://spam.example", "email": "a@x.com"}},
		{"extra-links", map[string]string{"email": "a@x.com", "message": "http://a.co http://b.co http://c.co"}},
		{"gibberish", map[string]string{"email": "a@x.com", "message": "xkcdqwrtplm zzzxqjvbn"}},
		{"pile-up", map[string]string{"email": "a@x.com", "message": "<a href=x>casino</a> backlinks http://a.co http://b.co"}},

		// The bypasses. Each of these was accepted at some point in rounds 1-3
		// while an allow rule for vip@customer.com / mike@works.com existed.
		{"bypass-r1-junk-field", map[string]string{"email": "mallory@spam.example", "zz": "vip@customer.com", "message": "casino backlinks"}},
		{"bypass-r2-case-variant", map[string]string{"email": "mallory@spam.example", "Email": "vip@customer.com", "message": "casino backlinks"}},
		{"bypass-r3-unicode-fold", map[string]string{"email": "MİKE@works.com", "message": "casino backlinks"}},
		{"bypass-r3-kelvin", map[string]string{"email": "MIKE@works.com", "message": "casino backlinks"}},
		{"bypass-r3-display-name", map[string]string{"email": "Bot <bot@example.com>", "message": "hello"}},

		// Sender edge cases.
		{"sender-uppercase", map[string]string{"email": "MIKE@Works.com", "message": "hello"}},
		{"sender-empty", map[string]string{"email": "", "message": "hello"}},
		{"sender-not-an-address", map[string]string{"email": "not-an-email", "message": "hello"}},
		{"sender-subdomain", map[string]string{"email": "a@mail.example.ru", "message": "hello"}},
		{"sender-lookalike-domain", map[string]string{"email": "a@notexample.ru", "message": "hello"}},
	}
}

// ruleSets exercise both kinds against every type, plus the precedence case.
func ruleSets() []struct {
	name  string
	rules []screen.Rule
} {
	r := func(id, kind, typ, val string) screen.Rule {
		return screen.Rule{ID: id, Kind: kind, Type: typ, Value: val}
	}
	return []struct {
		name  string
		rules []screen.Rule
	}{
		{"no-rules", nil},
		{"allow-email", []screen.Rule{r("A1", screen.KindAllow, screen.TypeEmail, "vip@customer.com")}},
		{"allow-email-mike", []screen.Rule{r("A2", screen.KindAllow, screen.TypeEmail, "mike@works.com")}},
		{"allow-domain", []screen.Rule{r("A3", screen.KindAllow, screen.TypeDomain, "customer.com")}},
		{"block-email", []screen.Rule{r("B1", screen.KindBlock, screen.TypeEmail, "bot@example.com")}},
		{"block-domain", []screen.Rule{r("B2", screen.KindBlock, screen.TypeDomain, "example.com")}},
		{"block-domain-ru", []screen.Rule{r("B3", screen.KindBlock, screen.TypeDomain, "example.ru")}},
		{"block-ip", []screen.Rule{r("B4", screen.KindBlock, screen.TypeIP, "203.0.113.5")}},
		{"block-cidr", []screen.Rule{r("B5", screen.KindBlock, screen.TypeCIDR, "203.0.113.0/24")}},
		{"block-keyword", []screen.Rule{r("B6", screen.KindBlock, screen.TypeKeyword, "widget")}},
		{"allow-beats-block", []screen.Rule{
			r("B7", screen.KindBlock, screen.TypeDomain, "customer.com"),
			r("A4", screen.KindAllow, screen.TypeEmail, "vip@customer.com"),
		}},
	}
}

// goldenCases is the full corpus, in a fixed order.
//
// The field × rule matrix at the default threshold is the bulk of it; the
// threshold sweep and the repeat-IP pass then vary one dimension at a time, so a
// diff points at which dimension changed.
func goldenCases() []goldenCase {
	const ip = "203.0.113.5"
	var cases []goldenCase

	for _, fs := range fieldSets() {
		for _, rs := range ruleSets() {
			cases = append(cases, goldenCase{
				name:      "matrix/" + fs.name + "/" + rs.name,
				fields:    fs.fields,
				ip:        ip,
				rules:     rs.rules,
				threshold: 6,
			})
		}
	}

	// Threshold sweep: the weights are absolute, so moving the threshold changes
	// which single signals are enough on their own.
	for _, th := range []int{4, 9, 20} {
		for _, fs := range fieldSets() {
			cases = append(cases, goldenCase{
				name:      fmt.Sprintf("threshold-%d/%s", th, fs.name),
				fields:    fs.fields,
				ip:        ip,
				threshold: th,
			})
		}
	}

	// Repeat-IP is stamped outside the scorer and weighted at the threshold, so
	// it holds on its own — except behind an allow rule, which skips it.
	//
	// Both sides of the boundary are sampled. prior=1 is the second submission
	// from this IP, which must NOT count as a repeat; prior=2 is the third, which
	// must. Recording only the second case is what let the threshold move without
	// the golden noticing.
	for _, prior := range []int{1, 2} {
		for _, fs := range fieldSets() {
			cases = append(cases, goldenCase{
				name:      fmt.Sprintf("repeat-prior-%d/%s", prior, fs.name),
				fields:    fs.fields,
				ip:        ip,
				threshold: 6,
				prior:     prior,
			})
		}
	}
	for _, rs := range ruleSets() {
		cases = append(cases, goldenCase{
			name:      "repeat-with-rules/" + rs.name,
			fields:    map[string]string{"email": "vip@customer.com", "message": "hello"},
			ip:        ip,
			rules:     rs.rules,
			threshold: 6,
			prior:     2,
		})
	}

	// An IP outside every block-ip/cidr rule, so those rules are characterised
	// in both directions.
	for _, rs := range ruleSets() {
		cases = append(cases, goldenCase{
			name:      "other-ip/" + rs.name,
			fields:    map[string]string{"email": "a@x.com", "message": "hello"},
			ip:        "198.51.100.9",
			rules:     rs.rules,
			threshold: 6,
		})
	}

	return cases
}
