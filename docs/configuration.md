# Configuration

All configuration is environment variables. In Docker Compose they go in `.env`.

Only two are genuinely required: `SECRET_KEY`, and `SMTP_FROM`/`SMTP_HOST` if
you want email notifications. dsforms starts and accepts submissions without
SMTP — it just has nowhere to send the notification.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SECRET_KEY` | **Yes** | — | Random string for signing flash cookies. Generate with `openssl rand -base64 32`. The process refuses to start without it rather than inventing a default, because a shared default would let anyone forge a cookie on any instance. |
| `SMTP_HOST` | For email | — | SMTP server hostname |
| `SMTP_PORT` | No | `587` | SMTP port |
| `SMTP_USER` | No | — | SMTP username (empty for auth-free servers like Mailpit) |
| `SMTP_PASS` | No | — | SMTP password |
| `SMTP_FROM` | For email | — | From address, e.g. `DSForms <noreply@example.com>` |
| `LISTEN_ADDR` | No | `:8080` | HTTP listen address |
| `BASE_URL` | No | — | Public URL. Sets the `Secure` cookie flag, builds links in emails, and acts as one of the origins `_redirect` may point at. |
| `DB_PATH` | No | `/data/dsforms.db` | SQLite database path |
| `SPAM_THRESHOLD` | No | `6` | Score at or above which a submission is held for review. Clamped to 1–20; `0` or unset means the default. Each form can override it in its settings. |
| `DIGEST_TO` | No | — | Address for the daily quarantine digest. Empty disables it. |
| `RATE_BURST` | No | `5` | Max form submissions per IP in a burst |
| `RATE_PER_MINUTE` | No | `6` | Sustained submission rate per IP per minute |
| `BACKUP_LOCAL_DIR` | No | — | Directory for CLI backup snapshots |
| `BROADCAST_THROTTLE_MS` | No | `200` | Pause in ms between individual waitlist broadcast sends |
| `BROADCAST_MAX_ATTEMPTS` | No | `3` | Delivery retries before marking a broadcast recipient failed |

## SMTP providers

`.env.example` ships ready-to-use blocks for:

- **Mailpit** — development, included in `docker-compose.dev.yml`, no auth
- **Gmail** — free, needs an App Password rather than your account password
- **Resend** — free tier, 3k emails/month
- **Brevo** — free tier, 300 emails/day

Any SMTP server works; these are just the ones with a tested example.

## Spam filtering

Submissions are scored, not silently dropped. Anything at or above the threshold
is **held in quarantine** with the breakdown that put it there, so a false
positive is recoverable rather than lost.

Signals include link markup, spam keywords, SQL-injection probes, a URL in a
name field, repeat activity from one IP, and synthetic-looking tokens. Most are
weighted below the threshold on purpose, so a single signal rarely holds a
message on its own — it takes a pile-up.

**Filter rules** (Admin → Filter rules) are the explicit overrides:

- **Blocklist** — email, domain, IP or CIDR. Matches are held on arrival whatever they score.
- **Custom keywords** — added to the built-in list, weighted below the threshold so one hit never holds alone.
- **Allowlist** — an address here is never held, whatever it scores. Allow always beats block.

Set `DIGEST_TO` to get a daily summary of what was held.
