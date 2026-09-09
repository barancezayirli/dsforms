package handler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/spam"
)

// Charts are hand-built inline SVG rendered from aggregate rows. A charting
// library would be a runtime dependency and a build step, which is exactly what
// "one Go binary, no JS framework" rules out — and these three shapes are all
// the admin needs.
//
// Every path is emitted in a fixed viewBox and drawn with
// preserveAspectRatio="none", so the SVG stretches to whatever width its card
// gets. That is why gridlines are HTML rather than SVG lines: a stroke inside a
// stretched viewBox would be smeared horizontally.

// Spark is the two paths of a sparkline: the line itself and the area beneath.
type Spark struct {
	Line string
	Fill string
}

// Sparkline normalises values into a w×h box and returns the line path plus the
// same path closed down to the baseline for the area fill.
//
// pad insets the curve vertically so the stroke is not clipped at the extremes.
func Sparkline(values []int, w, h, pad float64) Spark {
	if len(values) == 0 {
		return Spark{}
	}

	minV, maxV := values[0], values[0]
	for _, v := range values {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	// A flat series has no range. Dividing by a zero span yields NaN and an SVG
	// path the browser silently declines to draw, so the series is pinned to the
	// middle of the box instead.
	span := float64(maxV - minV)
	flat := span == 0

	points := make([]string, 0, len(values))
	for i, v := range values {
		var x float64
		if len(values) > 1 {
			x = float64(i) / float64(len(values)-1) * w
		}
		y := h / 2
		if !flat {
			y = h - pad - ((float64(v-minV) / span) * (h - pad*2))
		}
		points = append(points, fmt.Sprintf("%.1f %.1f", x, y))
	}

	line := "M" + strings.Join(points, " L")
	fill := fmt.Sprintf("M0 %.0f L", h) + strings.Join(points, " L") + fmt.Sprintf(" L%.0f %.0f Z", w, h)
	return Spark{Line: line, Fill: fill}
}

// Bar is one day's stacked column: an accepted segment above a held segment.
type Bar struct {
	X     float64
	Y     float64
	H     float64
	HeldY float64
	HeldH float64
}

// StackedBars lays out one column per day within a w×h viewBox, scaled so the
// busiest day reaches the top of the plot.
//
// Both slices are indexed by day and must be the same length; a day with no
// submissions still gets a zero-height bar so the x spacing stays even and the
// date ticks underneath keep lining up.
func StackedBars(accepted, held []int, w, h float64) []Bar {
	n := len(accepted)
	if len(held) < n {
		n = len(held)
	}
	if n == 0 {
		return nil
	}

	maxTotal := 0
	for i := 0; i < n; i++ {
		if t := accepted[i] + held[i]; t > maxTotal {
			maxTotal = t
		}
	}

	step := w / float64(n)
	bars := make([]Bar, 0, n)
	for i := 0; i < n; i++ {
		var ah, hh float64
		// A period with no submissions at all leaves every bar empty rather
		// than dividing by zero.
		if maxTotal > 0 {
			ah = float64(accepted[i]) / float64(maxTotal) * h
			hh = float64(held[i]) / float64(maxTotal) * h
		}
		bars = append(bars, Bar{
			X:     float64(i)*step + step/2 - barWidth/2,
			Y:     h - ah - hh,
			H:     ah,
			HeldY: h - hh,
			HeldH: hh,
		})
	}
	return bars
}

// barWidth is the column width in viewBox units. Because the SVG stretches, the
// rendered width scales with the card rather than being 14 CSS pixels.
const barWidth = 14

// BarWidth exposes the column width to templates.
func BarWidth() float64 { return barWidth }

// Grid is one horizontal gridline: a value label and how far down the plot it
// sits, as a percentage.
type Grid struct {
	Value int
	Top   float64
}

// GridLines returns the four labelled gridlines behind a chart, top (max) to
// bottom (zero).
//
// They are rendered as absolutely-positioned HTML rather than SVG lines,
// because the chart's SVG uses preserveAspectRatio="none" and would stretch any
// stroke drawn inside it.
func GridLines(maxValue int) []Grid {
	const steps = 4
	lines := make([]Grid, 0, steps)
	for i := 0; i < steps; i++ {
		frac := float64(i) / float64(steps-1)
		lines = append(lines, Grid{
			Value: int(float64(maxValue) * (1 - frac)),
			Top:   frac * 100,
		})
	}
	return lines
}

// Percent returns part/total as a rounded percentage, guarding the empty case
// so a brand-new instance shows 0% rather than NaN%.
func Percent(part, total int) int {
	if total <= 0 {
		return 0
	}
	return int(float64(part) / float64(total) * 100)
}

// Sparkline geometry shared by the forms grid and the overview KPIs. The
// viewBox is fixed and the SVG stretches, so these are proportions rather than
// pixel sizes.
const (
	sparkDays       = 30
	sparkWidth      = 220
	sparkHeight     = 40
	formSparkHeight = 34
)

// SparkViewBox and friends expose the geometry to templates so the viewBox in
// the markup cannot drift from the coordinates the paths were generated in.
func SparkViewBox() string     { return "0 0 220 40" }
func FormSparkViewBox() string { return "0 0 220 34" }

// ChartViewBox is the coordinate space StackedBars emits into.
func ChartViewBox() string { return "0 0 620 100" }

// ChartWidth and ChartHeight are those dimensions as numbers, for callers
// generating the bars.
const (
	ChartWidth  = 620.0
	ChartHeight = 100.0
)

// ruleIcons maps a stored spam signal to its Phosphor glyph and human label.
//
// The rule strings come from internal/spam and from the submit handler, and are
// stored verbatim in spam_signals. An unknown rule — a row written by an older
// or newer binary — must still render something, so both lookups fall back
// rather than producing a blank icon and an empty label.
// Keyed by spam.Rule, so adding a value to that type without giving it a label
// is a compile-time prompt rather than a blank icon nobody notices.
var ruleIcons = map[spam.Rule]string{
	spam.RuleMarkup:     "link",
	spam.RuleKeyword:    "text-aa",
	spam.RuleSQL:        "bug",
	spam.RuleGibberish:  "question",
	spam.RuleURLInName:  "user-focus",
	spam.RuleExtraLinks: "link",
	spam.RuleRepeatIP:   "fingerprint",
	spam.RuleBlocked:    "prohibit",
}

var ruleLabels = map[spam.Rule]string{
	spam.RuleMarkup:     "Link markup",
	spam.RuleKeyword:    "Keyword hit",
	spam.RuleSQL:        "SQL probe",
	spam.RuleGibberish:  "Gibberish token",
	spam.RuleURLInName:  "URL in a name field",
	spam.RuleExtraLinks: "Multiple raw URLs",
	spam.RuleRepeatIP:   "Repeat submissions from this IP",
	spam.RuleBlocked:    "Blocked by a filter rule",
}

// RuleIcon returns the icon name for a spam rule. The fallback matters: a row
// written by a newer binary must still render something rather than nothing.
func RuleIcon(rule spam.Rule) string {
	if name, ok := ruleIcons[rule]; ok {
		return name
	}
	return "shield-warning"
}

// RuleLabel returns the human-readable name for a spam rule.
func RuleLabel(rule spam.Rule) string {
	if label, ok := ruleLabels[rule]; ok {
		return label
	}
	return string(rule)
}

// Initial returns the first character of a name, uppercased, for an avatar.
// Falls back to "?" so the circle is never empty — an empty circle reads as a
// rendering bug rather than as a person without a name.
func Initial(s string) string {
	for _, r := range strings.TrimSpace(s) {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// itoa is strconv.Itoa under a shorter name, used by the small formatting
// helpers in this package.
func itoa(n int) string { return strconv.Itoa(n) }

// Age renders how long ago something happened, at the coarseness an operator
// triaging a queue actually reads: "6d", "3h", "just now".
func Age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h"
	default:
		return itoa(int(d.Hours()/24)) + "d"
	}
}
