package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	netmail "net/mail"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/youruser/dsforms/internal/store"
)

// ConfirmationMailer sends a single confirmation email. Implemented by *mail.Mailer.
type ConfirmationMailer interface {
	SendMail(to, subject, body string) error
}

// WaitlistSubmitHandler handles public waitlist signups via POST /w/{waitlistID}.
type WaitlistSubmitHandler struct {
	Store   *store.Store
	Mailer  ConfirmationMailer
	BaseURL string
}

// Handle processes a waitlist signup.
// Flow: lookup waitlist → honeypot → validate email → dedup upsert → position →
// async confirmation (if ConfirmSubject is set, the signup is new, and a Mailer is configured) → JSON or redirect.
func (h *WaitlistSubmitHandler) Handle(w http.ResponseWriter, r *http.Request) {
	waitlistID := chi.URLParam(r, "waitlistID")
	wl, err := h.Store.GetWaitlist(waitlistID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "waitlist not found", http.StatusNotFound)
			return
		}
		log.Printf("waitlist submit: get %s: %v", waitlistID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := r.ParseForm(); err != nil {
		log.Printf("waitlist submit: parse %s: %v", waitlistID, err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")

	// Honeypot — silently succeed without storing.
	if r.FormValue("_honeypot") != "" {
		if wantsJSON {
			writeJSON(w, http.StatusOK, map[string]bool{"success": true})
			return
		}
		http.Redirect(w, r, determineRedirect(r.FormValue("_redirect"), wl.Redirect), http.StatusFound)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}
	addr, err := netmail.ParseAddress(email)
	if err != nil {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	email = addr.Address

	// Extra fields → JSON data (exclude email + internal fields).
	data := make(map[string]string)
	for key, values := range r.PostForm {
		if key == "email" || internalFields[key] || len(values) == 0 {
			continue
		}
		data[key] = values[0]
	}
	rawData, err := json.Marshal(data)
	if err != nil {
		log.Printf("waitlist submit: marshal data: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	entry := store.WaitlistEntry{
		ID:         uuid.New().String(),
		WaitlistID: waitlistID,
		Email:      email,
		RawData:    string(rawData),
		IP:         ExtractIP(r),
	}
	position, alreadyJoined, err := h.Store.CreateEntry(entry)
	if err != nil {
		log.Printf("waitlist submit: create entry: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if !alreadyJoined && wl.ConfirmSubject != "" && h.Mailer != nil {
		go h.sendConfirmation(wl, email, data["name"], position)
	}

	if wantsJSON {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":        true,
			"position":       position,
			"already_joined": alreadyJoined,
		})
		return
	}
	redirectURL := determineRedirect(r.FormValue("_redirect"), wl.Redirect)
	http.Redirect(w, r, appendPosition(redirectURL, position), http.StatusFound)
}

// sendConfirmation sends the confirmation email, recovering from any panic.
func (h *WaitlistSubmitHandler) sendConfirmation(wl store.Waitlist, email, name string, position int) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("waitlist submit: panic in confirmation for %s: %v", email, rec)
		}
	}()
	vars := map[string]string{
		"email":    email,
		"name":     name,
		"position": strconv.Itoa(position),
	}
	subject := substituteVars(wl.ConfirmSubject, vars)
	body := substituteVars(wl.ConfirmBody, vars)
	if err := h.Mailer.SendMail(email, subject, body); err != nil {
		log.Printf("waitlist submit: confirmation email to %s failed: %v", email, err)
	}
}

// substituteVars replaces {{key}} tokens with literal values via strings.NewReplacer.
// Using literal replacement (not text/template execution) means signup-supplied
// values cannot inject template directives.
func substituteVars(s string, vars map[string]string) string {
	pairs := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// appendPosition adds a position query param to a redirect URL.
func appendPosition(redirectURL string, position int) string {
	u, err := url.Parse(redirectURL)
	if err != nil {
		return redirectURL
	}
	q := u.Query()
	q.Set("position", strconv.Itoa(position))
	u.RawQuery = q.Encode()
	return u.String()
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("waitlist submit: write JSON: %v", err)
	}
}
