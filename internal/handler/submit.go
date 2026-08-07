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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/youruser/dsforms/internal/spam"
	"github.com/youruser/dsforms/internal/store"
)

// Notifier sends notifications for form submissions.
type Notifier interface {
	SendNotification(form store.Form, sub store.Submission) error
}

// WebhookSender sends webhook notifications.
type WebhookSender interface {
	Send(form store.Form, sub store.Submission) error
}

// SubmitHandler handles form submissions via POST /f/{formID}.
type SubmitHandler struct {
	Store    *store.Store
	Notifier Notifier
	Webhook  WebhookSender
	BaseURL  string
	Tracker  *spam.Tracker
}

// internalFields lists form field names that are never stored in submission data.
var internalFields = map[string]bool{
	"_honeypot": true,
	"_redirect": true,
	"_subject":  true,
}

// emailFieldValid reports whether a submitted field named "email"
// (case-insensitive) is a well-formed address. A missing email field is
// valid — not every form has one. This is a hard rejection distinct from
// the spam filter: a malformed email is a form-usage error, not a signal to
// silently drop.
func emailFieldValid(data map[string]string) bool {
	for key, value := range data {
		if strings.EqualFold(key, "email") {
			_, err := mail.ParseAddress(value)
			return err == nil
		}
	}
	return true
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
//  8. Spam check: if IsSpam(data) or this is the 3rd+ submission from this IP to
//     this form → silently succeed without saving (mirrors honeypot)
//  9. Save submission to DB
//  10. Send email and webhook notifications async
//  11. Respond (JSON or redirect)
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

	// Honeypot check — silently succeed without storing anything.
	if r.FormValue("_honeypot") != "" {
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

	if len(data) == 0 {
		http.Error(w, "no form data", http.StatusBadRequest)
		return
	}

	if !emailFieldValid(data) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}

	redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
	ip := ExtractIP(r)

	// Content spam or repeat-IP abuse — silently drop like the honeypot: look
	// successful, store nothing. Tracker.Seen must run unconditionally — it also
	// records the submission, so short-circuiting on IsSpam via || would skip
	// the call and undercount this IP's repeat tally whenever content scoring
	// already caught it first. Guarded by
	// TestSubmitContentSpamStillCountsTowardIPRepeat, not by this comment alone.
	repeated := h.Tracker.Seen(formID, ip)
	contentSpam := spam.IsSpam(data)
	if contentSpam || repeated {
		// Log which signal fired so an operator can answer "a customer says they
		// submitted and never heard back". Field values are deliberately never
		// logged — the drop reason is diagnosable without copying submission
		// content (or spam payloads) into the log.
		log.Printf("submit: dropped submission for form %s from %s (content_spam=%v score=%d, ip_repeat=%v)",
			formID, ip, contentSpam, spam.Score(data), repeated)
		respondSuccess(w, r, formID, redirectURL)
		return
	}

	rawData, err := json.Marshal(data)
	if err != nil {
		log.Printf("submit: failed to marshal data: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sub := store.Submission{
		ID:      uuid.New().String(),
		FormID:  formID,
		RawData: string(rawData),
		Data:    data,
		IP:      ip,
	}
	if err := h.Store.CreateSubmission(sub); err != nil {
		log.Printf("submit: failed to save submission: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("submit: panic in notification for form %s submission %s: %v", formID, sub.ID, r)
			}
		}()
		if form.EmailTo != "" && h.Notifier != nil {
			if err := h.Notifier.SendNotification(form, sub); err != nil {
				log.Printf("submit: email failed for form %s submission %s: %v", formID, sub.ID, err)
			}
		}
		if form.WebhookURL != "" && h.Webhook != nil {
			if err := h.Webhook.Send(form, sub); err != nil {
				log.Printf("submit: webhook failed for form %s submission %s: %v", formID, sub.ID, err)
			}
		}
	}()

	respondSuccess(w, r, formID, redirectURL)
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
