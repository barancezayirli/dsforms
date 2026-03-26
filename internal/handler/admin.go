package handler

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/flash"
	"github.com/youruser/dsforms/internal/store"
)

// AdminHandler handles admin dashboard and forms management pages.
type AdminHandler struct {
	Store     *store.Store
	SecretKey string
	BaseURL   string
	Templates map[string]*template.Template
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
type dashboardData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Forms       []store.FormSummary
	TotalForms  int
	TotalUnread int
	TotalAll    int
}

// formNewData holds the data passed to form_new.html.
type formNewData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Form        store.Form
	Error       string
}

// formEditData holds the data passed to form_edit.html.
type formEditData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Form        store.Form
	BaseURL     string
	Error       string
}

// Dashboard renders the admin dashboard with form list and stats.
func (h *AdminHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

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

	totalUnread := 0
	for _, f := range forms {
		totalUnread += f.UnreadCount
	}

	data := dashboardData{
		Title:       "Forms",
		Active:      "forms",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Forms:       forms,
		TotalForms:  len(forms),
		TotalUnread: totalUnread,
		TotalAll:    totalAll,
	}

	if err := h.Templates["dashboard.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("dashboard template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// NewFormPage renders the new form creation page.
func (h *AdminHandler) NewFormPage(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	data := formNewData{
		Title:       "New Form",
		Active:      "forms",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
	}

	if err := h.Templates["form_new.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("form_new template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// CreateForm handles POST to create a new form.
func (h *AdminHandler) CreateForm(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())

	name := r.FormValue("name")
	emailTo := r.FormValue("email_to")
	redirect := r.FormValue("redirect")

	if name == "" || emailTo == "" {
		errMsg := "Name and email are required."
		if name == "" {
			errMsg = "Form name is required."
		} else {
			errMsg = "Notification email is required."
		}
		data := formNewData{
			Title:       "New Form",
			Active:      "forms",
			CurrentUser: user,
			Form: store.Form{
				Name:     name,
				EmailTo:  emailTo,
				Redirect: redirect,
			},
			Error: errMsg,
		}
		if err := h.Templates["form_new.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("form_new template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	f := store.Form{
		ID:       uuid.New().String(),
		Name:     name,
		EmailTo:  emailTo,
		Redirect: redirect,
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
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)
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
		Title:       "Edit Form",
		Active:      "forms",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Form:        f,
		BaseURL:     h.BaseURL,
	}

	if err := h.Templates["form_edit.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("form_edit template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// EditForm handles POST to update a form.
func (h *AdminHandler) EditForm(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	name := r.FormValue("name")
	emailTo := r.FormValue("email_to")
	redirect := r.FormValue("redirect")

	if name == "" || emailTo == "" {
		errMsg := "Name and email are required."
		if name == "" {
			errMsg = "Form name is required."
		} else {
			errMsg = "Notification email is required."
		}

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
		// Overlay submitted values so the user sees what they typed.
		f.Name = name
		f.EmailTo = emailTo
		f.Redirect = redirect

		flashType, flashMsg := flash.Get(r, w, h.SecretKey)
		data := formEditData{
			Title:       "Edit Form",
			Active:      "forms",
			CurrentUser: user,
			Flash:       newFlash(flashType, flashMsg),
			Form:        f,
			BaseURL:     h.BaseURL,
			Error:       errMsg,
		}
		if err := h.Templates["form_edit.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("form_edit template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	f := store.Form{
		ID:       id,
		Name:     name,
		EmailTo:  emailTo,
		Redirect: redirect,
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
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Form        store.Form
	Submissions []store.Submission
	TotalCount  int
	UnreadCount int
	Page        int
	HasPrev     bool
	HasNext     bool
	PrevPage    int
	NextPage    int
}

// submissionDetailData holds the data passed to submission_detail.html.
type submissionDetailData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Form        store.Form
	Submission  store.Submission
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

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	offset := (page - 1) * pageSize

	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	subs, _ := h.Store.ListSubmissionsPaged(formID, pageSize, offset)
	total, _ := h.Store.CountSubmissions(formID)
	unread, _ := h.Store.UnreadCount(formID)

	data := formDetailData{
		Title:       form.Name,
		Active:      "forms",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Form:        form,
		Submissions: subs,
		TotalCount:  total,
		UnreadCount: unread,
		Page:        page,
		HasPrev:     page > 1,
		HasNext:     offset+pageSize < total,
		PrevPage:    page - 1,
		NextPage:    page + 1,
	}

	if err := h.Templates["form_detail.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("form detail template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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

	// Auto-mark read
	if !sub.Read {
		if err := h.Store.MarkRead(subID); err != nil {
			log.Printf("submission detail: mark read %s error: %v", subID, err)
		}
		sub.Read = true
	}

	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	data := submissionDetailData{
		Title:       form.Name,
		Active:      "forms",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Form:        form,
		Submission:  sub,
	}

	if err := h.Templates["submission_detail.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("submission detail template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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
			row = append(row, s.Data[k])
		}
		if err := cw.Write(row); err != nil {
			log.Printf("export csv: write row error: %v", err)
			return
		}
	}
	cw.Flush()
}
