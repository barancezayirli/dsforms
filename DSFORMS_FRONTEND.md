# DSForms — Frontend Design Document

This document is the single source of truth for all UI decisions.
Claude Code must read this file before touching any template or CSS.
When making changes, update the relevant section here first, then implement.

---

## 0. Design Philosophy

**Theme:** Direction A — Midnight + Mint.
Dark navy navigation, clean white content surfaces, mint green (`#6EE7B7`) as
the single accent color. Feels like a sharp developer tool, not a SaaS dashboard.

**Principles:**
- Light, airy content areas with generous whitespace
- Navy nav grounds the page without making it feel dark overall
- Mint used sparingly — unread indicators, CTAs, active states, links only
- No gradients, no shadows (except 3px focus rings on inputs)
- Responsive first — every screen works at 375px with no horizontal scroll
- Server-rendered HTML — no JS frameworks, no hydration
- The only JS allowed: copy-to-clipboard button, mobile nav toggle

---

## 1. Design Tokens

All tokens are defined as CSS custom properties in `base.html` inside a
`<style>` tag in `<head>`. Every template inherits them. Never hardcode hex
values in individual templates — always use these variables.

```css
:root {
  /* Core palette */
  --navy:        #1B1F2E;   /* nav background */
  --navy-light:  #252B3D;   /* nav hover, active states */
  --navy-border: #3D4668;   /* nav dividers, avatar border */
  --navy-muted:  #8892AA;   /* nav secondary text */
  --navy-text:   #C8D0E0;   /* nav hover text */

  --mint:        #6EE7B7;   /* primary accent */
  --mint-dark:   #34D399;   /* mint hover, borders */
  --mint-bg:     #ECFDF5;   /* mint tinted backgrounds */
  --mint-border: #A7F3D0;   /* mint borders */
  --mint-deep:   #065F46;   /* text on mint backgrounds */
  --mint-focus:  #D1FAE5;   /* focus ring fill */

  /* Page */
  --page-bg:     #F0F2F5;   /* outer page background */
  --surface:     #ffffff;   /* card / panel backgrounds */
  --surface-alt: #F8F9FB;   /* secondary surface (table headers, card headers) */

  /* Text */
  --text-primary:   #1B1F2E;
  --text-secondary: #4A5568;
  --text-muted:     #8892AA;
  --text-hint:      #B0BAD0;

  /* Borders */
  --border:       #E4E8F0;   /* default borders */
  --border-light: #F0F2F5;   /* subtle dividers inside cards */

  /* Semantic */
  --danger:        #DC2626;
  --danger-bg:     #FEF2F2;
  --danger-border: #FECACA;
  --warn-bg:       #FFFBEB;
  --warn-border:   #FDE68A;
  --warn-text:     #92400E;
  --success-bg:    #F0FDF9;
  --success-border:#A7F3D0;
  --success-text:  #065F46;

  /* Typography */
  --font: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  --font-mono: "SFMono-Regular", Consolas, "Liberation Mono", monospace;

  /* Radius */
  --radius-sm: 6px;
  --radius:    8px;
  --radius-lg: 10px;
  --radius-xl: 12px;
}
```

---

## 2. Global Reset & Base Styles

Included once in `base.html`:

```css
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

body {
  font-family: var(--font);
  font-size: 14px;
  line-height: 1.5;
  color: var(--text-primary);
  background: var(--page-bg);
}

a { color: inherit; text-decoration: none; }
button { font-family: var(--font); cursor: pointer; }
input, textarea, select { font-family: var(--font); }
```

---

## 3. Component Library

These are the reusable components used across all pages.
Each is a snippet of HTML + CSS. Templates compose them together.

### 3.1 Navigation bar

Full-width, always at the top. Height 50px.

```html
<nav class="nav">
  <a class="nav-brand" href="/admin/forms">ds<em>forms</em></a>
  <div class="nav-links">
    <a class="nav-link {{ if eq .Active "forms" }}on{{ end }}" href="/admin/forms">Forms</a>
    <a class="nav-link {{ if eq .Active "users" }}on{{ end }}" href="/admin/users">Users</a>
    <a class="nav-link {{ if eq .Active "backups" }}on{{ end }}" href="/admin/backups">Backups</a>
  </div>
  <div class="nav-right">
    <a class="nav-user" href="/admin/account">{{ .CurrentUser.Username }}</a>
    <form method="POST" action="/admin/logout" style="margin:0">
      <button class="nav-logout">Log out</button>
    </form>
  </div>
  <button class="nav-burger" id="nav-burger" aria-label="Menu">
    <span></span><span></span><span></span>
  </button>
</nav>
```

```css
.nav {
  background: var(--navy);
  height: 50px;
  display: flex;
  align-items: center;
  padding: 0 20px;
  gap: 4px;
  position: sticky;
  top: 0;
  z-index: 100;
}
.nav-brand {
  color: #fff;
  font-size: 15px;
  font-weight: 500;
  letter-spacing: -0.02em;
  margin-right: 24px;
  flex-shrink: 0;
}
.nav-brand em { color: var(--mint); font-style: normal; }
.nav-links { display: flex; align-items: center; gap: 2px; flex: 1; }
.nav-link {
  color: var(--navy-muted);
  font-size: 13px;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
}
.nav-link:hover { background: var(--navy-light); color: var(--navy-text); }
.nav-link.on { background: var(--navy-light); color: #fff; }
.nav-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}
.nav-user { color: var(--navy-muted); font-size: 13px; }
.nav-user:hover { color: var(--navy-text); }
.nav-logout {
  color: var(--navy-muted);
  font-size: 12px;
  background: none;
  border: none;
  padding: 5px 8px;
  border-radius: var(--radius-sm);
}
.nav-logout:hover { background: var(--navy-light); color: var(--navy-text); }
.nav-burger {
  display: none;
  flex-direction: column;
  gap: 4px;
  background: none;
  border: none;
  padding: 6px;
  margin-left: 8px;
}
.nav-burger span {
  display: block;
  width: 18px;
  height: 1.5px;
  background: var(--navy-muted);
}

/* Mobile nav */
@media (max-width: 640px) {
  .nav-links { display: none; }
  .nav-links.open {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    position: absolute;
    top: 50px;
    left: 0;
    right: 0;
    background: var(--navy);
    padding: 8px 12px 12px;
    z-index: 99;
    border-bottom: 1px solid var(--navy-light);
  }
  .nav-right .nav-user { display: none; }
  .nav-burger { display: flex; }
}
```

Burger JS (inline at bottom of `base.html`):
```html
<script>
  document.getElementById('nav-burger').addEventListener('click', function() {
    document.querySelector('.nav-links').classList.toggle('open');
  });
</script>
```

---

### 3.2 Warning banner

Shown below the nav when `CurrentUser.IsDefaultPassword` is true.
Renders in `base.html` conditionally — no per-template code needed.

```html
{{ if .CurrentUser.IsDefaultPassword }}
<div class="warn-banner">
  Default password is active.
  <a href="/admin/account">Change it now &rarr;</a>
</div>
{{ end }}
```

```css
.warn-banner {
  background: var(--warn-bg);
  border-bottom: 1px solid var(--warn-border);
  padding: 9px 20px;
  font-size: 13px;
  color: var(--warn-text);
  display: flex;
  align-items: center;
  gap: 8px;
}
.warn-banner a {
  color: var(--warn-text);
  font-weight: 500;
  text-decoration: underline;
}
```

---

### 3.3 Flash message

One-time message from the `dsforms_flash` cookie.
Rendered in `base.html` just below the warning banner.

```html
{{ if .Flash }}
<div class="flash flash-{{ .Flash.Type }}">{{ .Flash.Message }}</div>
{{ end }}
```

```css
.flash {
  padding: 10px 20px;
  font-size: 13px;
  border-bottom: 1px solid;
}
.flash-success {
  background: var(--success-bg);
  border-color: var(--success-border);
  color: var(--success-text);
}
.flash-error {
  background: var(--danger-bg);
  border-color: var(--danger-border);
  color: var(--danger);
}
```

---

### 3.4 Page wrapper

Every admin content page wraps its body in `.page`.

```html
<div class="page">
  <!-- content here -->
</div>
```

```css
.page {
  padding: 28px 20px;
  max-width: 960px;
  width: 100%;
  margin: 0 auto;
}
@media (max-width: 640px) {
  .page { padding: 20px 16px; }
}
```

---

### 3.5 Page header

Title + optional action button on the right.

```html
<div class="page-header">
  <h1 class="page-title">Forms</h1>
  <a class="btn btn-mint" href="/admin/forms/new">+ New form</a>
</div>
```

```css
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 24px;
}
.page-title {
  font-size: 18px;
  font-weight: 500;
  color: var(--text-primary);
}
```

---

### 3.6 Buttons

```html
<!-- Default -->
<button class="btn">Cancel</button>

<!-- Mint / primary CTA -->
<button class="btn btn-mint">Save</button>

<!-- Danger -->
<button class="btn btn-danger">Delete</button>

<!-- Ghost (text only) -->
<button class="btn-ghost">Previous</button>
```

```css
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 7px 14px;
  border-radius: var(--radius);
  font-size: 13px;
  font-weight: 500;
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text-secondary);
  cursor: pointer;
  white-space: nowrap;
}
.btn:hover { background: var(--surface-alt); border-color: #C8D0E0; }
.btn:active { transform: scale(0.98); }

.btn-mint {
  background: var(--mint);
  color: var(--mint-deep);
  border-color: var(--mint-dark);
}
.btn-mint:hover { background: var(--mint-dark); }

.btn-danger {
  border-color: var(--danger-border);
  color: var(--danger);
  background: var(--surface);
}
.btn-danger:hover { background: var(--danger-bg); }

.btn-ghost {
  background: none;
  border: none;
  font-size: 13px;
  color: var(--text-muted);
  padding: 5px 8px;
  border-radius: var(--radius-sm);
  cursor: pointer;
}
.btn-ghost:hover { background: var(--page-bg); color: var(--text-secondary); }
```

---

### 3.7 Cards

```css
.card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  overflow: hidden;
}
.card-head {
  padding: 11px 16px;
  background: var(--surface-alt);
  border-bottom: 1px solid var(--border);
  font-size: 12px;
  font-weight: 500;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
```

---

### 3.8 Form inputs

```css
.form-group { margin-bottom: 16px; }
.form-group label {
  display: block;
  font-size: 12px;
  font-weight: 500;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: 5px;
}
input[type="text"],
input[type="email"],
input[type="password"],
textarea {
  width: 100%;
  padding: 8px 11px;
  border-radius: var(--radius);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text-primary);
  font-size: 13px;
  font-family: var(--font);
  transition: border-color .15s;
}
input:focus, textarea:focus {
  outline: none;
  border-color: var(--mint);
  box-shadow: 0 0 0 3px var(--mint-focus);
}
textarea { resize: vertical; min-height: 80px; line-height: 1.6; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.form-error { font-size: 12px; color: var(--danger); margin-top: 4px; }
```

---

### 3.9 Stat cards

Used on the dashboard header strip.

```html
<div class="stats">
  <div class="stat stat-accent">
    <div class="stat-label">Total forms</div>
    <div class="stat-val">3</div>
  </div>
  <div class="stat">
    <div class="stat-label">Unread</div>
    <div class="stat-val">14</div>
  </div>
  <div class="stat">
    <div class="stat-label">All time</div>
    <div class="stat-val">87</div>
  </div>
</div>
```

```css
.stats {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
  margin-bottom: 24px;
}
.stat {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  padding: 14px 16px;
}
.stat-accent { border-top: 3px solid var(--mint); }
.stat-label { font-size: 12px; color: var(--text-muted); margin-bottom: 6px; }
.stat-val { font-size: 22px; font-weight: 500; color: var(--text-primary); }
@media (max-width: 640px) {
  .stats { grid-template-columns: 1fr 1fr; }
  .stats .stat:last-child { grid-column: 1 / -1; }
}
```

---

### 3.10 Unread badge

```html
<span class="badge">3</span>
<span class="badge badge-zero">0</span>
```

```css
.badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 2px 9px;
  border-radius: 10px;
  font-size: 12px;
  font-weight: 500;
  background: var(--mint-bg);
  color: var(--mint-deep);
  border: 1px solid var(--mint-border);
  white-space: nowrap;
}
.badge-zero {
  background: var(--page-bg);
  color: var(--text-muted);
  border-color: var(--border);
}
```

---

### 3.11 Avatar

```html
<div class="avatar">AM</div>
```

```css
.avatar {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: var(--mint-bg);
  border: 1.5px solid var(--mint-border);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 500;
  color: var(--mint-deep);
  flex-shrink: 0;
}
.avatar-sm { width: 28px; height: 28px; font-size: 10px; }
.avatar-lg { width: 42px; height: 42px; font-size: 14px; }
```

---

### 3.12 Code snippet block

Used on the form detail page. Dark terminal style.

```html
<div class="snippet">
  <div class="snippet-bar">
    <div class="snippet-dots">
      <span class="dot dot-red"></span>
      <span class="dot dot-yellow"></span>
      <span class="dot dot-green"></span>
    </div>
    <span class="snippet-filename">contact.html</span>
    <button class="snippet-copy" onclick="copySnippet()">Copy</button>
  </div>
  <pre class="snippet-code" id="snippet-code">...</pre>
</div>
```

```css
.snippet { border-radius: var(--radius-lg); overflow: hidden; border: 1px solid var(--border); }
.snippet-bar {
  background: var(--navy);
  padding: 10px 14px;
  display: flex;
  align-items: center;
  gap: 10px;
}
.snippet-dots { display: flex; gap: 6px; }
.dot { width: 10px; height: 10px; border-radius: 50%; }
.dot-red    { background: #FF5F57; }
.dot-yellow { background: #FFBD2E; }
.dot-green  { background: #28C840; }
.snippet-filename {
  font-size: 12px;
  color: var(--navy-muted);
  font-family: var(--font-mono);
}
.snippet-copy {
  margin-left: auto;
  font-size: 12px;
  color: var(--mint);
  background: none;
  border: none;
  cursor: pointer;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
}
.snippet-copy:hover { background: var(--navy-light); }
.snippet-code {
  background: #252B3D;
  color: #C8D0E0;
  padding: 14px 16px;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.8;
  overflow-x: auto;
  white-space: pre;
  margin: 0;
}
/* Syntax highlight spans */
.t-tag  { color: #93C5FD; }
.t-attr { color: #C4B5FD; }
.t-val  { color: #6EE7B7; }
.t-kw   { color: #FCA5A5; }
```

Copy JS:
```html
<script>
function copySnippet() {
  const code = document.getElementById('snippet-code').innerText;
  navigator.clipboard.writeText(code).then(function() {
    const btn = document.querySelector('.snippet-copy');
    btn.textContent = 'Copied!';
    setTimeout(function() { btn.textContent = 'Copy'; }, 2000);
  });
}
</script>
```

---

## 4. Template Reference

### Template data convention

Every template receives a `TemplateData` struct:

```go
type TemplateData struct {
  CurrentUser  store.User      // always present on admin pages
  Flash        *flash.Message  // nil if no flash
  Active       string          // "forms", "users", "backups" — controls nav highlight
  // page-specific fields below
}
```

---

### 4.1 `base.html`

The shell every admin page extends. Defines all CSS tokens and global components.

**Slots:**
- `{{block "title" .}}` — page title (goes in `<title>` tag)
- `{{block "head" .}}` — optional extra `<style>` or `<link>` in `<head>`
- `{{block "content" .}}` — main page body

**Fixed elements rendered by base (in order):**
1. `<head>` with all design tokens + global CSS
2. `<nav>` — navigation bar
3. Warning banner (if `CurrentUser.IsDefaultPassword`)
4. Flash message (if `Flash` is set)
5. `{{block "content" .}}`
6. Burger menu JS snippet

---

### 4.2 `login.html`

**Route:** `GET /admin/login`
**No base.html** — standalone page, no nav.

**Layout:** Full viewport, content centered vertically and horizontally.

```
┌─────────────────────────────────┐
│                                 │
│                                 │
│   ┌─────────────────────────┐   │
│   │  ds·forms               │   │
│   │  ─────────────────────  │   │
│   │  Username    [_______]  │   │
│   │  Password    [_______]  │   │
│   │                         │   │
│   │          [ Log in ]     │   │
│   │                         │   │
│   │  ✕ Invalid credentials  │   │  ← shown if ?error=1
│   └─────────────────────────┘   │
│                                 │
└─────────────────────────────────┘
```

**Specific styles:**
```css
body.login-page {
  background: var(--navy);
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
}
.login-card {
  background: var(--surface);
  border-radius: var(--radius-xl);
  padding: 32px 28px;
  width: 100%;
  max-width: 360px;
}
.login-brand {
  font-size: 20px;
  font-weight: 500;
  color: var(--navy);
  letter-spacing: -0.02em;
  margin-bottom: 24px;
}
.login-brand em { color: var(--mint); font-style: normal; }
.login-error {
  background: var(--danger-bg);
  border: 1px solid var(--danger-border);
  border-radius: var(--radius);
  padding: 9px 12px;
  font-size: 13px;
  color: var(--danger);
  margin-top: 16px;
}
```

**Template data:** `LoginError bool`

---

### 4.3 `dashboard.html`

**Route:** `GET /admin/forms`
**Active:** `"forms"`

**Layout:**
```
nav
warn-banner?
flash?
.page
  .page-header  [Forms]  [+ New form]
  .stats        [3 forms]  [14 unread]  [87 total]
  .forms-list
    .form-card × N
  .snippet-card  (shown only if at least 1 form exists)
```

**Form card anatomy:**
```
┌──────────────────────────────────────────────────┐
│  [icon]  Contact form            [3 unread]  Edit  Delete  │
│          me@example.com · Jan 12             │
└──────────────────────────────────────────────────┘
```

```css
.forms-list { display: flex; flex-direction: column; gap: 10px; }
.form-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  padding: 14px 18px;
  display: flex;
  align-items: center;
  gap: 14px;
  cursor: pointer;
  transition: border-color .15s;
  text-decoration: none;
}
.form-card:hover { border-color: #B8C4D8; }
.form-icon {
  width: 36px;
  height: 36px;
  border-radius: var(--radius);
  background: var(--page-bg);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.form-icon svg { width: 16px; height: 16px; stroke: var(--text-muted); fill: none; stroke-width: 1.5; stroke-linecap: round; }
.form-info { flex: 1; min-width: 0; }
.form-name { font-size: 14px; font-weight: 500; color: var(--text-primary); }
.form-meta { font-size: 12px; color: var(--text-muted); margin-top: 2px; }
.form-right { margin-left: auto; display: flex; align-items: center; gap: 10px; flex-shrink: 0; }
.form-action { font-size: 12px; color: var(--text-muted); background: none; border: none; padding: 4px 8px; border-radius: var(--radius-sm); }
.form-action:hover { background: var(--page-bg); color: var(--text-secondary); }
.form-action.del:hover { background: var(--danger-bg); color: var(--danger); }
@media (max-width: 640px) {
  .form-right .form-action { display: none; }
  .form-card { cursor: default; }
}
```

The snippet card shown below the forms list is the code block component (§3.12)
showing the snippet for the first form. It includes a note: "Select a form above
to see its specific endpoint."

**Template data:**
```go
Forms       []store.FormSummary
TotalUnread int
TotalAll    int
```

---

### 4.4 `form_new.html` and `form_edit.html`

**Routes:** `GET /admin/forms/new`, `GET /admin/forms/:id/edit`
**Active:** `"forms"`

**Layout:**
```
nav / warn / flash
.page
  .page-header  [New form / Edit form]  [Cancel]
  .card
    .card-head  "Form details"
    .form-body
      Name         [_______________]
      Email to     [_______________]  hint: "where notifications are sent"
      Redirect URL [_______________]  hint: "where to send users after submit (optional)"
    .form-footer  [Save form]
```

Max width of form card: `560px`.

**Template data:**
```go
Form    store.Form   // empty for new, populated for edit
IsEdit  bool
Error   string       // validation error message if any
```

---

### 4.5 `form_detail.html`

**Route:** `GET /admin/forms/:id`
**Active:** `"forms"`

This is the email reader. Two-pane layout on desktop, stacked on mobile.

**Full layout:**
```
nav / warn / flash
.topbar  [Forms / Contact form]  [Mark all read]  [Export CSV]  [Edit form]
.reader
  ├── .msg-list  (280px, fixed left)
  │     .ml-header  "Submissions · 14 total · 3 unread"
  │     .msg-item × N  (unread | read | active)
  └── .pane  (flex: 1)
        .pane-header
          avatar + name + email
          [Mark read]  [Delete]
          meta strip: Received · IP · Form
        .pane-body
          field-block × N
          .field-msg  (message field, left-bordered)
        .pane-footer
          [Previous]  [Next]  "1 of 14 submissions"
```

**Topbar** (replaces `.page` wrapper on this screen):
```css
.reader-topbar {
  background: var(--surface);
  border-bottom: 1px solid var(--border);
  padding: 12px 20px;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.breadcrumb { font-size: 13px; color: var(--text-muted); }
.breadcrumb strong { color: var(--text-primary); font-weight: 500; }
.topbar-actions { margin-left: auto; display: flex; gap: 8px; flex-wrap: wrap; }
```

**Reader layout:**
```css
.reader {
  display: flex;
  flex: 1;
  min-height: 0;
  height: calc(100vh - 50px - 49px);  /* 100vh - nav - topbar */
}
```

**Message list panel:**
```css
.msg-list {
  width: 280px;
  min-width: 280px;
  background: var(--surface);
  border-right: 1px solid var(--border);
  overflow-y: auto;
  flex-shrink: 0;
}
.ml-header {
  padding: 11px 16px;
  border-bottom: 1px solid var(--border);
  font-size: 12px;
  color: var(--text-muted);
  position: sticky;
  top: 0;
  background: var(--surface);
  z-index: 1;
}
.msg-item {
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-light);
  cursor: pointer;
  border-left: 3px solid transparent;
  display: flex;
  gap: 10px;
  align-items: flex-start;
}
.msg-item:hover { background: var(--surface-alt); }
.msg-item.unread { border-left-color: var(--mint); background: #FAFFFE; }
.msg-item.active { background: var(--mint-bg) !important; border-left-color: var(--mint-dark); }
.mi-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--mint);
  flex-shrink: 0;
  margin-top: 5px;
}
.mi-dot.read { background: transparent; }
.mi-body { flex: 1; min-width: 0; }
.mi-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.mi-name { font-size: 13px; font-weight: 500; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mi-name.read { font-weight: 400; color: var(--text-secondary); }
.mi-time { font-size: 11px; color: var(--text-muted); flex-shrink: 0; }
.mi-preview { font-size: 12px; color: var(--text-muted); margin-top: 3px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
```

**Reading pane:**
```css
.pane { flex: 1; background: var(--surface); display: flex; flex-direction: column; min-width: 0; overflow: hidden; }
.pane-header { padding: 20px 24px; border-bottom: 1px solid var(--border-light); flex-shrink: 0; }
.ph-row1 { display: flex; align-items: flex-start; gap: 14px; margin-bottom: 16px; }
.ph-info { flex: 1; min-width: 0; }
.ph-name { font-size: 16px; font-weight: 500; color: var(--text-primary); margin-bottom: 3px; }
.ph-email { font-size: 13px; color: var(--mint); }
.ph-actions { display: flex; gap: 8px; flex-shrink: 0; }
.ph-meta { display: flex; gap: 24px; flex-wrap: wrap; }
.pm-item { display: flex; flex-direction: column; gap: 3px; }
.pm-label { font-size: 11px; font-weight: 500; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; }
.pm-val { font-size: 12px; color: var(--text-secondary); font-family: var(--font-mono); }
.pane-body { padding: 24px; flex: 1; overflow-y: auto; }
.field-block { margin-bottom: 22px; }
.field-label { font-size: 11px; font-weight: 500; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.06em; margin-bottom: 6px; }
.field-val { font-size: 14px; color: var(--text-primary); line-height: 1.6; }
.field-msg {
  background: var(--surface-alt);
  border: 1px solid var(--border);
  border-left: 3px solid var(--mint);
  border-radius: 0 var(--radius) var(--radius) 0;
  padding: 14px 16px;
  font-size: 14px;
  color: var(--text-primary);
  line-height: 1.8;
}
.pane-footer {
  padding: 12px 24px;
  border-top: 1px solid var(--border-light);
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: var(--surface-alt);
  flex-shrink: 0;
  flex-wrap: wrap;
  gap: 8px;
}
.pf-count { font-size: 12px; color: var(--text-muted); }
```

**Empty state** (no submissions yet):
```html
<div class="pane-empty">
  <div class="pane-empty-icon"><!-- envelope SVG --></div>
  <p>No submissions yet.</p>
  <p class="pane-empty-sub">When someone submits your form, it will appear here.</p>
</div>
```
```css
.pane-empty { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; color: var(--text-muted); gap: 8px; padding: 40px; text-align: center; }
.pane-empty-icon svg { width: 40px; height: 40px; stroke: var(--border); fill: none; stroke-width: 1; margin-bottom: 8px; }
.pane-empty-sub { font-size: 12px; }
```

**Responsive — mobile (≤ 640px):**
- `.reader` becomes `flex-direction: column`
- `.msg-list` becomes `width: 100%; border-right: none; border-bottom: 1px solid var(--border); max-height: 240px`
- `.pane-header .ph-actions .btn span` (text labels) hidden, icons only
- When a message item is tapped, JS scrolls the page down to the pane

**Template data:**
```go
Form         store.Form
Submissions  []store.Submission
Active       *store.Submission  // currently selected submission (first unread, or first)
ActiveIndex  int                // 1-based index for "X of N"
```

**Important:** mark the active submission as read automatically when it is
rendered in the pane (call `store.MarkRead` in the handler before rendering).

**Note on field order in pane body:** Iterate `Active.Data` key-value pairs.
The `message` field (if it exists) is always rendered last with `.field-msg`
styling. All other fields render as `.field-val`.

---

### 4.6 `users.html`

**Route:** `GET /admin/users`
**Active:** `"users"`

**Layout:**
```
nav / warn / flash
.page (max-width: 640px)
  .page-header  [Users]
  .card  "Active users"
    user-row × N
  .card  "Add user"
    form-grid: username / password / confirm
    [Add user]
  divider
  "Change password" section
    form-grid: current / new / confirm
    [Update password]
```

**User row:**
```css
.user-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-light);
}
.user-row:last-child { border-bottom: none; }
.user-info { flex: 1; }
.user-name { font-size: 13px; font-weight: 500; color: var(--text-primary); display: flex; align-items: center; gap: 8px; }
.user-date { font-size: 12px; color: var(--text-muted); margin-top: 2px; }
.you-tag { font-size: 11px; background: var(--mint-bg); color: var(--mint-deep); border: 1px solid var(--mint-border); padding: 1px 7px; border-radius: 10px; }
```

Delete button is disabled + muted if it is the current user or the last user.

**Template data:**
```go
Users       []store.User
CurrentUser store.User
Error       string
```

---

### 4.7 `backups.html`

**Route:** `GET /admin/backups`
**Active:** `"backups"`

**Layout:**
```
nav / warn / flash
.page (max-width: 640px)
  .page-header  [Backups]
  .card  "Export database"
    description text
    [Download backup]  filename hint
  .card  "Restore database"
    amber warning box
    upload zone (dashed border, hover: mint border)
    [Restore database]  (right-aligned, danger style)
```

**Upload zone:**
```css
.upload-zone {
  border: 1.5px dashed var(--border);
  border-radius: var(--radius);
  padding: 20px;
  text-align: center;
  cursor: pointer;
  transition: border-color .15s, background .15s;
}
.upload-zone:hover { border-color: var(--mint); background: var(--mint-bg); }
.upload-zone input[type="file"] { display: none; }
.upload-label { font-size: 13px; color: var(--text-muted); margin-bottom: 8px; }
.upload-hint { font-size: 11px; color: var(--text-hint); }
.upload-btn { display: inline-flex; padding: 6px 12px; border-radius: var(--radius); font-size: 12px; border: 1px solid var(--border); background: var(--surface-alt); color: var(--text-secondary); cursor: pointer; margin-bottom: 6px; }
```

**Warning box:**
```css
.warn-box {
  background: var(--warn-bg);
  border: 1px solid var(--warn-border);
  border-radius: var(--radius);
  padding: 11px 14px;
  font-size: 13px;
  color: var(--warn-text);
  line-height: 1.6;
  margin-bottom: 14px;
}
```

---

### 4.8 `success.html`

**Route:** `GET /success`
**No base.html** — standalone, no nav.

**Layout:** Centered vertically and horizontally. Minimal.

```
┌────────────────────────┐
│   ✓                    │
│   Your message         │
│   has been sent.       │
│                        │
│   ← Go back            │
└────────────────────────┘
```

```css
body.success-page {
  background: var(--page-bg);
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
}
.success-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-xl);
  padding: 40px 32px;
  text-align: center;
  max-width: 320px;
  width: 100%;
}
.success-icon {
  width: 48px;
  height: 48px;
  border-radius: 50%;
  background: var(--mint-bg);
  border: 1.5px solid var(--mint-border);
  display: flex;
  align-items: center;
  justify-content: center;
  margin: 0 auto 20px;
}
.success-icon svg { width: 22px; height: 22px; stroke: var(--mint-dark); fill: none; stroke-width: 2; }
.success-title { font-size: 16px; font-weight: 500; color: var(--text-primary); margin-bottom: 8px; }
.success-sub { font-size: 13px; color: var(--text-muted); margin-bottom: 20px; }
.success-back { font-size: 13px; color: var(--text-muted); }
.success-back:hover { color: var(--text-secondary); }
```

---

## 5. Responsive Rules Summary

| Breakpoint | Changes |
|---|---|
| `> 640px` | Full layout, all columns visible |
| `≤ 640px` | Single column, nav collapses to burger, reader stacks, stat grid 2-col |

```css
@media (max-width: 640px) {
  /* nav */
  .nav-links { display: none; }
  .nav-links.open { display: flex; flex-direction: column; ... }
  .nav-burger { display: flex; }
  .nav-right .nav-user { display: none; }

  /* dashboard */
  .form-right .form-action { display: none; }
  .stats { grid-template-columns: 1fr 1fr; }
  .stats .stat:last-child { grid-column: 1 / -1; }

  /* reader */
  .reader { flex-direction: column; height: auto; }
  .msg-list { width: 100%; min-width: 0; max-height: 240px; border-right: none; border-bottom: 1px solid var(--border); }
  .pane-header { padding: 16px; }
  .ph-actions .btn span { display: none; }
  .pane-body { padding: 16px; }

  /* pages */
  .page { padding: 16px; }
  .page-header { margin-bottom: 16px; }
}
```

---

## 6. Updating This Document

When Claude Code changes any visual aspect of the UI, it must:

1. Update the relevant section in this file first.
2. Then implement the change in the template(s).
3. Keep token names in section 1 in sync with what is actually in `base.html`.
4. If adding a new page, add a new subsection to section 4 following the same format:
   route, active nav value, layout diagram, CSS, template data struct.
5. Never introduce a new color hex value that is not already in section 1.
   Add it to section 1 first, give it a variable name, then use the variable.
