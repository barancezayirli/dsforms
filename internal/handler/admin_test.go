package handler

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/store"
)

func setupAdmin(t *testing.T) (*store.Store, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}

	// Inline templates for testing — standalone (no base template dependency).
	tmpl := template.New("")
	template.Must(tmpl.New("dashboard.html").Parse(
		`{{range .Forms}}<span class="form-name">{{.Name}}</span><span class="unread">{{.UnreadCount}}</span>{{end}}` +
			`{{if not .Forms}}<p>No forms yet</p>{{end}}` +
			`<span class="stat-forms">{{.TotalForms}}</span>` +
			`<span class="stat-unread">{{.TotalUnread}}</span>` +
			`<span class="stat-all">{{.TotalAll}}</span>`))
	template.Must(tmpl.New("form_new.html").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}<form>new form</form>`))
	template.Must(tmpl.New("form_edit.html").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}` +
			`<input value="{{.Form.Name}}"><input value="{{.Form.EmailTo}}">` +
			`<span class="base-url">{{.BaseURL}}</span>`))
	template.Must(tmpl.New("success.html").Parse(`<p>Your message has been sent.</p>`))

	ah := &AdminHandler{
		Store:     s,
		SecretKey: testSecretKey,
		BaseURL:   "https://example.com",
		Templates: tmpl,
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s, testSecretKey))
		r.Get("/admin/forms", ah.Dashboard)
		r.Get("/admin/forms/new", ah.NewFormPage)
		r.Post("/admin/forms/new", ah.CreateForm)
		r.Get("/admin/forms/{id}/edit", ah.EditFormPage)
		r.Post("/admin/forms/{id}/edit", ah.EditForm)
		r.Post("/admin/forms/{id}/delete", ah.DeleteForm)
	})
	r.Get("/success", ah.Success)

	return s, r
}

func doAdminRequest(t *testing.T, s *store.Store, r *chi.Mux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	admin, _ := s.GetUserByUsername("admin")
	cookie := auth.CreateSessionCookie(admin.ID, testSecretKey, "https://example.com")

	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestDashboardReturns200(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	w := doAdminRequest(t, s, r, "GET", "/admin/forms", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestDashboardListsForms(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "a@b.com"})
	_ = s.CreateForm(store.Form{ID: "f2", Name: "Support", EmailTo: "c@d.com"})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms", "")
	body := w.Body.String()
	if !strings.Contains(body, "Contact") {
		t.Error("Contact not in body")
	}
	if !strings.Contains(body, "Support") {
		t.Error("Support not in body")
	}
}

func TestDashboardUnreadCounts(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{"a":"b"}`})
	_ = s.CreateSubmission(store.Submission{ID: "s2", FormID: "f1", RawData: `{"a":"b"}`})
	_ = s.MarkRead("s1")
	w := doAdminRequest(t, s, r, "GET", "/admin/forms", "")
	// Unread count should be 1
	if !strings.Contains(w.Body.String(), ">1<") {
		t.Error("unread count 1 not found")
	}
}

func TestDashboardStatStrip(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(store.Submission{ID: "s2", FormID: "f1", RawData: `{}`})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms", "")
	body := w.Body.String()
	if !strings.Contains(body, ">1<") {
		t.Error("total forms 1 not found")
	} // TotalForms
	if !strings.Contains(body, ">2<") {
		t.Error("stats 2 not found")
	} // TotalUnread or TotalAll
}

func TestDashboardEmptyState(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	w := doAdminRequest(t, s, r, "GET", "/admin/forms", "")
	if !strings.Contains(w.Body.String(), "No forms yet") {
		t.Error("empty state not shown")
	}
}

func TestNewFormPage(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/new", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestCreateFormValid(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{"name": {"Contact"}, "email_to": {"me@example.com"}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	forms, _ := s.ListForms()
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
	if forms[0].Name != "Contact" {
		t.Errorf("Name = %q, want Contact", forms[0].Name)
	}
}

func TestCreateFormEmptyName(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{"name": {""}, "email_to": {"me@example.com"}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "error") {
		t.Error("error message not shown")
	}
}

func TestCreateFormEmptyEmail(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{"name": {"Contact"}, "email_to": {""}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render)", w.Code)
	}
}

func TestEditFormPage(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/edit", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Contact") {
		t.Error("form name not pre-filled")
	}
}

func TestEditFormPost(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Old", EmailTo: "old@example.com"})
	form := url.Values{"name": {"New"}, "email_to": {"new@example.com"}, "redirect": {"https://x.com"}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	f, _ := s.GetForm("f1")
	if f.Name != "New" {
		t.Errorf("Name = %q, want New", f.Name)
	}
}

func TestEditFormNotFound(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/nonexistent/edit", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeleteForm(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/delete", "")
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	_, err := s.GetForm("f1")
	if err == nil {
		t.Error("form still exists after delete")
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (cascade)", len(subs))
	}
}

func TestDeleteFormNotFound(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/nonexistent/delete", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSuccess(t *testing.T) {
	t.Parallel()
	_, r := setupAdmin(t)
	req := httptest.NewRequest("GET", "/success", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "message has been sent") {
		t.Error("success message not in body")
	}
}
