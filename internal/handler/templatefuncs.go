package handler

import (
	"html/template"

	"github.com/barancezayirli/dsforms/internal/redact"
	"github.com/barancezayirli/dsforms/internal/store"
)

// TemplateFuncs is the function map every template set is parsed with.
//
// It lives here, in one place, because there used to be five: the production map
// in main.go, one full copy of it in templates_test.go, and three single-entry
// stubs. The full copy is the dangerous one — a function added to production and
// not to it leaves the golden page tests rendering a template set that is not
// the one that ships, green while asserting nothing about the real pages. The
// stubs carry the opposite risk, a test template calling a function the harness
// never registered. TestTemplateFuncMapHasOneDefinition keeps it at one.
//
// The chart geometry entries exist so a viewBox in the markup can never drift
// from the coordinate space the paths in this package were generated in, and
// minPassword exists for the same reason one step further out: the account and
// new-user pages used to state a minimum as a hard-coded number, and it was
// wrong — the templates promised 12 while the store enforced none, then 8. A
// rule stated in two places is a rule that will disagree with itself.
func TemplateFuncs() template.FuncMap {
	return template.FuncMap{
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
		// A listing row asks only whether to show a badge, so it gets the
		// cheap answer from the same package that gives the detailed one.
		"hasHidden":   redact.Any,
		"minPassword": func() int { return store.MinPasswordLength },
	}
}
