# Waitlists

A waitlist is not a form. A form notifies *you* on each submission; a waitlist
collects unique signups by email, gives each person a position, and can email
*them* — a confirmation on signup, and a broadcast to everyone when you launch.

## Setting one up

**Waitlists → + New waitlist**. Give it a name and a redirect URL. Optionally
fill in a confirmation subject and body — leave them blank and no email is sent
to subscribers.

## Embedding it

```html
<form action="https://your-dsforms-host/w/YOUR_WAITLIST_ID" method="POST">
  <input type="email" name="email" required>
  <button type="submit">Join the waitlist</button>
</form>
```

Any extra fields you include (`name`, say) are stored alongside the entry. The
`_honeypot` and `_redirect` hidden fields work the same as on a form — see the
[README](../README.md#html-form-options).

## What comes back

**Plain form post** redirects to your redirect URL with `?position=N` appended,
so you can show "You're #42 on the list".

**AJAX** (`Accept: application/json`):

```json
{ "success": true, "position": 42, "already_joined": false }
```

A duplicate email returns the original position with `"already_joined": true`.
No second entry is created.

> **Launch traffic:** `/w/{id}` shares the per-IP rate limiter with form
> submissions (`RATE_BURST` / `RATE_PER_MINUTE`). If signups arrive through a
> shared proxy or CDN egress IP, raise those limits before a spike so real
> visitors are not throttled.

## Confirmation email

Set a subject and body on the waitlist and each new subscriber gets one on first
signup. Template variables: `{{email}}`, `{{name}}`, `{{position}}`. Needs SMTP
configured.

## Broadcasting

Open a waitlist and click **Send broadcast**. The queue is restart-safe: if the
server dies mid-send it resumes where it stopped rather than starting over or
double-sending.

| Variable | Default | Description |
|----------|---------|-------------|
| `BROADCAST_THROTTLE_MS` | `200` | Pause in ms between sends |
| `BROADCAST_MAX_ATTEMPTS` | `3` | Retries before marking a recipient failed |
