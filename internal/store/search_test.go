package store

import (
	"testing"
	"time"
)

func seedForSearch(t *testing.T, s *Store) {
	t.Helper()
	seedForm(t, s, "f1")
	now := time.Now().UTC()
	rows := []struct{ id, name, email, msg string }{
		{"s1", "Jane Doe", "jane@example.com", "I loved your article about static sites"},
		{"s2", "Bob Smith", "bob@other.example", "Question about your pricing tiers"},
		{"s3", "Carol Jones", "carol@example.com", "Static hosting question, following up"},
	}
	for _, r := range rows {
		if err := s.CreateSubmission(Submission{
			ID: r.id, FormID: "f1",
			Data:      map[string]string{"name": r.name, "email": r.email, "message": r.msg},
			RawData:   `{"name":"` + r.name + `","email":"` + r.email + `","message":"` + r.msg + `"}`,
			CreatedAt: now,
		}); err != nil {
			t.Fatalf("CreateSubmission(%s): %v", r.id, err)
		}
	}
}

func ids(results []SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.ID)
	}
	return out
}

func TestSearchSubmissions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForSearch(t, s)

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "single word", query: "static", want: 2},
		{name: "case insensitive", query: "STATIC", want: 2},
		{name: "two words are ANDed", query: "static hosting", want: 1},
		{name: "matches an email address", query: "jane@example.com", want: 1},
		{name: "matches a name", query: "Carol", want: 1},
		{name: "no match", query: "kangaroo", want: 0},
		{name: "empty query returns nothing", query: "   ", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.SearchSubmissions(tt.query, AllForms(), 25)
			if err != nil {
				t.Fatalf("SearchSubmissions(%q): %v", tt.query, err)
			}
			if len(got) != tt.want {
				t.Errorf("SearchSubmissions(%q) = %v, want %d results", tt.query, ids(got), tt.want)
			}
		})
	}
}

// FTS5 has its own query syntax, and a stray quote or bare AND is a syntax
// error — which would reach the user as a 500 rather than as "no results".
// Nobody searching for O'Brien is writing a query language.
func TestSearchSurvivesHostileInput(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForSearch(t, s)

	for _, query := range []string{
		`"`, `""`, `O'Brien`, `AND`, `OR`, `NOT`, `NEAR`,
		`foo AND bar`, `(unbalanced`, `col:umn`, `^caret`, `star*`,
		`-minus`, `a"b"c`, `"""`, `{}`, `[]`, `%`, `--`, `;DROP TABLE submissions`,
	} {
		if _, err := s.SearchSubmissions(query, AllForms(), 25); err != nil {
			t.Errorf("SearchSubmissions(%q) returned an error: %v", query, err)
		}
	}

	// And the table is still there after the injection-shaped one.
	if got, err := s.SearchSubmissions("static", AllForms(), 25); err != nil || len(got) != 2 {
		t.Errorf("after hostile input: %v results, err %v", len(got), err)
	}
}

// Held submissions are excluded: search is for the inbox, and quarantine has
// its own screen.
func TestSearchExcludesHeld(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	if err := s.CreateSubmission(Submission{
		ID: "clean", FormID: "f1", Data: map[string]string{"message": "unicorn sighting"},
		RawData: `{"message":"unicorn sighting"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if err := s.CreateHeldSubmission(Submission{
		ID: "held", FormID: "f1", Data: map[string]string{"message": "unicorn casino"},
		RawData: `{"message":"unicorn casino"}`, CreatedAt: now,
	}, 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	got, err := s.SearchSubmissions("unicorn", AllForms(), 25)
	if err != nil {
		t.Fatalf("SearchSubmissions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "clean" {
		t.Errorf("got %v, want only the accepted submission", ids(got))
	}
}

// The triggers are what keep the index honest: nothing in application code
// remembers to index, so a delete must drop out of results too.
func TestSearchIndexFollowsWrites(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForSearch(t, s)

	if got, _ := s.SearchSubmissions("pricing", AllForms(), 25); len(got) != 1 {
		t.Fatalf("baseline: got %v, want 1", ids(got))
	}
	if err := s.DeleteSubmissions("f1", []string{"s2"}); err != nil {
		t.Fatalf("DeleteSubmissions: %v", err)
	}
	if got, _ := s.SearchSubmissions("pricing", AllForms(), 25); len(got) != 0 {
		t.Errorf("after delete: got %v, want none — the delete trigger did not fire", ids(got))
	}
}

// A database that predates the index — or a restored backup — has rows and an
// empty index. Opening it must backfill rather than silently return nothing.
func TestSearchRebuildsIndexForExistingRows(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForSearch(t, s)

	// Simulate the pre-feature state: rows present, index empty.
	if _, err := s.db.Exec("INSERT INTO submissions_fts(submissions_fts) VALUES ('delete-all')"); err != nil {
		t.Fatalf("clearing index: %v", err)
	}
	if got, _ := s.SearchSubmissions("static", AllForms(), 25); len(got) != 0 {
		t.Fatalf("fixture is wrong: index should be empty, got %v", ids(got))
	}

	if err := syncSearchIndex(s.db); err != nil {
		t.Fatalf("syncSearchIndex: %v", err)
	}
	if got, _ := s.SearchSubmissions("static", AllForms(), 25); len(got) != 2 {
		t.Errorf("after rebuild: got %v, want 2", ids(got))
	}
}

func TestSearchLimit(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForSearch(t, s)
	if got, _ := s.SearchSubmissions("example.com", AllForms(), 1); len(got) != 1 {
		t.Errorf("limit not honoured: got %d results, want 1", len(got))
	}
}

// TestFTSQuery pins the sanitiser directly. The hostile-input test only asserts
// "no error", so it cannot catch ftsQuery becoming *over*-aggressive: strip
// apostrophes, hyphens or underscores from the character allowlist and every
// one of those cases still passes while search quietly stops finding O'Brien
// and order_id.
func TestFTSQuery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "single word", in: "static", want: `"static"`},
		{name: "two words are ANDed", in: "static hosting", want: `"static" AND "hosting"`},
		{name: "an apostrophe survives", in: "O'Brien", want: `"O'Brien"`},
		{name: "a hyphen survives", in: "re-order", want: `"re-order"`},
		{name: "an underscore survives", in: "order_id", want: `"order_id"`},
		{name: "an address survives intact", in: "jane@example.com", want: `"jane@example.com"`},
		{name: "operators are quoted, not interpreted", in: "AND", want: `"AND"`},
		{name: "punctuation is stripped", in: "budget?", want: `"budget"`},
		{name: "a quote splits the token rather than escaping into it", in: `a"b`, want: `"a" AND "b"`},
		{name: "parens are stripped", in: "(unbalanced", want: `"unbalanced"`},
		{name: "empty input yields nothing", in: "   ", want: ""},
		{name: "punctuation-only yields nothing", in: `"""`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ftsQuery(tt.in); got != tt.want {
				t.Errorf("ftsQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// And end to end, against rows containing exactly the characters most likely to
// be over-sanitised.
func TestSearchFindsPunctuatedTerms(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	rows := []struct{ id, msg string }{
		{"s1", "Please contact O'Brien about the re-order"},
		{"s2", "Reference order_id 88213 for the invoice"},
	}
	for _, r := range rows {
		if err := s.CreateSubmission(Submission{
			ID: r.id, FormID: "f1", Data: map[string]string{"message": r.msg},
			RawData: `{"message":"` + r.msg + `"}`, CreatedAt: now,
		}); err != nil {
			t.Fatalf("CreateSubmission(%s): %v", r.id, err)
		}
	}

	for _, q := range []string{"O'Brien", "re-order", "order_id"} {
		got, err := s.SearchSubmissions(q, AllForms(), 25)
		if err != nil {
			t.Fatalf("SearchSubmissions(%q): %v", q, err)
		}
		if len(got) != 1 {
			t.Errorf("SearchSubmissions(%q) found %d rows, want 1 — over-sanitised?", q, len(got))
		}
	}
}
