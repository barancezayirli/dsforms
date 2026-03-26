package handler

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/store"
)

type noopWebhookSender struct{}

func (n *noopWebhookSender) Send(form store.Form, sub store.Submission) error {
	return nil
}

func setupAdmin(t *testing.T) (*store.Store, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}

	// Inline templates for testing using the per-page clone strategy.
	// Each page template uses "base" as the entry point, which delegates to "content".
	baseTmpl := template.Must(template.New("base").Parse(`{{template "content" .}}`))

	dashTmpl, _ := baseTmpl.Clone()
	template.Must(dashTmpl.New("content").Parse(
		`{{range .Forms}}<span class="form-name">{{.Name}}</span><span class="unread">{{.UnreadCount}}</span>{{end}}` +
			`{{if not .Forms}}<p>No forms yet</p>{{end}}` +
			`<span class="stat-forms">{{.TotalForms}}</span>` +
			`<span class="stat-unread">{{.TotalUnread}}</span>` +
			`<span class="stat-all">{{.TotalAll}}</span>`))

	newTmpl, _ := baseTmpl.Clone()
	template.Must(newTmpl.New("content").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}<form>new form</form>`))

	editTmpl, _ := baseTmpl.Clone()
	template.Must(editTmpl.New("content").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}` +
			`<input value="{{.Form.Name}}"><input value="{{.Form.EmailTo}}">` +
			`<span class="base-url">{{.BaseURL}}</span>`))

	successTmpl := template.Must(template.New("success.html").Parse(`<p>Your message has been sent.</p>`))

	// form_detail template
	detailBase := template.Must(template.New("base").Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	detailTmpl, _ := detailBase.Clone()
	template.Must(detailTmpl.New("content").Parse(
		`{{if .Submissions}}` +
			`{{range .Submissions}}<span class="sub-id">{{.ID}}</span>{{end}}` +
			`<span class="total">{{.TotalCount}}</span>` +
			`<span class="unread">{{.UnreadCount}}</span>` +
			`<span class="page">{{.Page}}</span>` +
			`{{if .HasPrev}}<span class="has-prev">true</span>{{end}}` +
			`{{if .HasNext}}<span class="has-next">true</span>{{end}}` +
			`{{else}}<p>No submissions yet</p>{{end}}`))

	// submission_detail template
	subDetailBase := template.Must(template.New("base").Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	subDetailTmpl, _ := subDetailBase.Clone()
	template.Must(subDetailTmpl.New("content").Parse(
		`<span class="sub-id">{{.Submission.ID}}</span>` +
			`<span class="sub-read">{{.Submission.Read}}</span>` +
			`<span class="form-id">{{.Form.ID}}</span>` +
			`{{range $key, $val := .Submission.Data}}<span class="field-{{$key}}">{{$val}}</span>{{end}}`))

	templates := map[string]*template.Template{
		"dashboard.html":        dashTmpl,
		"form_new.html":         newTmpl,
		"form_edit.html":        editTmpl,
		"success.html":          successTmpl,
		"form_detail.html":      detailTmpl,
		"submission_detail.html": subDetailTmpl,
	}

	ah := &AdminHandler{
		Store:     s,
		SecretKey: testSecretKey,
		BaseURL:   "https://example.com",
		Templates: templates,
		Webhook:   &noopWebhookSender{},
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/forms", ah.Dashboard)
		r.Get("/admin/forms/new", ah.NewFormPage)
		r.Post("/admin/forms/new", ah.CreateForm)
		r.Get("/admin/forms/{id}/edit", ah.EditFormPage)
		r.Post("/admin/forms/{id}/edit", ah.EditForm)
		r.Post("/admin/forms/{id}/delete", ah.DeleteForm)
		r.Get("/admin/forms/{id}", ah.FormDetail)
		r.Post("/admin/forms/{id}/read-all", ah.MarkAllRead)
		r.Get("/admin/forms/{id}/export", ah.ExportCSV)
		r.Get("/admin/forms/{formID}/submissions/{subID}", ah.SubmissionDetail)
		r.Post("/admin/submissions/{id}/read", ah.MarkRead)
		r.Post("/admin/submissions/{id}/delete", ah.DeleteSubmission)
		r.Post("/admin/forms/{id}/test-webhook", ah.TestWebhook)
	})
	r.Get("/success", ah.Success)

	return s, r
}

func doAdminRequest(t *testing.T, s *store.Store, r *chi.Mux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	cookie := auth.CreateSessionCookie(token, "https://example.com")

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
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
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

func TestEditFormEmptyName(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Old", EmailTo: "old@example.com"})
	form := url.Values{"name": {""}, "email_to": {"new@example.com"}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "error") {
		t.Error("error message not shown")
	}
	// Ensure the form was NOT updated.
	f, _ := s.GetForm("f1")
	if f.Name != "Old" {
		t.Errorf("Name = %q, want Old (should not have been updated)", f.Name)
	}
}

func TestEditFormEmptyEmail(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Old", EmailTo: "old@example.com"})
	form := url.Values{"name": {"New"}, "email_to": {""}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	f, _ := s.GetForm("f1")
	if f.EmailTo != "" {
		t.Errorf("EmailTo = %q, want empty", f.EmailTo)
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

func TestFormDetailReturns200(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice"}`})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestFormDetailEmptyState(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1", "")
	if !strings.Contains(w.Body.String(), "No submissions") {
		t.Error("empty state not shown")
	}
}

func TestFormDetailPagination(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	// Create 25 submissions (more than one page of 20)
	for i := 1; i <= 25; i++ {
		_ = s.CreateSubmission(store.Submission{
			ID:      fmt.Sprintf("s%02d", i),
			FormID:  "f1",
			RawData: `{"name":"Test"}`,
		})
	}
	// Page 1 should show has-next but not has-prev
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1", "")
	body := w.Body.String()
	if !strings.Contains(body, `has-next`) {
		t.Error("page 1 should show has-next")
	}
	if strings.Contains(body, `has-prev`) {
		t.Error("page 1 should not show has-prev")
	}

	// Page 2 should show has-prev but not has-next
	w = doAdminRequest(t, s, r, "GET", "/admin/forms/f1?page=2", "")
	body = w.Body.String()
	if !strings.Contains(body, `has-prev`) {
		t.Error("page 2 should show has-prev")
	}
	if strings.Contains(body, `has-next`) {
		t.Error("page 2 should not show has-next")
	}
}

func TestSubmissionDetailReturns200(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice"}`})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/submissions/s1", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "s1") {
		t.Error("submission ID not in body")
	}
}

func TestSubmissionDetailAutoMarksRead(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice"}`})
	// Before viewing, s1 is unread
	sub, _ := s.GetSubmission("s1")
	if sub.Read {
		t.Fatal("submission should be unread before viewing")
	}
	// View the submission detail (auto-marks as read)
	doAdminRequest(t, s, r, "GET", "/admin/forms/f1/submissions/s1", "")
	// After viewing, s1 should be read
	sub, _ = s.GetSubmission("s1")
	if !sub.Read {
		t.Error("submission should be marked read after viewing detail page")
	}
}

func TestSubmissionDetailNotFound(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/submissions/nonexistent", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMarkReadPost(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	w := doAdminRequest(t, s, r, "POST", "/admin/submissions/s1/read", "")
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	sub, _ := s.GetSubmission("s1")
	if !sub.Read {
		t.Error("submission not marked read")
	}
}

func TestMarkReadNotFound(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	w := doAdminRequest(t, s, r, "POST", "/admin/submissions/nonexistent/read", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMarkAllReadPost(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	_ = s.CreateSubmission(store.Submission{ID: "s2", FormID: "f1", RawData: `{}`})
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/read-all", "")
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	count, _ := s.UnreadCount("f1")
	if count != 0 {
		t.Errorf("unread = %d, want 0", count)
	}
}

func TestDeleteSubmissionPost(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{}`})
	w := doAdminRequest(t, s, r, "POST", "/admin/submissions/s1/delete", "")
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0", len(subs))
	}
}

func TestDeleteSubmissionNotFound(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	w := doAdminRequest(t, s, r, "POST", "/admin/submissions/nonexistent/delete", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestExportCSVContentType(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "a@b.com"})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/export", "")
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
}

func TestExportCSVHeaders(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice","email":"a@b.com"}`})
	_ = s.CreateSubmission(store.Submission{ID: "s2", FormID: "f1", RawData: `{"name":"Bob","phone":"123"}`})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/export", "")
	body := w.Body.String()
	// Header should contain union of keys: email, name, phone (sorted)
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 1 {
		t.Fatal("no CSV output")
	}
	header := lines[0]
	if !strings.Contains(header, "email") || !strings.Contains(header, "name") || !strings.Contains(header, "phone") {
		t.Errorf("header = %q, missing expected field columns", header)
	}
}

func TestExportCSVValues(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	_ = s.CreateSubmission(store.Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice","email":"a@b.com"}`})
	_ = s.CreateSubmission(store.Submission{ID: "s2", FormID: "f1", RawData: `{"name":"Bob","phone":"123"}`})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/export", "")
	body := w.Body.String()
	// Row for s1 should have email=a@b.com but phone="" (missing)
	if !strings.Contains(body, "Alice") || !strings.Contains(body, "a@b.com") {
		t.Error("CSV missing Alice's data")
	}
	if !strings.Contains(body, "Bob") || !strings.Contains(body, "123") {
		t.Error("CSV missing Bob's data")
	}
}

func TestExportCSVEmpty(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "C", EmailTo: "a@b.com"})
	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/export", "")
	body := w.Body.String()
	// Should have header but no data rows
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 1 {
		t.Errorf("lines = %d, want 1 (header only)", len(lines))
	}
}

func TestCreateFormWebhookFields(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{
		"name":           {"Contact"},
		"email_to":       {"me@example.com"},
		"webhook_url":    {"https://hooks.slack.com/services/T00/B00/xxx"},
		"webhook_format": {"slack"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	forms, _ := s.ListForms()
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
	if forms[0].WebhookURL != "https://hooks.slack.com/services/T00/B00/xxx" {
		t.Errorf("WebhookURL = %q", forms[0].WebhookURL)
	}
	if forms[0].WebhookFormat != "slack" {
		t.Errorf("WebhookFormat = %q", forms[0].WebhookFormat)
	}
}

func TestEditFormWebhookFields(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Old", EmailTo: "old@example.com"})
	form := url.Values{
		"name":           {"New"},
		"email_to":       {"new@example.com"},
		"webhook_url":    {"https://discord.com/api/webhooks/123/abc"},
		"webhook_format": {"discord"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	f, _ := s.GetForm("f1")
	if f.WebhookURL != "https://discord.com/api/webhooks/123/abc" {
		t.Errorf("WebhookURL = %q", f.WebhookURL)
	}
	if f.WebhookFormat != "discord" {
		t.Errorf("WebhookFormat = %q", f.WebhookFormat)
	}
}

func TestCreateFormEmailOptional(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{
		"name":           {"WebhookOnly"},
		"webhook_url":    {"https://hooks.slack.com/services/T00/B00/xxx"},
		"webhook_format": {"slack"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (email should be optional now)", w.Code)
	}
	forms, _ := s.ListForms()
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
}

func TestEditFormEmailOptional(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "old@example.com"})
	form := url.Values{"name": {"Test"}, "email_to": {""}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (email should be optional)", w.Code)
	}
	f, _ := s.GetForm("f1")
	if f.EmailTo != "" {
		t.Errorf("EmailTo = %q, want empty", f.EmailTo)
	}
}

func TestTestWebhookSuccess(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{
		ID: "f1", Name: "Test", EmailTo: "a@b.com",
		WebhookURL: "https://hooks.example.com", WebhookFormat: "generic",
	})
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/test-webhook", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	// noopWebhookSender always succeeds, so we should get success: true
	if resp["success"] != true {
		t.Errorf("success = %v, want true", resp["success"])
	}
}

func TestTestWebhookNoWebhook(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "a@b.com"})
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/test-webhook", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["success"] != false {
		t.Error("expected success=false for form without webhook")
	}
}

func TestCreateFormWebhookFormatDefaulting(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{
		"name":           {"Test"},
		"webhook_url":    {"https://example.com/hook"},
		"webhook_format": {"teams"}, // invalid format
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	forms, _ := s.ListForms()
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
	if forms[0].WebhookFormat != "generic" {
		t.Errorf("WebhookFormat = %q, want generic (default)", forms[0].WebhookFormat)
	}
}

func TestCreateFormWebhookFormatClearedWhenNoURL(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{
		"name":           {"Test"},
		"webhook_url":    {""},
		"webhook_format": {"slack"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	forms, _ := s.ListForms()
	if forms[0].WebhookFormat != "" {
		t.Errorf("WebhookFormat = %q, want empty (URL is empty)", forms[0].WebhookFormat)
	}
}

func TestCreateFormWebhookInvalidURL(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{
		"name":           {"Test"},
		"webhook_url":    {"not-a-url"},
		"webhook_format": {"generic"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	// Should re-render with error (200), not redirect (302)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (validation error)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "error") {
		t.Error("expected validation error message")
	}
}
