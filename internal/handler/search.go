package handler

import (
	"log"
	"net/http"
	"strings"

	"github.com/barancezayirli/dsforms/internal/store"
)

// SearchStore is what search needs from storage: the search query, and nothing
// else.
type SearchStore interface {
	SearchSubmissions(query string, forms store.FormScope, limit int) ([]store.SearchResult, error)
}

// SearchHandler serves the header search field and ⌘K.
type SearchHandler struct {
	Base
	Store SearchStore
}

type searchRow struct {
	store.SearchResult
	Name    string
	Initial string
	Preview string
	Age     string
}

type searchData struct {
	PageData
	Rows []searchRow
}

// Page renders search results for the header query.
//
// Results are limited and unpaginated on purpose: this is a jump-to, not a
// second inbox. Someone who needs to page through matches wants the form's own
// list with a filter, which is a different feature.
func (h *SearchHandler) Page(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	data := searchData{PageData: h.Shell(w, r, "Search", "forms")}
	data.Title = "Search"

	if query != "" {
		results, err := h.Store.SearchSubmissions(query, store.AllForms(), 50)
		if err != nil {
			log.Printf("search %q: %v", query, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, res := range results {
			name := senderLabel(res.Data)
			data.Rows = append(data.Rows, searchRow{
				SearchResult: res,
				Name:         name,
				Initial:      Initial(name),
				Preview:      previewOf(res.Data),
				Age:          Age(res.CreatedAt),
			})
		}
	}
	h.Render(w, "search.html", data)
}
