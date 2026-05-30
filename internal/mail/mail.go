package mail

import (
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

// Mailer implements handler.Notifier using SMTP.
type Mailer struct {
	Host    string
	Port    int
	User    string
	Pass    string
	From    string
	BaseURL string
}

// SendNotification sends an email notification for a new submission.
func (m *Mailer) SendNotification(form store.Form, sub store.Submission) error {
	msg := m.buildMessage(form, sub)
	addr := net.JoinHostPort(m.Host, fmt.Sprintf("%d", m.Port))

	// Skip auth if no credentials (e.g., Mailpit for development)
	var auth smtp.Auth
	if m.User != "" && m.Pass != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}

	from := m.User
	if from == "" {
		from = m.From
	}

	if err := smtp.SendMail(addr, auth, from, []string{form.EmailTo}, []byte(msg)); err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	return nil
}

func (m *Mailer) buildMessage(form store.Form, sub store.Submission) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("From: %s\r\n", m.From))
	b.WriteString(fmt.Sprintf("To: %s\r\n", form.EmailTo))
	b.WriteString(fmt.Sprintf("Subject: [DSForms] New submission: %s\r\n", form.Name))
	b.WriteString(fmt.Sprintf("Date: %s\r\n", sub.CreatedAt.Format(time.RFC1123Z)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")

	b.WriteString(fmt.Sprintf("Form:      %s\r\n", form.Name))
	b.WriteString(fmt.Sprintf("Submitted: %s\r\n", sub.CreatedAt.Format(time.RFC1123Z)))
	b.WriteString(fmt.Sprintf("IP:        %s\r\n", sub.IP))
	b.WriteString("\r\n--- Fields ---\r\n")

	for key, val := range sub.Data {
		b.WriteString(fmt.Sprintf("%-8s %s\r\n", key+":", val))
	}

	b.WriteString("\r\n---\r\n")
	b.WriteString(fmt.Sprintf("View all submissions: %s/admin/forms/%s\r\n", m.BaseURL, form.ID))

	return b.String()
}

// SendMail sends a plain-text email to a single recipient. Used for waitlist
// confirmation and broadcast emails (sends to the subscriber, not the admin).
func (m *Mailer) SendMail(to, subject, body string) error {
	addr := net.JoinHostPort(m.Host, fmt.Sprintf("%d", m.Port))

	var auth smtp.Auth
	if m.User != "" && m.Pass != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}
	from := m.User
	if from == "" {
		from = m.From
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("From: %s\r\n", m.From))
	b.WriteString(fmt.Sprintf("To: %s\r\n", to))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)

	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(b.String())); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// MockCall records a single call to SendNotification.
type MockCall struct {
	Form store.Form
	Sub  store.Submission
}

// SentMail records a single SendMail call.
type SentMail struct {
	To      string
	Subject string
	Body    string
}

// MockMailer records calls for testing. Use NewMockMailer() to create.
type MockMailer struct {
	mu        sync.Mutex
	Calls     []MockCall
	SentMails []SentMail
	ch        chan struct{}
}

// NewMockMailer creates a MockMailer with a signaling channel.
func NewMockMailer() *MockMailer {
	return &MockMailer{ch: make(chan struct{}, 10)}
}

// SendNotification records the call and signals waiters.
func (m *MockMailer) SendNotification(form store.Form, sub store.Submission) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, MockCall{Form: form, Sub: sub})
	m.mu.Unlock()
	m.ch <- struct{}{}
	return nil
}

// CallCount returns the number of times SendNotification was called.
func (m *MockMailer) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Calls)
}

// SendMail records the call and signals waiters.
func (m *MockMailer) SendMail(to, subject, body string) error {
	m.mu.Lock()
	m.SentMails = append(m.SentMails, SentMail{To: to, Subject: subject, Body: body})
	m.mu.Unlock()
	m.ch <- struct{}{}
	return nil
}

// SendMailCount returns the number of SendMail calls recorded.
func (m *MockMailer) SendMailCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.SentMails)
}

// Wait blocks until at least one call is recorded, or timeout elapses.
func (m *MockMailer) Wait(timeout time.Duration) bool {
	select {
	case <-m.ch:
		return true
	case <-time.After(timeout):
		return false
	}
}
