package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/filter"
	"github.com/barancezayirli/dsforms/internal/safe"
	"github.com/barancezayirli/dsforms/internal/spam"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Notifier sends notifications for form submissions.
type Notifier interface {
	SendNotification(form store.Form, sub store.Submission) error
}

// WebhookSender sends webhook notifications.
type WebhookSender interface {
	Send(form store.Form, sub store.Submission) error
}

// deliver sends whatever a form is configured for and reports whether the email
// went. Both sends are attempted independently: they are separate promises to
// the operator, and a dead SMTP server must not also cost them the webhook.
//
// It is shared by the accept path and the restore path so those two cannot
// drift — they already had, when a stray return in the restore copy made a
// failed email skip the withheld webhook. The return value exists for the
// restore path, which records delivery in the notified column and must only do
// so when the email actually arrived.
//
// Callers run this in a goroutine (via safe.Do): nothing here may block the
// response.
func deliver(ctx string, n Notifier, wh WebhookSender, form store.Form, sub store.Submission) (emailed bool) {
	if form.EmailTo != "" && n != nil {
		if err := n.SendNotification(form, sub); err != nil {
			log.Printf("%s: email failed for submission %s: %v", ctx, sub.ID, err)
		} else {
			emailed = true
		}
	}
	if form.WebhookURL != "" && wh != nil {
		if err := wh.Send(form, sub); err != nil {
			log.Printf("%s: webhook failed for submission %s: %v", ctx, sub.ID, err)
		}
	}
	return emailed
}

// SubmitHandler handles form submissions via POST /f/{formID}.
type SubmitHandler struct {
	Store    *store.Store
	Notifier Notifier
	Webhook  WebhookSender
	BaseURL  string
	Tracker  *spam.Tracker

	// DefaultThreshold is the instance-wide spam threshold from config. A form
	// may override it; zero falls back to spam.DefaultThreshold.
	DefaultThreshold int
}

// effectiveThreshold resolves the score at which this form holds a submission:
// the form's own setting, else the instance default, else the package default.
// Zero means "unset" at every level — a literal threshold of zero would hold
// every submission ever received.
func (h *SubmitHandler) effectiveThreshold(form store.Form) int {
	if form.SpamThreshold > 0 {
		return form.SpamThreshold
	}
	if h.DefaultThreshold > 0 {
		return h.DefaultThreshold
	}
	return spam.DefaultThreshold
}

// internalFields lists form field names that are never stored in submission data.
var internalFields = map[string]bool{
	"_honeypot": true,
	"_redirect": true,
	"_subject":  true,
}

// emailFieldValid reports whether the submission's sender field is a well-formed
// address. A missing email field is valid — not every form has one. This is a
// hard rejection distinct from the spam filter: a malformed email is a
// form-usage error, not a signal to silently drop.
//
// Two fields named "email" is also a rejection. It is a broken form rather than
// a real submission, and resolving it by picking one would put the choice of
// which address we read in the submitter's hands — see filter.SenderAddress,
// which this shares so the validator and the allow-rule matcher cannot disagree
// about who the sender is.
//
// This deliberately parses rather than calling filter's canonicalAddress. The
// two answer different questions: validation asks "did the visitor type a
// well-formed address", matching asks "what is the comparable form". Matching
// requires a dot after the @ because it scans every field of every submission
// and must not treat prose tokens as addresses; validation must not, or an
// intranet form posting user@localhost would be rejected. Do not "unify" them.
func emailFieldValid(data map[string]string) bool {
	value, state := filter.SenderAddress(data)
	switch state {
	case filter.SenderNone:
		// No email field at all is legal — not every form has one.
		return true
	case filter.SenderOne:
		_, err := mail.ParseAddress(value)
		return err == nil
	default:
		// SenderAmbiguous today, and anything added later. The permissive
		// outcome must never be the one a new state falls into by default.
		return false
	}
}

// verdict is the outcome of screening one submission: whether to hold it, what
// it scored, and why.
type verdict struct {
	hold    bool
	score   int
	signals []spam.Signal

	// matchedRuleID is the operator rule that decided this, or "" if the
	// content scorer did. The caller counts the hit; decide does not, because
	// that is a write and this is a pure function.
	matchedRuleID string
}

// decide screens one submission against the operator's rules and the content
// scorer, and is the single place the hold/accept decision is made.
//
// Pure: everything it needs is an argument and everything it decided is in the
// return value. That matters because this decision has been wrong three times,
// each time in the seam between two packages that each owned part of it — so it
// is worth being able to characterise, in full, without a request or a database.
//
// The order is deliberate. An allow rule wins outright and skips scoring
// entirely, including the repeat-IP check, which is the point of allowlisting a
// busy office NAT. A block rule holds whatever the content scores.
func decide(fields map[string]string, ip string, rules []filter.Rule, threshold int, repeated bool) verdict {
	matched, ruleHit := filter.Match(rules, fields, ip)

	switch {
	case ruleHit && matched.Kind == filter.KindAllow:
		return verdict{matchedRuleID: matched.ID}

	case ruleHit && matched.Kind == filter.KindBlock:
		// The score is stamped at the threshold so the breakdown still adds up,
		// and the single signal carries the same weight — the content itself
		// scored nothing, and the meter should not imply otherwise.
		//
		// Field stays empty: it means "the form field whose value matched", and
		// putting the rule's *type* there rendered "field cidr · matched" to the
		// operator. The label already says a filter rule fired, and Match
		// carries the rule value.
		return verdict{
			hold:          true,
			score:         threshold,
			signals:       []spam.Signal{{Rule: spam.RuleBlocked, Match: matched.Value, Weight: threshold}},
			matchedRuleID: matched.ID,
		}

	default:
		score, signals := spam.DetailWith(fields, filter.Keywords(rules))
		if repeated {
			// Repeat-IP is stateful and lives outside the scorer, so it is
			// stamped here. Weighted at the threshold so it holds on its own —
			// matching the old behaviour, where a repeat IP was an outright
			// drop — while keeping the breakdown's weights summing to the score.
			signals = append(signals, spam.Signal{Rule: spam.RuleRepeatIP, Match: ip, Weight: threshold})
			score += threshold
		}
		return verdict{hold: score >= threshold, score: score, signals: signals}
	}
}

// Handle processes a form submission.
// Flow:
//  1. Look up form by ID → 404 if missing
//  2. Parse form body
//  3. Honeypot: if _honeypot non-empty → silently succeed without saving
//  4. Filter internal fields, build data map
//  5. Validate: data map must have ≥1 key → else 400
//  6. Validate: an "email" field, if present, must be a well-formed address → else 400
//  7. Determine redirect: _redirect > form.Redirect > /success; extract client IP
//  8. Filter rules: allow → accept and skip scoring; block → hold on arrival
//  9. Otherwise score, plus repeat-IP; at or above the effective threshold → hold
//  10. Held submissions are stored with their breakdown and notify nobody
//  11. Otherwise save, send email and webhook notifications async
//  12. Respond (JSON or redirect) — identical whether held or accepted
//
// The honeypot is checked before the filter rules, not after as the design
// handoff specified: allow rules match on the submitted email field, which is
// attacker-controlled, so consulting them first would let a bot bypass the
// honeypot by naming an allowlisted address.
func (h *SubmitHandler) Handle(w http.ResponseWriter, r *http.Request) {
	formID := chi.URLParam(r, "formID")
	form, err := h.Store.GetForm(formID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "form not found", http.StatusNotFound)
			return
		}
		log.Printf("submit: failed to get form %s: %v", formID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := r.ParseForm(); err != nil {
		log.Printf("submit: form %s parse error: %v", formID, err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Honeypot — drop without storing, but leave a trace.
	//
	// This is the one drop left in this handler; everything else is now held for
	// review. It stays a drop because the honeypot catches the highest-volume
	// bot traffic and quarantining it would bury the queue it exists to keep
	// reviewable. The log line is the compromise: password managers and autofill
	// extensions are a known benign trigger, so when someone reports that they
	// submitted and never heard back, there is something to correlate against.
	// Field values are never logged, here or anywhere else in this file.
	if r.FormValue("_honeypot") != "" {
		log.Printf("submit: dropped submission for form %s from %s (honeypot)", formID, ExtractIP(r))
		respondSuccess(w, r, formID, determineRedirect(r.FormValue("_redirect"), form.Redirect))
		return
	}

	// Build data map, filtering internal fields.
	data := make(map[string]string)
	for key, values := range r.PostForm {
		if internalFields[key] || len(values) == 0 {
			continue
		}
		data[key] = values[0]
	}

	// Both rejections below are logged for the same reason the honeypot drop is:
	// a site posting via fetch and checking only for a network error shows the
	// visitor a success message while the submission is gone. Without a log line
	// "I submitted and never heard back" has nothing to correlate against.
	// Field values are not logged, only the reason.
	if len(data) == 0 {
		log.Printf("submit: rejected submission for form %s from %s (no form data)", formID, ExtractIP(r))
		http.Error(w, "no form data", http.StatusBadRequest)
		return
	}

	if !emailFieldValid(data) {
		log.Printf("submit: rejected submission for form %s from %s (invalid or ambiguous email field)", formID, ExtractIP(r))
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}

	redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
	ip := ExtractIP(r)

	// Tracker.Seen must run unconditionally — it also *records* the submission,
	// so short-circuiting it behind a content check would undercount this IP's
	// repeat tally whenever content scoring caught the submission first.
	// Guarded by TestSubmitContentSpamStillCountsTowardIPRepeat.
	repeated := h.Tracker.Seen(formID, ip)

	// Operator overrides beat the scorer in both directions. A failure to read
	// them is not fatal: fall through to scoring rather than refusing the
	// submission, since losing real mail is the worse error.
	rules, err := h.Store.ListFilterRules()
	if err != nil {
		log.Printf("submit: form %s: reading filter rules: %v", formID, err)
	}
	threshold := h.effectiveThreshold(form)
	v := decide(data, ip, rules, threshold, repeated)
	score, signals, held := v.score, v.signals, v.hold

	// Counting the hit is a database write, so it stays out of decide: the
	// decision is a pure function of its arguments and this is a side effect of
	// having made it.
	if v.matchedRuleID != "" {
		h.countRuleHit(v.matchedRuleID)
	}

	if held {
		// Held, not dropped. The response below is indistinguishable from
		// success so a bot learns nothing, but the submission is now
		// recoverable: internal/spam used to bin it with no record at all, and
		// a false positive was unrecoverable.
		//
		// Field values are never logged — the reason is diagnosable without
		// copying submission content, or a spam payload, into the log.
		log.Printf("submit: held submission for form %s from %s (score=%d threshold=%d signals=%d)",
			formID, ip, score, threshold, len(signals))

		sub := store.Submission{
			ID:        uuid.New().String(),
			FormID:    formID,
			Data:      data,
			IP:        ip,
			CreatedAt: time.Now().UTC().Truncate(time.Second),
		}
		storeSignals := make([]store.SpamSignal, 0, len(signals))
		for _, sig := range signals {
			storeSignals = append(storeSignals, store.SpamSignal{
				Rule: sig.Rule, Field: sig.Field, Match: sig.Match, Weight: sig.Weight,
			})
		}
		if err := h.Store.CreateHeldSubmission(sub, score, threshold, storeSignals); err != nil {
			// Never report success for a submission we failed to store. Logging
			// and returning 302 would destroy it — reintroducing precisely the
			// silent loss this quarantine exists to end, one branch away from
			// the code that ends it.
			//
			// A 500 leaks nothing to a bot: the accepted path below returns the
			// same status under the same database conditions, so the response
			// cannot be used to tell "held" from "accepted".
			log.Printf("submit: form %s: holding submission %s failed: %v", formID, sub.ID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		respondSuccess(w, r, formID, redirectURL)
		return
	}

	rawData, err := json.Marshal(data)
	if err != nil {
		log.Printf("submit: failed to marshal data: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Stamp CreatedAt here rather than leaving it to the column default: this
	// struct is what the notification goroutine hands to the mailer and webhook,
	// and a zero time.Time renders as "01 Jan 0001" in the email Date header.
	// Truncated to the second because that is the resolution the row stores, so
	// the email and the admin UI report the identical timestamp.
	sub := store.Submission{
		ID:        uuid.New().String(),
		FormID:    formID,
		RawData:   string(rawData),
		Data:      data,
		IP:        ip,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := h.Store.CreateSubmission(sub); err != nil {
		log.Printf("submit: failed to save submission: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ctx := "submit: form " + formID
	go safe.Do(ctx, func() { deliver(ctx, h.Notifier, h.Webhook, form, sub) })

	respondSuccess(w, r, formID, redirectURL)
}

// countRuleHit records that a filter rule matched, for the "N blocked" column
// in the rules screen. A failure here must not affect the submission.
func (h *SubmitHandler) countRuleHit(id string) {
	if err := h.Store.IncrementRuleHits(id); err != nil {
		log.Printf("submit: incrementing hits for rule %s: %v", id, err)
	}
}

// respondSuccess writes a successful submission response — JSON if the client
// asked for it, otherwise a redirect. Shared by the honeypot, spam-drop, and
// normal-success paths so they cannot drift apart.
func respondSuccess(w http.ResponseWriter, r *http.Request, formID, redirectURL string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]bool{"success": true}); err != nil {
			log.Printf("submit: form %s failed to write JSON response: %v", formID, err)
		}
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// determineRedirect returns the redirect URL in priority order:
// formValue (_redirect field) > formDefault (form.Redirect) > "/success".
func determineRedirect(formValue, formDefault string) string {
	if formValue != "" {
		return formValue
	}
	if formDefault != "" {
		return formDefault
	}
	return "/success"
}

// ExtractIP returns the client IP address from the request.
// Priority: X-Forwarded-For (first IP) > X-Real-IP > RemoteAddr (port stripped).
func ExtractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
