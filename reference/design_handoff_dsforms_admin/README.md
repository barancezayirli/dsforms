# Handoff: dsforms Admin Redesign (dashboard, stats, spam quarantine)

## Overview

This package redesigns the dsforms admin UI (`barancezayirli/dsforms`, branch `main`). Today the admin is a 50px navy top bar, a 960px single column, three flat stat counters, and one table per page; spam is silently dropped by `internal/spam` with no record kept and no home/overview screen (login lands on the forms list).

The redesign delivers:

1. A **left-sidebar dashboard shell** replacing the top nav.
2. A real **stats home page** (submissions over time, spam rate, per-form breakdown, rate-limit activity, recent feed).
3. A **spam quarantine** — held submissions become reviewable instead of silently dropped, with a per-signal score breakdown traced to the actual weights in `internal/spam/spam.go`.
4. A **Gmail-style reader**: the submissions table is the list; clicking a row opens the submission in a right drawer over it.
5. A **Filter rules** screen for blocking/allowing emails, domains and IPs, and adding custom keywords.
6. A **redesigned public landing page** for GitHub Pages, replacing `docs/index.html`.

Target visual style: the **Nocturne** design system (dark, compact, Inter, single blurple accent, outlined buttons). This is a deliberate departure from the current mint/navy light theme.

---

## About the design files

The files in this bundle are **design references created in HTML** — prototypes that show intended look and behavior. They are **not production code to copy directly**.

dsforms is Go + `html/template` + vanilla JS with **no JS framework** (see `CLAUDE.md`: "No JS frameworks — vanilla JS only for copy-to-clipboard and mobile burger menu"). The task is to **recreate these designs in that existing environment**:

- Rewrite `templates/base.html`'s CSS variable block and layout for the sidebar shell.
- One Go template per screen, added to the `pageNames` clone list in `main.go`.
- Server-rendered HTML; vanilla JS only where genuinely needed (drawer open/close, mobile nav, bulk-select checkboxes, copy-to-clipboard, chart is inline SVG rendered by the template).
- Follow the repo's own rules: no hardcoded hex in page templates (use the CSS variables in `base.html`), all DB access in `internal/store/store.go`, all mutating admin actions POST-only, test-first.

Do **not** introduce React, Tailwind, or a build step.

The prototype files are Design Components — a single `.dc.html` each, with an inline-styled template plus a logic class holding fake data. Read them for exact values; ignore their component runtime.

## Fidelity

**High-fidelity.** Final colors, typography, spacing, radii, states and copy. Recreate pixel-accurately using the token set below. The one prototype-only concession: all data is fabricated, and screen switching is driven by a client-side `screen` prop rather than routes.

---

## Design tokens

All tokens come from the Nocturne stylesheet (`_ds/nocturne-.../styles.css` in this bundle). Port these into the `:root` block of `templates/base.html`, replacing the current navy/mint set.

### Color

| Token | Value | Use |
|---|---|---|
| `--color-bg` | `#161826` | Page ground, sidebar |
| `--color-surface` | `#232532` | Cards, drawer, inputs |
| `--color-text` | `#e9e9ed` | Primary text |
| `--color-accent` | `#9184d9` | Accent lines, icons, links, outlined primary buttons |
| `--color-divider` | `rgba(233,233,237,.16)` | Rules, hairlines |

Neutral ramp: `100 #f3f5fe` · `200 #e4e7f5` · `300 #cfd3e5` · `400 #b2b6ca` · `500 #9397ab` · `600 #75798c` · `700 #595d6c` · `800 #3f424d` · `900 #292b31`

Accent ramp: `100 #f5f4ff` · `200 #e7e5fe` · `300 #d2cefd` · `400 #b5abfc` · `500 #968ae0` · `600 #796cbf` · `700 #5d5294` · `800 #423a6a` · `900 #2b2741`

Practical mapping used throughout the designs:

- Muted body text `#9397ab`; secondary/meta text `#75798c`; strong-but-not-primary `#b2b6ca`.
- Card hairline / inset border `#3f424d` (via `box-shadow: inset 0 0 0 1px`).
- Tinted "notice" surfaces: background `#2b2741`, inset border `#5d5294`, text `#d2cefd`.
- Avatars: unread `#423a6a` bg / `#d2cefd` text; read `#3f424d` bg / `#cfd3e5` text.
- Chart bars: accepted `#968ae0`, quarantined `#423a6a`; gridlines `#292b31`; sparkline stroke `#968ae0` over `#423a6a` fill at 55% opacity.

**No red/green.** Nocturne is a mono palette — destructive and warning states are expressed with ramp steps and outline-vs-fill, not hue. Delete buttons are `.btn-secondary` with a `#595d6c` inset border. If you decide the product needs a true danger color, add it as a new token; do not improvise per-page.

### Type

- Family: **Inter** (400/500/600/700), `--font-heading` = `--font-body` = `"Inter", system-ui, sans-serif`. Load from Google Fonts (already `@import`ed in the Nocturne stylesheet).
- Headings never exceed weight **500** — hierarchy is size and space.
- Base body 15px / line-height 1.55. Interface text is smaller: 13.5px nav and table body, 13px list rows, 12px meta, 11px labels, 10–10.5px uppercase kickers and badges.
- Uppercase micro-labels: 10–11px, `letter-spacing: .08–.11em`, color `#75798c`.
- Big numbers: KPI value 31px/500/`letter-spacing:-.03em`; secondary stats 24px and 19px at `-.02em`.

### Spacing / radius / elevation

- Spacing scale (0.70× density): `2.8 / 5.6 / 8.4 / 11.2 / 16.8 / 22.4px`. In practice: 12px gaps between cards, 14–16px card padding, 20–24px page padding.
- Radii: `--radius-sm 4px`, `--radius-md 8px` (default for cards, buttons, inputs), `--radius-lg 14px` (dialogs). Badges/tags: 6px. Avatars: 50%.
- Elevation: `--shadow-sm 0 0 0 1px #3f424d` (all cards) · `--shadow-md 0 0 0 1px #595d6c, 0 6px 18px rgba(0,0,0,.55)` · `--shadow-lg 0 0 0 1px #9397ab, 0 16px 40px rgba(0,0,0,.65)` (drawer, dialogs). Never stack heavy shadows.

### Nocturne signature: fading rules

Freestanding rules and table row separators **fade to transparent over 48px at each end** rather than stopping cleanly. Implement as a background strip, not a border:

```css
background: linear-gradient(to right,
  transparent, var(--color-divider) 48px,
  var(--color-divider) calc(100% - 48px), transparent) no-repeat bottom / 100% 1px;
```

Applied on `<tr>` (row-level, so the fade spans the row not each cell), on the sticky header's bottom edge, on the sidebar's right edge (vertical variant, `to bottom` / `position:right` / `size:1px 100%`), and on card section dividers. Table body rows use a fainter `rgba(233,233,237,.08)`. Box outlines and in-control separators stay solid.

### Component classes (from the Nocturne stylesheet — port as-is)

`.btn` + `.btn-primary` (accent **outline**, never filled) / `.btn-secondary` (divider outline) / `.btn-ghost` / `.btn-icon` (36×36) / `.btn-block`; `.tag` + `.tag-accent` / `.tag-neutral` / `.tag-outline`; `.field > label` + `.input` + `.radio`+`.dot` + `.seg`+`.seg-opt`; `.card` + `.card-title` / `.card-kicker` / `.card-body` / `.card-meta`, `.elev-sm/md/lg`; `.table`; `.dialog`.

**Add `white-space: nowrap` to `.btn`** — the stylesheet omits it and labels wrap otherwise (the current dsforms `.btn` already sets it).

### Interaction states (do not leave browser defaults)

- Hover: `color-mix(in srgb, var(--color-accent) 12%, transparent)` on primary; `color-mix(in srgb, var(--color-text) 7%, transparent)` on secondary/ghost. Table rows: 4% text tint layered over the row rule.
- Pressed: one step further (22% accent / 14% text).
- Focus: `:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; }` on everything.
- Disabled: `opacity: .45`, `cursor: not-allowed`.
- `::selection`: 30% accent tint.

### Icons

**Phosphor** (regular weight) throughout — https://phosphoricons.com. The prototype loads the web font from unpkg; for a self-hosted single binary, **vendor the subset you need as inline SVG sprites** (or embed the woff2 via `//go:embed`) rather than depending on a CDN.

Icons used, by name: `tray-arrow-down` (brand mark), `chart-line-up`, `tray`, `users-three`, `shield-warning`, `funnel`, `user-circle`, `database`, `selection-slash`, `sidebar-simple`, `list`, `magnifying-glass`, `plus`, `download-simple`, `upload-simple`, `file-csv`, `envelope`, `envelope-open`, `envelope-simple-slash`, `gear-six`, `dots-three`, `caret-left/right/up/down`, `caret-double-left/right`, `arrow-left`, `arrow-up-right`, `arrow-u-up-left`, `arrow-bend-up-left`, `check`, `x`, `trash`, `prohibit`, `flag`, `user-check`, `user-focus`, `shield-check`, `sliders-horizontal`, `link`, `text-aa`, `bug`, `question`, `megaphone`, `paper-plane-tilt`, `copy`, `hard-drives`, `warning`, `sign-out`.

---

## Layout shell

Root: `display:flex; min-height:100vh`, `background:#161826`, `color:#e9e9ed`, Inter.

### Sidebar (`<aside>`)

- **≥860px**: in-flow, `width:228px`, `flex:none`, `position:sticky; top:0; height:100vh`, `padding:16px 10px 14px`, right edge = vertical fading rule.
- **640–860px**: auto-collapses to a **68px icon rail** — labels, group headings, version tag, DB card and user text hidden; nav items become `justify-content:center; padding:9px 0`. A `title` attribute carries the label.
- **<640px**: leaves the flow entirely and becomes a **slide-in drawer**: `position:fixed; top:0; bottom:0; left:0; width:252px; max-width:82vw; z-index:80; overflow-y:auto; transition:transform .22s ease`, `transform: translateX(-102%)` when closed (plus `pointer-events:none`), `translateX(0)` when open, `box-shadow: var(--shadow-lg)`. A backdrop `position:fixed; inset:0; z-index:75; background:rgba(41,43,49,.62)` sits under it; clicking the backdrop or any nav item closes it. The header's toggle button becomes a hamburger (`ph-list`) and a `ds`*`forms`* brand appears beside it.
- The user's manual toggle overrides the auto-collapse at all widths.

Contents, top to bottom:

1. **Brand row** — 26×26 rounded-7px mark (`#2b2741` bg, `#5d5294` inset border, `#b5abfc` `ph-tray-arrow-down` glyph), then `ds` + `<em>forms</em>` at 16px/500/`-.02em` with the `em` in `#9184d9` and `font-style:normal`, then a `.tag.tag-neutral` version chip pushed right.
2. **Nav groups** with 10px/`.11em` uppercase `#75798c` headings: **Overview** (Home) · **Collect** (Forms, Waitlists ·badge `2.4k`) · **Moderate** (Quarantine ·badge `12`, Filter rules) · **System** (Users, Backups).
   - Item: `display:flex; align-items:center; gap:11px; padding:8px 12px; border-radius:8px; font-size:13.5px`, icon 17px, label `flex:1`, badge `10.5px; padding:1px 7px; border-radius:6px; background:#423a6a; color:#d2cefd`.
   - Idle `color:#b2b6ca`; **active** `background:#2b2741; color:#d2cefd; box-shadow:inset 0 0 0 1px #423a6a`.
3. **Footer** (pushed down with `margin-top:auto`) — a DB status card (`ph-hard-drives`, "dsforms.db", "18.4 MB · WAL · backed up 6h ago") in an 8px box with `#3f424d` inset border, then a user row: 26px circular initials avatar (`#423a6a`/`#d2cefd`), username 13px + role 11px `#75798c`, `ph-sign-out` icon.

### Header (`<header>`, sticky)

`display:flex; flex-wrap:wrap; align-items:center; gap:14px 12px; padding:14px 22px; position:sticky; top:0; z-index:20; background:#161826`, bottom edge = fading rule.

Left: sidebar toggle (`.btn.btn-secondary.btn-icon`), then breadcrumb — 11px `#75798c` parent line over a 15px/500 current-screen title. Right (`margin-left:auto`, wrapping): search field (`flex:0 1 230px; min-width:150px; height:34px`, `#232532` bg, `#3f424d` inset border, 8px radius, `ph-magnifying-glass` 15px `#75798c`, transparent 13px input, `⌘K` chip in a 4px `#3f424d`-outlined box), `Export` secondary button, `New form` primary button — both 34px tall.

### Default-password banner

Rendered under the header when the current user still has the default password (mirrors the existing `.CurrentUser.IsDefaultPassword` check). `margin:16px 22px 0; padding:9px 13px; border-radius:8px; background:#2b2741; box-shadow:inset 0 0 0 1px #5d5294; font-size:13px; color:#d2cefd`, `ph-shield-warning` 16px `#b5abfc`, bolded `admin / admin`, right-aligned "Change it now →" link.

### Content

`padding:20px 22px 56px; display:flex; flex-direction:column; gap:16px`.

**Responsive discipline (applies to every screen — this was the source of most defects during design):**

- Two-column rows are **wrapping flex**, never fixed grid tracks: e.g. main `flex:1.5 1 440px; min-width:0`, side `flex:1 1 320px; min-width:0`.
- The reader drawer's message column uses `flex:1 1 0%` with a `min-width` (a non-zero basis forces premature wrapping).
- Auto-fit grids use `repeat(auto-fit, minmax(min(250px,100%), 1fr))` — the `min()` lets them collapse below the track floor.
- **Every card containing a `<table>` gets `overflow-x:auto`**, and wide tables get an explicit `min-width` (form detail 640px, by-form 460px, blocklist 460px) so they scroll instead of stretching siblings off-screen.
- Every button row with `margin-left:auto` also gets `flex-wrap:wrap`.

---

## Screens

### 1. Home — Overview (`/admin` — new; currently `/admin/forms` is the landing page)

Purpose: answer "what arrived, what's waiting, what got blocked" in one view.

**Header row**: `Good afternoon, {username}` (h3, 25px) over a 13px `#9397ab` summary line ("4 forms and 1 waitlist across this instance · 12 submissions held in quarantine"); right-aligned `.seg` range switcher **7d / 30d / 90d / All**, 30d selected.

**KPI row** — `grid-template-columns: repeat(auto-fit, minmax(min(210px,100%),1fr)); gap:12px`. Each card: `.card.elev-sm`, `padding:14px 15px 46px` (the bottom padding **reserves the sparkline band** — without it the sub-caption sits under the chart and becomes illegible), `position:relative; overflow:hidden`.
- Row 1: 15px accent icon + 11px/`.09em` uppercase `#9397ab` label.
- Row 2 (`margin-top:9px`): 31px/500/`-.03em` value + 12px `#b2b6ca` delta.
- Row 3: 11px `#75798c` sub-caption.
- Sparkline: `<svg viewBox="0 0 220 40" preserveAspectRatio="none">` absolutely positioned `right/bottom/left:0; height:40px; opacity:.55`, an `#423a6a` area path under a 1.5px `#968ae0` line.
- The four: **Submissions** 784 / +12% / "vs. 700 previous 30d" (`ph-tray-arrow-down`) · **Unread** 17 / "4 forms" / "oldest waiting 2 days" (`ph-envelope`) · **Spam held** 146 / 15.7% / "12 awaiting review" (`ph-shield-warning`) · **Waitlist** 2,481 / +134 / "Launch list · open" (`ph-users-three`).

**Chart row** — wrapping flex; chart card `flex:2 1 440px`, side column `flex:1 1 280px`.

*Submissions over time* card (`padding:15px 16px 12px`): title + right-aligned legend (9px rounded swatches: `#968ae0` Accepted, `#423a6a` Quarantined). Plot area is `position:relative; flex:1 1 auto; min-height:190px` so it **grows to fill the card's height** (matching the side column):
- Gridlines are **HTML**, not SVG (they must not distort): 4 absolutely-positioned rows at `top: 0/33.33/66.67/100%` with `transform:translateY(-50%)`, each a right-aligned 26px 9.5px `#75798c` value label + a 1px `#292b31` line filling the rest.
- Bars are one `<svg viewBox="0 0 620 100" preserveAspectRatio="none">` inset `left:34px; right:0; top:0; bottom:0`, so bar heights are percentages of the plot box and always reach the top gridline. 30 bars, `width:14`, `rx:2`, x = `i*(620/30) + step/2 - 7`; quarantined segment stacked beneath the accepted segment.
- Below: 10px `#75798c` date ticks (Feb 4 → Mar 4), `margin-left:34px` to align with the plot.

*Top spam signals* card: title, 11px sub "Weighted hits from `internal/spam`, last 30 days", then six rows of `label / count · w{weight}` over a 6px `#292b31` track with a `#968ae0` (>60%) or `#5d5294` fill. **The weights must match `internal/spam/spam.go`:** Link markup `<a href`, `[url=` → **+6** (equals threshold; drops alone) · Multiple raw URLs → **+2 per link past the first** · Keyword hit → **+5** · Gibberish token → **+3** · SQL probe (`-- -`, `union select`) → **+6** · URL inside a name field → **+4**.

*Rate-limit activity* card: title + `.tag-outline` "5 / min" (from `RATE_BURST`/`RATE_PER_MINUTE`), then rows of monospace IP / request count / state tag (`ok` neutral, `throttled` accent, `blocked` outline), each row separated by a faint fading rule.

**Bottom row** — wrapping flex; *By form* card `flex:1.4 1 420px` (`overflow-x:auto`, table `min-width:460px`): columns Form / Received / Held / Unread / Read rate, where Held is a tag (accent above 40, else neutral) and Read rate is a 64px `#292b31` track with a `#5d5294` fill plus an 11px percentage. *Recent* card `flex:1 1 300px`: five rows of avatar + name (500 weight if unread) + right-aligned time, 12px truncated preview, 11px form name; each row links into the reader drawer.

**Variation — "Focused inbox" home** (prototype prop `homeLayout`): same data, inverted emphasis. `max-width:1000px`; an `Inbox` h3 + `17 unread` accent tag + a `.seg` Unread/All/Held filter; the four KPIs compress into a single 13px×16px surface strip (icon + 19px value + 10px uppercase label) with a shared sparkline filling the remaining width; then a full-width list card of 8 rows (avatar, name, email, state tag, right-aligned time, 13px preview, trailing `ph-caret-right`). Ship one; the strip layout is better for low-volume single-admin installs.

### 2. Forms (replaces `templates/dashboard.html`)

Card grid, `repeat(auto-fit, minmax(min(250px,100%),1fr))`. Each form card (`padding:15px 16px`): `.card-kicker` "form" + `ph-dots-three` menu; 17px/500 name link; 12px `#75798c` "→ {emailTo}"; a three-stat row (`total` / `unread` in `#b5abfc` / `held`, each 19px/500 over a 10px uppercase label); a full-width 34px sparkline (`#5d5294` stroke, no fill); then `Submissions` secondary button (`flex:1`) + a `ph-gear-six` icon button. Final tile is a dashed-border (`1px dashed #3f424d`) 200px-min **New form** button: accent `ph-plus`, 13px label, 11px `#75798c` hint "Name it, pick a notify address, paste the snippet".

### 3. Form detail — the submissions list (replaces `templates/form_detail.html`)

**Header**: form name (h4, 20px) + monospace `.tag-neutral` endpoint chip `/f/4b91c0`; right-aligned wrapping actions `Mark all read` · `Export CSV` · `Settings` (primary).

**Stat strip**: four `.card.elev-sm` (`padding:12px 16px; min-width:150px; flex:1`) — Total 612 / "all time", Unread 9 / "oldest 2d", Held 84 / "13.7% of traffic", Median reply 4h / "last 30d".

**Bulk bar** (visible when ≥1 row selected): `#232532` 8px box with `--shadow-sm`, a select-all `.radio`, "3 selected" 12px `#9397ab`, then right-aligned `Mark read` · `Move to quarantine` · `Delete`. Preserve the existing standalone-`<form>` + `form=""` attribute trick from `form_detail.html` so checkboxes inside the table can target the bulk form without DOM nesting.

**Table** (`overflow-x:auto`, `min-width:640px`): checkbox · From · Preview · Signals · Received · Actions.
- From cell: a 7px unread dot (`#9184d9`, transparent when read) + name (13.5px, `#e9e9ed`/500 unread vs `#b2b6ca` read) over an 11px `#75798c` email.
- Preview: `max-width:300px`, `#9397ab`, ellipsised.
- Signals: `new` accent tag / `flagged 4` outline tag / `read` neutral tag.
- Actions: three ghost icon buttons — open (`ph-arrow-up-right`), quarantine, delete.
- **Row click / name click / open button all open the reader drawer** (see §4).

**Pagination footer** (top edge = fading rule, `flex-wrap:wrap`): a `Rows` label + `<select class="input">` 25/50/100; "1–25 of 612" in 12px `#9397ab`; right-aligned first / prev / numbered pages `1 2 3 … 25` / next / last — 30×30 icon buttons, current page `.btn-primary`, the `…` a disabled non-tabbable item.

### 4. Submission reader — right drawer (replaces `templates/submission_detail.html`)

Opens **over** the form detail list; the list stays visible behind a `position:fixed; inset:0; z-index:65; background:rgba(41,43,49,.55)` backdrop. Drawer: `position:fixed; top:0; right:0; bottom:0; z-index:66; width:min(640px,100%); background:#232532; box-shadow:var(--shadow-lg)`, flex column with an internally scrolling body. Closing: the ✕, the backdrop, or Esc.

**Sticky toolbar** (`padding:11px 14px; position:sticky; top:0`, bottom fading rule, `#232532` background so content scrolls under it): ✕ close · up/down caret buttons (30×30) with an 11px `1 of 612` counter — **steps between submissions without closing the drawer** · right-aligned icon buttons Mark read / Move to quarantine / Delete and a `Reply` primary (`mailto:` with the sender's address).

**Body** (`padding:20px 22px 8px`):
1. Sender header — 38px circular initials avatar (`#423a6a`/`#d2cefd`), 17px/500 name over a 12px `#9397ab` email; right-aligned 12px timestamp over an 11px monospace `{IP} · {country}`.
2. `.hr` fading rule, `margin:18px 0`.
3. **Field grid** — `grid-template-columns: minmax(0,120px) minmax(0,1fr); gap:9px 18px`, one pair per key of the submission's data (11px/`.07em` uppercase `#75798c` key, 13px `#e9e9ed` value). Iterate the stored JSON keys exactly as `submission_detail.html` does today; **exclude** `message`/`body`/`content`, which render below.
4. `message` label, then the body at 15px/1.65, `white-space:pre-wrap`, `text-wrap:pretty`, **flush left with no accent border** (the old `.field-msg` left rule is deliberately dropped).
5. **Spam check panel** — an 8px box (`#3f424d` inset border, `margin:0 22px`): `ph-shield-check` + "Spam check passed" + right-aligned `.tag-outline` `score 0 / 6`, then a 12px `#75798c` line naming which checks passed and the notification delivery time. For a held submission this becomes the score breakdown from §5.

### 5. Quarantine — the new spam queue (no equivalent today)

Purpose: `internal/spam` currently drops matches silently, so a false positive is unrecoverable. This screen makes held submissions reviewable for 30 days.

**Header**: `Quarantine` h4 + `12 held` accent tag + 12px `#9397ab` "Auto-deleted after 30 days · threshold 6"; right-aligned `Tune filter` (→ Filter rules) · `Empty quarantine`.

**Stat strip**: `repeat(auto-fit, minmax(min(160px,100%),1fr))` — Held (30d) 146 / "15.7% of traffic", Awaiting review 12 / "oldest 6d", Restored 3 / "2.1% false positive", Auto-deleted 131 / "after 30d".

**Two-column wrapping flex** — table card `flex:1.5 1 440px` (`overflow-x:auto`), detail panel `flex:1 1 320px`.

*Table card*: a bulk bar (select-all radio, "2 selected", right-aligned `Not spam` primary + `Confirm spam` secondary), then columns Rule-free: checkbox · **From** (monospace name/first-field over an 11px monospace IP) · **Signals** (one nowrap neutral tag per matched rule) · **Score** (a 26×22 6px badge — `#423a6a`/`#d2cefd` at ≥9, `#292b31`/`#b2b6ca` below) · **Form** (12px `#9397ab` form name over an 11px `#75798c` age). Selected row: `background: rgba(43,39,65,.6)`, name in `#d2cefd`.

*Detail panel — "Why it was held"*: header (11px uppercase kicker + accent `score N / 6` tag, 15px/500 name, 12px monospace `{ip} · {form} · {age}`); then
- a **threshold meter**: 8px `#292b31` track with a `linear-gradient(to right,#5d5294,#968ae0)` fill at `score/max`, with `0` / `threshold 6` / `max` labels beneath;
- a **per-rule breakdown**: one 8px box per contributing rule (`#3f424d` inset border) containing an accent rule icon + 12px/500 rule name + right-aligned `.tag-outline` `+{weight}`, an 11px `field {name} · matched` line, and the **matched substring** in an 11px monospace `#161826` code block. Rule → icon: link markup `ph-link`, keyword `ph-text-aa`, SQL probe `ph-bug`, gibberish `ph-question`, URL-in-name `ph-user-focus`;
- **actions**, a 2-column grid: `Not spam` (primary) · `Confirm spam` · `Block this IP` · `Block sender` · `Allowlist sender`, then a full-width ghost `Report as false positive — retune weights`;
- an 11px note: "Restoring moves the submission back into **{form}** as unread and sends the notification that was withheld."

### 6. Filter rules (new)

Purpose: give the operator explicit control — **mark an email address, a domain, or an IP/CIDR as spam**, allowlist after a false positive, and extend the keyword list.

- **Score threshold** card: title + 12px explainer, right-aligned `.seg` **Lenient · 9 / Balanced · 6 / Strict · 4**.
- **Blocklist** card (`flex:1.2 1 420px`, `overflow-x:auto`): `ph-prohibit` + title + right-aligned `{n} rules` accent tag; 12px explainer "Anything matching a rule here is held as spam on arrival, whatever it scores."; then an add row — a `.seg` type switch **Email / Domain / IP / CIDR**, a flexible `.input` (placeholder `spammer@example.com, example.ru, or 45.155.204.0/24`), and a `Block` primary button. Table (`min-width:460px`): monospace Rule · Type tag · Blocked count · Added date · a ghost ✕ remove button.
- **Allowlist** card: `ph-user-check` + title, "Never held, whatever the score. Use it after a false positive."; an input + `Allow` secondary; then rows of monospace value / 11px note (`domain`, `restored Mar 3`, `office IP`) / ✕.
- **Custom keywords** card: `ph-text-aa` + title, "Added to the built-in list. Each hit scores +5 — a single keyword never holds a submission alone."; a wrap of neutral tags each with a trailing ✕; an input + `Add` secondary.

Entry points: the sidebar (Moderate → Filter rules), Quarantine's `Tune filter`, and the drawer's `Block this IP` / `Block sender` / `Allowlist sender` actions (which should pre-fill the add field with that submission's value).

### 7. Form settings (replaces `form_edit.html` / `form_new.html`)

Two wrapping columns, `max-width:1080px`.

Left: a **Form** card (`.field` + `.input` for Name, Notify address, Redirect after submit, Webhook URL with placeholder `https://hooks.slack.com/services/…`) and a **Spam & abuse** card — 12px explainer, the sensitivity `.seg`, then four custom toggles. Toggle: a 34×19px 10px-radius pill (`#5d5294` bg + `#796cbf` inset border when on, `#292b31`/`#3f424d` when off, knob drawn as an inset `#d2cefd` box-shadow offset 15px on / 3px off) beside a 13px label + 11px `#75798c` hint. The four: **Honeypot field** ("Drop submissions where `_honeypot` is filled", on) · **Quarantine instead of drop** ("Held submissions stay reviewable for 30 days", on) · **Email me about held submissions** ("One digest per day, never per submission", off) · **Block IPs after 3 held submissions** ("Uses the in-process tracker, resets on restart", on — this mirrors `spam.Tracker.Seen`'s 3rd-submission threshold).

Right: an **Endpoint** card — the `POST https://…/f/{id}` URL in a 12px monospace `#161826` 8px block with a ghost `Copy` button, then the paste-ready HTML snippet in an 11.5px/1.7 `<pre>` (same content as the README's snippet, including `_honeypot` and `_redirect`), each with its own Copy. Then a **Danger zone** card: 12px warning copy naming the submission count, `Export CSV` + `Delete form` (secondary with a `#595d6c` inset border).

### 8. Waitlists (replaces `waitlists.html` / `waitlist_detail.html` / `broadcast_new.html`)

Left column (`flex:1.4 1 400px`): a **Launch list** card — title + `.tag-outline` "open" + right-aligned Settings link; a three-stat row (2,481 signups / +134 this week in `#b5abfc` / 38% confirm open rate, each 26px/500/`-.03em`); a 90px full-width area chart (`#2b2741` fill under a 1.75px `#968ae0` line). Below, a signups table (`overflow-x:auto`): monospace position `#2481` · Email · Source neutral tag (`landing`/`hn`/`newsletter`) · Joined.

Right column (`flex:1 1 320px`): a **Broadcast** card — `ph-megaphone` + title, "Restart-safe queue — 200ms between sends, 3 attempts per recipient." (i.e. `BROADCAST_THROTTLE_MS` / `BROADCAST_MAX_ATTEMPTS`); Subject `.input`; Body `textarea.input` (min-height 120px) showing the `{{name}}` / `{{position}}` template variables; a footer line "2,481 recipients · ~8 min" + `Send broadcast` primary; a fading `.hr`; then **Recent sends** — subject / count / state tag (`delivered` neutral, `sending` accent).

### 9. Users (replaces `users.html` + `account.html`)

Left (`flex:1.3 1 400px`, `overflow-x:auto`): a **Users** card with a right-aligned `Add user` primary; table columns User (28px initials avatar + name + role tag — `you · owner` accent, `admin` neutral, `cli only` outline — over an 11px "added {date}") · Last seen · Sessions · a right-aligned ghost action (`Sessions` / `Remove`). Keep the existing "Cannot delete yourself" guard.

Right (`flex:1 1 300px`): **Your account** — "Changing your password signs out every session on every device." (this is real: password change invalidates all sessions); Current password + New password `.input`s; a 4-segment strength meter (4px bars, filled `#796cbf`, empty `#292b31`); an 11px note "bcrypt cost 12 · minimum 12 characters"; `Update password` primary block button.

### 10. Backups (replaces `backups.html`)

Left column: a **dsforms.db** card — `ph-database` + title + `.tag-outline` "healthy"; a three-stat row (18.4 MB size / 3,847 rows / WAL journal); `Download snapshot` primary block button; an 11px note "VACUUM INTO a temp file, then streamed — safe while the server is running." Then a **Restore** card — a tinted warning box (`#2b2741` / `#5d5294` / `#d2cefd`, `ph-warning`) reading "This replaces every form, submission and user with the uploaded file's contents. It cannot be undone."; a dashed-border drop zone (`ph-upload-simple`, "Drop a `.db` file, or browse", "up to 100 MB" — matching the 100MB backup-import exception); a disabled `Restore database` block button that enables on file select.

Right (`overflow-x:auto`): a **Snapshots** card — "Written to `BACKUP_LOCAL_DIR` by the CLI" — table of Taken / Size / Source tag (`cron`/`manual`) / a ghost `Download`.

### 11. Login (replaces `login.html`)

Full-viewport `#161826` takeover, content centred, `max-width:380px`. A decorative radial glow above the card: `position:absolute; top:-160px; left:50%; translateX(-50%); width:760px; height:420px; border-radius:50%; background:radial-gradient(closest-side,#2b2741,transparent); pointer-events:none`. Above the card, a centred 30px brand mark + `ds`*`forms`* at 20px/500. Card is `.card.elev-md`, `padding:22px`: Username + Password `.field`s, a 38px `Log in` primary block button, then an 11px centred note "5 attempts, then a 15-minute lockout on this IP." (matching the existing login guard). Below the card, an 11px centred footer "{host} · dsforms {version} · self-hosted". Invalid credentials: reuse the tinted notice treatment (`#2b2741` / `#5d5294` / `#d2cefd`) above the fields — not a red banner.

### 12. Empty states

Four reference cards (`repeat(auto-fit, minmax(min(300px,100%),1fr))`), each centred with `padding:32px 24px`: a 46px 12px-radius icon tile (`#2b2741` bg, `#5d5294` inset border, `#b5abfc` glyph), a 16px/500 title, a 12.5px `#9397ab` body at `max-width:34ch` with `text-wrap:pretty`, and one CTA.

- **No forms yet** / "A form is a name, a notify address and an endpoint. Takes about twenty seconds." / `Create your first form` (primary)
- **Quarantine is clear** / "Nothing has crossed the threshold in the last 30 days. Held submissions appear here with their score breakdown." / `Tune the filter`
- **Inbox zero** / "Every submission on this instance has been read. New ones arrive here and trigger a notification." / `View all submissions`
- **No signups yet** / "Paste the waitlist snippet on your landing page and positions start filling in from #1." / `Copy the snippet`

## The landing page (`docs/index.html`)

Separate deliverable, same token set. The current `docs/index.html` is a single self-contained file with its own inline `:root` (GitHub-dark `#0d1117` / green `#3fb950` / DM Mono) served by GitHub Pages. **Keep that shape** — one file, all CSS inline, no build step, no external stylesheet beyond the Google Fonts link — and swap the token block and layout for the Nocturne set above. Inter only; the mono role uses `ui-monospace, Menlo, monospace` (DM Mono is dropped, so one fewer font request).

The file is `dsforms Landing.dc.html`. Content is lifted from the existing page and `README.md`, so the copy is already accurate — recheck it against the repo at build time, not against this document.

### Direction

Where the admin is dense, the landing page is **flush-left and asymmetric** per Nocturne: headings hug the left edge, whitespace lives on the right, and no section is centred. The page is fluid — `max-width:1240px` content column, `padding:0 28px`, wrapping flex rows throughout, `clamp()` on every display size.

The **section kicker** is the page's structural motif, replacing the old `// how it works` mono comments: a solid 18×2px accent bar, then an 11px `.11em` uppercase accent label, then an `h2` at `clamp(27px,3.2vw,34px)` / `-.02em`. Every section opens this way — it is what gives the page rhythm without a single divider line.

### Sections, in order

1. **Sticky nav** — `rgba(22,24,38,.88)` + `backdrop-filter:blur(10px)`, bottom edge a fading rule. Brand mark + `ds`*`forms`*, then anchor links (How it works / Spam filter / Webhooks / Waitlist / Admin), then `Star` secondary + `Self-host it` primary pushed right. Wraps rather than scrolls on narrow screens.
2. **Hero** — two-column wrapping flex over a decorative radial glow (`radial-gradient(closest-side,#2b2741,transparent)`, 820×560, offset above-left, `pointer-events:none`). Left: the kicker, `h1` at `clamp(38px,5.4vw,58px)` / `-.03em` / `text-wrap:balance` — "Form submissions for static sites." — an 18px `#b2b6ca` paragraph, two 42px CTAs, and a row of five neutral tags (One Go binary / SQLite, no ORM / MIT licensed / ~20MB image / No SaaS). Right: the code card (below).
3. **How it works** — three step cards, each with a mono `01/02/03`, a 34px accent icon tile pushed right, a 17px title and a sentence. `repeat(auto-fit,minmax(min(260px,100%),1fr))`.
4. **Spam filter** — copy on the left (four accent-checked lines), and on the right a **live replica of the quarantine breakdown panel** from the admin: threshold meter, two rule boxes with their matched strings, `Not spam` / `Block this IP` buttons. Showing the actual reviewer UI is the argument; a feature bullet is not.
5. **Webhooks** — three format cards (Slack / Discord / Generic JSON, with `ph-slack-logo`, `ph-discord-logo`, `ph-brackets-curly`) beside the generic JSON payload in a code card.
6. **Waitlist mode** — the `POST /w/{id}` snippet and JSON response beside three cards (Dedup by email / Position / Broadcast).
7. **Admin UI** — a scaled-down mock of the redesigned dashboard: the 158px sidebar with its five nav items, four mini KPI tiles, and the stacked bar chart. Below it, a caption pointing at `docs/screenshots/`. **Replace this mock with real screenshots once the admin ships** — it exists so the page has something to show today.
8. **What you get** — three labelled clusters (**Notifications** / **Data** / **Security**), each a 2×2 grid of feature cards: 30px accent icon tile, 14.5px title, a 12.5px sentence, and the relevant env var or route in 10.5px mono underneath. Grid is `repeat(auto-fit,minmax(min(320px,100%),1fr))` capped at `max-width:960px` so a four-item group is always an even 2×2 and never orphans a card; the group heading rule is capped to the same width.
9. **Self-host** — copy plus the `ghcr.io` image callout on the left; three terminal cards (Clone and configure / Start it / Log in) on the right, each with a `ph-terminal-window` header and a Copy button.
10. **Closing CTA** — a 12px-radius panel on the `--color-section` ground (`#262a60` with a `#353b80` radial bloom, the one place besides nothing else on this page where a saturated fill is allowed), an `h2`, a `#d2cefd` paragraph, and two **outlined** buttons (`Star on GitHub` in `--color-text`, `Deploy it now` in `#4c5397`) — outlined even here, because Nocturne never solid-fills a button.
11. **Footer** — top fading rule, brand mark + "MIT · Go + SQLite", then GitHub / Issues / author links pushed right.

### Code blocks

The page's own idiom, used in the hero, webhooks, waitlist and self-host sections: an 8px `#232532` card with `--shadow-sm`, a header strip (an icon, a mono filename or route label in 12px `#9397ab`, and a ghost `Copy` button) separated by a fading rule, then a `<pre>` at 12.5px / line-height 1.85 with `overflow-x:auto`.

Syntax colors are **mono-tinted from the accent ramp**, not a conventional rainbow theme — tag `#b5abfc`, attribute name `#9397ab`, attribute value `#d2cefd`, bare keyword (`required`) `#968ae0`, comment `#75798c`, plain text `#cfd3e5`. Terminal blocks put the `$` prompt in the accent and `#` comments in `#75798c`.

Two hard-won constraints:

- **Keep sample lines under ~60 mono characters.** These cards are `flex:1 1 340–380px` with a `max-width:520px`, so a `<pre>` never gets more than ~490px of content box — a long `action="https://…"` attribute clips with no visible scrollbar on overlay-scrollbar platforms. Break attributes onto continuation lines (the hero splits `method` and `action`) and break JSON responses across lines rather than inlining them.
- Reuse the existing copy-to-clipboard helper from the current page (button text swaps to "Copied!" for 2s, `aria-label` updated with it).

### Notes

- The old page's `fadeUp` hero animation, macOS traffic-light dots, and green `✓` feature list are all dropped. Nothing on the page animates except CSS hover tints; `scroll-behavior:smooth` handles the anchor links.
- All nav/CTA anchors are in-page (`#how`, `#spam`, `#webhooks`, `#waitlist`, `#admin`, `#self-host`, `#top`) except the GitHub, Issues and author links.
- Phosphor icons here too — for a static Pages file, inline the SVGs you use rather than loading the icon font over a CDN.
- Set the same `<meta name="description">` as today; the page has no analytics, no tracker, and no third-party request beyond the font.

---

## Interactions & behavior

| Interaction | Behavior |
|---|---|
| Sidebar toggle | ≥640px: expand ⇄ 68px rail. <640px: open/close the drawer. Manual state overrides the width-based default. Persist the preference (localStorage or a cookie). |
| Nav item click | Navigate; on mobile also close the drawer. |
| Submission row click | Open the reader drawer for that submission. Ideally `GET /admin/forms/{id}/submissions/{sid}` returning a fragment, with a full-page fallback for no-JS. |
| Drawer close | ✕, backdrop click, or Esc. Return focus to the originating row. |
| Drawer up/down | Load previous/next submission in the current list order without closing; disable at the ends. |
| Mark read | POST; row loses its dot and 500-weight name; the unread KPI and the form's unread count decrement. Opening a submission should mark it read (as today). |
| Bulk select | Header checkbox toggles all; the bulk bar's label reads "N selected"; actions disabled at 0. Confirm destructive bulk actions with a count. |
| Quarantine row click | Select it; the right panel re-renders its breakdown. |
| Not spam | POST restore → the submission returns to its form as unread, the withheld notification sends, the row leaves the queue. |
| Confirm spam | POST delete; consider offering "and block this sender" in the same confirm. |
| Block IP / Block sender | POST a blocklist rule for that value, then delete the submission. Surfaced in Filter rules. |
| Allowlist sender | POST an allowlist rule; if the submission is held, restore it too. |
| Report false positive | POST a labelled sample for weight tuning. At minimum, log it — do not silently discard. |
| Filter rules add | Validate by type (RFC-ish email, hostname, IPv4/IPv6/CIDR); reject duplicates and overlapping CIDRs with an inline `.form-error`-equivalent. |
| Pagination | Rows-per-page and page number are querystring params; keep server-side `LIMIT/OFFSET` as today. |
| Copy buttons | Clipboard write + a 1.5s "Copied" label swap (the repo already has this vanilla helper). |
| Range switcher (7/30/90/All) | Re-query the aggregates; a querystring param is fine. |
| Transitions | Drawer/sidebar `transform .22s ease`. Hover/focus tints are instant. Nothing else animates. |
| Charts | Server-rendered inline SVG from aggregate rows. No charting library — it would break the "single binary, no runtime deps" rule. |

**Accessibility**: `:focus-visible` accent ring on everything; drawer is `role="dialog" aria-modal="true"` with a focus trap; `title` attributes on the collapsed rail and all icon-only buttons plus `aria-label`; the unread dot needs a text equivalent (it is decorative colour otherwise); confirm dialogs for every destructive action. Accent-on-ground is tuned to ~3:1 — fine for icons, chrome and large text, **not** for body copy: use `#d2cefd` (accent-300) for paragraph-size accent text.

---

## State & data model

Client state is deliberately minimal (server-rendered): active screen (URL), sidebar expanded/collapsed (persisted), mobile drawer open, reader drawer open + current submission id, row selection set, current page + rows-per-page, range filter.

### Server work this design requires

Two items are **not optional** — the quarantine breakdown and the sensitivity controls are non-functional without them: `internal/spam` must return its individual signals, and the score threshold must become configurable. Both are specified below.

The rest is new `internal/store` methods and schema, all additive; follow the repo's `CREATE TABLE IF NOT EXISTS`-on-startup migration style, raw `database/sql` with `?` placeholders, and test-first discipline.

**1. Quarantine.** Today `spam.IsSpam` causes a silent drop. Change to: hold the submission with its score and the rules that fired.

```
submissions: + is_held INTEGER NOT NULL DEFAULT 0
             + spam_score INTEGER NOT NULL DEFAULT 0
             + held_at TIMESTAMP NULL
             + notified INTEGER NOT NULL DEFAULT 0   -- so restore can send the withheld notification

spam_signals: id, submission_id → submissions(id) ON DELETE CASCADE,
              rule TEXT,        -- 'markup' | 'keyword' | 'sql' | 'gibberish' | 'url_in_name' | 'extra_links' | 'repeat_ip'
              field TEXT,       -- which form field matched
              match TEXT,       -- the matched substring (truncate, and treat as untrusted: escape on render)
              weight INTEGER
```

**Required: `internal/spam` must expose its signals.** The score breakdown panel is the point of the quarantine screen, and today `Score` returns a bare `int`, so nothing can say *why* a submission was held. Add a detail-returning entry point and reduce the existing functions to wrappers, so every current caller and test keeps working:

```go
// Signal is one rule hit that contributed to a submission's score.
type Signal struct {
    Rule   string // "markup" | "keyword" | "sql" | "gibberish" | "url_in_name" | "extra_links" | "rule"
    Field  string // the form field whose value matched ("" for whole-submission rules like extra_links)
    Match  string // the matched substring, truncated to ~200 runes
    Weight int    // points this hit contributed
}

// Detail scores a submission and reports every rule hit that contributed.
func Detail(data map[string]string) (score int, signals []Signal)

func Score(data map[string]string) int          { s, _ := Detail(data); return s }
func IsSpam(data map[string]string) bool        { return Score(data) >= DefaultThreshold }
```

Notes for the implementation:

- Keep the weight constants as the single source of truth (`markupWeight`, `keywordWeight`, `gibberishWeight`, the `+4` url-in-name, the `2 * (links-1)` pile-up) and stamp each `Signal.Weight` from them — the UI reads weights off the rows, so a constant change must flow through without touching the templates.
- `extra_links` is one signal for the whole submission with `Field: ""` and `Weight: 2*(links-1)`, not one per link.
- Gibberish is checked against the original-case value (per the existing comment) — capture the matched token as-is for `Match`.
- `Match` is attacker-controlled text. Truncate it, store it, and **escape it on render** (Go's `html/template` does this by default — do not reach for `template.HTML`).
- Table-driven tests per the repo's TDD rules: for each rule, one case asserting both the score and the exact `[]Signal` (rule, field, weight); plus a multi-signal case asserting order and a clean-submission case asserting `nil` signals.

Persist the returned signals into `spam_signals` at hold time, so the breakdown is a stored record rather than a re-computation (a later weight change must not rewrite history).

Store methods: `HeldSubmissions(page, size)`, `HeldCount()`, `SubmissionSignals(id)`, `RestoreSubmission(id)` (clear `is_held`, mark unread, enqueue the notification), `DeleteHeld(ids)`, `PurgeHeldOlderThan(30 days)` (a startup + daily sweep).

**Required: a configurable threshold.** `const threshold = 6` becomes a default that an operator can move, globally and per form — the Filter rules `.seg` and the per-form sensitivity control both depend on it.

```go
// internal/spam
const DefaultThreshold = 6 // unchanged default; the old const's value and comment stay accurate

// internal/config
SpamThreshold int // SPAM_THRESHOLD, default 6, clamped to 1..20 (0 or unset ⇒ default)
```

```
forms: + spam_threshold INTEGER NULL   -- NULL = inherit the instance default
```

- The submit handler resolves the effective threshold as `form.SpamThreshold ?? config.SpamThreshold ?? spam.DefaultThreshold`, and compares the score from `Detail` against it — so `spam.IsSpam` is no longer on the hot path for form submissions (keep it for callers that want the default).
- Store the effective threshold alongside the held submission (or render it from the form) so the quarantine meter's "threshold N" label reflects what was actually applied, not today's setting.
- The UI's three presets map to **Lenient 9 / Balanced 6 / Strict 4**; the field accepts any value in range, the `.seg` is just the common cases.
- Document `SPAM_THRESHOLD` in `README.md`'s configuration table and `.env.example`.
- Guard-rail worth keeping: the existing comments explain that `markupWeight == threshold` is deliberate (one markup link drops on its own) and that `keywordWeight` sits *below* it so a single keyword never drops a message alone. A configurable threshold breaks both invariants at the extremes — at Strict 4, a lone keyword (+5) now holds a submission, and at Lenient 9 a single markup link (+6) no longer does. Either derive the weights from the threshold, or state the tradeoff in the UI hint text. Do not leave the code comments claiming an invariant the config can violate.

**2. Filter rules.**

```
filter_rules: id, kind TEXT,       -- 'block' | 'allow'
                  type TEXT,       -- 'email' | 'domain' | 'ip' | 'cidr' | 'keyword'
                  value TEXT,
                  hits INTEGER NOT NULL DEFAULT 0,
                  created_at TIMESTAMP,
                  UNIQUE(kind, type, value)
```

Evaluation order in the submit handler: **allow** (accept immediately, skip scoring) → **block** (hold immediately, `spam_score` = threshold, one `spam_signals` row with rule `'rule'`) → honeypot → score. Increment `hits` on match. Custom keywords are `kind='block', type='keyword'` and feed the keyword pass at +5 rather than short-circuiting — matching the existing comment that one keyword must never drop a message alone.

**3. Aggregates for the home page.** `SubmissionsPerDay(days)` grouped by `date(created_at)` split by `is_held`; `UnreadCount()`; `HeldCount(range)`; `PerFormStats()`; `RecentSubmissions(n)`; `WaitlistGrowth(days)`. Add indexes on `submissions(created_at)`, `submissions(form_id, is_held)`, `submissions(read)`.

**4. Rate-limit visibility.** The Home panel needs the top offending IPs. `ratelimit.Limiter` is in-process and resets on restart — either expose a read-only snapshot of its buckets, or persist a small `ip_activity` counter table. Say which you chose in the UI copy; do not imply persistence that does not exist. Same caveat for `spam.Tracker` (count-based, resets on restart) behind the "Block IPs after 3 held submissions" toggle.

**5. Routes.** New: `GET /admin` (overview), `GET /admin/quarantine`, `POST /admin/quarantine/{id}/restore`, `POST /admin/quarantine/bulk-delete`, `POST /admin/quarantine/{id}/report`, `GET /admin/rules`, `POST /admin/rules`, `POST /admin/rules/{id}/delete`, `GET /admin/forms/{id}/submissions/{sid}` (drawer fragment). Register in `main.go`, add each new template to the `pageNames` clone list. All mutations POST-only.

---

## Extra features added beyond today's app

Everything here is **new** — flagged so you can descope deliberately rather than by accident.

1. **Overview/stats home** — did not exist; `/admin/forms` was the landing page. Needs the aggregate queries above.
2. **Spam quarantine + score breakdown** — the biggest addition. Turns an unrecoverable silent drop into a 30-day reviewable queue showing exactly which rule fired, on which field, on what text.
3. **Filter rules: block/allow emails, domains, IPs, CIDRs; custom keywords** — no equivalent today (the keyword list is a hardcoded `var` in `spam.go` with an explicit "kept short on purpose" comment; honor that spirit — custom entries are the operator's own risk).
4. **Right-drawer reader with prev/next** — replaces the standalone submission page; the list stays in context.
5. **Better pagination** — rows-per-page + numbered pages, replacing `← Newer / Older →`.
6. **Signals column + per-form "held" counts** on the submissions list.
7. **Rate-limit / top-IP activity panel** — new visibility into `internal/ratelimit`.
8. **Configurable spam threshold + per-form sensitivity** — required, see "Required: a configurable threshold" above. The threshold moves from a hardcoded `const` to an env default plus an optional per-form override.
9. **Digest email for held submissions** (toggle, off by default) — needs a scheduled job; the only piece here that adds a background timer.
10. **Waitlist growth chart, broadcast history, session counts, snapshot list, DB health card** — presentational additions that each need a small query.
11. **Search field (⌘K)** — decorative in the prototype. Either implement (SQLite FTS5 or a `LIKE` over submission JSON) or remove it; do not ship a dead input.
12. **Sidebar collapse/drawer** — new responsive behavior; the current admin only has a burger for the top nav.

Suggested order: (1) tokens + shell → (2) forms/list/drawer → (3) quarantine + signals → (4) filter rules → (5) home aggregates → (6) the rest.

---

## Assets

- **Fonts** — Inter 400/500/600/700. The Nocturne stylesheet `@import`s Google Fonts; for a self-hosted binary, vendor the woff2 and `//go:embed` it.
- **Icons** — Phosphor regular (MIT). Vendor as inline SVG or an embedded woff2 subset; the CDN link in the prototypes is for prototyping only.
- **Images** — none. No photography, no illustration, no logo file: the brand mark is a Phosphor glyph in a tinted rounded box, and `ds`*`forms`* is set in Inter with an accent-coloured `forms`.
- **Charts** — hand-built inline SVG. No library.

## Files in this bundle

| File | What it is |
|---|---|
| `dsforms Redesign.dc.html` | The redesigned admin — all 12 screens, the sidebar shell, the drawer, the charts. Open in a browser; use the Tweaks/props (`screen`, `homeLayout`, `sidebar`, `showDefaultPasswordBanner`) to move between screens and variations. Logic class at the bottom holds the fake data. |
| `dsforms Landing.dc.html` | The redesigned **GitHub Pages landing page** (replaces `docs/index.html`). One long scrolling page; the `showSpamSection` prop toggles the spam block. |
| `dsforms Current UI.dc.html` | Pixel recreation of **today's** admin (forms list, form detail, submission reader, login), rebuilt from `templates/`. Use it as the before-shot and to confirm nothing existing was dropped. |
| `_ds/nocturne-.../styles.css` | The Nocturne token sheet + component layer — the source of truth for every color, size, radius and state. Port its `:root` block into `templates/base.html`. |
| `_ds/nocturne-.../readme.md` | The design system's own written guidance (direction, do/don't, contrast rules). |
| `support.js` | Prototype runtime only. **Ignore.** |
| `github.md` | Repo association + screen→source-file map. |

### Source files each screen was built from

| Screen | Repo files |
|---|---|
| Home | `templates/dashboard.html`, `internal/spam/spam.go`, `internal/ratelimit/ratelimit.go` |
| Forms | `templates/dashboard.html` |
| Form detail | `templates/form_detail.html` |
| Reader drawer | `templates/submission_detail.html` |
| Quarantine | `internal/spam/spam.go`, `internal/spam/tracker.go`, `internal/spam/gibberish.go` |
| Filter rules | `internal/spam/spam.go` (keyword/marker lists, threshold, weights) |
| Form settings | `templates/form_edit.html`, `templates/form_new.html`, `README.md` (snippet + env vars) |
| Waitlists | `templates/waitlists.html`, `templates/waitlist_detail.html`, `templates/broadcast_new.html` |
| Users | `templates/users.html`, `templates/account.html` |
| Backups | `templates/backups.html`, `internal/backup/backup.go` |
| Login | `templates/login.html` |
| Shell / tokens | `templates/base.html` |
| Landing page | `docs/index.html`, `README.md`, `internal/webhook/webhook.go`, `internal/spam/spam.go` |
