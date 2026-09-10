repo: barancezayirli/dsforms
branch: main

## Last sync

date: 2026-09-09T18:13:22Z

### Updated in this project

- Recreated today's admin UI (forms list, form detail, submission reader, login) from `templates/` as a baseline
- Redesigned admin on the Nocturne design system: sidebar shell, stats home, Gmail-style reader drawer
- New spam quarantine with a per-signal score breakdown using the real weights and threshold from `internal/spam/spam.go`
- New Filter rules screen — block or allow emails, domains, IPs and CIDRs, plus custom keywords
- New GitHub Pages landing page rebuilt from `docs/index.html` content in the Nocturne style
- Handoff package for Claude Code in `design_handoff_dsforms_admin/`

## Screen map

| Project screen | Repo files |
| --- | --- |
| dsforms Current UI · Forms list | templates/base.html, templates/dashboard.html |
| dsforms Current UI · Form detail | templates/base.html, templates/form_detail.html |
| dsforms Current UI · Submission reader | templates/submission_detail.html |
| dsforms Current UI · Login | templates/login.html |
| dsforms Redesign · Home | templates/dashboard.html, internal/spam/spam.go, internal/ratelimit/ratelimit.go |
| dsforms Redesign · Forms | templates/dashboard.html |
| dsforms Redesign · Form detail | templates/form_detail.html |
| dsforms Redesign · Reader drawer | templates/submission_detail.html |
| dsforms Redesign · Quarantine | internal/spam/spam.go, internal/spam/tracker.go, internal/spam/gibberish.go |
| dsforms Redesign · Filter rules | internal/spam/spam.go |
| dsforms Redesign · Form settings | templates/form_edit.html, templates/form_new.html, README.md |
| dsforms Redesign · Waitlists | templates/waitlists.html, templates/waitlist_detail.html, templates/broadcast_new.html |
| dsforms Redesign · Users | templates/users.html, templates/account.html |
| dsforms Redesign · Backups | templates/backups.html, internal/backup/backup.go |
| dsforms Redesign · Login | templates/login.html |
| dsforms Landing | docs/index.html, README.md, internal/webhook/webhook.go, internal/spam/spam.go |
