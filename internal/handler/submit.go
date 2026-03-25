package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/youruser/dsforms/internal/store"
)

// Notifier sends notifications for form submissions.
type Notifier interface {
	SendNotification(form store.Form, sub store.Submission) error
}

// SubmitHandler handles form submissions via POST /f/{formID}.
type SubmitHandler struct {
	Store    *store.Store
	Notifier Notifier
	BaseURL  string
}

// internalFields lists form field names that are never stored in submission data.
var internalFields = map[string]bool{
	"_honeypot": true,
	"_redirect": true,
	"_subject":  true,
}

// Handle processes a form submission.
// Flow:
//  1. Look up form by ID → 404 if missing
//  2. Parse form body
//  3. Honeypot: if _honeypot non-empty → silently succeed without saving
//  4. Filter internal fields, build data map
//  5. Validate: data map must have ≥1 key → else 400
//  6. Determine redirect: _redirect > form.Redirect > /success
//  7. Extract client IP
//  8. Save submission to DB
//  9. Send email async
//  10. Respond (JSON or redirect)
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
		redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}
		http.Redirect(w, r, redirectURL, http.StatusFound)
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

	redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
	ip := ExtractIP(r)

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
				log.Printf("submit: panic in email for form %s submission %s: %v", formID, sub.ID, r)
			}
		}()
		if err := h.Notifier.SendNotification(form, sub); err != nil {
			log.Printf("submit: failed to send email for form %s submission %s: %v", formID, sub.ID, err)
		}
	}()

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
