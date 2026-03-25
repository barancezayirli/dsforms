package handler

import (
	"database/sql"
	"errors"
	"html/template"
	"log"
	"net/http"

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
	Templates *template.Template
}

// dashboardData holds the data passed to dashboard.html.
type dashboardData struct {
	CurrentUser store.User
	FlashType   string
	FlashMsg    string
	Forms       []store.FormSummary
	TotalForms  int
	TotalUnread int
	TotalAll    int
}

// formNewData holds the data passed to form_new.html.
type formNewData struct {
	CurrentUser store.User
	FlashType   string
	FlashMsg    string
	Form        store.Form
	Error       string
}

// formEditData holds the data passed to form_edit.html.
type formEditData struct {
	CurrentUser store.User
	FlashType   string
	FlashMsg    string
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
		CurrentUser: user,
		FlashType:   flashType,
		FlashMsg:    flashMsg,
		Forms:       forms,
		TotalForms:  len(forms),
		TotalUnread: totalUnread,
		TotalAll:    totalAll,
	}

	if err := h.Templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("dashboard template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// NewFormPage renders the new form creation page.
func (h *AdminHandler) NewFormPage(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	data := formNewData{
		CurrentUser: user,
		FlashType:   flashType,
		FlashMsg:    flashMsg,
	}

	if err := h.Templates.ExecuteTemplate(w, "form_new.html", data); err != nil {
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
			CurrentUser: user,
			Form: store.Form{
				Name:     name,
				EmailTo:  emailTo,
				Redirect: redirect,
			},
			Error: errMsg,
		}
		if err := h.Templates.ExecuteTemplate(w, "form_new.html", data); err != nil {
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
		CurrentUser: user,
		FlashType:   flashType,
		FlashMsg:    flashMsg,
		Form:        f,
		BaseURL:     h.BaseURL,
	}

	if err := h.Templates.ExecuteTemplate(w, "form_edit.html", data); err != nil {
		log.Printf("form_edit template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// EditForm handles POST to update a form.
func (h *AdminHandler) EditForm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	f := store.Form{
		ID:       id,
		Name:     r.FormValue("name"),
		EmailTo:  r.FormValue("email_to"),
		Redirect: r.FormValue("redirect"),
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
	if err := h.Templates.ExecuteTemplate(w, "success.html", nil); err != nil {
		log.Printf("success template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
