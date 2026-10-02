package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestWriteError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    *Error
		status int
	}{
		{"default status is 400", &Error{Code: CodeInvalidGrant, Description: "used"}, http.StatusBadRequest},
		{"explicit status", &Error{Code: CodeInvalidClient, Status: http.StatusUnauthorized}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			WriteError(rec, tc.err)

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			// RFC 6749 §5.1/§5.2: token endpoint responses must not be cached.
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tc.err.Code {
				t.Errorf("error = %q, want %q", body["error"], tc.err.Code)
			}
			if body["error_description"] != tc.err.Description {
				t.Errorf("error_description = %q, want %q", body["error_description"], tc.err.Description)
			}
		})
	}
}

func TestErrorOmitsEmptyDescription(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteError(rec, &Error{Code: CodeInvalidRequest})
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if _, ok := body["error_description"]; ok {
		t.Error("empty error_description was written")
	}
}

func TestRedirectURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		base     string
		params   url.Values
		wantQ    url.Values
		wantPath string
	}{
		{
			name:     "adds params",
			base:     "https://client.example.com/cb",
			params:   url.Values{"code": {"c1"}, "state": {"s"}},
			wantQ:    url.Values{"code": {"c1"}, "state": {"s"}},
			wantPath: "/cb",
		},
		{
			// RFC 6749 §3.1.2: a registered URI's own query must be kept.
			name:     "keeps the registered query",
			base:     "https://client.example.com/cb?tenant=7",
			params:   url.Values{"code": {"c1"}},
			wantQ:    url.Values{"tenant": {"7"}, "code": {"c1"}},
			wantPath: "/cb",
		},
		{
			// A registered "code" parameter must not survive alongside ours, or a
			// client reading the first value would read the attacker's.
			name:     "our params replace same-named ones",
			base:     "https://client.example.com/cb?code=planted",
			params:   url.Values{"code": {"real"}},
			wantQ:    url.Values{"code": {"real"}},
			wantPath: "/cb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := url.Parse(RedirectURL(tc.base, tc.params))
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.Path, tc.wantPath)
			}
			q := got.Query()
			for k, v := range tc.wantQ {
				if len(q[k]) != len(v) || q.Get(k) != v[0] {
					t.Errorf("%s = %v, want %v", k, q[k], v)
				}
			}
			if len(q) != len(tc.wantQ) {
				t.Errorf("query = %v, want %v", q, tc.wantQ)
			}
		})
	}
}
