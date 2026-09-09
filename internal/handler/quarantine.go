package handler

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/barancezayirli/dsforms/internal/filter"
	"github.com/barancezayirli/dsforms/internal/flash"
	"github.com/barancezayirli/dsforms/internal/safe"
	"github.com/barancezayirli/dsforms/internal/spam"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

// QuarantineHandler serves the spam review queue and the filter rules screen.
//
// The queue exists because internal/spam used to drop a matching submission
// with no record kept, which made a false positive unrecoverable. Every action
// here is about making that recoverable: see why it was held, put it back, or
// confirm it was right.
type QuarantineHandler struct {
	Base

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
	seen := map[spam.Rule]bool{}
	var out []string
	for _, sig := range h.Signals {
		if seen[sig.Rule] {
			continue
		}
		seen[sig.Rule] = true
		out = append(out, RuleLabel(sig.Rule))
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

	names := h.formNames()
	rows := make([]heldRow, 0, len(held))
	for _, sub := range held {
		signals, err := h.Store.SubmissionSignals(sub.ID)
		signalsFailed := err != nil
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
	data.Degraded = data.Degraded || sinceErr != nil
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

	// The held submission is read first so the form can be resolved *before*
	// anything is mutated. Restoring and then discovering the form is
	// unreadable left the row out of the queue, its notification unsent, and
	// the operator looking at a green flash reading "Restored to  as unread."
	sub, err := h.Store.GetHeldSubmission(id)
	if err != nil {
		// A row that is no longer held is the ordinary result of a double-click
		// or a resubmitted POST, not a failure — the first request restored it.
		// Saying "could not be restored" here sends the operator back to the
		// queue to hunt for a submission now sitting unread in the inbox.
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("quarantine: restore %s: already restored", id)
			flash.Set(w, h.SecretKey, "success", "Already restored — it is in the inbox.")
			http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
			return
		}
		log.Printf("quarantine: restore %s: load: %v", id, err)
		flash.Set(w, h.SecretKey, "error", "That submission could not be restored.")
		http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
		return
	}
	form, err := h.Store.GetForm(sub.FormID)
	if err != nil {
		log.Printf("quarantine: restore %s: get form %s: %v", id, sub.FormID, err)
		flash.Set(w, h.SecretKey, "error",
			"That submission could not be restored — its form could not be read.")
		http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
		return
	}

	sub, err = h.Store.RestoreSubmission(id)
	if err != nil {
		// Same race, one step later: another request restored it between the
		// read above and this UPDATE. Still not a failure.
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("quarantine: restore %s: already restored", id)
			flash.Set(w, h.SecretKey, "success", "Already restored — it is in the inbox.")
			http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
			return
		}
		log.Printf("quarantine: restore %s: %v", id, err)
		flash.Set(w, h.SecretKey, "error", "That submission could not be restored.")
		http.Redirect(w, r, "/admin/quarantine", http.StatusSeeOther)
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
		go safe.Do("quarantine: notify for restored "+sub.ID, func() {
			// The two deliveries are independent. A failed email must not skip
			// the webhook: the hold withheld both, so the restore owes both, and
			// a dead SMTP server would otherwise silently cost the operator the
			// CRM delivery as well. Only MarkNotified is gated on the send, since
			// that flag records delivery rather than intent.
			if form.EmailTo != "" && h.Notifier != nil {
				if err := h.Notifier.SendNotification(form, sub); err != nil {
					log.Printf("quarantine: withheld notification for %s failed: %v", sub.ID, err)
				} else if err := h.Store.MarkNotified(sub.ID); err != nil {
					log.Printf("quarantine: mark notified %s: %v", sub.ID, err)
				}
			}
			if form.WebhookURL != "" && h.Webhook != nil {
				if err := h.Webhook.Send(form, sub); err != nil {
					log.Printf("quarantine: withheld webhook for %s failed: %v", sub.ID, err)
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
	if err != nil {
		log.Printf("quarantine: delete: %v", err)
		flash.Set(w, h.SecretKey, "error", "Those submissions could not be deleted.")
	} else {
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
		if errors.Is(err, sql.ErrNoRows) {
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
		rules = append(rules, string(sig.Rule))
	}
	// Field values are deliberately not logged, here as everywhere else: the
	// rules and the score are what a weight-tuning exercise needs, and the
	// submission itself stays in the database where it already is.
	log.Printf("spam: FALSE POSITIVE reported for submission %s (form %s) — score %d, threshold %d, rules [%s]",
		sub.ID, sub.FormID, sub.SpamScore, sub.HeldThreshold, strings.Join(rules, " "))

	flash.Set(w, h.SecretKey, "success",
		"Logged as a false positive. Restore it too if you have not already.")
	http.Redirect(w, r, "/admin/quarantine?sel="+sub.ID, http.StatusSeeOther)
}

// formNames maps form ids to names for the queue, which spans every form.
func (h *QuarantineHandler) formNames() map[string]string {
	forms, err := h.Store.ListForms()
	if err != nil {
		// Callers fall back to the form id. Returning nil blanked the form
		// column for every row while the page otherwise looked entirely normal,
		// leaving the operator unable to tell which form each hold came from.
		log.Printf("quarantine: list forms: %v", err)
		return nil
	}
	names := make(map[string]string, len(forms))
	for _, f := range forms {
		names[f.ID] = f.Name
	}
	return names
}

// senderLabel picks the best available identity for a held submission. The
// queue spans every form, so the field names vary.
func senderLabel(data map[string]string) string {
	for _, key := range []string{"name", "email", "from", "subject"} {
		for k, v := range data {
			if strings.EqualFold(k, key) && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	for _, v := range data {
		if strings.TrimSpace(v) != "" {
			return v
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
	Block     []filter.Rule
	Allow     []filter.Rule
	Keywords  []filter.Rule
	Threshold int
	Error     string
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

	data := filterRulesData{
		PageData:  h.Shell(w, r, "Filter rules", "rules"),
		Threshold: h.DefaultThreshold,
		Error:     errMsg,
	}
	for _, rule := range rules {
		switch {
		case rule.Type == filter.TypeKeyword:
			data.Keywords = append(data.Keywords, rule)
		case rule.Kind == filter.KindAllow:
			data.Allow = append(data.Allow, rule)
		default:
			data.Block = append(data.Block, rule)
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
	if err := h.Store.DeleteFilterRule(chi.URLParam(r, "id")); err != nil {
		log.Printf("rules: delete: %v", err)
		flash.Set(w, h.SecretKey, "error", "That rule could not be removed.")
	} else {
		flash.Set(w, h.SecretKey, "success", "Rule removed.")
	}
	http.Redirect(w, r, "/admin/rules", http.StatusSeeOther)
}
