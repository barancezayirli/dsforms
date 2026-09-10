package handler

import (
	"log"
	"net/http"
	"time"

	"github.com/barancezayirli/dsforms/internal/ratelimit"
	"github.com/barancezayirli/dsforms/internal/store"
)

// OverviewHandler renders the admin home: what arrived, what is waiting, and
// what got blocked, in one view. Before this the landing page was the forms
// list, which answered none of those questions.
type OverviewHandler struct {
	Base

	// Limiter is read for the rate-limit panel. It is in-process and resets on
	// restart; the panel's copy says so rather than implying persistence that
	// does not exist.
	Limiter *ratelimit.Limiter

	RateBurst     int
	RatePerMinute int
	RetentionDays int
}

// kpi is one of the four headline cards.
type kpi struct {
	Icon  string
	Label string
	Value string
	Delta string
	Sub   string
	Spark Spark
}

// signalBar is one row of the "Top spam signals" panel.
type signalBar struct {
	Label   string
	Icon    string
	Hits    int
	Weight  int
	Percent int
	Hot     bool
}

// formRow is one row of the "By form" table.
type formRow struct {
	store.FormStats
	ReadRate int
	HeldHot  bool
}

// recentRow is one entry in the activity feed.
type recentRow struct {
	store.RecentSubmission
	Name    string
	Initial string
	Preview string
	Age     string
}

type overviewData struct {
	PageData
	Greeting string
	Summary  string
	Range    int

	KPIs []kpi

	Bars       []Bar
	BarWidth   float64
	Grid       []Grid
	TickLabels []string

	Signals   []signalBar
	Forms     []formRow
	Recent    []recentRow
	Activity  []ratelimit.Activity
	RateLabel string

	TotalHeld     int
	RetentionDays int
}

// ranges are the periods the header switcher offers.
var overviewRanges = []int{7, 30, 90}

// Page renders the overview.
func (h *OverviewHandler) Page(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("range"); v != "" {
		for _, allowed := range overviewRanges {
			if v == itoa(allowed) {
				days = allowed
			}
		}
	}

	data := overviewData{
		PageData:      h.Shell(w, r, "Home", "home"),
		Range:         days,
		BarWidth:      BarWidth(),
		RetentionDays: h.RetentionDays,
	}
	data.Greeting = greeting(time.Now()) + ", " + data.CurrentUser.Username

	series, err := h.Store.SubmissionsPerDay(days)
	if err != nil {
		log.Printf("overview: submissions per day: %v", err)
		data.Degraded = true
	}

	accepted := make([]int, 0, len(series))
	held := make([]int, 0, len(series))
	maxTotal, totalAccepted, totalHeld := 0, 0, 0
	for _, d := range series {
		accepted = append(accepted, d.Accepted)
		held = append(held, d.Held)
		totalAccepted += d.Accepted
		totalHeld += d.Held
		if t := d.Accepted + d.Held; t > maxTotal {
			maxTotal = t
		}
	}
	data.Bars = StackedBars(accepted, held, ChartWidth, ChartHeight)
	data.Grid = GridLines(maxTotal)
	data.TickLabels = tickLabels(series)

	nav := data.Nav
	data.TotalHeld = nav.Held

	// The previous window of the same length, so the delta is a comparison
	// rather than a number floating on its own.
	prev, err := h.Store.SubmissionsPerDay(days * 2)
	if err != nil {
		log.Printf("overview: previous window: %v", err)
		data.Degraded = true
	}
	prevAccepted := 0
	if len(prev) >= days {
		for _, d := range prev[:len(prev)-days] {
			prevAccepted += d.Accepted
		}
	}

	heldRecent, trafficRecent, err := h.Store.HeldSince(days)
	if err != nil {
		log.Printf("overview: held since: %v", err)
		data.Degraded = true
	}

	data.KPIs = []kpi{
		{
			Icon: "tray-arrow-down", Label: "Submissions",
			Value: itoa(totalAccepted),
			Delta: deltaLabel(totalAccepted, prevAccepted),
			Sub:   "vs. " + itoa(prevAccepted) + " previous " + itoa(days) + "d",
			Spark: Sparkline(accepted, sparkWidth, sparkHeight, 4),
		},
		{
			Icon: "envelope", Label: "Unread",
			Value: itoa(nav.Unread),
			Sub:   "across every form",
			Spark: Sparkline(accepted, sparkWidth, sparkHeight, 4),
		},
		{
			Icon: "shield-warning", Label: "Spam held",
			Value: itoa(heldRecent),
			Delta: itoa(Percent(heldRecent, trafficRecent)) + "%",
			Sub:   itoa(nav.Held) + " awaiting review",
			Spark: Sparkline(held, sparkWidth, sparkHeight, 4),
		},
		{
			Icon: "users-three", Label: "Waitlist",
			Value: itoa(nav.Waitlist),
			Sub:   "signups across every list",
			Spark: Sparkline(accepted, sparkWidth, sparkHeight, 4),
		},
	}

	if tallies, err := h.Store.TopSpamSignals(days); err != nil {
		log.Printf("overview: top signals: %v", err)
		data.Degraded = true
	} else {
		maxHits := 0
		for _, t := range tallies {
			if t.Hits > maxHits {
				maxHits = t.Hits
			}
		}
		for _, t := range tallies {
			p := Percent(t.Hits, maxHits)
			data.Signals = append(data.Signals, signalBar{
				Label: RuleLabel(t.Check), Icon: RuleIcon(t.Check),
				Hits: t.Hits, Weight: t.Weight, Percent: p, Hot: p > 60,
			})
		}
	}

	if stats, err := h.Store.PerFormStats(); err != nil {
		log.Printf("overview: per-form stats: %v", err)
		data.Degraded = true
	} else {
		for _, st := range stats {
			data.Forms = append(data.Forms, formRow{
				FormStats: st,
				ReadRate:  Percent(st.Read, st.Received),
				HeldHot:   st.Held > 0,
			})
		}
	}

	if recent, err := h.Store.RecentSubmissions(5); err != nil {
		log.Printf("overview: recent: %v", err)
		data.Degraded = true
	} else {
		for _, rs := range recent {
			name := senderLabel(rs.Data)
			data.Recent = append(data.Recent, recentRow{
				RecentSubmission: rs,
				Name:             name,
				Initial:          Initial(name),
				Preview:          previewOf(rs.Data),
				Age:              Age(rs.CreatedAt),
			})
		}
	}

	if h.Limiter != nil {
		data.Activity = h.Limiter.Snapshot(5)
	}
	data.RateLabel = itoa(h.RateBurst) + " burst · " + itoa(h.RatePerMinute) + " / min"

	forms := len(data.Forms)
	data.Summary = plural(forms, "form") + " on this instance"
	if nav.Held > 0 {
		data.Summary += " · " + plural(nav.Held, "submission") + " held in quarantine"
	}

	h.Render(w, "home.html", data)
}

// greeting picks a time-of-day salutation from the server's clock.
func greeting(t time.Time) string {
	switch h := t.Hour(); {
	case h < 12:
		return "Good morning"
	case h < 18:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}

// deltaLabel renders a period-over-period change, or "—" when there is no
// previous period to compare against. Showing "+100%" for a first week would be
// arithmetic rather than information.
func deltaLabel(now, prev int) string {
	if prev == 0 {
		return "—"
	}
	change := Percent(now-prev, prev)
	if change >= 0 {
		return "+" + itoa(change) + "%"
	}
	return itoa(change) + "%"
}

// tickLabels picks five evenly-spaced date labels for the chart's x axis.
func tickLabels(series []store.DayCounts) []string {
	if len(series) == 0 {
		return nil
	}
	const ticks = 5
	out := make([]string, 0, ticks)
	for i := 0; i < ticks; i++ {
		idx := i * (len(series) - 1) / (ticks - 1)
		out = append(out, series[idx].Day.Format("Jan 2"))
	}
	return out
}

// previewOf picks the message-ish field for the activity feed.
func previewOf(data map[string]string) string {
	for _, key := range []string{"message", "body", "content"} {
		if v, ok := data[key]; ok && v != "" {
			return v
		}
	}
	return ""
}
