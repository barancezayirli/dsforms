package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

type stubMailer struct {
	to, subject, body string
	calls             int
	err               error
}

func (m *stubMailer) SendMail(to, subject, body string) error {
	m.calls++
	m.to, m.subject, m.body = to, subject, body
	return m.err
}

func digestFixture(t *testing.T) (*store.Store, *stubMailer, *Digest) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateForm(store.Form{ID: "f1", Name: "Contact"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
	m := &stubMailer{}
	return s, m, &Digest{
		Store: s, Mailer: m, To: "ops@example.com",
		BaseURL: "https://forms.example.com/", Retention: 30,
	}
}

// An empty daily email trains people to ignore the ones that matter.
func TestDigestSaysNothingWhenNothingIsHeld(t *testing.T) {
	t.Parallel()
	_, m, d := digestFixture(t)

	sent, err := d.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sent {
		t.Error("a digest was sent with nothing held")
	}
	if m.calls != 0 {
		t.Errorf("mailer called %d times, want 0", m.calls)
	}
}

func TestDigestSummarisesHeldSubmissions(t *testing.T) {
	t.Parallel()
	s, m, d := digestFixture(t)

	for i, id := range []string{"h1", "h2"} {
		if err := s.CreateHeldSubmission(store.Submission{
			ID: id, FormID: "f1",
			Data:      map[string]string{"name": "Bot " + id, "message": "buy cheap pills at evil.example"},
			RawData:   `{"name":"Bot ` + id + `","message":"buy cheap pills at evil.example"}`,
			CreatedAt: time.Now().UTC(),
		}, 6+i, 6, []store.SpamSignal{{Rule: "markup", Field: "message", Match: "[url=", Weight: 6}}); err != nil {
			t.Fatalf("CreateHeldSubmission: %v", err)
		}
	}

	sent, err := d.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sent || m.calls != 1 {
		t.Fatalf("sent = %v, calls = %d, want one digest", sent, m.calls)
	}
	if m.to != "ops@example.com" {
		t.Errorf("to = %q", m.to)
	}
	if !strings.Contains(m.subject, "2 submissions") {
		t.Errorf("subject = %q, want the count", m.subject)
	}
	for _, want := range []string{"Bot h1", "Bot h2", "Link markup", "https://forms.example.com/admin/quarantine", "30 days"} {
		if !strings.Contains(m.body, want) {
			t.Errorf("body missing %q:\n%s", want, m.body)
		}
	}

	// The body of a held submission must never leave the server by email.
	// Forwarding spam payloads is how a quarantine digest becomes a delivery
	// mechanism for the very thing it is reporting.
	if strings.Contains(m.body, "buy cheap pills") || strings.Contains(m.body, "evil.example") {
		t.Errorf("digest forwarded the spam payload:\n%s", m.body)
	}
}

func TestDigestDisabledWithoutRecipientOrMailer(t *testing.T) {
	t.Parallel()
	s, m, d := digestFixture(t)
	if err := s.CreateHeldSubmission(store.Submission{
		ID: "h1", FormID: "f1", Data: map[string]string{"name": "Bot"},
		RawData: `{"name":"Bot"}`, CreatedAt: time.Now().UTC(),
	}, 6, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	d.To = ""
	if sent, err := d.Run(); sent || err != nil {
		t.Errorf("with no recipient: sent=%v err=%v, want a silent no-op", sent, err)
	}
	d.To, d.Mailer = "ops@example.com", nil
	if sent, err := d.Run(); sent || err != nil {
		t.Errorf("with no mailer: sent=%v err=%v, want a silent no-op", sent, err)
	}
	if m.calls != 0 {
		t.Errorf("mailer called %d times", m.calls)
	}
}

// A failure to send must be reported, not swallowed — otherwise a broken SMTP
// config looks exactly like a quiet week.
func TestDigestReportsSendFailure(t *testing.T) {
	t.Parallel()
	s, m, d := digestFixture(t)
	m.err = errors.New("smtp: connection refused")
	if err := s.CreateHeldSubmission(store.Submission{
		ID: "h1", FormID: "f1", Data: map[string]string{"name": "Bot"},
		RawData: `{"name":"Bot"}`, CreatedAt: time.Now().UTC(),
	}, 6, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	sent, err := d.Run()
	if err == nil {
		t.Fatal("a send failure returned no error")
	}
	if sent {
		t.Error("sent = true despite the failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error does not name the cause: %v", err)
	}
}
