package handler

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"

	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// BroadcastNotifier lets the handler wake the broadcast worker. Implemented by
// *broadcaster.Worker.
type BroadcastNotifier interface {
	Notify()
}

// WaitlistStore is what the waitlist admin needs from storage.
//
// Disjoint from the forms and quarantine side entirely — no method here appears
// on any other admin handler's surface, and its only overlap anywhere is
// GetWaitlist, shared with the public WaitlistSubmitStore.
//
// Stated as an observation, not a guarantee: nothing stops a future AdminStore
// from gaining ListWaitlists, and no test asserts the disjointness.
type WaitlistStore interface {
	CountEntries(waitlistID string) (int, error)
	CreateBroadcast(b store.Broadcast, emails []string) error
	CreateWaitlist(wl store.Waitlist) error
	DeleteEntry(waitlistID, id string) error
	DeleteWaitlist(id string) error
	GetBroadcastSummary(id string) (store.BroadcastSummary, error)
	GetWaitlist(id string) (store.Waitlist, error)
	ListBroadcasts(waitlistID string) ([]store.BroadcastSummary, error)
	ListEntries(waitlistID string) ([]store.WaitlistEntry, error)
	ListEntriesPaged(waitlistID string, limit, offset int) ([]store.WaitlistEntry, error)
	ListWaitlists() ([]store.WaitlistSummary, error)
	UpdateWaitlist(wl store.Waitlist) error
}

// WaitlistHandler handles admin waitlist pages.
type WaitlistHandler struct {
	Base
	Store       WaitlistStore
	Broadcaster BroadcastNotifier
}

type waitlistListData struct {
	PageData
	Waitlists []store.WaitlistSummary
}

type waitlistFormData struct {
	PageData
	Waitlist store.Waitlist
	BaseURL  string
	Error    string
}

// List renders all waitlists.
func (h *WaitlistHandler) List(w http.ResponseWriter, r *http.Request) {

	wls, err := h.Store.ListWaitlists()
	if err != nil {
		log.Printf("waitlist list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := waitlistListData{
		PageData:  h.Shell(w, r, "Waitlist", "waitlists"),
		Waitlists: wls,
	}
	h.render(w, "waitlists.html", data)
}

// NewPage renders the create-waitlist form.
func (h *WaitlistHandler) NewPage(w http.ResponseWriter, r *http.Request) {
	data := waitlistFormData{
		PageData: h.Shell(w, r, "New Waitlist", "waitlists"),
		BaseURL:  h.BaseURL,
	}
	h.render(w, "waitlist_new.html", data)
}

// Create handles POST to create a new waitlist.
func (h *WaitlistHandler) Create(w http.ResponseWriter, r *http.Request) {

	wl := store.Waitlist{
		Name:           r.FormValue("name"),
		Redirect:       r.FormValue("redirect"),
		ConfirmSubject: r.FormValue("confirm_subject"),
		ConfirmBody:    r.FormValue("confirm_body"),
	}

	if wl.Name == "" {
		data := waitlistFormData{
			PageData: h.Shell(w, r, "New Waitlist", "waitlists"),
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

// render executes a template against the base layout, buffering first so a
// mid-render error produces a clean 500 rather than corrupted partial output.
func (h *WaitlistHandler) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := h.Templates[name].ExecuteTemplate(&buf, "base", data); err != nil {
		log.Printf("%s template error: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("%s write error: %v", name, err)
	}
}

// getWaitlistOr404 fetches a waitlist or writes a 404/500.
func (h *WaitlistHandler) getWaitlistOr404(w http.ResponseWriter, id string) (store.Waitlist, bool) {
	wl, err := h.Store.GetWaitlist(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
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
	id := chi.URLParam(r, "id")

	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}
	data := waitlistFormData{
		PageData: h.Shell(w, r, "Edit Waitlist", "waitlists"),
		Waitlist: wl,
		BaseURL:  h.BaseURL,
	}
	h.render(w, "waitlist_edit.html", data)
}

// Edit handles POST to update a waitlist.
func (h *WaitlistHandler) Edit(w http.ResponseWriter, r *http.Request) {
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
			PageData: h.Shell(w, r, "Edit Waitlist", "waitlists"),
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
		if errors.Is(err, store.ErrNotFound) {
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
	PageData
	Waitlist   store.Waitlist
	Entries    []store.WaitlistEntry
	TotalCount int
	Page       int
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
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

	entries, err := h.Store.ListEntriesPaged(id, pageSize, offset)
	if err != nil {
		log.Printf("waitlist detail: list entries %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	total, err := h.Store.CountEntries(id)
	if err != nil {
		log.Printf("waitlist detail: count entries %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := waitlistDetailData{
		PageData:   h.Shell(w, r, wl.Name, "waitlists"),
		Waitlist:   wl,
		Entries:    entries,
		TotalCount: total,
		Page:       page,
		HasPrev:    page > 1,
		HasNext:    offset+pageSize < total,
		PrevPage:   page - 1,
		NextPage:   page + 1,
	}
	h.render(w, "waitlist_detail.html", data)
}

// DeleteEntry handles POST to delete one entry, scoped to its waitlist.
func (h *WaitlistHandler) DeleteEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entryID := chi.URLParam(r, "entryID")
	if err := h.Store.DeleteEntry(id, entryID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "entry not found", http.StatusNotFound)
			return
		}
		log.Printf("waitlist delete entry %s/%s: %v", id, entryID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/waitlists/"+id, http.StatusFound)
}

// writeCSV writes a header and rows, and reports whether the download the client
// received is complete.
//
// csv.Writer buffers, so a write failure surfaces either on a later Write or not
// until Flush — which means the last chunk of a large export can fail after the
// handler has already decided everything went well. The forms export called
// Flush and never asked, so a truncated CSV was served as a clean 200 and the
// operator got a short file with no indication it was short. The waitlist export
// checked; nothing kept the two in step, which is how one of two copies ends up
// wrong.
//
// The response cannot be un-sent — status and part of the body have already
// gone — so the error exists to be logged and correlated, not recovered from.
// Returning it rather than logging inside keeps that decision at the call site,
// which is the only place that knows which export this was.
func writeCSV(w io.Writer, header []string, rows [][]string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	for i, row := range rows {
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("writing row %d of %d: %w", i+1, len(rows), err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("flushing after %d rows: %w", len(rows), err)
	}
	return nil
}

// csvSafe neutralizes CSV formula injection by prefixing values that begin with
// a formula trigger character with a single quote. Entry data comes from public
// signups, so it is untrusted.
func csvSafe(s string) string {
	if len(s) > 0 {
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			return "'" + s
		}
	}
	return s
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

	safeKeys := make([]string, len(keys))
	for i, k := range keys {
		safeKeys[i] = csvSafe(k)
	}
	header := append([]string{"position", "email", "joined_at"}, safeKeys...)
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		row := []string{strconv.Itoa(e.Position), csvSafe(e.Email), e.CreatedAt.Format("2006-01-02T15:04:05Z")}
		for _, k := range keys {
			row = append(row, csvSafe(e.Data[k]))
		}
		rows = append(rows, row)
	}

	if err := writeCSV(w, header, rows); err != nil {
		log.Printf("waitlist export: waitlist %s download is truncated: %v", id, err)
	}
}

type broadcastNewData struct {
	PageData
	Waitlist   store.Waitlist
	EntryCount int
	Broadcasts []store.BroadcastSummary
	Subject    string
	Body       string
	Error      string

	// RecipientCountKnown suppresses the count rather than rendering 0 into a
	// confirmation dialog that says the send cannot be recalled.
	RecipientCountKnown bool
}

type broadcastDetailData struct {
	PageData
	Waitlist  store.Waitlist
	Broadcast store.BroadcastSummary
}

// BroadcastPage renders the compose form plus past broadcasts.
func (h *WaitlistHandler) BroadcastPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}
	count, err := h.Store.CountEntries(id)
	if err != nil {
		log.Printf("broadcast page: count entries %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	past, err := h.Store.ListBroadcasts(id)
	if err != nil {
		log.Printf("broadcast page: list broadcasts %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "broadcast_new.html", broadcastNewData{
		PageData:   h.Shell(w, r, "Broadcast", "waitlists"),
		Waitlist:   wl,
		EntryCount: count,
		Broadcasts: past,
	})
}

// CreateBroadcast validates input, snapshots recipients into a queue, and wakes the worker.
func (h *WaitlistHandler) CreateBroadcast(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wl, ok := h.getWaitlistOr404(w, id)
	if !ok {
		return
	}

	subject := r.FormValue("subject")
	body := r.FormValue("body")

	// The GET path 500s on these same two queries. Degrading them to zero here
	// renders "0 recipients" into both the page and the send-confirmation
	// dialog for a list that may have thousands, and silently empties the past-
	// broadcasts panel — so the reasonable read is that the signups are gone.
	rerender := func(errMsg string) {
		count, countErr := h.Store.CountEntries(id)
		if countErr != nil {
			log.Printf("broadcast rerender: count entries %s: %v", id, countErr)
		}
		past, listErr := h.Store.ListBroadcasts(id)
		if listErr != nil {
			log.Printf("broadcast rerender: list broadcasts %s: %v", id, listErr)
		}
		data := broadcastNewData{
			PageData: h.Shell(w, r, "Broadcast", "waitlists"),
			Waitlist: wl, EntryCount: count, Broadcasts: past,
			Subject: subject, Body: body, Error: errMsg,
		}
		data.Degraded = data.Degraded || countErr != nil || listErr != nil
		data.RecipientCountKnown = countErr == nil
		h.render(w, "broadcast_new.html", data)
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
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "broadcast not found", http.StatusNotFound)
			return
		}
		log.Printf("broadcast detail %s: %v", bid, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if sum.WaitlistID != id {
		http.Error(w, "broadcast not found", http.StatusNotFound)
		return
	}

	h.render(w, "broadcast_detail.html", broadcastDetailData{
		PageData:  h.Shell(w, r, "Broadcast", "waitlists"),
		Waitlist:  wl,
		Broadcast: sum,
	})
}
