package handler

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/barancezayirli/dsforms/internal/screen"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// AdminHandler handles admin dashboard and forms management pages.
type AdminHandler struct {
	Base
	Webhook WebhookSender
}

// FlashData holds a flash message for display in templates via .Flash.Type and .Flash.Message.
type FlashData struct {
	Type    string
	Message string
}

// newFlash returns a *FlashData if msgType is non-empty, otherwise nil.
func newFlash(msgType, message string) *FlashData {
	if msgType == "" {
		return nil
	}
	return &FlashData{Type: msgType, Message: message}
}

// dashboardData holds the data passed to dashboard.html.
// formCard is one tile in the forms grid: the form itself, its counts, and the
// 30-day sparkline drawn on it.
type formCard struct {
	store.Form
	Total  int
	Unread int
	Held   int
	Spark  Spark
}

type dashboardData struct {
	PageData
	Cards       []formCard
	TotalForms  int
	TotalUnread int
	TotalAll    int
}

// formNewData holds the data passed to form_new.html.
type formNewData struct {
	PageData
	Form  store.Form
	Error string
}

// formEditData holds the data passed to form_edit.html.
type formEditData struct {
	PageData
	Form    store.Form
	BaseURL string
	Error   string
}

// Dashboard renders the admin dashboard with form list and stats.
func (h *AdminHandler) Dashboard(w http.ResponseWriter, r *http.Request) {

	forms, err := h.Store.ListForms()
	if err != nil {
		log.Printf("dashboard: list forms error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	totalAll, err := h.Store.CountAllSubmissions()
	if err != nil {
		log.Printf("dashboard: count submissions error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Counts and sparklines come from two grouped queries rather than a pair
	// per form: an instance with fifty forms would otherwise issue a hundred
	// queries to draw one page.
	stats, err := h.Store.PerFormStats()
	degraded := err != nil
	if err != nil {
		log.Printf("dashboard: per-form stats: %v", err)
	}
	byForm := make(map[string]store.FormStats, len(stats))
	for _, st := range stats {
		byForm[st.FormID] = st
	}
	series, err := h.Store.SubmissionsPerFormPerDay(sparkDays)
	if err != nil {
		log.Printf("dashboard: per-form series: %v", err)
		degraded = true
	}

	totalUnread := 0
	cards := make([]formCard, 0, len(forms))
	for _, f := range forms {
		totalUnread += f.UnreadCount
		st := byForm[f.ID]
		cards = append(cards, formCard{
			Form:   f.Form,
			Total:  st.Received,
			Unread: f.UnreadCount,
			Held:   st.Held,
			Spark:  Sparkline(series[f.ID], sparkWidth, formSparkHeight, 3),
		})
	}

	data := dashboardData{
		PageData:    h.Shell(w, r, "Forms", "forms"),
		Cards:       cards,
		TotalForms:  len(forms),
		TotalUnread: totalUnread,
		TotalAll:    totalAll,
	}
	// Without this the cards read Total 0 / Held 0 while the header above them
	// reports a real submission count from a query that succeeded — a page
	// contradicting itself and flagging nothing.
	data.Degraded = data.Degraded || degraded
	h.Render(w, "dashboard.html", data)
}

// NewFormPage renders the new form creation page.
func (h *AdminHandler) NewFormPage(w http.ResponseWriter, r *http.Request) {

	data := formNewData{
		PageData: h.Shell(w, r, "New Form", "forms"),
	}

	if err := h.Templates["form_new.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("form_new template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// CreateForm handles POST to create a new form.
func (h *AdminHandler) CreateForm(w http.ResponseWriter, r *http.Request) {

	name := r.FormValue("name")
	emailTo := r.FormValue("email_to")
	redirect := r.FormValue("redirect")
	webhookURL := r.FormValue("webhook_url")
	webhookFormat := r.FormValue("webhook_format")

	if name == "" {
		data := formNewData{
			PageData: h.Shell(w, r, "New Form", "forms"),
			Form: store.Form{
				Name:          name,
				EmailTo:       emailTo,
				Redirect:      redirect,
				WebhookURL:    webhookURL,
				WebhookFormat: webhookFormat,
			},
			Error: "Form name is required.",
		}
		if err := h.Templates["form_new.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("form_new template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	if webhookURL != "" {
		switch webhookFormat {
		case "generic", "slack", "discord":
		default:
			webhookFormat = "generic"
		}
	} else {
		webhookFormat = ""
	}

	if webhookURL != "" {
		u, parseErr := url.Parse(webhookURL)
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") {
			data := formNewData{
				PageData: h.Shell(w, r, "New Form", "forms"),
				Form: store.Form{
					Name: name, EmailTo: emailTo, Redirect: redirect,
					WebhookURL: webhookURL, WebhookFormat: webhookFormat,
				},
				Error: "Webhook URL must use http or https.",
			}
			if err := h.Templates["form_new.html"].ExecuteTemplate(w, "base", data); err != nil {
				log.Printf("form_new template error: %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
			return
		}
	}

	f := store.Form{
		ID:            uuid.New().String(),
		Name:          name,
		EmailTo:       emailTo,
		Redirect:      redirect,
		WebhookURL:    webhookURL,
		WebhookFormat: webhookFormat,
	}
	if err := h.Store.CreateForm(f); err != nil {
		log.Printf("create form error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+f.ID+"/edit", http.StatusFound)
}

// EditFormPage renders the form edit page.
func (h *AdminHandler) EditFormPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	f, err := h.Store.GetForm(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "form not found", http.StatusNotFound)
			return
		}
		log.Printf("edit form page: get form %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := formEditData{
		PageData: h.Shell(w, r, "Edit Form", "forms"),
		Form:     f,
		BaseURL:  h.BaseURL,
	}

	if err := h.Templates["form_edit.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("form_edit template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// EditForm handles POST to update a form.
func (h *AdminHandler) EditForm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	name := r.FormValue("name")
	emailTo := r.FormValue("email_to")
	redirect := r.FormValue("redirect")
	webhookURL := r.FormValue("webhook_url")
	webhookFormat := r.FormValue("webhook_format")
	// 0 means "inherit the instance default", which is also what an unparseable
	// or out-of-range value falls back to — never a literal threshold of zero,
	// which would hold every submission the form ever received.
	spamThreshold := 0
	if v, err := strconv.Atoi(r.FormValue("spam_threshold")); err == nil && v >= screen.MinThreshold && v <= screen.MaxThreshold {
		spamThreshold = v
	}

	if name == "" {
		f, err := h.Store.GetForm(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "form not found", http.StatusNotFound)
				return
			}
			log.Printf("edit form: get form %s error: %v", id, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		f.Name = name
		f.EmailTo = emailTo
		f.Redirect = redirect
		f.WebhookURL = webhookURL
		f.WebhookFormat = webhookFormat

		data := formEditData{
			PageData: h.Shell(w, r, "Edit Form", "forms"),
			Form:     f,
			BaseURL:  h.BaseURL,
			Error:    "Form name is required.",
		}
		if err := h.Templates["form_edit.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("form_edit template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	if webhookURL != "" {
		switch webhookFormat {
		case "generic", "slack", "discord":
		default:
			webhookFormat = "generic"
		}
	} else {
		webhookFormat = ""
	}

	if webhookURL != "" {
		u, parseErr := url.Parse(webhookURL)
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") {
			ef, err := h.Store.GetForm(id)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					http.Error(w, "form not found", http.StatusNotFound)
					return
				}
				log.Printf("edit form: get form %s error: %v", id, err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			ef.Name = name
			ef.EmailTo = emailTo
			ef.Redirect = redirect
			ef.WebhookURL = webhookURL
			ef.WebhookFormat = webhookFormat
			data := formEditData{
				PageData: h.Shell(w, r, "Edit Form", "forms"),
				Form:     ef,
				BaseURL:  h.BaseURL,
				Error:    "Webhook URL must use http or https.",
			}
			if err := h.Templates["form_edit.html"].ExecuteTemplate(w, "base", data); err != nil {
				log.Printf("form_edit template error: %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
			return
		}
	}

	f := store.Form{
		ID:            id,
		Name:          name,
		SpamThreshold: spamThreshold,
		EmailTo:       emailTo,
		Redirect:      redirect,
		WebhookURL:    webhookURL,
		WebhookFormat: webhookFormat,
	}

	if err := h.Store.UpdateForm(f); err != nil {
		log.Printf("edit form: update %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+id+"/edit", http.StatusFound)
}

// DeleteForm handles POST to delete a form and its submissions.
func (h *AdminHandler) DeleteForm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := h.Store.DeleteForm(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "form not found", http.StatusNotFound)
			return
		}
		log.Printf("delete form: %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms", http.StatusFound)
}

// Success renders the public success page (no auth required).
func (h *AdminHandler) Success(w http.ResponseWriter, r *http.Request) {
	if err := h.Templates["success.html"].Execute(w, nil); err != nil {
		log.Printf("success template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// formDetailData holds the data passed to form_detail.html.
type formDetailData struct {
	PageData
	Form          store.Form
	Submissions   []store.Submission
	TotalCount    int
	UnreadCount   int
	UnreadUnknown bool
	HeldCount     int
	HeldUnknown   bool
	Pager         Pagination
}

// submissionDetailData holds the data passed to submission_detail.html.
// Field is one key/value pair in the reader's field grid.
type Field struct {
	Key   string
	Value string
}

type submissionDetailData struct {
	PageData
	Form       store.Form
	Submission store.Submission

	// Fields are the submission's data keys in sorted order, excluding the
	// message body. Sorted because Go randomises map iteration and the old
	// template ranged over the map directly — so the reader reshuffled its own
	// field order on every refresh.
	Fields  []Field
	Message string

	Signals []store.SpamSignal

	NewerID  string
	OlderID  string
	Position int
	Total    int
	// SignalsFailed distinguishes "nothing was recorded" from "the breakdown
	// could not be read"; PositionKnown suppresses the "N of M" counter rather
	// than rendering "0 of 0" from a query that failed.
	SignalsFailed bool
	PositionKnown bool
}

const pageSize = 20

// FormDetail renders the paginated submissions table for a form.
func (h *AdminHandler) FormDetail(w http.ResponseWriter, r *http.Request) {
	formID := chi.URLParam(r, "id")
	form, err := h.Store.GetForm(formID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "form not found", http.StatusNotFound)
			return
		}
		log.Printf("admin: get form %s: %v", formID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	total, err := h.Store.CountSubmissions(formID)
	if err != nil {
		log.Printf("admin: count submissions for %s: %v", formID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pager := PaginationFrom(r, total)

	subs, err := h.Store.ListSubmissionsPaged(formID, pager.PageSize, pager.Offset())
	if err != nil {
		log.Printf("admin: list submissions for %s: %v", formID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// "inbox zero" is a positive assertion, so a swallowed error here is worse
	// than the bare 0 it renders: it tells the operator there is nothing waiting.
	// Same treatment as the Held stat below.
	unread, unreadErr := h.Store.UnreadCount(formID)
	if unreadErr != nil {
		log.Printf("admin: unread count for %s: %v", formID, unreadErr)
	}
	// The Held stat is the only per-form sign that this form's submissions are
	// being quarantined, so a swallowed error here tells an operator debugging
	// "why did my client's enquiry never arrive" that nothing is held — which
	// may be false. HeldUnknown makes the template render "—" instead of "0".
	held, heldErr := h.Store.HeldCountForForm(formID)
	if heldErr != nil {
		log.Printf("admin: held count for %s: %v", formID, heldErr)
	}

	data := formDetailData{
		PageData:      h.Shell(w, r, form.Name, "forms"),
		Form:          form,
		Submissions:   subs,
		TotalCount:    total,
		UnreadCount:   unread,
		UnreadUnknown: unreadErr != nil,
		HeldCount:     held,
		HeldUnknown:   heldErr != nil,
		Pager:         pager,
	}
	data.Degraded = data.Degraded || unreadErr != nil || heldErr != nil
	h.Render(w, "form_detail.html", data)
}

// SubmissionDetail renders a single submission detail page and auto-marks it read.
func (h *AdminHandler) SubmissionDetail(w http.ResponseWriter, r *http.Request) {
	formID := chi.URLParam(r, "formID")
	subID := chi.URLParam(r, "subID")

	form, err := h.Store.GetForm(formID)
	if err != nil {
		http.Error(w, "form not found", http.StatusNotFound)
		return
	}

	sub, err := h.Store.GetSubmission(subID)
	if err != nil {
		http.Error(w, "submission not found", http.StatusNotFound)
		return
	}

	// A held submission belongs to the quarantine screen, which shows the score
	// breakdown and the restore/confirm controls this page has none of. The
	// guard comes before the auto-mark below: without it, opening a guessable
	// held id here would silently mark it read from a screen that cannot act on
	// it. Sending the operator to the right screen beats a 404.
	if sub.IsHeld {
		http.Redirect(w, r, "/admin/quarantine?sel="+subID, http.StatusSeeOther)
		return
	}

	// Auto-mark read. The in-memory flag follows the write, not the intent: it
	// used to be set unconditionally, so a failed MarkRead rendered the
	// submission as read while the database still said unread — the sidebar
	// badge kept counting it and the inbox row kept its unread styling, and the
	// reader disagreed with both.
	markReadFailed := false
	if !sub.Read {
		if err := h.Store.MarkRead(subID); err != nil {
			log.Printf("submission detail: mark read %s error: %v", subID, err)
			markReadFailed = true
		} else {
			sub.Read = true
		}
	}

	fields, message := splitSubmissionFields(sub.Data)

	// A restored submission keeps its spam score, so an unreadable breakdown
	// renders "score 11" with nothing beside it — the inverse of the bug the
	// full-column read was written to fix.
	signals, signalsErr := h.Store.SubmissionSignals(subID)
	if signalsErr != nil {
		log.Printf("submission detail: signals for %s: %v", subID, signalsErr)
	}
	// A failed Neighbours renders the drawer counter as "0 of 0" — a hard
	// numeric claim manufactured from a query that did not run.
	newer, older, position, total, neighboursErr := h.Store.Neighbours(formID, subID)
	if neighboursErr != nil {
		log.Printf("submission detail: neighbours for %s: %v", subID, neighboursErr)
	}

	data := submissionDetailData{
		PageData:   h.Shell(w, r, form.Name, "forms"),
		Form:       form,
		Submission: sub,
		Fields:     fields,
		Message:    message,
		Signals:    signals,
		NewerID:    newer,
		OlderID:    older,
		Position:   position,
		Total:      total,

		// Distinguishes "no signals recorded" from "the breakdown could not be
		// read", which look identical otherwise — the same distinction heldRow
		// makes on the quarantine screen.
		SignalsFailed: signalsErr != nil,
		PositionKnown: neighboursErr == nil && total > 0,
	}
	data.Degraded = data.Degraded || signalsErr != nil || neighboursErr != nil || markReadFailed

	// app.js asks for the same URL with X-Fragment when it opens the drawer over
	// the list. Without JS — or when the link is opened directly, or shared —
	// the identical content renders as a full page instead.
	if r.Header.Get("X-Fragment") != "" {
		tmpl := h.Templates["submission_detail.html"]
		if err := tmpl.ExecuteTemplate(w, "drawer", data); err != nil {
			log.Printf("submission drawer template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	h.Render(w, "submission_detail.html", data)
}

// messageKeys are the field names rendered as the message body rather than as a
// row in the field grid.
var messageKeys = map[string]bool{"message": true, "body": true, "content": true}

// splitSubmissionFields separates the message body from the rest of a
// submission's fields and returns those fields in a stable order.
func splitSubmissionFields(data map[string]string) ([]Field, string) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var (
		fields  []Field
		message string
	)
	for _, k := range keys {
		if messageKeys[strings.ToLower(k)] && message == "" {
			message = data[k]
			continue
		}
		fields = append(fields, Field{Key: k, Value: data[k]})
	}
	return fields, message
}

// MarkRead handles POST to mark a single submission as read.
func (h *AdminHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	sub, err := h.Store.GetSubmission(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "submission not found", http.StatusNotFound)
			return
		}
		log.Printf("mark read: get submission %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.Store.MarkRead(id); err != nil {
		log.Printf("mark read: %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+sub.FormID, http.StatusFound)
}

// MarkAllRead handles POST to mark all submissions for a form as read.
func (h *AdminHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := h.Store.MarkAllRead(id); err != nil {
		log.Printf("mark all read: form %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+id, http.StatusFound)
}

// DeleteSubmission handles POST to delete a single submission.
func (h *AdminHandler) DeleteSubmission(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	sub, err := h.Store.GetSubmission(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "submission not found", http.StatusNotFound)
			return
		}
		log.Printf("delete submission: get %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.Store.DeleteSubmission(id); err != nil {
		log.Printf("delete submission: %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+sub.FormID, http.StatusFound)
}

// BulkDeleteSubmissions handles POST to delete multiple submissions for a
// form at once. Missing/empty "ids" deletes nothing.
func (h *AdminHandler) BulkDeleteSubmissions(w http.ResponseWriter, r *http.Request) {
	formID := chi.URLParam(r, "id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ids := r.PostForm["ids"]

	if err := h.Store.DeleteSubmissions(formID, ids); err != nil {
		log.Printf("bulk delete submissions: form %s error: %v", formID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+formID, http.StatusFound)
}

// TestWebhook handles POST to test a form's webhook configuration.
func (h *AdminHandler) TestWebhook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	w.Header().Set("Content-Type", "application/json")

	form, err := h.Store.GetForm(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "form not found",
			})
			return
		}
		log.Printf("test webhook: get form %s error: %v", id, err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "internal error",
		})
		return
	}

	if form.WebhookURL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "No webhook configured",
		})
		return
	}

	testSub := store.Submission{
		ID:     "test",
		FormID: form.ID,
		Data: map[string]string{
			"name":    "Test User",
			"email":   "test@example.com",
			"message": "This is a test from DSForms",
		},
		IP: "127.0.0.1",
	}

	if h.Webhook == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "webhook sender not configured",
		})
		return
	}

	if err := h.Webhook.Send(form, testSub); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// ExportCSV handles GET to export submissions as CSV.
func (h *AdminHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	f, err := h.Store.GetForm(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "form not found", http.StatusNotFound)
			return
		}
		log.Printf("export csv: get form %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	subs, err := h.Store.ListSubmissions(id)
	if err != nil {
		log.Printf("export csv: list submissions for %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Collect union of all data keys
	keySet := make(map[string]struct{})
	for _, s := range subs {
		for k := range s.Data {
			keySet[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	filename := fmt.Sprintf("%s-submissions.csv", f.ID)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	cw := csv.NewWriter(w)
	// Write header: id, submitted_at, ip, read, then data keys
	header := append([]string{"id", "submitted_at", "ip", "read"}, keys...)
	if err := cw.Write(header); err != nil {
		log.Printf("export csv: write header error: %v", err)
		return
	}

	for _, s := range subs {
		readVal := "false"
		if s.Read {
			readVal = "true"
		}
		row := []string{s.ID, s.CreatedAt.Format("2006-01-02T15:04:05Z"), s.IP, readVal}
		for _, k := range keys {
			row = append(row, csvSafe(s.Data[k]))
		}
		if err := cw.Write(row); err != nil {
			log.Printf("export csv: write row error: %v", err)
			return
		}
	}
	cw.Flush()
}
