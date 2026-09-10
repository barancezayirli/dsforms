package handler

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/filter"
	"github.com/barancezayirli/dsforms/internal/ratelimit"
	"github.com/barancezayirli/dsforms/internal/spam"
	"github.com/barancezayirli/dsforms/internal/store"
)

// templateDir is the real templates/ directory. Read from disk rather than
// embedded because go:embed cannot reach outside its own package directory —
// the same reason waitlist_admin_test.go uses ParseFiles.
const templateDir = "../../templates"

// realTemplates parses templates/ exactly as main.go does, including the
// funcmap. Kept in step with parseTemplates by TestRealTemplatesCoverEveryPage
// below, which fails if a page is added to one list and not the other.
func realTemplates(t *testing.T) map[string]*template.Template {
	t.Helper()

	funcMap := template.FuncMap{
		"add":              func(a, b int) int { return a + b },
		"sub":              func(a, b int) int { return a - b },
		"pct":              Percent,
		"SparkViewBox":     SparkViewBox,
		"FormSparkViewBox": FormSparkViewBox,
		"ChartViewBox":     ChartViewBox,
		"BarWidth":         BarWidth,
		"ruleIcon":         RuleIcon,
		"ruleLabel":        RuleLabel,
		"initial":          Initial,
	}

	base, err := template.New("base").Funcs(funcMap).ParseFiles(
		filepath.Join(templateDir, "base.html"), filepath.Join(templateDir, "icons.html"))
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}

	out := map[string]*template.Template{}
	for _, name := range basePageNames {
		clone, err := base.Clone()
		if err != nil {
			t.Fatalf("clone for %s: %v", name, err)
		}
		if _, err := clone.ParseFiles(filepath.Join(templateDir, name)); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out[name] = clone
	}
	return out
}

// basePageNames mirrors main.go's basePages. It cannot import it (main is not
// importable), so TestRealTemplatesCoverEveryPage guards the duplication by
// walking the templates directory.
var basePageNames = []string{
	"dashboard.html", "form_new.html", "form_edit.html", "form_detail.html",
	"submission_detail.html", "users.html", "users_new.html", "account.html",
	"backups.html", "waitlists.html", "waitlist_new.html", "waitlist_edit.html",
	"waitlist_detail.html", "broadcast_new.html", "broadcast_detail.html",
	"quarantine.html", "rules.html", "home.html", "search.html",
}

// TestBasePagesExecuteWithTheirRealData is the coverage the parse tests cannot
// give. html/template resolves struct fields at *execution*, so a template
// referencing a field that no longer exists parses cleanly and 500s in
// production — and every handler test substitutes an inline stub, so nothing
// else would notice.
//
// The fixtures are *populated*, not zero values. That distinction is the whole
// test: a nil slice or pointer means {{if}} and {{range}} bodies never run, so
// a zero-valued struct silently skips most of the template — including the
// quarantine breakdown panel, which is where the interesting fields live.
func TestBasePagesExecuteWithTheirRealData(t *testing.T) {
	t.Parallel()
	templates := realTemplates(t)

	for _, name := range basePageNames {
		data, ok := populatedPageData()[name]
		if !ok {
			t.Errorf("%s has no fixture in populatedPageData — add it, or the page is untested", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := templates[name].ExecuteTemplate(&buf, "base", data); err != nil {
				t.Fatalf("executing %s: %v", name, err)
			}
			if !strings.Contains(buf.String(), "</html>") {
				t.Errorf("%s rendered no closing document tag; output truncated?", name)
			}
			if strings.Contains(buf.String(), "<no value>") {
				t.Errorf("%s rendered \"<no value>\", which means a field did not resolve", name)
			}
			// Reaching </html> only proves the template ran, not that it ran the
			// branches that matter. Each marker appears solely inside a populated
			// branch, so a fixture that quietly stops taking one fails here.
			for _, marker := range pageMarkers[name] {
				if !strings.Contains(buf.String(), marker) {
					t.Errorf("%s did not render %q — the fixture is not taking the populated branch", name, marker)
				}
			}
		})
	}
}

// populatedPageData returns one fixture per base page, filled enough that every
// {{if}} and {{range}} in the template takes its populated branch.
func populatedPageData() map[string]any {
	sub := store.Submission{
		ID: "s1", FormID: "f1", IP: "203.0.113.5", SpamScore: 11, HeldThreshold: 6,
		Data:      map[string]string{"name": "Jane Doe", "email": "jane@example.com", "message": "Hello"},
		CreatedAt: time.Now(),
	}
	form := store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com",
		WebhookURL: "https://hooks.example.com/x", WebhookFormat: "generic", SpamThreshold: 6}
	signals := []store.SpamSignal{
		{Rule: spam.RuleMarkup, Field: "message", Match: "[url=", Weight: 6},
		{Rule: spam.RuleKeyword, Field: "message", Match: "backlinks", Weight: 5},
	}
	wl := store.Waitlist{ID: "w1", Name: "Launch", ConfirmSubject: "Welcome"}
	// Every field the shell can carry is set. Leaving one at its zero value
	// silently skips a {{if}} in base.html, which is the failure this whole test
	// exists to catch: Query unset meant search.html rendered its empty state and
	// the entire results branch never executed.
	shell := PageData{Title: "T", Active: "forms", CurrentUser: store.User{Username: "admin"},
		Flash: &FlashData{Type: "success", Message: "done"}, Version: "v1", Degraded: true,
		Query: "jane",
		Nav:   store.NavCounts{Unread: 3, Held: 12, Waitlist: 2},
		DB:    DBStatus{Name: "dsforms.db", Size: "1.2 MB", Journal: "wal"}}
	pager := NewPagination(2, 25, 612)

	held := heldRow{Submission: sub, FormName: "Contact", Signals: signals,
		From: "Jane Doe", Age: "2d"}

	return map[string]any{
		"dashboard.html": dashboardData{PageData: shell, TotalForms: 1, TotalUnread: 3, TotalAll: 9,
			Cards: []formCard{{Form: form, Total: 9, Unread: 3, Held: 1,
				Spark: Sparkline([]int{1, 4, 2}, sparkWidth, formSparkHeight, 3)}}},
		"form_new.html":  formNewData{PageData: shell, Form: form, Error: "bad"},
		"form_edit.html": formEditData{PageData: shell, Form: form, BaseURL: "https://x.example", Error: "bad"},
		"form_detail.html": formDetailData{PageData: shell, Form: form,
			Submissions: []store.Submission{sub}, TotalCount: 612, UnreadCount: 3,
			HeldCount: 1, HeldUnknown: false, Pager: pager},
		"submission_detail.html": submissionDetailData{PageData: shell, Form: form, Submission: sub,
			Fields:  []Field{{Key: "email", Value: "jane@example.com"}},
			Message: "Hello", Signals: signals,
			NewerID: "s0", OlderID: "s2", Position: 2, Total: 612,
			PositionKnown: true},
		"users.html": usersListData{PageData: shell, Error: "bad",
			Users: []UserWithYou{{User: store.User{ID: "u1", Username: "admin"}, IsYou: true}}},
		"users_new.html": usersNewData{PageData: shell, Error: "bad", FormUsername: "new"},
		"account.html":   accountData{PageData: shell, Error: "bad"},
		"backups.html":   backupPageData{PageData: shell},
		"waitlists.html": waitlistListData{PageData: shell,
			Waitlists: []store.WaitlistSummary{{Waitlist: wl, EntryCount: 42}}},
		"waitlist_new.html":  waitlistFormData{PageData: shell, Waitlist: wl, BaseURL: "https://x.example", Error: "bad"},
		"waitlist_edit.html": waitlistFormData{PageData: shell, Waitlist: wl, BaseURL: "https://x.example", Error: "bad"},
		"waitlist_detail.html": waitlistDetailData{PageData: shell, Waitlist: wl, TotalCount: 42,
			Entries: []store.WaitlistEntry{{ID: "e1", Email: "a@example.com", Position: 1, CreatedAt: time.Now()}},
			Page:    1, HasPrev: true, HasNext: true, PrevPage: 1, NextPage: 2},
		"broadcast_new.html": broadcastNewData{PageData: shell, Waitlist: wl, EntryCount: 42,
			RecipientCountKnown: true,
			Subject:             "Hi", Body: "Body", Error: "bad",
			Broadcasts: []store.BroadcastSummary{{Broadcast: store.Broadcast{ID: "b1", Subject: "Hi",
				Status: store.BroadcastStatusSending, CreatedAt: time.Now()}, Total: 42, Sent: 40}}},
		"broadcast_detail.html": broadcastDetailData{PageData: shell, Waitlist: wl,
			Broadcast: store.BroadcastSummary{Broadcast: store.Broadcast{ID: "b1", Subject: "Hi", Body: "B",
				Status: store.BroadcastStatusSending, CreatedAt: time.Now()}, Total: 42, Sent: 40, Pending: 2}},
		"quarantine.html": quarantineData{PageData: shell, Rows: []heldRow{held}, Selected: &held,
			MeterPercent: 92, MeterMax: 12, Threshold: 6, HeldTotal: 12, AwaitingCount: 12,
			HeldRecent: 146, TrafficRecent: 930, RetentionDays: 30, Pager: pager},
		"rules.html": filterRulesData{PageData: shell, Threshold: 6, Error: "bad",
			Block:    []filter.Rule{{ID: "r1", Kind: filter.KindBlock, Type: filter.TypeDomain, Value: "spam.example", Hits: 3, CreatedAt: time.Now()}},
			Allow:    []filter.Rule{{ID: "r2", Kind: filter.KindAllow, Type: filter.TypeEmail, Value: "vip@example.com", Note: "restored", CreatedAt: time.Now()}},
			Keywords: []filter.Rule{{ID: "r3", Kind: filter.KindBlock, Type: filter.TypeKeyword, Value: "crypto pump", CreatedAt: time.Now()}}},
		"home.html": overviewData{PageData: shell, Greeting: "Good afternoon, admin",
			Summary: "1 form", Range: 30, BarWidth: BarWidth(),
			KPIs: []kpi{{Icon: "tray", Label: "Submissions", Value: "784", Delta: "+12%", Sub: "vs 700", Spark: Sparkline([]int{1, 3, 2}, sparkWidth, sparkHeight, 4)}},
			Bars: StackedBars([]int{3, 1}, []int{0, 2}, ChartWidth, ChartHeight),
			Grid: GridLines(9), TickLabels: []string{"Feb 4", "Mar 4"},
			Signals:   []signalBar{{Label: "Link markup", Icon: "link", Hits: 3, Weight: 6, Percent: 80, Hot: true}},
			Forms:     []formRow{{FormStats: store.FormStats{FormID: "f1", Name: "Contact", Received: 9, Held: 1, Unread: 3, Read: 6}, ReadRate: 66, HeldHot: true}},
			Recent:    []recentRow{{RecentSubmission: store.RecentSubmission{Submission: sub, FormName: "Contact"}, Name: "Jane", Initial: "J", Preview: "Hello", Age: "2h"}},
			Activity:  []ratelimit.Activity{{IP: "203.0.113.5", Requests: 6, Throttled: 3, State: "throttled"}},
			RateLabel: "5 burst · 6 / min", TotalHeld: 12, RetentionDays: 30},
		"search.html": searchData{PageData: shell,
			Rows: []searchRow{{SearchResult: store.SearchResult{Submission: sub, FormName: "Contact"},
				Name: "Jane", Initial: "J", Preview: "Hello", Age: "2h"}}},
	}
}

// pageMarkers are strings that appear only when a page's populated branch runs.
// Without them a fixture can drift back to zero values and the execution test
// passes on a page that rendered nothing but its empty state — which is exactly
// what happened to search.html, whose whole results branch went unexecuted
// because the shared fixture never set Query.
//
// Every base page needs an entry, enforced by TestPageMarkersCoverEveryBasePage.
// A page with no meaningful populated/empty split gets an explicit empty slice
// and a reason, so opting out is a decision someone made rather than a page
// nobody thought about.
var pageMarkers = map[string][]string{
	// Pages whose populated branch is the thing worth pinning.
	"search.html":            {"/admin/forms/f1/submissions/s1", "jane"},
	"quarantine.html":        {"/admin/quarantine/s1/restore", "Link markup"},
	"dashboard.html":         {"/admin/forms/f1"},
	"form_detail.html":       {"/admin/forms/f1/submissions/s1"},
	"home.html":              {"203.0.113.5", "Contact", "Jane"},
	"rules.html":             {"spam.example"},
	"waitlists.html":         {"/admin/waitlists/w1"},
	"submission_detail.html": {"Link markup", "backlinks", "2 of 612"},
	"waitlist_detail.html":   {"a@example.com"},
	"broadcast_detail.html":  {"Hi"},
	"broadcast_new.html":     {"42 recipient"},
	"users.html":             {"admin"},

	// No populated/empty split: these render the same shape whatever the data,
	// so a marker would pin nothing. Stated rather than omitted, so the next
	// person adding a page has to make the same decision explicitly.
	"backups.html":       {}, // one conditional, on DB.Size, which the shell sets
	"form_new.html":      {}, // a form; no collections
	"form_edit.html":     {}, // a form; no collections
	"account.html":       {}, // a form
	"users_new.html":     {}, // a form
	"waitlist_new.html":  {}, // a form
	"waitlist_edit.html": {}, // a form
}

// TestPageMarkersCoverEveryBasePage makes opting out explicit.
//
// The marker table was hand-maintained with eight entries while nineteen pages
// existed, so five pages with real populated/empty splits — including
// submission_detail.html, the richest on the branch — silently had no guard at
// all. That is the same defect the markers were introduced to fix, one level up:
// a list maintained by memory.
func TestPageMarkersCoverEveryBasePage(t *testing.T) {
	t.Parallel()
	for _, name := range basePageNames {
		if _, ok := pageMarkers[name]; !ok {
			t.Errorf("%s has no pageMarkers entry — add the string that only its populated "+
				"branch renders, or an explicit empty entry with the reason", name)
		}
	}
	for name := range pageMarkers {
		found := false
		for _, n := range basePageNames {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pageMarkers has an entry for %q, which is not a base page", name)
		}
	}
}

// The quarantine panel is executed on its own for the fragment response, so it
// needs its own execution check — "held-panel" is never reached by rendering
// "base".
func TestQuarantinePanelFragmentExecutes(t *testing.T) {
	t.Parallel()
	templates := realTemplates(t)

	var buf bytes.Buffer
	if err := templates["quarantine.html"].ExecuteTemplate(&buf, "held-panel", populatedPageData()["quarantine.html"]); err != nil {
		t.Fatalf("executing held-panel: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("held-panel rendered nothing")
	}
}

// The reader drawer is likewise executed directly as a fragment.
func TestSubmissionDrawerFragmentExecutes(t *testing.T) {
	t.Parallel()
	templates := realTemplates(t)

	var buf bytes.Buffer
	if err := templates["submission_detail.html"].ExecuteTemplate(&buf, "drawer", populatedPageData()["submission_detail.html"]); err != nil {
		t.Fatalf("executing drawer: %v", err)
	}
	if !strings.Contains(buf.String(), `class="drawer"`) {
		t.Errorf("drawer fragment did not render the panel: %.120q", buf.String())
	}
}

// TestRealTemplatesCoverEveryPage guards the list above against drift: a new
// page added to templates/ and to main.go but not here would otherwise go
// untested silently.
func TestRealTemplatesCoverEveryPage(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(templateDir)
	if err != nil {
		t.Fatalf("read templates: %v", err)
	}
	known := map[string]bool{
		// Not base pages: the shell, the sprite, and the standalone documents
		// main_test.go covers.
		"base.html": true, "icons.html": true,
		"login.html": true, "success.html": true, "404.html": true, "500.html": true,
	}
	for _, name := range basePageNames {
		known[name] = true
	}
	for _, e := range entries {
		if e.IsDir() || known[e.Name()] {
			continue
		}
		t.Errorf("templates/%s is not covered by basePageNames — add it with its data struct", e.Name())
	}
}
