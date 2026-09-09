package handler

import (
	"math"
	"strconv"
	"strings"
	"testing"
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
