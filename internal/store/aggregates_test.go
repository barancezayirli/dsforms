package store

import (
	"testing"
	"time"
)

// seedSubmission writes an accepted submission stamped at a given time.
func seedSubmission(t *testing.T, s *Store, id, formID string, at time.Time) {
	t.Helper()
	if err := s.CreateSubmission(Submission{
		ID: id, FormID: formID, Data: map[string]string{"name": "Jane"},
		RawData: `{"name":"Jane"}`, CreatedAt: at,
	}); err != nil {
		t.Fatalf("CreateSubmission(%s): %v", id, err)
	}
}

// The zero-filling is the whole point of this query: a day with no submissions
// still needs its slot, or the chart's timeline silently compresses and a quiet
// week reads as a busy one.
func TestSubmissionsPerDayZeroFillsQuietDays(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	seedSubmission(t, s, "today", "f1", now)
	seedSubmission(t, s, "twoAgo", "f1", now.AddDate(0, 0, -2))
	if err := s.CreateHeldSubmission(heldFixture("heldToday", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	days, err := s.SubmissionsPerDay(3)
	if err != nil {
		t.Fatalf("SubmissionsPerDay: %v", err)
	}
	if len(days) != 3 {
		t.Fatalf("got %d buckets, want 3 — quiet days must still occupy a slot", len(days))
	}
	// Oldest first.
	if !days[0].Day.Before(days[2].Day) {
		t.Errorf("buckets are not oldest-first: %v then %v", days[0].Day, days[2].Day)
	}
	if days[0].Accepted != 1 {
		t.Errorf("two days ago accepted = %d, want 1", days[0].Accepted)
	}
	if days[1].Accepted != 0 || days[1].Held != 0 {
		t.Errorf("yesterday should be empty, got %+v", days[1])
	}
	if days[2].Accepted != 1 {
		t.Errorf("today accepted = %d, want 1", days[2].Accepted)
	}
	if days[2].Held != 1 {
		t.Errorf("today held = %d, want 1 — held rows are counted separately, not dropped", days[2].Held)
	}
}

func TestSubmissionsPerDayEmptyInstance(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	days, err := s.SubmissionsPerDay(7)
	if err != nil {
		t.Fatalf("SubmissionsPerDay: %v", err)
	}
	if len(days) != 7 {
		t.Errorf("got %d buckets on an empty instance, want 7 zeroed ones", len(days))
	}
}

func TestSubmissionsPerFormPerDay(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	seedForm(t, s, "f2")

	now := time.Now().UTC()
	seedSubmission(t, s, "a", "f1", now)
	seedSubmission(t, s, "b", "f1", now)
	seedSubmission(t, s, "c", "f2", now.AddDate(0, 0, -1))
	if err := s.CreateHeldSubmission(heldFixture("held", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	series, err := s.SubmissionsPerFormPerDay(3)
	if err != nil {
		t.Fatalf("SubmissionsPerFormPerDay: %v", err)
	}
	if len(series["f1"]) != 3 || len(series["f2"]) != 3 {
		t.Fatalf("series lengths = %d/%d, want 3 each", len(series["f1"]), len(series["f2"]))
	}
	if got := series["f1"][2]; got != 2 {
		t.Errorf("f1 today = %d, want 2 (held rows must not inflate the sparkline)", got)
	}
	if got := series["f2"][1]; got != 1 {
		t.Errorf("f2 yesterday = %d, want 1", got)
	}
}

func TestPerFormStats(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	seedSubmission(t, s, "a", "f1", now)
	seedSubmission(t, s, "b", "f1", now)
	if err := s.MarkRead("b"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if err := s.CreateHeldSubmission(heldFixture("held", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	stats, err := s.PerFormStats()
	if err != nil {
		t.Fatalf("PerFormStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d rows, want 1", len(stats))
	}
	got := stats[0]
	if got.Received != 2 || got.Held != 1 || got.Unread != 1 || got.Read != 1 {
		t.Errorf("stats = %+v, want Received 2 / Held 1 / Unread 1 / Read 1", got)
	}
}

// A form with no submissions at all must still appear, or an operator cannot
// see that it exists and is receiving nothing.
func TestPerFormStatsIncludesEmptyForms(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "quiet")

	stats, err := s.PerFormStats()
	if err != nil {
		t.Fatalf("PerFormStats: %v", err)
	}
	if len(stats) != 1 || stats[0].Received != 0 {
		t.Errorf("stats = %+v, want one row with zero received", stats)
	}
}

func TestRecentSubmissionsExcludesHeld(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	seedSubmission(t, s, "clean", "f1", now)
	if err := s.CreateHeldSubmission(heldFixture("spam", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	recent, err := s.RecentSubmissions(10)
	if err != nil {
		t.Fatalf("RecentSubmissions: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != "clean" {
		t.Fatalf("recent = %+v, want just the clean submission", recent)
	}
	if recent[0].FormName != "Contact" {
		t.Errorf("FormName = %q, want %q", recent[0].FormName, "Contact")
	}
	if recent[0].Data["name"] != "Jane" {
		t.Errorf("Data was not decoded: %v", recent[0].Data)
	}
}

func TestTopSpamSignals(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	for i, id := range []string{"a", "b", "c"} {
		signals := []SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 6}}
		if i == 0 {
			signals = append(signals, SpamSignal{Check: "keyword", Field: "message", Match: "casino", Weight: 5})
		}
		if err := s.CreateHeldSubmission(heldFixture(id, "f1", 8, now), 8, 6, signals); err != nil {
			t.Fatalf("CreateHeldSubmission(%s): %v", id, err)
		}
	}

	tallies, err := s.TopSpamSignals(30)
	if err != nil {
		t.Fatalf("TopSpamSignals: %v", err)
	}
	if len(tallies) != 2 {
		t.Fatalf("got %d rules, want 2: %+v", len(tallies), tallies)
	}
	if tallies[0].Check != "markup" || tallies[0].Hits != 3 {
		t.Errorf("top tally = %+v, want markup with 3 hits", tallies[0])
	}
	if tallies[0].Weight != 6 {
		t.Errorf("markup weight = %d, want the 6 it was recorded at", tallies[0].Weight)
	}
}

func TestHeldSince(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	seedSubmission(t, s, "clean", "f1", now)
	if err := s.CreateHeldSubmission(heldFixture("spam", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	held, total, err := s.HeldSince(30)
	if err != nil {
		t.Fatalf("HeldSince: %v", err)
	}
	if held != 1 || total != 2 {
		t.Errorf("HeldSince = %d held of %d, want 1 of 2", held, total)
	}
}

// TestStoredTimestampsAreQueryable is a regression test for a bug that unit
// tests alone could not catch: CreateHeldSubmission passed a time.Time straight
// to the driver, which stringified it as "2026-09-09 19:53:04 +0000 UTC".
// SQLite's date()/datetime() return NULL for that, and because created_at is
// TEXT, range comparisons against it become mismatched string comparisons — so
// the 30-day retention sweep was quietly comparing two different formats.
//
// Every write path that stamps a timestamp must produce the one text layout
// SQLite's own datetime('now') produces.
func TestStoredTimestampsAreQueryable(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	seedSubmission(t, s, "accepted", "f1", now)
	if err := s.CreateHeldSubmission(heldFixture("held", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	// Assert through SQLite's own date functions rather than by string-matching
	// the scanned value: the driver normalises a DATETIME column to RFC3339 when
	// you scan it into a string, so the read-back text is not the stored text.
	// What actually matters is whether SQLite can parse what we wrote.
	rows, err := s.db.Query("SELECT id, date(created_at), datetime(created_at), datetime(held_at), held_at FROM submissions")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	wantDay := now.Format("2006-01-02")
	seen := 0
	for rows.Next() {
		var id, heldAtRaw string
		var day, stamp, heldStamp *string
		if err := rows.Scan(&id, &day, &stamp, &heldStamp, &heldAtRaw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		if day == nil || stamp == nil {
			t.Errorf("submission %s: SQLite cannot parse created_at — date() returned NULL. "+
				"A time.Time handed straight to the driver stringifies as "+
				"\"... +0000 UTC\", which is unparseable and sorts wrongly.", id)
			continue
		}
		if *day != wantDay {
			t.Errorf("submission %s: date(created_at) = %q, want %q", id, *day, wantDay)
		}
		// held_at is empty for accepted rows; when set it must parse too.
		if heldAtRaw != "" && heldStamp == nil {
			t.Errorf("submission %s: SQLite cannot parse held_at %q", id, heldAtRaw)
		}
	}
	if seen != 2 {
		t.Fatalf("checked %d rows, want 2", seen)
	}
}

// PurgeHeldOlderThan compares a TEXT column, so its cutoff must be formatted
// the same way the stored values are or the comparison is nonsense.
func TestPurgeHeldUsesComparableTimestamps(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if err := s.CreateHeldSubmission(heldFixture("old", "f1", 8, now.AddDate(0, 0, -40)), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}
	if err := s.CreateHeldSubmission(heldFixture("new", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	n, err := s.PurgeHeldOlderThan(now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PurgeHeldOlderThan: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want exactly the one older than the cutoff", n)
	}
	held, _ := s.HeldSubmissions(10, 0)
	if len(held) != 1 || held[0].ID != "new" {
		t.Errorf("survivors = %+v, want only \"new\"", held)
	}
}
