package handler

import (
	"fmt"
	"math"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/screen"
)

// coords pulls the numeric pairs out of an SVG path so tests can assert on
// geometry rather than on string formatting.
func coords(t *testing.T, path string) [][2]float64 {
	t.Helper()
	var out [][2]float64
	for _, seg := range strings.Split(strings.NewReplacer("M", " ", "L", " ", "Z", " ").Replace(path), "  ") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		parts := strings.Fields(seg)
		if len(parts) != 2 {
			continue
		}
		x, err1 := strconv.ParseFloat(parts[0], 64)
		y, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("unparseable path segment %q in %q", seg, path)
		}
		out = append(out, [2]float64{x, y})
	}
	return out
}

func TestSparklineGeometry(t *testing.T) {
	t.Parallel()
	spark := Sparkline([]int{0, 5, 10}, 220, 40, 4)

	pts := coords(t, spark.Line)
	if len(pts) != 3 {
		t.Fatalf("got %d points, want 3: %q", len(pts), spark.Line)
	}
	// x spans the full width, evenly.
	if pts[0][0] != 0 {
		t.Errorf("first x = %v, want 0", pts[0][0])
	}
	if pts[2][0] != 220 {
		t.Errorf("last x = %v, want the full width 220", pts[2][0])
	}
	if math.Abs(pts[1][0]-110) > 0.05 {
		t.Errorf("middle x = %v, want 110", pts[1][0])
	}
	// y is inverted (SVG grows downward) and inset by the padding.
	if pts[0][1] <= pts[2][1] {
		t.Errorf("the lowest value should sit lower on screen: got y %v then %v", pts[0][1], pts[2][1])
	}
	if pts[2][1] < 4 {
		t.Errorf("peak y = %v, want it inset by the 4px padding", pts[2][1])
	}
	if pts[0][1] > 36 {
		t.Errorf("trough y = %v, want it inset by the 4px padding", pts[0][1])
	}
}

// A flat series has no range to normalise against. Dividing by a zero span
// would produce NaN and an SVG path the browser silently refuses to draw.
func TestSparklineFlatSeries(t *testing.T) {
	t.Parallel()
	for _, vals := range [][]int{{0, 0, 0}, {7, 7, 7}} {
		spark := Sparkline(vals, 220, 40, 4)
		if strings.Contains(spark.Line, "NaN") || strings.Contains(spark.Fill, "NaN") {
			t.Fatalf("flat series %v produced NaN: %q", vals, spark.Line)
		}
		pts := coords(t, spark.Line)
		if len(pts) != len(vals) {
			t.Errorf("flat series %v gave %d points, want %d", vals, len(pts), len(vals))
		}
		for _, p := range pts {
			if math.IsNaN(p[1]) {
				t.Fatalf("NaN y in %q", spark.Line)
			}
		}
	}
}

func TestSparklineDegenerateInput(t *testing.T) {
	t.Parallel()
	if got := Sparkline(nil, 220, 40, 4); got.Line != "" || got.Fill != "" {
		t.Errorf("Sparkline(nil) = %+v, want empty paths", got)
	}
	// One point cannot make a line; it must not divide by (len-1) == 0.
	got := Sparkline([]int{5}, 220, 40, 4)
	if strings.Contains(got.Line, "NaN") || strings.Contains(got.Line, "Inf") {
		t.Errorf("single-point sparkline = %q, want no NaN/Inf", got.Line)
	}
}

// The fill is the line closed down to the baseline, so it must start and end on
// it — otherwise the area renders as a floating wedge.
func TestSparklineFillIsClosedToBaseline(t *testing.T) {
	t.Parallel()
	spark := Sparkline([]int{1, 9, 3}, 220, 40, 4)
	if !strings.HasPrefix(spark.Fill, "M0 40") {
		t.Errorf("fill starts %q, want it to start on the baseline at M0 40", spark.Fill[:min(20, len(spark.Fill))])
	}
	if !strings.HasSuffix(spark.Fill, "L220 40 Z") {
		t.Errorf("fill ends %q, want it closed along the baseline", spark.Fill[max(0, len(spark.Fill)-20):])
	}
}

func TestStackedBars(t *testing.T) {
	t.Parallel()
	bars := StackedBars([]int{10, 0, 5}, []int{0, 2, 5}, 620, 100)
	if len(bars) != 3 {
		t.Fatalf("got %d bars, want 3", len(bars))
	}

	// The tallest total must reach the top of the plot, or the chart wastes its
	// vertical range and reads as flatter than the data is.
	top := bars[0].Y
	if math.Abs(top) > 0.01 {
		t.Errorf("tallest bar starts at y=%v, want 0 (the top gridline)", top)
	}
	// Segments stack: the held portion sits under the accepted portion.
	b := bars[2]
	if math.Abs((b.Y+b.H)-b.HeldY) > 0.01 {
		t.Errorf("bar 2 segments do not meet: accepted ends at %v, held starts at %v", b.Y+b.H, b.HeldY)
	}
	if math.Abs((b.HeldY+b.HeldH)-100) > 0.01 {
		t.Errorf("bar 2 does not sit on the baseline: held ends at %v, want 100", b.HeldY+b.HeldH)
	}
	// An empty day is a zero-height bar, not a missing one — the x spacing has
	// to stay even or the dates below stop lining up.
	if bars[1].H != 0 {
		t.Errorf("empty day should have zero accepted height, got %v", bars[1].H)
	}
	if bars[0].X >= bars[1].X || bars[1].X >= bars[2].X {
		t.Errorf("bar x positions are not increasing: %v %v %v", bars[0].X, bars[1].X, bars[2].X)
	}
}

func TestStackedBarsAllZero(t *testing.T) {
	t.Parallel()
	bars := StackedBars([]int{0, 0}, []int{0, 0}, 620, 100)
	if len(bars) != 2 {
		t.Fatalf("got %d bars, want 2", len(bars))
	}
	for i, b := range bars {
		if math.IsNaN(b.H) || math.IsNaN(b.Y) || math.IsNaN(b.HeldH) {
			t.Fatalf("bar %d has NaN from dividing by a zero maximum: %+v", i, b)
		}
		if b.H != 0 || b.HeldH != 0 {
			t.Errorf("bar %d should be empty, got %+v", i, b)
		}
	}
}

func TestGridLines(t *testing.T) {
	t.Parallel()
	lines := GridLines(90)
	if len(lines) != 4 {
		t.Fatalf("got %d gridlines, want 4", len(lines))
	}
	// Top line carries the maximum, bottom carries zero, positioned as
	// percentages down the plot.
	if lines[0].Value != 90 || lines[0].Top != 0 {
		t.Errorf("top line = %+v, want value 90 at 0%%", lines[0])
	}
	if lines[3].Value != 0 || lines[3].Top != 100 {
		t.Errorf("bottom line = %+v, want value 0 at 100%%", lines[3])
	}
}

func TestAgeSince(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		when time.Time
		want string
	}{
		{name: "seconds ago", when: now.Add(-20 * time.Second), want: "just now"},
		{name: "under a minute", when: now.Add(-59 * time.Second), want: "just now"},
		{name: "minutes", when: now.Add(-5 * time.Minute), want: "5m"},
		{name: "just under an hour", when: now.Add(-59 * time.Minute), want: "59m"},
		{name: "hours", when: now.Add(-3 * time.Hour), want: "3h"},
		{name: "just under a day", when: now.Add(-23 * time.Hour), want: "23h"},
		{name: "days", when: now.AddDate(0, 0, -6), want: "6d"},
		{name: "the retention edge", when: now.AddDate(0, 0, -30), want: "30d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ageSince(tt.when, now); got != tt.want {
				t.Errorf("ageSince(%v) = %q, want %q", tt.when, got, tt.want)
			}
		})
	}
}

// RuleIcon and RuleLabel both fall back rather than rendering nothing, because a
// spam_signals row written by a newer binary carries a value this one has never
// heard of — and a blank icon has no error, no console message and no
// broken-image glyph to notice.
func TestRuleIconAndLabelFallBack(t *testing.T) {
	t.Parallel()

	if got := RuleIcon(screen.CheckMarkup); got != "link" {
		t.Errorf("RuleIcon(markup) = %q, want %q", got, "link")
	}
	if got := RuleLabel(screen.CheckMarkup); got != "Link markup" {
		t.Errorf("RuleLabel(markup) = %q", got)
	}

	const unknown = screen.Check("invented_by_a_newer_binary")
	if got := RuleIcon(unknown); got != "shield-warning" {
		t.Errorf("RuleIcon(unknown) = %q, want the fallback glyph", got)
	}
	if got := RuleLabel(unknown); got != string(unknown) {
		t.Errorf("RuleLabel(unknown) = %q, want the raw value", got)
	}

	// Every declared rule must have both, or the quarantine panel renders a
	// nameless row with a blank icon.
	//
	// Ranged over screen.AllChecks rather than a list written out here. Restating
	// the constants made this test a second copy of the value set kept in step by
	// memory — the same defect the screen.Check type was introduced to end,
	// relocated from a doc comment into a test. (The count that used to sit in
	// this sentence was itself the thing the rule forbids.)
	for _, rule := range screen.AllChecks {
		if _, ok := ruleIcons[rule]; !ok {
			t.Errorf("rule %q has no icon", rule)
		}
		if _, ok := ruleLabels[rule]; !ok {
			t.Errorf("rule %q has no label", rule)
		}
	}
}

// Initial backs the avatar. An empty circle reads as a rendering bug rather than
// as a person, which is why it falls back to "?".
func TestInitial(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"Jane Doe", "J"},
		{"  padded", "P"},
		{"", "?"},
		{"   ", "?"},
		{"éclair", "É"},
		{"日本", "日"},
	}
	for _, tt := range tests {
		if got := Initial(tt.in); got != tt.want {
			t.Errorf("Initial(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Offset feeds every paged query in the admin. Off by one page and the first
// rows of every list are silently skipped, which no other assertion would catch.
func TestPaginationOffset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		page, size, total, want int
	}{
		{page: 1, size: 25, total: 612, want: 0},
		{page: 2, size: 25, total: 612, want: 25},
		{page: 3, size: 25, total: 612, want: 50},
		{page: 1, size: 50, total: 612, want: 0},
		{page: 4, size: 50, total: 612, want: 150},
		{page: 1, size: 25, total: 0, want: 0},
		{page: 999, size: 25, total: 612, want: 600}, // clamped to the last page
	}
	for _, tt := range tests {
		got := NewPagination(tt.page, tt.size, tt.total).Offset()
		if got != tt.want {
			t.Errorf("NewPagination(%d,%d,%d).Offset() = %d, want %d",
				tt.page, tt.size, tt.total, got, tt.want)
		}
	}
}

func TestPaginationFromQuery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query    string
		wantPage int
		wantSize int
	}{
		{query: "", wantPage: 1, wantSize: 25},
		{query: "?page=3&size=50", wantPage: 3, wantSize: 50},
		{query: "?page=2", wantPage: 2, wantSize: 25},
		{query: "?size=100", wantPage: 1, wantSize: 100},
		{query: "?page=abc&size=xyz", wantPage: 1, wantSize: 25},
		{query: "?page=-4&size=7", wantPage: 1, wantSize: 25},
		{query: "?size=100000", wantPage: 1, wantSize: 25}, // not an offered option
	}
	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/admin/forms/f1"+tt.query, nil)
		p := PaginationFrom(req, 1000)
		if p.Page != tt.wantPage || p.PageSize != tt.wantSize {
			t.Errorf("PaginationFrom(%q) = page %d size %d, want page %d size %d",
				tt.query, p.Page, p.PageSize, tt.wantPage, tt.wantSize)
		}
	}
	if got := NewPagination(1, 25, 10).Sizes(); len(got) == 0 {
		t.Error("Sizes() returned nothing; the rows-per-page selector would be empty")
	}
}

// TestViewBoxesContainWhatIsDrawnInThem is the invariant the viewBox functions
// claim to protect, asserted instead of assumed.
//
// They used to return hand-written literals — "0 0 220 40" sitting beside a
// sparkWidth of 220 — while every path was generated from the constants. Change
// a constant and the drawing moves while the viewBox stays, so the browser
// silently crops or rescales it. The function whose stated purpose is preventing
// that drift was the one place it could happen.
//
// Deriving the strings is the fix; this checks the thing that actually matters,
// which is that the coordinates land inside the box. That holds however the
// constants change, and a test comparing the string to the same constants that
// built it would prove nothing.
func TestViewBoxesContainWhatIsDrawnInThem(t *testing.T) {
	t.Parallel()

	// Deliberately awkward series: a flat one takes the pinned-to-the-middle
	// branch, a single value takes the no-span branch, and a spiky one pushes the
	// extremes.
	series := [][]int{
		{5, 5, 5, 5, 5},
		{0, 100, 0, 100, 0},
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		{42},
	}

	num := regexp.MustCompile(`-?\d+(?:\.\d+)?`)

	// inBox checks every coordinate pair in an SVG path against a viewBox.
	inBox := func(t *testing.T, name, box, path string) {
		t.Helper()
		var ox, oy, w, h float64
		if _, err := fmt.Sscanf(box, "%g %g %g %g", &ox, &oy, &w, &h); err != nil {
			t.Fatalf("%s viewBox %q is not four numbers: %v", name, box, err)
		}
		coords := num.FindAllString(path, -1)
		if len(coords) < 2 {
			t.Fatalf("%s path %q has no coordinates; the scan is not reading it", name, path)
		}
		for i := 0; i+1 < len(coords); i += 2 {
			x, _ := strconv.ParseFloat(coords[i], 64)
			y, _ := strconv.ParseFloat(coords[i+1], 64)
			if x < ox || x > ox+w || y < oy || y > oy+h {
				t.Errorf("%s draws at (%g, %g), outside its viewBox %q.\n"+
					"The browser crops or rescales silently, so this shows up as a "+
					"chart that looks subtly wrong rather than as an error.\npath: %s",
					name, x, y, box, path)
				return
			}
		}
	}

	for _, vals := range series {
		sp := Sparkline(vals, sparkWidth, sparkHeight, 4)
		inBox(t, "Sparkline.Line", SparkViewBox(), sp.Line)
		inBox(t, "Sparkline.Fill", SparkViewBox(), sp.Fill)

		fs := Sparkline(vals, sparkWidth, formSparkHeight, 4)
		inBox(t, "FormSparkline.Line", FormSparkViewBox(), fs.Line)
	}

	// StackedBars emits rectangles; the far corner of each is what has to fit.
	var ox, oy, w, h float64
	if _, err := fmt.Sscanf(ChartViewBox(), "%g %g %g %g", &ox, &oy, &w, &h); err != nil {
		t.Fatalf("ChartViewBox %q is not four numbers: %v", ChartViewBox(), err)
	}
	bars := StackedBars([]int{1, 5, 3, 9}, []int{0, 2, 1, 4}, ChartWidth, ChartHeight)
	if len(bars) == 0 {
		t.Fatal("StackedBars produced nothing; the assertion below is vacuous")
	}
	for i, b := range bars {
		right, bottom := b.X+BarWidth(), b.Y+b.H
		if b.X < ox || right > ox+w || b.Y < oy || bottom > oy+h {
			t.Errorf("bar %d spans x=[%g,%g] y=[%g,%g], outside viewBox %q",
				i, b.X, right, b.Y, bottom, ChartViewBox())
		}
		if heldBottom := b.HeldY + b.HeldH; b.HeldH > 0 && (b.HeldY < oy || heldBottom > oy+h) {
			t.Errorf("bar %d held segment spans y=[%g,%g], outside viewBox %q",
				i, b.HeldY, heldBottom, ChartViewBox())
		}
	}
}
