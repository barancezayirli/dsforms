package handler

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/safe"
	"github.com/barancezayirli/dsforms/internal/store"
)

// DigestMailer sends the daily quarantine digest. It is the SendMail half of
// mail.Mailer, kept narrow so main.go can wire a nil and get no digest rather
// than a crash.
type DigestMailer interface {
	SendMail(to, subject, body string) error
}

// DigestStore is what the quarantine digest needs from storage.
//
// Read-only. The digest reports what is held; MarkNotified and the restore path
// belong to the operator acting on the screen, not to the mail that tells them
// to look.
type DigestStore interface {
	HeldCount() (int, error)
	HeldSubmissions(forms store.FormScope, limit, offset int) ([]store.Submission, error)
	SubmissionSignals(submissionID string) ([]store.SpamSignal, error)
}

// Digest emails an operator once a day about what quarantine is holding.
//
// One message per day, never one per held submission: a spam run produces
// hundreds of holds, and a notification per hold would be indistinguishable
// from the spam it is reporting. The digest is off unless a recipient is
// configured.
type Digest struct {
	Store     DigestStore
	Mailer    DigestMailer
	To        string
	BaseURL   string
	Retention int
}

// Run sends one digest if there is anything to report.
//
// Returns whether a message was sent, so the caller and the tests can tell
// "nothing held" from "failed to send" — a silent no-op would look identical to
// a broken mailer.
func (d *Digest) Run() (bool, error) {
	if d.Mailer == nil || d.To == "" {
		return false, nil
	}

	held, err := d.Store.HeldSubmissions(store.AllForms(), 50, 0)
	if err != nil {
		return false, fmt.Errorf("digest: list held: %w", err)
	}
	if len(held) == 0 {
		// Nothing to say. An empty daily email trains people to ignore it.
		return false, nil
	}

	total, err := d.Store.HeldCount()
	if err != nil {
		return false, fmt.Errorf("digest: count held: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s awaiting review in quarantine.\n\n", plural(total, "submission"))
	for _, sub := range held {
		signals, err := d.Store.SubmissionSignals(sub.ID)
		if err != nil {
			log.Printf("digest: signals for %s: %v", sub.ID, err)
		}
		rules := make([]string, 0, len(signals))
		for _, sig := range signals {
			rules = append(rules, RuleLabel(sig.Check))
		}
		// The sender and the reasons, never the submission body: this email
		// leaves the server, and forwarding spam payloads by mail is how a
		// quarantine digest becomes a delivery mechanism.
		fmt.Fprintf(&b, "  · %s — score %d", senderLabel(sub.Data), sub.SpamScore)
		if len(rules) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(rules, ", "))
		}
		b.WriteString("\n")
	}
	if total > len(held) {
		fmt.Fprintf(&b, "  … and %d more\n", total-len(held))
	}
	fmt.Fprintf(&b, "\nReview them: %s/admin/quarantine\n", strings.TrimRight(d.BaseURL, "/"))
	fmt.Fprintf(&b, "Held submissions are deleted automatically after %d days.\n", d.Retention)

	subject := fmt.Sprintf("dsforms: %s held for review", plural(total, "submission"))
	if err := d.Mailer.SendMail(d.To, subject, b.String()); err != nil {
		return false, fmt.Errorf("digest: send: %w", err)
	}
	return true, nil
}

// Start runs the digest once a day until the process exits.
func (d *Digest) Start(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			// Per tick: a malformed submission should cost one digest, not the
			// process. Run reads every held row, so it is the loop here most
			// exposed to bad data.
			safe.Do("quarantine digest", func() {
				sent, err := d.Run()
				if err != nil {
					log.Printf("quarantine digest: %v", err)
					return
				}
				if sent {
					log.Printf("quarantine digest sent to %s", d.To)
				}
			})
		}
	}()
}
