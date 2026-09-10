package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/barancezayirli/dsforms/internal/flash"
	"github.com/barancezayirli/dsforms/internal/safe"
	"github.com/barancezayirli/dsforms/internal/screen"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

// QuarantineStore is what the quarantine screen needs from storage.
//
// It is the only surface that can both read held submissions and *edit* the
// rules that hold them, because reviewing what was caught and adjusting what
// catches it are the same operator task on the same screen. SubmitStore also
// reads the rules, but cannot add or delete one.
type QuarantineStore interface {
	AddFilterRule(kind, ruleType, value, note string) (screen.Rule, error)
	DeleteAllHeld() (int, error)
	DeleteFilterRule(id string) (bool, error)
	DeleteHeld(ids []string) (int, error)
	GetForm(id string) (store.Form, error)
	GetHeldSubmission(id string) (store.Submission, error)
	HeldCount() (int, error)
	HeldSince(days int) (held, total int, err error)
	HeldSubmissions(limit, offset int) ([]store.Submission, error)
	ListFilterRules() ([]screen.Rule, error)
	ListForms() ([]store.FormSummary, error)
	MarkNotified(id string) error
	RestoreSubmission(id string) (store.Submission, error)
	SubmissionSignals(submissionID string) ([]store.SpamSignal, error)
}

// QuarantineHandler serves the spam review queue and the filter rules screen.
//
// The queue exists because the pre-quarantine scorer used to drop a matching submission
// with no record kept, which made a false positive unrecoverable. Every action
// here is about making that recoverable: see why it was held, put it back, or
// confirm it was right.
type QuarantineHandler struct {
	Base
	Store QuarantineStore

	// Notifier and Webhook send what was withheld while a submission sat in
	// quarantine. The hold path withholds *both*, so restoring must make good on
	// both — sending only the email leaves a restored lead in the inbox and
	// never in the CRM, with nothing anywhere to say so.
	Notifier Notifier
	Webhook  WebhookSender

	// RetentionDays is how long held submissions are kept, for the UI copy. It
	// is derived from quarantineRetention in main.go, so the two cannot drift.
	RetentionDays int

	// DefaultThreshold is the instance-wide threshold, shown as the meter's
	// reference point when a held row predates per-submission recording.
	DefaultThreshold int
}

// heldRow is one row of the queue, with the breakdown already resolved.
type heldRow struct {
	store.Submission
	FormName string
	Signals  []store.SpamSignal
	From     string
	Age      string

	// SignalsFailed distinguishes "this submission has no recorded signals"
	// from "the breakdown could not be read", which look identical otherwise.
	SignalsFailed bool
}

// Rules returns the rule names that fired, for the Signals column.
func (h heldRow) Rules() []string {
	seen := map[screen.Check]bool{}
	var out []string
	for _, sig := range h.Signals {
		if seen[sig.Check] {
			continue
		}
		seen[sig.Check] = true
		out = append(out, RuleLabel(sig.Check))
	}
	return out
}

type quarantineData struct {
	PageData
	Rows     []heldRow
	Selected *heldRow

	// Meter geometry for the selected row.
	MeterPercent int
	MeterMax     int
	Threshold    int

	HeldTotal     int
	AwaitingCount int
	HeldRecent    int
	TrafficRecent int
	RetentionDays int
	Pager         Pagination

	// Fragment is set when the response is the panel alone rather than the whole
	// page, so the panel knows to carry the degraded notice the shell would
	// otherwise provide.
	Fragment bool
}

// Page renders the quarantine queue.
func (h *QuarantineHandler) Page(w http.ResponseWriter, r *http.Request) {
	total, err := h.Store.HeldCount()
	if err != nil {
		log.Printf("quarantine: count: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pager := PaginationFrom(r, total)

	held, err := h.Store.HeldSubmissions(pager.PageSize, pager.Offset())
	if err != nil {
		log.Printf("quarantine: list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	names, namesFailed := h.formNames()
	anySignalsFailed := false
	rows := make([]heldRow, 0, len(held))
	for _, sub := range held {
		signals, err := h.Store.SubmissionSignals(sub.ID)
		signalsFailed := err != nil
		anySignalsFailed = anySignalsFailed || signalsFailed
		if err != nil {
			// Rendering a hold with no stated reason reads as "the filter is
			// arbitrary" — the exact impression this queue exists to prevent —
			// so an unreadable breakdown says so instead of showing nothing.
			log.Printf("quarantine: signals for %s: %v", sub.ID, err)
		}
		name := names[sub.FormID]
		if name == "" {
			// An opaque id beats a blank cell.
			name = sub.FormID
		}
		rows = append(rows, heldRow{
			Submission:    sub,
			FormName:      name,
			Signals:       signals,
			From:          senderLabel(sub.Data),
			Age:           Age(sub.CreatedAt),
			SignalsFailed: signalsFailed,
		})
	}

	// The selected row drives the right-hand breakdown. Defaulting to the first
	// means the panel is never an empty box next to a full queue.
	var selected *heldRow
	wanted := r.URL.Query().Get("sel")
	for i := range rows {
		if rows[i].ID == wanted {
			selected = &rows[i]
			break
		}
	}
	if selected == nil && len(rows) > 0 {
		selected = &rows[0]
	}

	// On the screen dedicated to spam volume, a failed query rendering "0 held ·
	// 0% of traffic" reads as "the filter caught nothing in 30 days".
	heldRecent, trafficRecent, sinceErr := h.Store.HeldSince(30)
	if sinceErr != nil {
		log.Printf("quarantine: held since: %v", sinceErr)
	}

	data := quarantineData{
		PageData:      h.Shell(w, r, "Quarantine", "quarantine"),
		Rows:          rows,
		Selected:      selected,
		HeldTotal:     total,
		AwaitingCount: total,
		HeldRecent:    heldRecent,
		TrafficRecent: trafficRecent,
		RetentionDays: h.RetentionDays,
		Threshold:     h.DefaultThreshold,
		Pager:         pager,
	}
	// Per-row degradation is right for a single unreadable breakdown, but the
	// page still owes an aggregate signal — a queue where every Form column has
	// fallen back to a raw id is the "reads like a fresh install" shape.
	data.Degraded = data.Degraded || sinceErr != nil || anySignalsFailed || namesFailed
	if selected != nil {
		data.Threshold = selected.HeldThreshold
		if data.Threshold == 0 {
			data.Threshold = h.DefaultThreshold
		}
		// The meter never reads as full at the threshold itself: a submission
		// that scraped in at exactly the threshold looks very different from
		// one that tripled it, and the operator is judging that difference.
		data.MeterMax = max(12, selected.SpamScore)
		data.MeterPercent = min(100, Percent(selected.SpamScore, data.MeterMax))
	}

	// app.js swaps just the panel when a row is clicked.
	if r.Header.Get("X-Fragment") != "" {
		data.Fragment = true
		if err := h.Templates["quarantine.html"].ExecuteTemplate(w, "held-panel", data); err != nil {
			log.Printf("quarantine panel template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	h.Render(w, "quarantine.html", data)
}

// Restore puts a held submission back into its form as unread and sends the
// notification that was withheld while it sat in the queue.
func (h *QuarantineHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Every exit from here is a flash plus a redirect to the queue; only the
	// wording differs, and the wording is the whole user-visible result of a
	// restore. Keeping them in one closure keeps the differences legible.
	done := func(kind, msg, logLine string) {
		log.Printf("quarantine: restore %s: %s", id, logLine)
		flash.Set(w, h.SecretKey, kind, msg)
		http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
	}
	// A row that is no longer held is the ordinary result of a double-click or a
	// resubmitted POST, not a failure — the first request restored it. Saying
	// "could not be restored" sends the operator back to the queue to hunt for a
	// submission now sitting unread in the inbox.
	alreadyRestored := func() {
		done("success", "Already restored — it is in the inbox.", "already restored")
	}
	// And the opposite case, which the first version of this fix reported as a
	// success: the row is not merely un-held, it is gone. Retention purges after
	// 30 days, so an operator working a stale queue hits this. Telling them it
	// reached the inbox sends them looking for something that no longer exists.
	gone := func() {
		done("error", "That submission no longer exists — it was deleted or aged out of quarantine.", "gone")
	}

	// The held submission is read first so the form can be resolved *before*
	// anything is mutated. Restoring and then discovering the form is
	// unreadable left the row out of the queue, its notification unsent, and
	// the operator looking at a green flash reading "Restored to  as unread."
	sub, err := h.Store.GetHeldSubmission(id)
	if err != nil {
		if errors.Is(err, store.ErrSubmissionGone) {
			gone()
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			alreadyRestored()
			return
		}
		done("error", "That submission could not be restored.", fmt.Sprintf("load: %v", err))
		return
	}
	form, err := h.Store.GetForm(sub.FormID)
	if err != nil {
		// Names the actual cause. A generic message here would point the
		// operator at the submission, which is fine, rather than at the form,
		// which is not.
		done("error", "That submission could not be restored — its form could not be read.",
			fmt.Sprintf("get form %s: %v", sub.FormID, err))
		return
	}

	sub, err = h.Store.RestoreSubmission(id)
	if err != nil {
		// Same races, one step later: another request restored or deleted it
		// between the read above and this UPDATE.
		if errors.Is(err, store.ErrSubmissionGone) {
			gone()
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			alreadyRestored()
			return
		}
		done("error", "That submission could not be restored.", err.Error())
		return
	}

	if !sub.Notified {
		// Async and panic-guarded, matching the submit path — and sending the
		// same pair the hold withheld. MarkNotified runs only after the email
		// succeeds, so a failure leaves notified = 0.
		//
		// Nothing currently re-drives a notified = 0 row: there is no sweep and
		// no admin action that reads the column, so in practice a failed send
		// here is not retried. Tracked as a follow-up rather than papered over.
		// The hold withheld both deliveries, so the restore owes both. deliver
		// attempts them independently — shared with the submit path precisely so
		// the two cannot drift again.
		go safe.Do("quarantine: restore", func() {
			// notified records delivery, not intent, so it is set only when the
			// email actually went.
			if deliver("quarantine: restore", h.Notifier, h.Webhook, form, sub) {
				if err := h.Store.MarkNotified(sub.ID); err != nil {
					log.Printf("quarantine: mark notified %s: %v", sub.ID, err)
				}
			}
		})
	}

	flash.Set(w, h.SecretKey, "success", "Restored to "+form.Name+" as unread.")
	http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
}

// Delete permanently removes held submissions.
func (h *QuarantineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ids := r.PostForm["ids"]
	if len(ids) == 0 {
		http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
		return
	}
	// Report what went, not what was asked for. A stale page can carry ids the
	// retention sweep already purged or another admin already deleted; those
	// match nothing, and counting the request instead of the result overstates
	// it — the same defect Empty was rewritten to fix.
	n, err := h.Store.DeleteHeld(ids)
	switch {
	case err != nil && n > 0:
		// A partial delete is not a failure to report as "nothing happened":
		// those rows are permanently gone, and an operator who re-selects and
		// retries would otherwise reconcile against a count that never happened.
		log.Printf("quarantine: delete: %v", err)
		flash.Set(w, h.SecretKey, "error",
			plural(n, "submission")+" deleted before the delete failed; the rest are still held.")
	case err != nil:
		log.Printf("quarantine: delete: %v", err)
		flash.Set(w, h.SecretKey, "error", "Those submissions could not be deleted.")
	default:
		flash.Set(w, h.SecretKey, "success", plural(n, "submission")+" deleted.")
	}
	http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
}

// Empty deletes the entire queue.
//
// One set-based statement rather than a fetch-then-delete-by-id: enumerating ids
// capped the button at SQLite's 32766-parameter limit and silently truncated at
// whatever page size was fetched, so on a queue large enough to need emptying it
// either failed outright or reported a count it had not actually deleted.
func (h *QuarantineHandler) Empty(w http.ResponseWriter, r *http.Request) {
	n, err := h.Store.DeleteAllHeld()
	if err != nil {
		log.Printf("quarantine: empty: %v", err)
		flash.Set(w, h.SecretKey, "error", "The quarantine could not be emptied.")
	} else {
		flash.Set(w, h.SecretKey, "success", plural(n, "submission")+" deleted.")
	}
	http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
}

// Report records a false positive for weight tuning.
//
// There is nowhere to send it — dsforms is self-hosted and talks to nobody — so
// this logs a labelled sample locally. The design handoff asked for "at minimum,
// log it; do not silently discard", and an operator who reports one deserves to
// find it in their own logs.
func (h *QuarantineHandler) Report(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	sub, err := h.Store.GetHeldSubmission(id)
	if err != nil {
		// Both reasons a held row can be missing are a 404 here: there is
		// nothing to report on either way. Restore has to tell them apart
		// because its two messages differ; this one does not.
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrSubmissionGone) {
			http.Error(w, "submission not found", http.StatusNotFound)
			return
		}
		log.Printf("quarantine: report %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	signals, err := h.Store.SubmissionSignals(id)
	if err != nil {
		log.Printf("quarantine: report %s: signals: %v", id, err)
	}

	rules := make([]string, 0, len(signals))
	for _, sig := range signals {
		rules = append(rules, string(sig.Check))
	}
	// Field values are deliberately not logged, here as everywhere else: the
	// rules and the score are what a weight-tuning exercise needs, and the
	// submission itself stays in the database where it already is.
	log.Printf("screen: FALSE POSITIVE reported for submission %s (form %s) — score %d, threshold %d, rules [%s]",
		sub.ID, sub.FormID, sub.SpamScore, sub.HeldThreshold, strings.Join(rules, " "))

	flash.Set(w, h.SecretKey, "success",
		"Logged as a false positive. Restore it too if you have not already.")
	http.Redirect(w, r, "/admin/quarantine?sel="+sub.ID, http.StatusSeeOther)
}

// formNames maps form ids to names for the queue, which spans every form.
// formNames returns form ids to display names, and whether the lookup failed.
//
// The failure is reported rather than only logged because callers fall back to
// the raw form id: every row's Form column degrades at once, which is the
// "page reads like a fresh install" shape the Degraded banner exists for.
func (h *QuarantineHandler) formNames() (map[string]string, bool) {
	forms, err := h.Store.ListForms()
	if err != nil {
		log.Printf("quarantine: list forms: %v", err)
		return nil, true
	}
	names := make(map[string]string, len(forms))
	for _, f := range forms {
		names[f.ID] = f.Name
	}
	return names, false
}

// senderLabel picks the best available identity for a held submission. The
// queue spans every form, so the field names vary.
func senderLabel(data map[string]string) string {
	// Both loops below iterate sorted keys rather than the map. Ranging a map
	// meant a submission carrying both "name" and "Name" — legal, since only
	// ambiguity in the *email* field is rejected — displayed a different sender
	// on every render, and the final fallback returned a wholly arbitrary field.
	// So the quarantine queue, the search results and the digest could each name
	// a different person for the same submission.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, want := range []string{"name", "email", "from", "subject"} {
		for _, k := range keys {
			if strings.EqualFold(k, want) && strings.TrimSpace(data[k]) != "" {
				return data[k]
			}
		}
	}
	for _, k := range keys {
		if strings.TrimSpace(data[k]) != "" {
			return data[k]
		}
	}
	return "Submission"
}

// plural renders "1 submission" / "3 submissions".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

// filterRulesData backs the Filter rules screen.
type filterRulesData struct {
	PageData
	Block     []screen.Rule
	Allow     []screen.Rule
	Keywords  []screen.Rule
	Threshold int
	Error     string

	// Problems are stored rules that can never fire — typically written under an
	// older normalisation. A block rule in that state fails open while looking
	// active, so the operator is told rather than left to discover it.
	Problems []screen.RuleProblem
}

// RulesPage renders the operator's block/allow lists and custom keywords.
func (h *QuarantineHandler) RulesPage(w http.ResponseWriter, r *http.Request) {
	h.renderRules(w, r, "")
}

func (h *QuarantineHandler) renderRules(w http.ResponseWriter, r *http.Request, errMsg string) {
	rules, err := h.Store.ListFilterRules()
	if err != nil {
		log.Printf("rules: list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	problems := screen.CheckRules(rules)
	if len(problems) > 0 {
		log.Printf("rules: %d rule(s) can never match; see the filter rules screen", len(problems))
	}

	data := filterRulesData{
		PageData:  h.Shell(w, r, "Filter rules", "rules"),
		Threshold: h.DefaultThreshold,
		Error:     errMsg,
		Problems:  problems,
	}
	for _, rule := range rules {
		// Every column is named explicitly. A rule with an unrecognised Kind used
		// to land in Block by default, which told the operator a sender was
		// blocked while filter.Match — which iterates only {KindAllow, KindBlock}
		// — never matched it. Claiming a protection that does not exist is worse
		// than omitting the row.
		switch {
		case rule.Type == screen.TypeKeyword:
			data.Keywords = append(data.Keywords, rule)
		case rule.Kind == screen.KindAllow:
			data.Allow = append(data.Allow, rule)
		case rule.Kind == screen.KindBlock:
			data.Block = append(data.Block, rule)
		default:
			log.Printf("rules: rule %s has unknown kind %q; not displayed", rule.ID, rule.Kind)
		}
	}
	h.Render(w, "rules.html", data)
}

// AddRule creates a block, allow or keyword rule.
func (h *QuarantineHandler) AddRule(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	kind := r.PostFormValue("kind")
	ruleType := r.PostFormValue("type")
	value := r.PostFormValue("value")

	if _, err := h.Store.AddFilterRule(kind, ruleType, value, r.PostFormValue("note")); err != nil {
		// Validation failures are the operator mistyping, not a server fault,
		// so the message is shown inline rather than logged as an error.
		h.renderRules(w, r, strings.TrimPrefix(err.Error(), "add filter rule: "))
		return
	}
	flash.Set(w, h.SecretKey, "success", "Rule added.")
	http.Redirect(w, r, "/admin/rules", http.StatusSeeOther)
}

// DeleteRule removes a rule.
func (h *QuarantineHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	removed, err := h.Store.DeleteFilterRule(id)
	switch {
	case err != nil:
		log.Printf("rules: delete %s: %v", id, err)
		flash.Set(w, h.SecretKey, "error", "That rule could not be removed.")
	case !removed:
		// Confirming a removal that did not happen leaves the operator believing
		// the filter is configured differently than it is.
		log.Printf("rules: delete %s: no such rule", id)
		flash.Set(w, h.SecretKey, "error", "That rule no longer exists.")
	default:
		flash.Set(w, h.SecretKey, "success", "Rule removed.")
	}
	http.Redirect(w, r, "/admin/rules", http.StatusSeeOther)
}
