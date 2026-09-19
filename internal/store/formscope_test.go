package store

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The zero value denying is the property every scoped query rests on. If a read
// path added later forgets to set a scope, it must return nothing and be
// noticed, not quietly hand back every form's submissions.
func TestTheZeroFormScopeMatchesNothing(t *testing.T) {
	t.Parallel()

	var unset FormScope
	tests := []struct {
		name  string
		scope FormScope
		want  bool // does it allow a form?
	}{
		{"zero value", unset, false},
		{"OnlyForms(nil)", OnlyForms(nil), false},
		{"OnlyForms(empty)", OnlyForms([]string{}), false},
		{"OnlyForms of another form", OnlyForms([]string{"other"}), false},
		{"OnlyForms of this form", OnlyForms([]string{"f1"}), true},
		{"AllForms", AllForms(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.scope.Allows("f1"); got != tc.want {
				t.Errorf("Allows(f1) = %v, want %v", got, tc.want)
			}
		})
	}
}

// clause is the half Allows cannot check: a scope that says no to Allows but
// emits no SQL would filter nothing at all.
func TestTheZeroFormScopeEmitsARefusingClause(t *testing.T) {
	t.Parallel()

	var unset FormScope
	for _, tc := range []struct {
		name  string
		scope FormScope
	}{
		{"zero value", unset},
		{"OnlyForms(nil)", OnlyForms(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sql, args := tc.scope.clause("form_id")
			if strings.TrimSpace(sql) == "" {
				t.Fatalf("clause = %q with args %v — an empty clause filters nothing", sql, args)
			}
			if len(args) != 0 {
				t.Errorf("args = %v, want none for a scope that matches nothing", args)
			}
		})
	}

	if sql, args := AllForms().clause("form_id"); sql != "" || len(args) != 0 {
		t.Errorf("AllForms clause = %q %v, want no clause at all", sql, args)
	}

	sql, args := OnlyForms([]string{"a", "b"}).clause("s.form_id")
	if !strings.Contains(sql, "s.form_id") {
		t.Errorf("clause = %q, want it to name the column it was given", sql)
	}
	if len(args) != 2 {
		t.Errorf("args = %v, want one per form id", args)
	}
}

// ParseFormScope is the one place "" is read as every form. It has to be,
// because that is what the column holds for every token that existed before
// scoping did, and an upgrade must not silently revoke access.
func TestParseFormScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stored    string
		allowsAny bool
		allowsF1  bool
	}{
		{"empty is every form, for the upgrade path", "", true, true},
		{"whitespace only is also every form", "   ", true, true},
		{"one id", "f1", false, true},
		{"several ids", "f2,f1", false, true},
		{"padded ids", " f2 , f1 ", false, true},
		{"another form's id", "f2", false, false},
		{"separators with nothing between them grant nothing", ",,", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := ParseFormScope(tc.stored)
			if got := scope.Allows("anything-at-all"); got != tc.allowsAny {
				t.Errorf("Allows(arbitrary) = %v, want %v", got, tc.allowsAny)
			}
			if got := scope.Allows("f1"); got != tc.allowsF1 {
				t.Errorf("Allows(f1) = %v, want %v", got, tc.allowsF1)
			}
		})
	}
}

// A scoped token is the reason this type exists, so the round trip through
// storage has to preserve exactly which forms it named.
func TestAPITokensCarryTheirForms(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	_, scoped, err := s.CreateAPIToken(u.ID, "one form", []string{"read"}, []string{"f2", "f1"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if len(scoped.FormIDs) != 2 {
		t.Fatalf("FormIDs = %v, want both", scoped.FormIDs)
	}

	raw, _, err := s.CreateAPIToken(u.ID, "every form", []string{"read"}, nil, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	// nil is "every form" on the way in, because that is what the admin form
	// sends when the operator leaves it at All forms.
	back, err := s.GetAPIToken(raw)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	if len(back.FormIDs) != 0 {
		t.Errorf("FormIDs = %v, want none recorded", back.FormIDs)
	}
	if !back.Scope().Allows("anything") {
		t.Error("a token created with no forms named cannot reach any form")
	}

	listed, err := s.ListAPITokens(u.ID)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	var found bool
	for _, tok := range listed {
		if tok.ID == scoped.ID {
			found = true
			if tok.Scope().Allows("f3") || !tok.Scope().Allows("f1") {
				t.Errorf("listed scope is wrong: %v", tok.FormIDs)
			}
		}
	}
	if !found {
		t.Fatalf("the scoped token is not in the listing")
	}
}

// seedTwoForms plants two forms with distinguishable content, plus one held
// submission each, so a scoped read has something it must not return.
func seedTwoForms(t *testing.T, s *Store) {
	t.Helper()
	for _, f := range []Form{
		{ID: "mine", Name: "Mine", EmailTo: "me@example.com"},
		{ID: "theirs", Name: "Theirs", EmailTo: "them@example.com"},
	} {
		if err := s.CreateForm(f); err != nil {
			t.Fatalf("CreateForm(%s): %v", f.ID, err)
		}
	}
	base := time.Now().UTC().Truncate(time.Second)
	for i, spec := range []struct{ id, form, word string }{
		{"m1", "mine", "quarklight"},
		{"t1", "theirs", "zephyrine"},
	} {
		if err := s.CreateSubmission(Submission{
			ID: spec.id, FormID: spec.form,
			RawData:   `{"message":"about ` + spec.word + `"}`,
			CreatedAt: base.Add(-time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("CreateSubmission(%s): %v", spec.id, err)
		}
		if err := s.CreateHeldSubmission(
			Submission{ID: spec.id + "h", FormID: spec.form,
				RawData: `{"message":"held ` + spec.word + `"}`, CreatedAt: base},
			9, 6, []SpamSignal{{Check: "markup", Field: "message", Match: "x", Weight: 9}},
		); err != nil {
			t.Fatalf("CreateHeldSubmission(%sh): %v", spec.id, err)
		}
	}
}

// TestEveryScopedReadHonoursTheScope walks the reads the MCP path makes and
// asserts none of them returns the other form's rows — and that each returns
// the in-scope ones, so a method that simply broke could not pass.
func TestEveryScopedReadHonoursTheScope(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedTwoForms(t, s)
	mine := OnlyForms([]string{"mine"})

	t.Run("ListForms", func(t *testing.T) {
		forms, err := s.ListForms(mine)
		if err != nil {
			t.Fatalf("ListForms: %v", err)
		}
		if len(forms) != 1 || forms[0].ID != "mine" {
			t.Fatalf("forms = %+v, want only mine", forms)
		}
	})

	t.Run("ListSubmissionsFiltered", func(t *testing.T) {
		subs, err := s.ListSubmissionsFiltered("", ReadAny, mine, 50, 0)
		if err != nil {
			t.Fatalf("ListSubmissionsFiltered: %v", err)
		}
		assertOnlyMine(t, subs)
	})

	t.Run("HeldSubmissions", func(t *testing.T) {
		subs, err := s.HeldSubmissions(mine, 50, 0)
		if err != nil {
			t.Fatalf("HeldSubmissions: %v", err)
		}
		assertOnlyMine(t, subs)
	})

	t.Run("SearchSubmissions", func(t *testing.T) {
		hits, err := s.SearchSubmissions("zephyrine", mine, 50)
		if err != nil {
			t.Fatalf("SearchSubmissions: %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("searching for the other form's word returned %+v", hits)
		}
		mineHits, err := s.SearchSubmissions("quarklight", mine, 50)
		if err != nil {
			t.Fatalf("SearchSubmissions: %v", err)
		}
		if len(mineHits) != 1 {
			t.Errorf("searching for my own word returned %d rows, want 1", len(mineHits))
		}
	})

	t.Run("CountAllSubmissions", func(t *testing.T) {
		n, err := s.CountAllSubmissions(mine)
		if err != nil {
			t.Fatalf("CountAllSubmissions: %v", err)
		}
		if n != 1 {
			t.Errorf("count = %d, want 1 (one accepted submission on my form)", n)
		}
	})

	t.Run("NavCounts", func(t *testing.T) {
		n, err := s.NavCounts(mine)
		if err != nil {
			t.Fatalf("NavCounts: %v", err)
		}
		if n.Unread != 1 || n.Held != 1 {
			t.Errorf("counts = %+v, want one unread and one held", n)
		}
		// The waitlist has no form, so a scoped call cannot answer it. It says
		// so rather than reporting a zero it did not measure.
		if n.WaitlistKnown {
			t.Error("a scoped NavCounts claimed to know the waitlist count")
		}
	})

	t.Run("PerFormStats", func(t *testing.T) {
		stats, err := s.PerFormStats(mine)
		if err != nil {
			t.Fatalf("PerFormStats: %v", err)
		}
		for _, st := range stats {
			if st.FormID == "theirs" || st.Name == "Theirs" {
				t.Errorf("the other form appeared: %+v", st)
			}
		}
		if len(stats) != 1 {
			t.Errorf("stats = %+v, want one row", stats)
		}
	})

	t.Run("HeldSince", func(t *testing.T) {
		held, total, err := s.HeldSince(7, mine)
		if err != nil {
			t.Fatalf("HeldSince: %v", err)
		}
		if held != 1 || total != 2 {
			t.Errorf("held=%d total=%d, want 1 and 2 — my form's rows only", held, total)
		}
	})

	t.Run("SubmissionsPerDay", func(t *testing.T) {
		days, err := s.SubmissionsPerDay(7, mine)
		if err != nil {
			t.Fatalf("SubmissionsPerDay: %v", err)
		}
		sum := 0
		for _, d := range days {
			sum += d.Accepted + d.Held
		}
		if sum != 2 {
			t.Errorf("counted %d submissions across the window, want 2", sum)
		}
	})

	t.Run("TopSpamSignals", func(t *testing.T) {
		tallies, err := s.TopSpamSignals(7, mine)
		if err != nil {
			t.Fatalf("TopSpamSignals: %v", err)
		}
		hits := 0
		for _, tl := range tallies {
			hits += tl.Hits
		}
		if hits != 1 {
			t.Errorf("counted %d signals, want 1 — the other form's held row must not be tallied", hits)
		}
	})

	t.Run("DeleteHeld", func(t *testing.T) {
		// A mixed list: the out-of-scope id must match nothing, and the count
		// must report what actually went rather than what was asked for.
		n, err := s.DeleteHeld([]string{"m1h", "t1h"}, mine)
		if err != nil {
			t.Fatalf("DeleteHeld: %v", err)
		}
		if n != 1 {
			t.Errorf("deleted %d, want 1", n)
		}
		if _, err := s.GetHeldSubmission("t1h"); err != nil {
			t.Errorf("the other form's held submission was deleted: %v", err)
		}
	})
}

func assertOnlyMine(t *testing.T, subs []Submission) {
	t.Helper()
	if len(subs) == 0 {
		t.Fatal("no rows at all, so this asserted nothing")
	}
	for _, sub := range subs {
		if sub.FormID != "mine" {
			t.Errorf("returned a submission from %q", sub.FormID)
		}
	}
}

// TestAScopedListingFiltersInSQLNotAfterPaging.
//
// This branch has already shipped this bug once, in the read filter: thinning
// the page that LIMIT and OFFSET already chose means rows behind a screenful of
// out-of-scope ones report as not existing. Here it would read as "the form you
// are scoped to is empty", which is the most convincing wrong answer available.
func TestAScopedListingFiltersInSQLNotAfterPaging(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedTwoForms(t, s)

	// A full page of the other form's submissions, all newer than mine, so a
	// post-filtered first page would contain nothing of mine at all.
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 30; i++ {
		if err := s.CreateSubmission(Submission{
			ID: "pad" + strconv.Itoa(i), FormID: "theirs",
			RawData: `{"message":"padding"}`, CreatedAt: base.Add(time.Duration(i+1) * time.Hour),
		}); err != nil {
			t.Fatalf("CreateSubmission: %v", err)
		}
	}

	subs, err := s.ListSubmissionsFiltered("", ReadAny, OnlyForms([]string{"mine"}), 25, 0)
	if err != nil {
		t.Fatalf("ListSubmissionsFiltered: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("got %d rows, want 1 — my form's submission sits behind a page of the other form's", len(subs))
	}
	if subs[0].FormID != "mine" {
		t.Errorf("returned a submission from %q", subs[0].FormID)
	}
}

// TestAnUnscopedReadStillSeesEverything is the other direction. Every one of
// these methods serves the admin too, and the admin is not scoped.
func TestAnUnscopedReadStillSeesEverything(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedTwoForms(t, s)

	forms, err := s.ListForms(AllForms())
	if err != nil {
		t.Fatalf("ListForms: %v", err)
	}
	if len(forms) != 2 {
		t.Errorf("forms = %d, want both", len(forms))
	}

	n, err := s.NavCounts(AllForms())
	if err != nil {
		t.Fatalf("NavCounts: %v", err)
	}
	if n.Unread != 2 || n.Held != 2 {
		t.Errorf("counts = %+v, want both forms' rows", n)
	}
	if !n.WaitlistKnown {
		t.Error("an unscoped NavCounts must be able to answer the waitlist count")
	}
}

// TestAScopeThatNamesNothingReadsNothing is the zero value reaching SQL rather
// than only Allows. A forgotten scope must return an empty page.
func TestAScopeThatNamesNothingReadsNothing(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedTwoForms(t, s)

	var unset FormScope
	subs, err := s.ListSubmissionsFiltered("", ReadAny, unset, 50, 0)
	if err != nil {
		t.Fatalf("ListSubmissionsFiltered: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("an unset scope returned %d rows", len(subs))
	}
	forms, err := s.ListForms(unset)
	if err != nil {
		t.Fatalf("ListForms: %v", err)
	}
	if len(forms) != 0 {
		t.Errorf("an unset scope returned %d forms", len(forms))
	}
}

// TestUpgradeFromAnUnscopedTokenTable is the migration test that matters here.
//
// Every other test starts from the current schema, so none of them would notice
// if the form_ids ALTER stopped running — and the failure mode is not a missing
// feature but a revoked credential: a token minted before scoping existed has
// no form_ids column to read, and if the upgrade left it empty *and* empty
// meant "no forms", every MCP client an operator has would stop working on
// restart with nothing to say why.
func TestUpgradeFromAnUnscopedTokenTable(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/legacy.db"

	// users and api_tokens exactly as they were before scoping.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE api_tokens (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT '',
			token_hash TEXT NOT NULL UNIQUE,
			scopes TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			last_used_at DATETIME NOT NULL DEFAULT '',
			expires_at DATETIME NOT NULL DEFAULT ''
		);
		INSERT INTO users (id, username) VALUES ('u1', 'olduser');
		INSERT INTO api_tokens (id, user_id, name, token_hash, scopes)
			VALUES ('t1', 'u1', 'laptop', 'somehash', 'read,write');
	`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	s, err := New(path)
	if err != nil {
		t.Fatalf("New() on a pre-scoping database failed: %v", err)
	}
	defer s.Close()

	tokens, err := s.ListAPITokens("u1")
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens = %d, want the one that was already there", len(tokens))
	}
	if len(tokens[0].FormIDs) != 0 {
		t.Errorf("FormIDs = %v, want none recorded", tokens[0].FormIDs)
	}
	if !tokens[0].Scope().All() {
		t.Error("a token that predates scoping no longer reaches every form — " +
			"upgrading in place has revoked a live credential")
	}
	if !tokens[0].Scope().Allows("any-form-at-all") {
		t.Error("the migrated token reaches no form")
	}
}
