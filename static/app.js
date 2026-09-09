/* ==========================================================================
   dsforms — admin interactions
   --------------------------------------------------------------------------
   Vanilla JS, no framework, no build step. Everything here is an enhancement
   over markup that already works without it: the reader drawer intercepts a
   link that otherwise navigates to a real page, bulk bars decorate real form
   posts, and the quarantine panel swaps a fragment of a page that renders
   server-side anyway. Turning JS off costs you the overlay, not the feature.

   Written as delegated listeners on document so it keeps working after a
   fragment swap replaces part of the DOM.
   ========================================================================== */
(function () {
  'use strict';

  var RAIL_KEY = 'dsforms.rail';

  function $(sel, root) { return (root || document).querySelector(sel); }
  function $$(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); }
  function closest(el, sel) { return el && el.closest ? el.closest(sel) : null; }

  /* — sidebar ————————————————————————————————————————————————————————————
     Two different behaviours behind one button. Above 640px it toggles the
     228px sidebar against the 68px icon rail and the choice persists. Below
     640px the sidebar is an off-canvas drawer and the button opens it; that
     state is deliberately not persisted, because a drawer that reopens itself
     on every page load would be a bug, not a preference. */
  function isMobile() { return window.matchMedia('(max-width: 640px)').matches; }

  function applyRail(railed) {
    document.body.classList.toggle('rail', railed);
    // rail-manual tells the stylesheet the user has expressed a preference, so
    // the 640–860px auto-collapse should stop overriding them.
    document.body.classList.add('rail-manual');
  }

  function toggleSidebar() {
    if (isMobile()) {
      document.body.classList.toggle('nav-open');
      return;
    }
    var railed = !document.body.classList.contains('rail');
    applyRail(railed);
    try { localStorage.setItem(RAIL_KEY, railed ? '1' : '0'); } catch (e) { /* private mode */ }
  }

  function closeMobileNav() { document.body.classList.remove('nav-open'); }

  /* — clipboard ————————————————————————————————————————————————————————
     One helper for every copy button. Previously this logic lived inline in an
     onclick attribute in form_edit.html and reached for previousElementSibling,
     which only worked for one specific DOM shape. Now a button names its source
     explicitly with data-copy="#selector" (or data-copy-text="literal"). */
  function copyFrom(btn) {
    var text = btn.getAttribute('data-copy-text');
    if (!text) {
      var target = $(btn.getAttribute('data-copy'));
      if (!target) return;
      text = target.innerText || target.textContent || '';
    }
    var done = function () {
      var label = $('.copy-label', btn) || btn;
      var original = label.textContent;
      label.textContent = 'Copied';
      btn.setAttribute('aria-label', 'Copied');
      setTimeout(function () {
        label.textContent = original;
        btn.setAttribute('aria-label', 'Copy');
      }, 1500);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text.trim()).then(done, function () {});
    }
  }

  /* — bulk selection ————————————————————————————————————————————————————
     The checkboxes live inside a table beside each row's own delete form, so
     they target a standalone <form> by id via the HTML form="" attribute. That
     structure is load-bearing; this only maintains the count, the select-all
     tri-state and the disabled state of the actions. */
  function bulkScope(el) { return closest(el, '[data-bulk]'); }

  function refreshBulk(scope) {
    if (!scope) return;
    var boxes = $$('[data-bulk-item]', scope);
    var checked = boxes.filter(function (b) { return b.checked; });
    var all = $('[data-bulk-all]', scope);

    if (all) {
      all.checked = checked.length > 0 && checked.length === boxes.length;
      all.indeterminate = checked.length > 0 && checked.length < boxes.length;
    }
    $$('[data-bulk-count]', scope).forEach(function (n) {
      n.textContent = checked.length + ' selected';
    });
    $$('[data-bulk-action]', scope).forEach(function (b) { b.disabled = checked.length === 0; });

    var bar = $('[data-bulk-bar]', scope);
    if (bar) bar.hidden = checked.length === 0;
  }

  /* — reader drawer ————————————————————————————————————————————————————
     A row's link points at the real submission URL. We intercept it, ask the
     same URL for a fragment, and lay it over the list. The URL is pushed to
     history so Back closes the drawer and a copied link still opens the full
     page for someone without JS.

     On the innerHTML below: the fragment is not untrusted input. It comes from
     a same-origin, session-authenticated route we own, rendered by Go's
     html/template, which contextually escapes every interpolation by default.
     Submission fields and spam `match` strings are attacker-controlled *data*
     inside that template, but they arrive here already escaped — which is
     exactly why internal/spam and the quarantine templates must never wrap
     them in template.HTML. If that invariant is ever broken, this line becomes
     the XSS sink; the fix belongs on the server side, not here. Note also that
     innerHTML does not execute <script>, so a fragment cannot introduce one. */
  var lastFocus = null;

  function drawerRoot() {
    var root = $('#drawer-root');
    if (!root) {
      root = document.createElement('div');
      root.id = 'drawer-root';
      document.body.appendChild(root);
    }
    return root;
  }

  function openDrawer(url, push) {
    var root = drawerRoot();
    root.setAttribute('aria-busy', 'true');
    fetch(url, { headers: { 'X-Fragment': '1' }, credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.text() : Promise.reject(r.status); })
      .then(function (html) {
        root.innerHTML = html;
        root.removeAttribute('aria-busy');
        document.body.style.overflow = 'hidden';
        if (push) history.pushState({ drawer: url }, '', url);
        var panel = $('.drawer', root);
        if (panel) {
          var focusable = panel.querySelector('[autofocus], button, a[href], input');
          (focusable || panel).focus();
        }
      })
      .catch(function () {
        // A fragment we cannot load should still take the user somewhere real.
        window.location.href = url;
      });
  }

  function closeDrawer(pop) {
    var root = $('#drawer-root');
    if (!root || !root.innerHTML) return;
    root.innerHTML = '';
    document.body.style.overflow = '';
    if (!pop && history.state && history.state.drawer) history.back();
    if (lastFocus && document.contains(lastFocus)) lastFocus.focus();
    lastFocus = null;
  }

  /* Keep focus inside the open drawer — it is aria-modal, so tabbing out of it
     into the list behind would be a lie. */
  function trapFocus(e) {
    var panel = $('#drawer-root .drawer');
    if (!panel || e.key !== 'Tab') return;
    var items = $$('a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])', panel)
      .filter(function (el) { return el.offsetParent !== null; });
    if (!items.length) return;
    var first = items[0];
    var last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  }

  /* — quarantine detail ————————————————————————————————————————————————
     Selecting a held submission swaps the right-hand breakdown panel. Same
     fragment mechanism as the drawer; the row is a real link to
     /admin/quarantine?sel=ID, so it works without JS too. */
  function selectHeld(link) {
    var panel = $('#held-panel');
    if (!panel) return false;
    fetch(link.href, { headers: { 'X-Fragment': '1' }, credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.text() : Promise.reject(r.status); })
      .then(function (html) {
        panel.innerHTML = html;
        history.replaceState({}, '', link.href);
        $$('[data-held-row]').forEach(function (row) { row.classList.remove('on'); });
        var row = closest(link, '[data-held-row]');
        if (row) row.classList.add('on');
      })
      .catch(function () { window.location.href = link.href; });
    return true;
  }

  /* — password strength ————————————————————————————————————————————————
     Four segments, matching the bcrypt-cost/12-character note beside it. This
     is advisory only; the server enforces the real minimum. */
  function scorePassword(v) {
    if (!v) return 0;
    var score = 0;
    if (v.length >= 12) score++;
    if (v.length >= 16) score++;
    if (/[a-z]/.test(v) && /[A-Z]/.test(v)) score++;
    if (/[0-9]/.test(v) || /[^A-Za-z0-9]/.test(v)) score++;
    return Math.min(score, 4);
  }

  /* — wiring ————————————————————————————————————————————————————————————— */
  document.addEventListener('click', function (e) {
    var el;

    if ((el = closest(e.target, '[data-sidebar-toggle]'))) { e.preventDefault(); toggleSidebar(); return; }
    if (closest(e.target, '.nav-backdrop')) { closeMobileNav(); return; }
    if (closest(e.target, '.sidebar .nav-item') && isMobile()) { closeMobileNav(); return; }

    if ((el = closest(e.target, '[data-copy], [data-copy-text]'))) { e.preventDefault(); copyFrom(el); return; }

    if ((el = closest(e.target, '[data-drawer]'))) {
      // Let modified clicks (new tab, download) behave normally.
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
      e.preventDefault();
      lastFocus = document.activeElement;
      openDrawer(el.getAttribute('href') || el.getAttribute('data-drawer'), true);
      return;
    }

    if (closest(e.target, '[data-drawer-close]') || closest(e.target, '#drawer-root .backdrop')) {
      e.preventDefault(); closeDrawer(false); return;
    }

    if ((el = closest(e.target, '[data-held-link]'))) {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
      if (selectHeld(el)) e.preventDefault();
      return;
    }

    if ((el = closest(e.target, '[data-confirm]'))) {
      if (!window.confirm(el.getAttribute('data-confirm'))) { e.preventDefault(); e.stopPropagation(); }
      return;
    }

    // A click anywhere on a submission row opens it, except on a control that
    // has its own job (a checkbox, one of the row's action buttons).
    if ((el = closest(e.target, '[data-row-open]'))) {
      if (closest(e.target, 'a, button, input, label')) return;
      var link = $('[data-drawer]', el);
      if (link) { lastFocus = document.activeElement; openDrawer(link.getAttribute('href'), true); }
    }
  });

  document.addEventListener('change', function (e) {
    var el = e.target;

    if (el.matches && el.matches('[data-bulk-all]')) {
      var scope = bulkScope(el);
      $$('[data-bulk-item]', scope).forEach(function (b) { b.checked = el.checked; });
      refreshBulk(scope);
      return;
    }
    if (el.matches && el.matches('[data-bulk-item]')) { refreshBulk(bulkScope(el)); return; }

    // Segmented controls and rows-per-page selects submit their own form, so a
    // range or page-size change is a normal server round trip.
    if (el.matches && el.matches('[data-autosubmit]')) {
      var form = el.form || closest(el, 'form');
      if (form) form.submit();
    }
  });

  document.addEventListener('input', function (e) {
    if (!e.target.matches || !e.target.matches('[data-strength]')) return;
    var meter = $(e.target.getAttribute('data-strength'));
    if (!meter) return;
    var n = scorePassword(e.target.value);
    $$('span', meter).forEach(function (seg, i) { seg.classList.toggle('on', i < n); });
  });

  document.addEventListener('keydown', function (e) {
    // ⌘K / Ctrl-K focuses search, matching the hint chip in the header.
    if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
      var search = $('[data-search]');
      if (search) { e.preventDefault(); search.focus(); search.select(); }
      return;
    }
    if (e.key === 'Escape') {
      if ($('#drawer-root .drawer')) { closeDrawer(false); return; }
      if (document.body.classList.contains('nav-open')) closeMobileNav();
      return;
    }
    trapFocus(e);
  });

  window.addEventListener('popstate', function (e) {
    if (e.state && e.state.drawer) openDrawer(e.state.drawer, false);
    else closeDrawer(true);
  });

  // Leaving mobile width with the drawer open would otherwise strand the
  // backdrop over an already-visible sidebar.
  window.addEventListener('resize', function () {
    if (!isMobile()) closeMobileNav();
  });

  /* — drop zone ——————————————————————————————————————————————————————————
     Backups restore. The button stays disabled until a file is actually
     chosen, because a restore replaces every form, submission and user. */
  $$('[data-dropzone]').forEach(function (zone) {
    var input = $('input[type=file]', zone) || $(zone.getAttribute('data-dropzone'));
    if (!input) return;
    var label = $('[data-dropzone-name]', zone);
    var submit = $(zone.getAttribute('data-dropzone-submit') || '');

    function chosen(name) {
      if (label) label.textContent = name;
      if (submit) submit.disabled = !name;
    }
    zone.addEventListener('click', function (e) { if (e.target !== input) input.click(); });
    input.addEventListener('change', function () { chosen(input.files.length ? input.files[0].name : ''); });
    ['dragenter', 'dragover'].forEach(function (t) {
      zone.addEventListener(t, function (e) { e.preventDefault(); zone.classList.add('over'); });
    });
    ['dragleave', 'drop'].forEach(function (t) {
      zone.addEventListener(t, function (e) { e.preventDefault(); zone.classList.remove('over'); });
    });
    zone.addEventListener('drop', function (e) {
      if (!e.dataTransfer.files.length) return;
      input.files = e.dataTransfer.files;
      chosen(e.dataTransfer.files[0].name);
    });
  });

  // Initial pass so a server-rendered page with pre-checked rows is consistent.
  $$('[data-bulk]').forEach(refreshBulk);
})();
