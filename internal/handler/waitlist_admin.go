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

type waitlistDetailData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Waitlist    store.Waitlist
	Entries     []store.WaitlistEntry
	TotalCount  int
	Page        int
	HasPrev     bool
	HasNext     bool
	PrevPage    int
	NextPage    int
}

// Detail renders the paginated entries table for a waitlist.
func (h *WaitlistHandler) Detail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
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

	entries, _ := h.Store.ListEntriesPaged(id, pageSize, offset)
	total, _ := h.Store.CountEntries(id)

	data := waitlistDetailData{
		Title:       wl.Name,
		Active:      "waitlists",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Waitlist:    wl,
		Entries:     entries,
		TotalCount:  total,
		Page:        page,
		HasPrev:     page > 1,
		HasNext:     offset+pageSize < total,
		PrevPage:    page - 1,
		NextPage:    page + 1,
	}
	h.render(w, "waitlist_detail.html", data)
}

// DeleteEntry handles POST to delete one entry.
func (h *WaitlistHandler) DeleteEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entryID := chi.URLParam(r, "entryID")
	if err := h.Store.DeleteEntry(entryID); err != nil {
		log.Printf("waitlist delete entry %s: %v", entryID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/waitlists/"+id, http.StatusFound)
}

// ExportCSV handles GET to export entries as CSV.
func (h *WaitlistHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}

	entries, err := h.Store.ListEntries(id)
	if err != nil {
		log.Printf("waitlist export %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Union of all extra-field keys.
	keySet := map[string]struct{}{}
	for _, e := range entries {
		for k := range e.Data {
			keySet[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-waitlist.csv"`, wl.ID))

	cw := csv.NewWriter(w)
	header := append([]string{"position", "email", "joined_at"}, keys...)
	if err := cw.Write(header); err != nil {
		log.Printf("waitlist export: write header: %v", err)
		return
	}
	for _, e := range entries {
		row := []string{strconv.Itoa(e.Position), e.Email, e.CreatedAt.Format("2006-01-02T15:04:05Z")}
		for _, k := range keys {
			row = append(row, e.Data[k])
		}
		if err := cw.Write(row); err != nil {
			log.Printf("waitlist export: write row: %v", err)
			return
		}
	}
	cw.Flush()
}

type broadcastNewData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Waitlist    store.Waitlist
	EntryCount  int
	Broadcasts  []store.BroadcastSummary
	Subject     string
	Body        string
	Error       string
}

type broadcastDetailData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
	Waitlist    store.Waitlist
	Broadcast   store.BroadcastSummary
}

// BroadcastPage renders the compose form plus past broadcasts.
func (h *WaitlistHandler) BroadcastPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)
	count, _ := h.Store.CountEntries(id)
	past, _ := h.Store.ListBroadcasts(id)

	h.render(w, "broadcast_new.html", broadcastNewData{
		Title:       "Broadcast",
		Active:      "waitlists",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Waitlist:    wl,
		EntryCount:  count,
		Broadcasts:  past,
	})
}

// CreateBroadcast validates input, snapshots recipients into a queue, and wakes the worker.
func (h *WaitlistHandler) CreateBroadcast(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}
	user, _ := auth.UserFromContext(r.Context())

	subject := r.FormValue("subject")
	body := r.FormValue("body")

	rerender := func(errMsg string) {
		count, _ := h.Store.CountEntries(id)
		past, _ := h.Store.ListBroadcasts(id)
		h.render(w, "broadcast_new.html", broadcastNewData{
			Title: "Broadcast", Active: "waitlists", CurrentUser: user,
			Waitlist: wl, EntryCount: count, Broadcasts: past,
			Subject: subject, Body: body, Error: errMsg,
		})
	}

	if subject == "" || body == "" {
		rerender("Subject and body are required.")
		return
	}

	entries, err := h.Store.ListEntries(id)
	if err != nil {
		log.Printf("broadcast create: list entries %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(entries) == 0 {
		rerender("This waitlist has no signups to send to.")
		return
	}
	emails := make([]string, 0, len(entries))
	for _, e := range entries {
		emails = append(emails, e.Email)
	}

	b := store.Broadcast{
		ID:         uuid.New().String(),
		WaitlistID: id,
		Subject:    subject,
		Body:       body,
	}
	if err := h.Store.CreateBroadcast(b, emails); err != nil {
		log.Printf("broadcast create %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if h.Broadcaster != nil {
		h.Broadcaster.Notify()
	}
	http.Redirect(w, r, "/admin/waitlists/"+id+"/broadcasts/"+b.ID, http.StatusFound)
}

// BroadcastDetail renders a single broadcast's progress.
func (h *WaitlistHandler) BroadcastDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	bid := chi.URLParam(r, "bid")
	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}
	sum, err := h.Store.GetBroadcastSummary(bid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "broadcast not found", http.StatusNotFound)
			return
		}
		log.Printf("broadcast detail %s: %v", bid, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	h.render(w, "broadcast_detail.html", broadcastDetailData{
		Title:       "Broadcast",
		Active:      "waitlists",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		Waitlist:    wl,
		Broadcast:   sum,
	})
}
