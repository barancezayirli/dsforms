package handler

import (
	"net/http"
	"strconv"
)

// PageSizes are the rows-per-page options the pager offers. Anything else in
// the querystring falls back to the first entry — a hand-typed ?size=100000
// would otherwise be a cheap way to make the server materialise every
// submission in the database.
var PageSizes = []int{25, 50, 100}

// Pagination is the state one pager control needs. It is shared by every paged
// list in the admin (submissions, quarantine, waitlist entries) rather than
// each one re-deriving offsets and page numbers slightly differently.
type Pagination struct {
	Page       int
	PageSize   int
	Total      int
	TotalPages int

	// From and To are 1-based inclusive row numbers for the "1–25 of 612"
	// label. Both are 0 when there is nothing to show.
	From int
	To   int

	HasPrev  bool
	HasNext  bool
	PrevPage int
	NextPage int

	// Pages is the windowed list of page numbers to render. A 0 entry marks a
	// gap the template draws as an ellipsis.
	Pages []int
}

// Offset is the SQL OFFSET for this page.
func (p Pagination) Offset() int { return (p.Page - 1) * p.PageSize }

// Sizes exposes the rows-per-page options to templates.
func (p Pagination) Sizes() []int { return PageSizes }

// NewPagination derives pager state, clamping page into range.
//
// Clamping rather than rendering an empty page matters: ?page=999 is a typo or
// a bookmark left over from before a bulk delete, and showing the last real
// page is more useful than showing nothing with no explanation.
func NewPagination(page, pageSize, total int) Pagination {
	pageSize = NormalisePageSize(pageSize)

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	p := Pagination{
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		PrevPage:   max(1, page-1),
		NextPage:   min(totalPages, page+1),
	}

	if total > 0 {
		p.From = (page-1)*pageSize + 1
		p.To = min(page*pageSize, total)
	}
	p.Pages = pageWindow(page, totalPages)
	return p
}

// NormalisePageSize maps a requested rows-per-page onto an offered option.
func NormalisePageSize(n int) int {
	for _, size := range PageSizes {
		if n == size {
			return n
		}
	}
	return PageSizes[0]
}

// pageWindow returns the page numbers to render: always the first and last,
// always the current and its immediate neighbours, and a 0 wherever a run of
// pages is elided.
func pageWindow(page, totalPages int) []int {
	const window = 7 // beyond this many pages, start eliding
	if totalPages <= window {
		pages := make([]int, 0, totalPages)
		for i := 1; i <= totalPages; i++ {
			pages = append(pages, i)
		}
		return pages
	}

	want := map[int]bool{1: true, totalPages: true}
	for i := page - 1; i <= page+1; i++ {
		if i >= 1 && i <= totalPages {
			want[i] = true
		}
	}
	// Keep the control a stable width near the ends, where the current page's
	// neighbours fall outside the range and would otherwise shrink it.
	if page <= 3 {
		want[2], want[3], want[4] = true, true, true
	}
	if page >= totalPages-2 {
		want[totalPages-1], want[totalPages-2], want[totalPages-3] = true, true, true
	}

	var pages []int
	prev := 0
	for i := 1; i <= totalPages; i++ {
		if !want[i] {
			continue
		}
		if prev != 0 && i != prev+1 {
			pages = append(pages, 0) // ellipsis
		}
		pages = append(pages, i)
		prev = i
	}
	return pages
}

// PaginationFrom reads page and size out of a request's querystring.
func PaginationFrom(r *http.Request, total int) Pagination {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	return NewPagination(page, size, total)
}
