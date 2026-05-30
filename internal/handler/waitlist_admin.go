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

// BroadcastNotifier lets the handler wake the broadcast worker. Implemented by
// *broadcaster.Worker.
type BroadcastNotifier interface {
	Notify()
}

// WaitlistHandler handles admin waitlist pages.
type WaitlistHandler struct {
	Store       *store.Store
	SecretKey   string
	BaseURL     string
	Templates   map[string]*template.Template
	Broadcaster BroadcastNotifier
}

type waitlistListData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Waitlists   []store.WaitlistSummary
}

type waitlistFormData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Waitlist    store.Waitlist
	BaseURL     string
	Error       string
}

// List renders all waitlists.
func (h *WaitlistHandler) List(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	wls, err := h.Store.ListWaitlists()
	if err != nil {
		log.Printf("waitlist list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := waitlistListData{
		Title:       "Waitlist",
		Active:      "waitlists",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Waitlists:   wls,
	}
	h.render(w, "waitlists.html", data)
}

// NewPage renders the create-waitlist form.
func (h *WaitlistHandler) NewPage(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)
	data := waitlistFormData{
		Title:       "New Waitlist",
		Active:      "waitlists",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		BaseURL:     h.BaseURL,
	}
	h.render(w, "waitlist_new.html", data)
}

// Create handles POST to create a new waitlist.
func (h *WaitlistHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())

	wl := store.Waitlist{
		Name:           r.FormValue("name"),
		Redirect:       r.FormValue("redirect"),
		ConfirmSubject: r.FormValue("confirm_subject"),
		ConfirmBody:    r.FormValue("confirm_body"),
	}

	if wl.Name == "" {
		data := waitlistFormData{
			Title: "New Waitlist", Active: "waitlists", CurrentUser: user,
			Waitlist: wl, BaseURL: h.BaseURL, Error: "Waitlist name is required.",
		}
		h.render(w, "waitlist_new.html", data)
		return
	}

	wl.ID = uuid.New().String()
	if err := h.Store.CreateWaitlist(wl); err != nil {
		log.Printf("waitlist create: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/waitlists/"+wl.ID+"/edit", http.StatusFound)
}

// render executes a template against the base layout.
func (h *WaitlistHandler) render(w http.ResponseWriter, name string, data any) {
	if err := h.Templates[name].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("%s template error: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// getWaitlistOr404 fetches a waitlist or writes a 404/500.
func (h *WaitlistHandler) getWaitlistOr404(w http.ResponseWriter, id string) (store.Waitlist, bool) {
	wl, err := h.Store.GetWaitlist(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "waitlist not found", http.StatusNotFound)
			return store.Waitlist{}, false
		}
		log.Printf("waitlist get %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return store.Waitlist{}, false
	}
	return wl, true
}

// EditPage renders the edit form for a waitlist.
func (h *WaitlistHandler) EditPage(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)
	id := chi.URLParam(r, "id")

	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}
	data := waitlistFormData{
		Title:       "Edit Waitlist",
		Active:      "waitlists",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Waitlist:    wl,
		BaseURL:     h.BaseURL,
	}
	h.render(w, "waitlist_edit.html", data)
}

// Edit handles POST to update a waitlist.
func (h *WaitlistHandler) Edit(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	wl := store.Waitlist{
		ID:             id,
		Name:           r.FormValue("name"),
		Redirect:       r.FormValue("redirect"),
		ConfirmSubject: r.FormValue("confirm_subject"),
		ConfirmBody:    r.FormValue("confirm_body"),
	}

	if wl.Name == "" {
		data := waitlistFormData{
			Title: "Edit Waitlist", Active: "waitlists", CurrentUser: user,
			Waitlist: wl, BaseURL: h.BaseURL, Error: "Waitlist name is required.",
		}
		h.render(w, "waitlist_edit.html", data)
		return
	}

	if _, ok := h.getWaitlistOr404(w, id); !ok {
		return
	}
	if err := h.Store.UpdateWaitlist(wl); err != nil {
		log.Printf("waitlist edit %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/waitlists/"+id+"/edit", http.StatusFound)
}

// Delete handles POST to delete a waitlist and its entries/broadcasts.
func (h *WaitlistHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Store.DeleteWaitlist(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "waitlist not found", http.StatusNotFound)
			return
		}
		log.Printf("waitlist delete %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/waitlists", http.StatusFound)
}
