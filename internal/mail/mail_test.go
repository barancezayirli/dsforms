package mail

import (
	"strings"
	"testing"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

func TestMockMailerRecordsCalls(t *testing.T) {
	t.Parallel()
	m := NewMockMailer()
	form := store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}
	sub := store.Submission{ID: "s1", FormID: "f1", Data: map[string]string{"name": "Alice"}}
	err := m.SendNotification(form, sub)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if m.CallCount() != 1 {
		t.Errorf("CallCount = %d, want 1", m.CallCount())
	}
}

func TestMockMailerWait(t *testing.T) {
	t.Parallel()
	m := NewMockMailer()
	form := store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}
	sub := store.Submission{ID: "s1", FormID: "f1"}
	go func() {
		m.SendNotification(form, sub)
	}()
	if !m.Wait(time.Second) {
		t.Fatal("Wait timed out")
	}
}

func TestMockMailerSendMail(t *testing.T) {
	t.Parallel()
	m := NewMockMailer()
	if err := m.SendMail("to@x.com", "Subj", "Body"); err != nil {
		t.Fatalf("SendMail error = %v", err)
	}
	if !m.Wait(time.Second) {
		t.Fatal("expected SendMail call to be signaled")
	}
	if got := m.SendMailCount(); got != 1 {
		t.Errorf("SendMailCount = %d, want 1", got)
	}
	if m.SentMails[0].To != "to@x.com" || m.SentMails[0].Subject != "Subj" {
		t.Errorf("recorded mail = %+v, want To/Subject set", m.SentMails[0])
	}
}

func TestStripHeaderChars(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"normal subject":            "normal subject",
		"inject\r\nBcc: evil@x.com": "injectBcc: evil@x.com",
		"line\nbreak":               "linebreak",
		"carriage\rreturn":          "carriagereturn",
		"":                          "",
	}
	for in, want := range cases {
		if got := stripHeaderChars(in); got != want {
			t.Errorf("stripHeaderChars(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildMessage(t *testing.T) {
	t.Parallel()
	m := &Mailer{
		From:    "DSForms <noreply@example.com>",
		BaseURL: "https://forms.example.com",
	}
	form := store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}
	sub := store.Submission{
		ID:        "s1",
		FormID:    "f1",
		Data:      map[string]string{"name": "Alice", "email": "alice@example.com"},
		IP:        "1.2.3.4",
		CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
	}
	msg := m.buildMessage(form, sub)
	if msg == "" {
		t.Fatal("message is empty")
	}
	tests := []struct {
		name string
		want string
	}{
		{"subject", "Subject: [DSForms] New submission: Contact"},
		{"to", "To: me@example.com"},
		{"from", "From: DSForms <noreply@example.com>"},
		{"form name", "Form:      Contact"},
		{"ip", "IP:        1.2.3.4"},
		{"view link", "https://forms.example.com/admin/forms/f1"},
		{"content type", "Content-Type: text/plain; charset=utf-8"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(msg, tt.want) {
				t.Errorf("message does not contain %q\nmessage:\n%s", tt.want, msg)
			}
		})
	}
}
