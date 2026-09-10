package handler

import (
	"reflect"
	"testing"
)

func TestNewPagination(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		page      int
		pageSize  int
		total     int
		want      Pagination
		wantPages []int
	}{
		{
			name: "empty list", page: 1, pageSize: 25, total: 0,
			want: Pagination{Page: 1, PageSize: 25, Total: 0, TotalPages: 1, From: 0, To: 0,
				PrevPage: 1, NextPage: 1},
			wantPages: []int{1},
		},
		{
			name: "single partial page", page: 1, pageSize: 25, total: 7,
			want: Pagination{Page: 1, PageSize: 25, Total: 7, TotalPages: 1, From: 1, To: 7,
				PrevPage: 1, NextPage: 1},
			wantPages: []int{1},
		},
		{
			name: "first of several", page: 1, pageSize: 25, total: 612,
			want: Pagination{Page: 1, PageSize: 25, Total: 612, TotalPages: 25, From: 1, To: 25,
				HasNext: true, PrevPage: 1, NextPage: 2},
		},
		{
			name: "last page is partial", page: 25, pageSize: 25, total: 612,
			want: Pagination{Page: 25, PageSize: 25, Total: 612, TotalPages: 25, From: 601, To: 612,
				HasPrev: true, PrevPage: 24, NextPage: 25},
		},
		{
			name: "middle page", page: 13, pageSize: 25, total: 612,
			want: Pagination{Page: 13, PageSize: 25, Total: 612, TotalPages: 25, From: 301, To: 325,
				HasPrev: true, HasNext: true, PrevPage: 12, NextPage: 14},
		},
		{
			// A page number past the end is clamped rather than shown empty:
			// ?page=999 is a typo or a stale bookmark, not a request for nothing.
			name: "page beyond the end clamps", page: 999, pageSize: 25, total: 612,
			want: Pagination{Page: 25, PageSize: 25, Total: 612, TotalPages: 25, From: 601, To: 612,
				HasPrev: true, PrevPage: 24, NextPage: 25},
		},
		{
			name: "page zero clamps up", page: 0, pageSize: 25, total: 612,
			want: Pagination{Page: 1, PageSize: 25, Total: 612, TotalPages: 25, From: 1, To: 25,
				HasNext: true, PrevPage: 1, NextPage: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NewPagination(tt.page, tt.pageSize, tt.total)
			if tt.wantPages != nil {
				if !reflect.DeepEqual(got.Pages, tt.wantPages) {
					t.Errorf("Pages = %v, want %v", got.Pages, tt.wantPages)
				}
			}
			got.Pages = nil
			tt.want.Pages = nil
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewPagination(%d, %d, %d) =\n  %+v\nwant\n  %+v",
					tt.page, tt.pageSize, tt.total, got, tt.want)
			}
		})
	}
}

// The page list is windowed so a form with hundreds of pages does not render
// hundreds of buttons. Zero marks the gap the template renders as an ellipsis.
func TestPaginationPageWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		page  int
		total int
		want  []int
	}{
		{name: "all pages fit", page: 1, total: 5 * 25, want: []int{1, 2, 3, 4, 5}},
		{name: "near the start", page: 2, total: 25 * 25, want: []int{1, 2, 3, 4, 0, 25}},
		{name: "in the middle", page: 13, total: 25 * 25, want: []int{1, 0, 12, 13, 14, 0, 25}},
		{name: "near the end", page: 24, total: 25 * 25, want: []int{1, 0, 22, 23, 24, 25}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NewPagination(tt.page, 25, tt.total).Pages
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Pages = %v, want %v", got, tt.want)
			}
		})
	}
}

// A page list must never contain the same page twice, or the pager renders two
// buttons that both claim to be current.
func TestPaginationPagesAreUnique(t *testing.T) {
	t.Parallel()
	for total := 0; total <= 40*25; total += 25 {
		for page := 1; page <= 40; page++ {
			seen := map[int]bool{}
			for _, p := range NewPagination(page, 25, total).Pages {
				if p == 0 {
					continue
				}
				if seen[p] {
					t.Fatalf("page %d repeated for page=%d total=%d: %v",
						p, page, total, NewPagination(page, 25, total).Pages)
				}
				seen[p] = true
			}
		}
	}
}

func TestNormalisePageSize(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want int }{
		{0, 25}, {25, 25}, {50, 50}, {100, 100},
		{7, 25},    // not an offered option
		{5000, 25}, // not an offered option, and would be a denial of service
		{-1, 25},
	}
	for _, tt := range tests {
		if got := NormalisePageSize(tt.in); got != tt.want {
			t.Errorf("NormalisePageSize(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
