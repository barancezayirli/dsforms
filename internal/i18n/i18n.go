// Package i18n holds the translation catalogue and the language rules.
//
// It is a leaf: it imports nothing from internal/, so the handler, main and the
// templates can all reach it without creating a cycle. The catalogue is keyed by
// the English text rather than by invented identifiers. That keeps a template
// readable while it is being translated, and it makes a missing translation fall
// back to the sentence the operator can still understand instead of to a key
// such as "forms.new.subtitle".
package i18n

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Lang is a language tag. Only the values in Supported are selectable.
type Lang string

const (
	// English is the source language, and the fallback for every missing entry.
	English Lang = "en"
	// Russian is the first complete translation.
	Russian Lang = "ru"
	// Auto asks FromRequest to choose from Accept-Language, falling back to
	// English. It is a configuration value, not a language a page renders in.
	Auto Lang = "auto"
)

// Supported is every language a page can render in, in the order the switcher
// offers them.
var Supported = []Lang{English, Russian}

// CookieName is the browser preference set by the language switcher.
const CookieName = "dsforms_lang"

// Normalize turns a tag such as "ru-RU" or " EN " into a supported Lang.
// Unknown tags return "".
func Normalize(raw string) Lang {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if tag == "" {
		return ""
	}
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	switch Lang(tag) {
	case English, Russian:
		return Lang(tag)
	default:
		return ""
	}
}

// Valid reports whether l names a language this build can render.
func (l Lang) Valid() bool {
	return l == English || l == Russian
}

// Name is the short label shown in the language switcher.
func (l Lang) Name() string {
	switch l {
	case Russian:
		return "Русский"
	case English:
		return "English"
	default:
		return string(l)
	}
}

// Short is the two-letter label used where space is tight.
func (l Lang) Short() string {
	if l.Valid() {
		return strings.ToUpper(string(l))
	}
	return ""
}

// FromRequest resolves the language for one request.
//
// The order is deliberate: an explicit ?lang= wins (the switcher links carry it),
// then the cookie the switcher set, then the operator's configured default, then
// the browser's Accept-Language, then English. "Auto" and an empty fallback mean
// "ask the browser", which is why a Russian browser gets Russian on a fresh
// install without anyone setting an environment variable.
func FromRequest(r *http.Request, fallback string) Lang {
	if r != nil {
		if lang := Normalize(r.URL.Query().Get("lang")); lang.Valid() {
			return lang
		}
		if c, err := r.Cookie(CookieName); err == nil {
			if lang := Normalize(c.Value); lang.Valid() {
				return lang
			}
		}
	}

	configured := Normalize(fallback)
	if configured.Valid() {
		return configured
	}
	if r != nil {
		if lang := fromAcceptLanguage(r.Header.Get("Accept-Language")); lang.Valid() {
			return lang
		}
	}
	return English
}

// fromAcceptLanguage picks the best supported language from an Accept-Language
// header. It honours q-values and falls back to the first supported base tag, so
// "ru-RU,ru;q=0.9,en;q=0.8" selects Russian.
func fromAcceptLanguage(header string) Lang {
	if strings.TrimSpace(header) == "" {
		return ""
	}

	type candidate struct {
		lang Lang
		q    float64
		pos  int
	}
	var candidates []candidate
	for i, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if len(fields) == 0 {
			continue
		}
		lang := Normalize(fields[0])
		if !lang.Valid() {
			continue
		}
		q := 1.0
		for _, param := range fields[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || strings.TrimSpace(key) != "q" {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				q = parsed
			}
		}
		if q <= 0 {
			continue
		}
		candidates = append(candidates, candidate{lang: lang, q: q, pos: i})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].q == candidates[j].q {
			return candidates[i].pos < candidates[j].pos
		}
		return candidates[i].q > candidates[j].q
	})
	return candidates[0].lang
}

// T translates key into lang, falling back to key itself. Arguments are applied
// with fmt.Sprintf only when they are present, so a translation containing a
// literal percent sign is safe when it has no arguments.
func T(lang Lang, key string, args ...any) string {
	text := key
	if entries, ok := catalog[lang]; ok {
		if translated, ok := entries[key]; ok {
			text = translated
		}
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

// Plural renders a count and its noun in the language's plural form:
// "1 form", "3 forms"; "1 форма", "3 формы", "5 форм".
//
// The noun is the English singular, because it is also the catalogue key. A
// language that needs more than two forms keeps them beside the noun rather than
// spreading a switch through every template.
func Plural(lang Lang, n int, noun string) string {
	return strconv.Itoa(n) + " " + pluralNoun(lang, n, noun)
}

func pluralNoun(lang Lang, n int, noun string) string {
	if lang == Russian {
		if forms, ok := russianPlurals[noun]; ok {
			return forms.pick(n)
		}
	}
	if n == 1 {
		return noun
	}
	switch noun {
	case "day":
		return "days"
	default:
		return noun + "s"
	}
}

type pluralForms struct {
	one, few, many string
}

func (p pluralForms) pick(n int) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return p.one
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return p.few
	default:
		return p.many
	}
}
