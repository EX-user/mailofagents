// agentmail compose domain — S2 car 1 of the zero-build ESM split.
// HARD CONSTRAINT (audit_frontend_imports.sh): imports ONLY ./core.js;
// every cross-domain interaction goes through DOM CustomEvents:
//   listens:  compose:to {address}  compose:reply {to,subject}
//             compose:reply-self {m}  compose:forward {m}
//             compose:entered (tab activation hook from the entry)
//   emits:    nav:activate {tab:"compose"} (entry owns tab switching)
// The i18n dictionary stays a classic global (window.I18N).
import { $, $$, esc, api, getSession, basicAuth, toast, fmtTime, fmtBytes } from "./core.js";

(function () {
  "use strict";

  // i18n shortcut, same semantics as the entry's copy.
  function t(key, vars) {
    return window.I18N ? window.I18N.t(key, vars) : key;
  }

  // In-reply-to chip (superior request): mirrors the Cc-chip pattern —
  // the draft's reply anchor shows as a removable chip under the Cc row;
  // clicking it opens the anchored letter in the thread pane (same view
  // the Compose tab already uses for this conversation).
  // Visibility rules (superior feedback round 3):
  //   - the row opens on the ＋ toggle (Cc pattern) or when a reply sets
  //     an anchor;
  //   - once an anchor exists the input closes (irt is single-value) —
  //     only ×-ing the chip re-opens the input.
  var irtOpen = false; // user expanded the row via the ＋ toggle
  function renderInReplyTo() {
    var row = document.getElementById("compose-inreplyto-row");
    var chip = document.getElementById("compose-inreplyto-chip");
    var clear = document.getElementById("compose-inreplyto-clear");
    var wrap = document.getElementById("compose-inreplyto-input-wrap");
    var toggle = document.getElementById("btn-toggle-irt");
    if (!row || !chip) return;
    row.classList.toggle("hidden", !(composeInReplyTo || irtOpen));
    if (toggle) toggle.classList.toggle("hidden", !!(composeInReplyTo || irtOpen));
    chip.classList.toggle("hidden", !composeInReplyTo);
    if (clear) clear.classList.toggle("hidden", !composeInReplyTo);
    if (composeInReplyTo) chip.textContent = composeInReplyTo;
    // Input (and its set button) hides while an anchor is set.
    var inp = !!(composeInReplyTo);
    if (wrap) wrap.classList.toggle("hidden", inp);
  }

  // Manual anchor entry, Cc-autocomplete style. Typing filters the recent
  // inbox+sent subjects live; an empty focused input shows the full list;
  // picking a suggestion (or Enter on a pasted raw id) sets the anchor and
  // closes the input.
  var irtLabels = [];            // display strings for the dropdown
  var irtLabelToId = {};         // display string -> message id
  function loadIrtCandidates() {
    var cur = getSession();
    if (!cur) return Promise.resolve();
    return Promise.all([
      api("/api/inbox?limit=30", { keepSession: true }).catch(function () { return { messages: [] }; }),
      api("/api/sent?limit=30", { keepSession: true }).catch(function () { return { messages: [] }; })
    ]).then(function (res) {
      var items = [];
      (res[0].messages || []).forEach(function (m) { items.push({ id: m.id || m.message_id, subj: m.subject || "(no subject)", dir: "←", ts: m.received_at || 0 }); });
      (res[1].messages || []).forEach(function (m) { items.push({ id: m.id || m.message_id, subj: m.subject || "(no subject)", dir: "→", ts: m.received_at || 0 }); });
      items.sort(function (a, b) { return b.ts - a.ts; });
      items = items.slice(0, 30);
      irtLabels = items.map(function (it) {
        return it.dir + " " + it.subj + "  [" + String(it.id).slice(-6) + "]";
      });
      irtLabelToId = {};
      items.forEach(function (it, i) { irtLabelToId[irtLabels[i]] = it.id; });
    });
  }
  function wireIrtAutocomplete() {
    var row = document.getElementById("compose-inreplyto-row");
    var input = document.getElementById("compose-inreplyto-input");
    var dd = document.getElementById("irt-dropdown");
    var toggle = document.getElementById("btn-toggle-irt");
    if (!row || !input || !dd) return;
    if (toggle) toggle.addEventListener("click", function () {
      irtOpen = true;
      renderInReplyTo();
      input.focus();          // focus with empty input shows the full list
      // First-ever open races the candidates fetch — once it lands, re-fire
      // the focus-driven refresh so the full list is showing.
      loadIrtCandidates().then(function () {
        if (document.activeElement === input && !input.value) {
          input.dispatchEvent(new FocusEvent("focus"));
        }
      });
    });
    loadIrtCandidates();
    input.addEventListener("focus", loadIrtCandidates);
    attachAutocomplete(input, dd, {
      fragment: function () { return input.value; },
      exclude: function () { return []; },
      source: function () { return irtLabels; },
      showAllOnEmpty: true,   // empty + focused = browse the recent list
      pick: function (label) {
        var id = irtLabelToId[label];
        if (id) { composeInReplyTo = id; renderInReplyTo(); }
        input.value = "";
      },
    });
    // Closed-dropdown Enter commits the typed text as a raw id (manual path).
    input.addEventListener("keydown", function (ev) {
      if (ev.key === "Enter" && dd.classList.contains("hidden")) {
        ev.preventDefault();
        var v = (input.value || "").trim();
        if (v) { composeInReplyTo = v; renderInReplyTo(); input.value = ""; }
      }
    });
  }
  wireIrtAutocomplete();

  function wireInReplyTo() {
    var chip = document.getElementById("compose-inreplyto-chip");
    var clear = document.getElementById("compose-inreplyto-clear");
    if (chip) chip.addEventListener("click", function () {
      if (!composeInReplyTo) return;
      loadComposeThread().then(function () {
        var item = document.querySelector('.thread-item[data-mid="' + composeInReplyTo + '"]');
        if (item && typeof toggleThreadItem === "function") {
          item.scrollIntoView({ block: "center" });
          toggleThreadItem(item);
        }
      });
    });
    if (clear) clear.addEventListener("click", function () {
      composeInReplyTo = null;
      irtOpen = true;          // ×-ing the chip re-opens the input
      renderInReplyTo();
      var input = document.getElementById("compose-inreplyto-input");
      if (input) input.focus();
    });
  }
  wireInReplyTo();

  // Cross-domain navigation request: app.js owns activateTab.
  function navActivateCompose() {
    document.dispatchEvent(new CustomEvent("nav:activate", { detail: { tab: "compose" } }));
  }

  function composeTo(address) {
    composeInReplyTo = null;
    renderInReplyTo();
    $("#compose-to").value = address || "";
    // Entering compose from a Compose button starts a fresh letter: clear
    // any leftover draft body (feedback). Plain tab switches keep the
    // draft; Reply/Forward overwrite the body with their own prefill.
    $("#compose-body").value = "";
    navActivateCompose();
    loadComposeThread();
  }

  // composeReply jumps to Compose with To = the sender and Subject =
  // "Re: " + the original subject. Stacking is deliberate (superior ruling
  // B, 01M14EHTY): anti-stacking lets repeated replies produce identical
  // subjects; every reply adds one more Re:, Gmail/Outlook chain style.
  function composeReply(toAddress, subject, parentId) {
    composeInReplyTo = parentId || null;
    renderInReplyTo();
    $("#compose-to").value = toAddress || "";
    var subj = (subject || "").trim();
    $("#compose-subject").value = subj ? "Re: " + subj : "";
    // Reply never prefills the body (To/Subject only — reviewer's model);
    // entering it clears any leftover draft like the Compose button does.
    $("#compose-body").value = "";
    navActivateCompose();
    loadComposeThread();
    $("#compose-body").focus();
  }

  // Cc chips (v0.5.9; vertical layout + autocomplete since follow-ups):
  // the input keeps its own row; committed recipients render as removable
  // tag chips in a wrap area below it. Enter/comma commits, Backspace on
  // the empty input removes the last chip, x removes any chip. Collapsed
  // behind "+ Cc" while empty.
  let composeCcChips = [];
  // Thread link (v0.6.16 ②): the message id a reply anchors to, carried
  // through compose:reply and submitted as in_reply_to. Fresh composes and
  // forwards never carry one (forward = a new letter, not a reply).
  let composeInReplyTo = null;

  // composeRecipientList backs BOTH the To and Cc autocomplete (feedback):
  // visible directory + own contacts for regular accounts, all accounts for
  // admins — populated by ensureComposeAccounts.
  let composeRecipientList = [];

  function renderComposeCc() {
    const tags = $("#cc-tags");
    if (!tags) return;
    tags.textContent = "";
    composeCcChips.forEach(function (addr, i) {
      const chip = document.createElement("span");
      chip.className = "cc-chip";
      chip.textContent = addr;
      const x = document.createElement("button");
      x.type = "button";
      x.className = "attach-x";
      x.textContent = "×";
      x.title = t("compose.ccRemove");
      x.addEventListener("click", function () {
        composeCcChips.splice(i, 1);
        renderComposeCc();
        syncCcVisibility();
      });
      chip.appendChild(x);
      tags.appendChild(chip);
    });
    tags.classList.toggle("hidden", !composeCcChips.length);
  }

  // commitCcInput turns the raw text into chips (comma or space separated
  // pastes both work); loose validation: must contain "@".
  function commitCcInput() {
    const input = $("#compose-cc");
    if (!input) return;
    const parts = (input.value || "").split(/[,，\s]+/).map(function (s) { return s.trim(); })
      .filter(function (s) { return s && s.indexOf("@") !== -1; });
    if (parts.length) {
      parts.forEach(function (p) { if (composeCcChips.indexOf(p) === -1) composeCcChips.push(p); });
      input.value = "";
      renderComposeCc();
      syncCcVisibility();
    }
  }

  function syncCcVisibility() {
    const row = $("#compose-cc-row");
    const btn = $("#btn-toggle-cc");
    if (!row || !btn) return;
    const has = composeCcChips.length > 0;
    row.classList.toggle("hidden", !has);
    btn.classList.toggle("hidden", has);
  }

  // ---- recipient autocomplete (alice's task): typing filters the known
  // address list and offers matches in a dropdown, shared by To and Cc.
  // Debounced (150ms), keyboard navigable (Up/Down/Enter/Esc), closes on
  // blur; picks via click or Enter.
  function attachAutocomplete(input, panel, opts) {
    let items = [], active = -1, timer = null;
    function hide() { panel.classList.add("hidden"); items = []; active = -1; }
    function paint() {
      $$(".dd-item", panel).forEach(function (el, i) { el.classList.toggle("active", i === active); });
    }
    function render() {
      panel.textContent = "";
      if (!items.length) { hide(); return; }
      items.forEach(function (a, i) {
        const it = document.createElement("div");
        it.className = "dd-item" + (i === active ? " active" : "");
        it.textContent = a;
        it.addEventListener("mousedown", function (ev) {
          // mousedown beats blur so the input keeps focus through the pick.
          ev.preventDefault();
        });
        it.addEventListener("click", function () { opts.pick(a); hide(); });
        it.addEventListener("mouseenter", function () { active = i; paint(); });
        panel.appendChild(it);
      });
      panel.classList.remove("hidden");
    }
    function refresh() {
      const q = (opts.fragment() || "").trim().toLowerCase();
      // opts.source lets non-address fields (in-reply-to) supply their own
      // candidate pool; the default stays the shared recipient list.
      const pool = opts.source ? opts.source() : composeRecipientList;
      if (!q && !opts.showAllOnEmpty) { hide(); return; }
      const ex = opts.exclude();
      items = pool.filter(function (a) {
        return a.toLowerCase().indexOf(q) !== -1 && ex.indexOf(a) === -1;
      }).slice(0, 8);
      active = items.length ? 0 : -1;
      render();
    }
    input.addEventListener("input", function () {
      clearTimeout(timer);
      timer = setTimeout(refresh, 150); // debounce keystrokes
    });
    // Empty + focused = browse the whole pool (in-reply-to UX); address
    // fields opt out, so their behavior is unchanged.
    if (opts.showAllOnEmpty) input.addEventListener("focus", refresh);
    input.addEventListener("keydown", function (ev) {
      if (panel.classList.contains("hidden") || !items.length) return;
      if (ev.key === "ArrowDown") { ev.preventDefault(); active = (active + 1) % items.length; paint(); }
      else if (ev.key === "ArrowUp") { ev.preventDefault(); active = (active - 1 + items.length) % items.length; paint(); }
      else if (ev.key === "Enter") { ev.preventDefault(); opts.pick(items[active < 0 ? 0 : active]); hide(); }
      else if (ev.key === "Escape") { hide(); }
    });
    input.addEventListener("blur", function () { setTimeout(hide, 150); });
  }


  (function wireCcField() {
    const btn = $("#btn-toggle-cc");
    const input = $("#compose-cc");
    const dd = $("#cc-dropdown");
    if (btn) btn.addEventListener("click", function () {
      $("#compose-cc-row").classList.remove("hidden");
      btn.classList.add("hidden");
      if (input) input.focus();
    });
    if (input && dd) {
      attachAutocomplete(input, dd, {
        fragment: function () { return input.value; },
        // Feedback: addresses already typed into To must not surface in the
        // Cc suggestions — cc-ing someone who is already a recipient is
        // noise (the wire-level dedup stays as the backstop).
        exclude: function () {
          var ex = composeCcChips.slice();
          ($("#compose-to").value || "").split(",").forEach(function (p) {
            p = p.trim();
            if (p && ex.indexOf(p) === -1) ex.push(p);
          });
          return ex;
        },
        pick: function (a) {
          if (composeCcChips.indexOf(a) === -1) composeCcChips.push(a);
          input.value = "";
          renderComposeCc();
          syncCcVisibility();
          input.focus();
        },
      });
      input.addEventListener("keydown", function (ev) {
        if (ev.key === ",") { ev.preventDefault(); commitCcInput(); }
        else if (ev.key === "Enter") {
          // Open-dropdown Enter is handled by attachAutocomplete (pick);
          // closed Enter commits the typed text as a chip.
          if (dd.classList.contains("hidden")) { ev.preventDefault(); commitCcInput(); }
        }
        // Note: Backspace no longer removes the last chip (feedback: bad
        // feel) — the × button on each chip is the only removal path.
      });
      // Commit any leftover typed text when the user leaves the field
      // (after the dropdown's blur-close timer).
      input.addEventListener("blur", function () { setTimeout(commitCcInput, 200); });
      input.placeholder = t("compose.ccPh");
      document.addEventListener("i18n:change", function () {
        input.placeholder = t("compose.ccPh");
      });
    }
    renderComposeCc();
    syncCcVisibility();
  })();

  (function wireToAutocomplete() {
    const input = $("#compose-to");
    const dd = $("#compose-dropdown");
    if (!input || !dd) return;
    // The typed fragment = text after the last comma (To stays
    // comma-separated multi-recipient).
    attachAutocomplete(input, dd, {
      fragment: function () {
        const parts = input.value.split(",");
        return parts[parts.length - 1];
      },
      exclude: function () { return []; },
      pick: function (addr) {
        const parts = input.value.split(",");
        parts[parts.length - 1] = addr;
        input.value = parts.join(",").replace(/^\s*,\s*/, "");
        input.focus();
        loadComposeThread();
      },
    });
  })();


  // composeForward (v0.5.9, feedback): panel-side forward. /api/send has no
  // forward_of (that is a gateway-side composition), so the panel mirrors
  // the same wire format the gateway produces: user comment on top, the
  // "── forwarded from ──" separator, then the original body. Attachments
  // are not carried (same ruling as subordinate Q2) — noted in the body.
  function composeForward(m) {
    composeInReplyTo = null;
    renderInReplyTo();
    $("#compose-to").value = "";
    var subj = (m.subject || "").trim();
    $("#compose-subject").value = subj ? "Fwd: " + subj : "";
    const files = (m.attachments && m.attachments.length) || m.files || 0;
    $("#compose-body").value = "\n\n" +
      t("fwd.header", { sender: m.from, date: fmtTime(m.received_at), subject: m.subject || "" }) + "\n" +
      (files ? t("fwd.attachNote", { n: files }) + "\n" : "") +
      "\n" + (m.body != null ? m.body : (m.preview || ""));
    navActivateCompose();
    loadComposeThread();
    $("#compose-to").focus();
  }


  // ---- compose attachments (v0.5.1) ----
  // Picked files upload immediately (multipart, Basic auth); chips show
  // name/size with a remove ×; ids join the Send body. Failed uploads
  // surface as error chips; nothing blocks composing without attachments.
  let composeAttachmentIds = [];

  function renderComposeAttachments(items) {
    const wrap = $("#compose-attachments");
    wrap.innerHTML = items.map(function (a, i) {
      return '<div class="attach-card' + (a.error ? " attach-error" : "") + '">' +
        '<span class="attach-clip">📎</span>' +
        '<span class="attach-name">' + esc(a.filename) + "</span>" +
        (a.error
          ? '<span class="attach-size">' + esc(a.error) + "</span>"
          : '<span class="attach-size">' + esc(fmtBytes(a.size)) + "</span>") +
        '<button type="button" class="attach-x" data-rm="' + i + '" title="Remove">×</button>' +
        "</div>";
    }).join("");
    $$("[data-rm]", wrap).forEach(function (btn) {
      btn.addEventListener("click", function () {
        const i = +btn.dataset.rm;
        composeAttachmentItems.splice(i, 1);
        composeAttachmentIds = composeAttachmentItems.filter(function (a) { return a.id; }).map(function (a) { return a.id; });
        renderComposeAttachments(composeAttachmentItems);
      });
    });
  }

  let composeAttachmentItems = [];

  $("#btn-attach").addEventListener("click", function () {
    $("#compose-file-input").click();
  });

  $("#compose-file-input").addEventListener("change", async function () {
    const files = Array.from(this.files || []);
    this.value = "";
    for (const f of files) {
      const item = { filename: f.name, size: f.size };
      composeAttachmentItems.push(item);
      renderComposeAttachments(composeAttachmentItems);
      try {
        const fd = new FormData();
        fd.append("file", f, f.name);
        const res = await fetch("/api/files/upload", {
          method: "POST",
          headers: { Authorization: basicAuth() },
          body: fd,
        });
        if (!res.ok) {
          let msg = res.status + " " + res.statusText;
          try { const tx = await res.text(); if (tx) msg = tx; } catch (_) {}
          throw new Error(msg);
        }
        const meta = await res.json();
        item.id = meta.id;
        item.size = meta.size;
        composeAttachmentIds = composeAttachmentItems.filter(function (a) { return a.id; }).map(function (a) { return a.id; });
        renderComposeAttachments(composeAttachmentItems);
      } catch (e) {
        item.error = (e.message || "").indexOf("too large") >= 0 ? t("attach.tooLarge") : t("attach.upFailed");
        renderComposeAttachments(composeAttachmentItems);
      }
    }
  });

  // ---- attachments (v0.5.1) ----
  // Attachment cards for message detail views. Download goes through an
  // authenticated fetch -> blob -> object URL (plain <a href> would lack the
  // Basic auth header the /api/files route requires).
  // Image attachments get an inline preview (feedback): authenticated
  // fetch -> blob -> object URL feeding an <img>. svg is deliberately
  // excluded (XSS surface, low value); unknown/failed loads fall back to
  // the plain download card without error toasts.
  const ATTACH_IMAGE_RE = /\.(png|jpe?g|gif|webp)$/i;


  function composeReplyAsSelf(m) {
    composeInReplyTo = (m && (m.id || m.message_id)) || null;
    renderInReplyTo();
    $("#compose-to").value = m.from || "";
    var subj = (m.subject || "").trim();
    $("#compose-subject").value = subj ? "Re: " + subj : "";
    const text = (m.body != null ? m.body : m.preview) || "";
    const quoted = text.split("\n").map(function (l) { return "> " + l; }).join("\n");
    $("#compose-body").value = t("subs.quotePrefix", { date: fmtTime(m.received_at), sender: m.from }) + "\n" + quoted + "\n\n";
    navActivateCompose();
    loadComposeThread();
    $("#compose-body").focus();
  }

  // ---- register-subordinate (v0.5.11, superior's request; v0.6 named option) ----
  // Panel-only affordance: POST /api/register-subordinate mints an account
  // already declared under the caller — random by default, or a caller-
  // specified name (superior-approved ask modal; a taken name errors inline
  // rather than silently renaming, because credentials show exactly once).


  function applyComposeShowcaseVisibility(setRes) {
    const wrap = $("#compose-public-wrap");
    if (wrap) wrap.style.display = (setRes && setRes.showcase_enabled === true) ? "" : "none";
    const cb = $("#compose-public");
    if (cb) cb.checked = false; // always reset to off (default)
  }

  // ensureComposeShowcaseVisibility fetches settings once per page load and
  // applies the compose-toggle visibility (admin's global showcase switch).
  async function ensureComposeShowcaseVisibility() {
    const input = $("#compose-public-wrap");
    if (!input || input.dataset.settingsLoaded === "1") return;
    input.dataset.settingsLoaded = "1";
    try {
      applyComposeShowcaseVisibility(await api("/api/info?query=settings"));
    } catch (_) {
      applyComposeShowcaseVisibility(null);
    }
  }


  async function ensureComposeAccounts() {
    const input = $("#compose-to");
    if (input.dataset.listLoaded === "1") return;
    const s = getSession();
    const isRegular = s && !s.is_admin;
    var items = [];
    try {
      if (isRegular) {
        // Regular accounts: To dropdown = directory (public listed accounts)
        // ∪ their own contacts, deduped. Mirrors what the Accounts tab shows
        // them (contacts + listed). Admins still see every account.
        const [dirRes, conRes, subsRes] = await Promise.all([
          api("/api/info?query=directory").catch(function () { return { entries: [] }; }),
          api("/api/contacts").catch(function () { return { contacts: [] }; }),
          api("/api/subs").catch(function () { return { subordinates: [] }; }),
        ]);
        const seen = {};
        (dirRes.entries || []).forEach(function (a) {
          if (a.address && !seen[a.address]) { seen[a.address] = 1; items.push(a.address); }
        });
        (conRes.contacts || []).forEach(function (c) {
          if (c && !seen[c]) { seen[c] = 1; items.push(c); }
        });
        // Subordinates (v0.5.9): mail the viewer can read is mail they may
        // well be writing to (alice's ruling on the autocomplete source).
        (subsRes.subordinates || []).forEach(function (e) {
          if (e.address && !seen[e.address]) { seen[e.address] = 1; items.push(e.address); }
        });
      } else {
        const data = await api("/admin/accounts");
        items = (data.accounts || []).map(function (a) { return a.address; });
      }
    } catch (e) {
      // Non-fatal: the user can still type addresses manually.
    }
    // v0.6.33 (superior report 01M13X9W): letter headers carry addresses in
    // whatever case the sender used ("PoP@"), while accounts are stored
    // lowercase — normalize here so contacts/directory/subordinates surface
    // as one identity, never two variants of the same box.
    items = items.map(function (a) { return String(a || "").toLowerCase(); });
    // v0.6.34 (alice 01M14D3VK): lowercasing can collapse two source entries
    // (pop + PoP) into duplicates — dedupe after normalization.
    items = Array.from(new Set(items));
    input.dataset.recipients = JSON.stringify(items);
    input.dataset.listLoaded = "1";
    // Shared by the To and Cc autocomplete (feedback: match-as-you-type
    // against the visible list).
    composeRecipientList = items;

    // Toggle the dropdown from the picker button.
    const btn = $("#btn-compose-dropdown");
    const panel = $("#compose-dropdown");
    btn.addEventListener("click", function (e) {
      e.preventDefault();
      if (panel.classList.contains("hidden")) openComposeDropdown();
      else panel.classList.add("hidden");
    });
    // Close when clicking outside, or when a recipient is picked.
    document.addEventListener("click", function (e) {
      if (panel.classList.contains("hidden")) return;
      if (!e.target.closest(".to-field")) panel.classList.add("hidden");
    });
  }

  function openComposeDropdown() {
    const input = $("#compose-to");
    const panel = $("#compose-dropdown");
    var items = [];
    try { items = JSON.parse(input.dataset.recipients || "[]"); } catch (_) {}
    if (!items.length) {
      panel.innerHTML = '<div class="dd-empty">No recipients yet.</div>';
    } else {
      panel.innerHTML = items.map(function (a) {
        return '<div class="dd-item" data-addr="' + esc(a) + '">' + esc(a) + "</div>";
      }).join("");
      $$(".dd-item", panel).forEach(function (el) {
        el.addEventListener("click", function () {
          // "Click clears the input then fills" — admin's requested behavior.
          input.value = el.dataset.addr;
          panel.classList.add("hidden");
          input.focus();
          loadComposeThread();
        });
      });
    }
    panel.classList.remove("hidden");
  }

  $("#btn-send").addEventListener("click", async function () {
    const toRaw = $("#compose-to").value.trim();
    const subject = $("#compose-subject").value.trim();
    const bodyText = $("#compose-body").value;
    const status = $("#compose-status");

    if (!toRaw) { status.textContent = t("compose.needTo"); return; }
    if (!subject) { status.textContent = t("compose.needSubject"); return; }
    if (!bodyText) { status.textContent = t("compose.needBody"); return; }

    // Comma-separated list of addresses, trimmed, de-duplicated.
    const to = Array.from(new Set(
      toRaw.split(",").map(function (s) { return s.trim(); }).filter(Boolean)
    ));
    // CC (v0.5.7, chips since v0.5.9): chip list minus anyone already in To
    // (server dedups too; this keeps the wire clean).
    const cc = composeCcChips.filter(function (a) { return to.indexOf(a) === -1; });

    status.textContent = t("compose.sending");
    try {
      const sender = getSession();
      // Both roles send via /api/send (the admin credential satisfies
      // account auth, same as the inbox reads). /admin/send does not parse
      // the attachments field — routing admins there silently dropped them
      // (v0.5.1 live bug).
      const sendPath = "/api/send";
      // Public showcase opt-in (v0.4.4): include the flag when checked; the
      // server ignores it until the showcase tee ships (unknown JSON fields
      // are ignored), so this is safe to send already.
      const payload = { to: to, subject: subject, body: bodyText };
      if (cc.length) payload.cc = cc;
      const pub = $("#compose-public");
      if (pub && pub.checked) payload.public = true;
      if (composeAttachmentIds.length) payload.attachments = composeAttachmentIds.slice();
      if (composeInReplyTo) payload.in_reply_to = composeInReplyTo;
      const res = await api(sendPath, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      composeInReplyTo = null;
      renderInReplyTo();
      status.textContent = t("compose.sent", { id: res.message_id });
      toast(t("toast.sent"), "success");
      // Clear subject/body but keep To (so the thread reloads for the same contact).
      $("#compose-subject").value = "";
      $("#compose-body").value = "";
      $("#compose-cc").value = "";
      composeCcChips = [];
      renderComposeCc();
      syncCcVisibility(); // collapse the now-empty Cc field back
      composeAttachmentItems = [];
      composeAttachmentIds = [];
      renderComposeAttachments(composeAttachmentItems);
      loadComposeThread();
    } catch (e) {
      status.textContent = t("common.error", { msg: e.message });
      toast(t("toast.sendFailed"), "error");
    }
  });

  // Refresh button + To-field blur both reload the thread.
  $("#btn-refresh-thread").addEventListener("click", loadComposeThread);

  // Load the conversation between admin and the address in "To".
  // Combines admin's sent-to-that-address + that-address's mail-to-admin.
  // Both are read-only and rely on the admin Basic auth already cached.
  async function loadComposeThread() {
    const to = ($("#compose-to").value || "").trim();
    const threadEl = $("#compose-thread");
    const titleEl = $("#thread-title");
    if (!to) {
      titleEl.textContent = "Recent conversation";
      threadEl.className = "thread-list muted";
      threadEl.textContent = "Fill in \"To\" to load the thread.";
      return;
    }
    titleEl.textContent = "Conversation with " + to;
    threadEl.className = "thread-list";
    threadEl.textContent = t("common.loading");

    try {
      // Server-side thread endpoint (v0.5.2): server merges both directions
      // per peer — replaces the old "fetch 50 inbox + 50 sent, filter
      // client-side" approach, which missed conversations with low-frequency
      // contacts that fell outside the 50-message windows.
      const cur = getSession();
      const isRegular = cur && !cur.is_admin;
      const threadRes = isRegular
        ? await api("/api/thread?with=" + encodeURIComponent(to) + "&limit=50")
        : await api("/admin/thread?account=" + encodeURIComponent("admin@" + composeDomain) +
            "&with=" + encodeURIComponent(to) + "&limit=50");
      const all = (threadRes.messages || []).map(function (m) {
        return m.dir === "out"
          ? { dir: "out", id: m.id, subject: m.subject, preview: m.preview, ts: m.received_at, peer: to }
          : { dir: "in", id: m.id, subject: m.subject, preview: m.preview, ts: m.received_at,
              peer: to, from: m.from, unread: m.unread };
      }).sort(function (a, b) { return b.ts - a.ts; });
      if (!all.length) {
        threadEl.className = "thread-list muted";
        threadEl.textContent = "No conversation with " + to + " yet.";
        return;
      }
      threadEl.innerHTML = all.map(function (m) {
        const arrow = m.dir === "out" ? "→ sent" : "← received";
        const cls = m.dir === "out" ? "thread-out" : "thread-in";
        const unreadMark = (m.dir === "in" && m.unread) ? '<span class="unread-dot" title="unread">●</span>' : "";
        const subjCls = (m.dir === "in" && m.unread) ? " thread-subj-unread" : "";
        // Quick action button: "Reply" for received, "Follow up" for sent.
        // Clicking fills To + Subject in the compose form above.
        const actionLabel = m.dir === "in" ? "↩ Reply" : "↪ Follow up";
        const actionTarget = m.dir === "in" ? (m.from || m.peer) : m.peer;
        const subjBase = m.subject || "";
        // Always prepend the prefix on each reply/follow-up (matches standard
        // mail clients like Gmail/Outlook, where Re:Re:… is expected). Earlier
        // the code skipped the prefix when one was already present, which made
        // a reply-to-a-reply lose the stacking.
        const newSubj = m.dir === "in"
          ? "Re: " + subjBase
          : "Follow-up: " + subjBase;
        const actionBtn = '<span class="thread-action" data-target="' + esc(actionTarget) +
          '" data-subj="' + esc(newSubj) + '" data-mid="' + esc(m.id) + '">' + actionLabel + '</span>';
        return '<div class="thread-item ' + cls + '" data-mid="' + esc(m.id) + '" data-loaded="0">' +
          '<div class="thread-meta"><b>' + arrow + "</b> · <small>" + fmtTime(m.ts) + "</small>" +
          ' <span class="thread-toggle">▾ click to expand</span> ' + actionBtn + '</div>' +
          '<div class="thread-subj' + subjCls + '">' + unreadMark + esc(m.subject || "(no subject)") + "</div>" +
          '<div class="thread-prev">' + esc(m.preview || "") + "</div>" +
          '<div class="thread-full hidden"></div>' +
          "</div>";
      }).join("");
      // Wire Reply/Follow-up buttons: fill the compose form's To + Subject.
      $$(".thread-action", threadEl).forEach(function (btn) {
        btn.addEventListener("click", function (e) {
          e.stopPropagation(); // don't trigger the item's expand toggle
          $("#compose-to").value = btn.dataset.target;
          $("#compose-subject").value = btn.dataset.subj;
          composeInReplyTo = btn.dataset.mid || null;
          renderInReplyTo();
          $("#compose-body").focus();
          $("#compose-status").textContent = "Replying to " + btn.dataset.target;
        });
      });
      // Click-to-expand anywhere on the item; but once expanded, the content
      // area (.thread-full) does NOT collapse on click (so the user can select
      // text freely). Only the header (.thread-meta / .thread-toggle) collapses.
      // Drag-selecting text never triggers a toggle.
      $$(".thread-item", threadEl).forEach(function (item) {
        const full = $(".thread-full", item);
        const meta = $(".thread-meta", item);
        item.addEventListener("click", function (e) {
          if (window.getSelection && window.getSelection().toString()) return;
          // If already expanded and the click landed inside the full body, leave it open.
          if (full && !full.classList.contains("hidden") && full.contains(e.target)) return;
          // Special case: if the click is on the header while collapsed, expand.
          // If on the header while expanded, collapse. The item-level handler
          // already covers "click anywhere to expand"; this meta handler covers
          // "click header to collapse".
          toggleThreadItem(item);
        });
      });
    } catch (e) {
      threadEl.className = "thread-list";
      threadEl.textContent = "Error loading thread: " + e.message;
    }
  }

  // Reload the thread when the user leaves the To field (covers typing a peer
  // manually then tabbing away).
  $("#compose-to").addEventListener("change", loadComposeThread);

  // Toggle a thread item's full body (lazy-load the message on first expand).
  // Admins read via /admin/message (any account's mail); regular accounts read
  // their own mail via /api/message. The thread only shows mail to/from the
  // current user, so /api/message works for both roles for the viewer's own
  // messages — and regular accounts CANNOT call /admin/* (401 → session reset).
  async function toggleThreadItem(item) {
    const full = $(".thread-full", item);
    const toggle = $(".thread-toggle", item);
    const mid = item.dataset.mid;
    const loaded = item.dataset.loaded === "1";

    if (full.classList.contains("hidden")) {
      // Expand: load body on first time, then show.
      if (!loaded) {
        full.textContent = t("common.loading");
        try {
          const cur = getSession();
          const path = (cur && !cur.is_admin)
            ? "/api/message?id=" + encodeURIComponent(mid)
            : "/admin/message?id=" + encodeURIComponent(mid);
          const m = await api(path);
          // v0.5.3: thread expansion shows attachments too (parity with the
          // inbox/mail detail panes), including image previews.
          full.innerHTML =
          (m.cc && m.cc.length ? '<div class="detail-row"><b>Cc:</b> ' + esc(m.cc.join(", ")) + "</div>" : "") +
          "<pre class=\"thread-body\">" + esc(m.body || "") + "</pre>" + attachmentCards(m);
          wireAttachmentDownloads(full, m);
          hydrateAttachmentPreviews(full, m);
          item.dataset.loaded = "1";
        } catch (e) {
          full.textContent = t("common.error", { msg: e.message });
        }
      }
      full.classList.remove("hidden");
      toggle.textContent = t("thread.collapse");
      // On expand, locally mark this thread item as read (remove unread dot/bold).
      // This is pure UI feedback; backend read state is owned by each account
      // reading via its own /api/message call. Admin viewing does not mutate it.
      const subj = $(".thread-subj", item);
      if (subj) subj.classList.remove("thread-subj-unread");
      const dot = $(".unread-dot", item);
      if (dot) dot.remove();
    } else {
      // Collapse.
      full.classList.add("hidden");
      toggle.textContent = t("thread.expand");
    }
  }


  // Attachment rendering for the thread view (v0.6.17 P0): the helpers
  // lived in manage.js after car2 — cross-module closure, invisible here.
  // Module-local copy; REUSE CANDIDATE for the deferred unification train
  // (superior ruling: splits first, reuse consolidation later).
  function attachIsImage(a) {
    return !!(a && a.filename && ATTACH_IMAGE_RE.test(a.filename));
  }

  // Audio attachments (v0.5.12): inline <audio controls> preview, same
  // authenticated-blob + MIME-rebuild pattern as images.
  const ATTACH_AUDIO_RE = /\.(mp3|wav|ogg|m4a|webm)$/i;
  function attachIsAudio(a) {
    return !!(a && a.filename && ATTACH_AUDIO_RE.test(a.filename));
  }

  // attachTTLBadge renders the remaining validity under the file TTL
  // (v0.5.3): "约 N 天后过期" / "已过期" once past. Absent expires_at
  // (older server) shows nothing.
  function attachTTLBadge(a) {
    if (!a || !a.expires_at) return "";
    const exp = new Date(typeof a.expires_at === "number" ? a.expires_at * 1000 : a.expires_at);
    if (isNaN(exp.getTime())) return "";
    const days = Math.floor((exp.getTime() - Date.now()) / 86400000);
    const txt = days < 0 ? t("attach.expired") : t("attach.expiresIn", { n: days });
    return '<span class="attach-ttl' + (days < 0 ? " attach-ttl-over" : "") + '">' + txt + "</span>";
  }

  function attachmentCards(m) {
    const list = (m && m.attachments) || [];
    if (!list.length) return "";
    return '<div class="attach-list">' + list.map(function (a, i) {
      const isImg = attachIsImage(a), isAud = attachIsAudio(a);
      const preview = (isImg || isAud) ? '<div class="attach-preview" data-pv="' + i + '"></div>' : "";
      return '<div class="attach-card attach-card-' + (isImg ? "img" : isAud ? "audio" : "file") + '">' +
        '<span class="attach-clip">📎</span>' +
        '<span class="attach-name">' + esc(a.filename) + "</span>" +
        '<span class="attach-size">' + esc(fmtBytes(a.size)) + "</span>" +
        attachTTLBadge(a) +
        '<button class="row-action" data-dl="' + i + '">Download</button>' +
        preview +
        "</div>";
    }).join("") + "</div>";
  }

  // openImageLightbox (feedback): full-screen view for attachment
  // previews. Backdrop click or Esc closes. The image itself is NOT a
  // download trigger (superior feedback: accidental downloads) — an
  // explicit download button sits at the bottom and carries THIS image's
  // url/filename (the old second-click hack always grabbed the first
  // image's card button, wrong for multi-image messages).
  function openImageLightbox(url, filename) {
    closeImageLightbox();
    const lb = document.createElement("div");
    lb.className = "img-lightbox";
    const im = document.createElement("img");
    im.src = url;
    im.alt = filename || "";
    im.addEventListener("click", function (ev) {
      // Swallow so a click on the picture doesn't close or download.
      ev.stopPropagation();
    });
    lb.appendChild(im);
    const dl = document.createElement("button");
    dl.className = "img-lightbox-dl";
    dl.type = "button";
    dl.textContent = t("attach.download");
    dl.addEventListener("click", function (ev) {
      ev.stopPropagation();
      const a = document.createElement("a");
      a.href = url;
      a.download = filename || "attachment";
      document.body.appendChild(a);
      a.click();
      a.remove();
    });
    lb.appendChild(dl);
    lb.addEventListener("click", closeImageLightbox);
    document.addEventListener("keydown", closeImageLightbox);
    document.body.appendChild(lb);
  }
  function closeImageLightbox() {
    $$(".img-lightbox").forEach(function (el) { el.remove(); });
    document.removeEventListener("keydown", closeImageLightbox);
  }

  // Audio players on the page form a sequential queue (feedback): starting
  // one pauses the rest; autoplay (when enabled) walks the queue in order.
  let audioPlayers = [];
  // Hydration fetches resolve out of order, so players REGISTER out of
  // order — the queue once played in fetch-completion order, i.e. random
  // (superior bug report). Players now carry their attachment index and
  // insert sorted; autoplay only starts once every expected player is in
  // (or the fallback timer fires, covering failed fetches).
  let audioExpected = 0;
  function planAudioAutostart(expected) {
    audioExpected = expected;
    setTimeout(tryAudioAutostart, 1500);
  }
  function tryAudioAutostart() {
    if (!(composePrefs && composePrefs.audio_autoplay === true)) return;
    if (audioPlayers.some(function (p) { return p.autoplaying || !p.paused; })) return;
    const first = audioPlayers[0];
    if (!first) return;
    first.autoplaying = true;
    first.play().catch(function () { first.autoplaying = false; });
  }
  // Detached <audio> keeps playing after its detail pane re-renders —
  // pause and drop the queue whenever a message view is (re)opened.
  function resetAudioPlayers() {
    audioPlayers.forEach(function (p) { try { p.pause(); } catch (_) {} });
    audioPlayers = [];
    audioExpected = 0;
  }

  function registerAudioPlayer(au, idx) {
    au.dataset.pvi = String(idx);
    if (typeof idx === "number") {
      let i = 0;
      while (i < audioPlayers.length && (+audioPlayers[i].dataset.pvi || 0) < idx) i++;
      audioPlayers.splice(i, 0, au);
    } else {
      audioPlayers.push(au);
    }
    au.addEventListener("play", function () {
      audioPlayers.forEach(function (other) {
        if (other !== au && !other.paused) other.pause();
      });
    });
    au.addEventListener("ended", function () {
      if (!(composePrefs && composePrefs.audio_autoplay === true)) return;
      var next = null;
      for (var i = 0; i < audioPlayers.length; i++) {
        if (audioPlayers[i] === au) { next = audioPlayers[i + 1] || null; break; }
      }
      if (next) next.play().catch(function () {});
    });
    // Start only when the full queue is present — guarantees attachment
    // order even when blob fetches complete out of sequence.
    if (audioPlayers.length >= audioExpected) tryAudioAutostart();
  }

  // hydrateAttachmentPreviews loads image blobs (authenticated) into the
  // preview holders. Clicking a preview triggers the same download flow.
  function hydrateAttachmentPreviews(root, m) {
    const list = (m && m.attachments) || [];
    // New render = new set of players; drop stale references.
    audioPlayers = audioPlayers.filter(function (p) { return document.contains(p); });
    // Plan ordered autoplay: only start once every audio attachment has a
    // player (fetches resolve out of order — see registerAudioPlayer).
    planAudioAutostart(list.filter(function (a) { return attachIsAudio(a); }).length);
    $$(".attach-preview", root).forEach(async function (holder) {
      const a = list[+holder.dataset.pv];
      if (!a) return;
      // Preferences (v0.6): image previews can be disabled; audio
      // autoplay honors the account preference.
      if (attachIsImage(a) && composePrefs && composePrefs.image_preview === false) { holder.remove(); return; }
      try {
        const res = await fetch("/api/files/" + encodeURIComponent(a.id) + "/download?code=" + encodeURIComponent(a.access_code), {
          headers: { Authorization: basicAuth() },
        });
        if (!res.ok) throw new Error(res.status);
        // The download endpoint serves everything as octet-stream (correct
        // for downloads); an <img> refuses that MIME even via objectURL.
        // Rebuild the blob with the extension-mapped image type.
        const IMG_MIME = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp" };
        const AUDIO_MIME = { mp3: "audio/mpeg", wav: "audio/wav", ogg: "audio/ogg", m4a: "audio/mp4", webm: "audio/webm" };
        const ext = (/[.]([a-z0-9]+)$/i.exec(a.filename || "") || [])[1];
        const isAudio = attachIsAudio(a);
        const mime = (isAudio ? AUDIO_MIME : IMG_MIME)[(ext || "").toLowerCase()];
        if (!mime) throw new Error(isAudio ? "unsupported audio" : "not an image");
        const blob = new Blob([await res.arrayBuffer()], { type: mime });
        const url = URL.createObjectURL(blob);
        if (isAudio) {
          // Inline player (v0.5.12). Autoplay is a QUEUE (feedback): multiple
          // audios in one message play sequentially, never simultaneously;
          // manual play pauses the others. The first one starts once ready.
          const au = document.createElement("audio");
          au.controls = true;
          au.preload = "metadata";
          au.src = url;
          au.style.cssText = "display:block; width:100%; height:40px; margin-top:6px;";
          holder.appendChild(au);
          registerAudioPlayer(au, +holder.dataset.pv);
          setTimeout(function () { URL.revokeObjectURL(url); }, 10 * 60 * 1000);
          return;
        }
        const img = document.createElement("img");
        img.src = url;
        img.alt = a.filename;
        img.title = t("attach.clickFullscreen");
        // Click = fullscreen view (feedback); download stays on the card's
        // Download button (and on a second click inside the lightbox).
        img.addEventListener("click", function (ev) {
          ev.stopPropagation();
          openImageLightbox(url, a.filename);
        });
        holder.appendChild(img);
        // The detail pane re-renders on message switch; drop the URL then.
        setTimeout(function () { URL.revokeObjectURL(url); }, 10 * 60 * 1000);
      } catch (_) {
        // Silent fallback: leave the card as a plain download row.
        holder.remove();
      }
    });
  }

  function wireAttachmentDownloads(root, m) {
    const list = (m && m.attachments) || [];
    $$(".attach-card [data-dl]", root).forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const a = list[+btn.dataset.dl];
        if (!a) return;
        btn.disabled = true;
        try {
          const res = await fetch("/api/files/" + encodeURIComponent(a.id) + "/download?code=" + encodeURIComponent(a.access_code), {
            headers: { Authorization: basicAuth() },
          });
          if (!res.ok) throw new Error(res.status + " " + res.statusText);
          const blob = await res.blob();
          const url = URL.createObjectURL(blob);
          const link = document.createElement("a");
          link.href = url;
          link.download = a.filename || "attachment";
          document.body.appendChild(link);
          link.click();
          link.remove();
          setTimeout(function () { URL.revokeObjectURL(url); }, 5000);
        } catch (e) {
          toast(t("attach.dlFailed") + e.message, "error");
        }
        btn.disabled = false;
      });
    });
  }

  // mailStepNav steps through the loaded Messages list (own or subordinate
  // view alike); boundaries toast, matching the inbox pill's behavior minus
  // auto paging (the mail list loads in one shot per account/folder).


  // Module-local prefs + domain (v0.6.17 P0): same self-fetch pattern as
  // manage.js — the entry's closures are not visible across modules.
  let composePrefs = null;
  let composeDomain = "agentmail.local";
  function ensureComposeMeta() {
    var p1 = api("/api/profile/self", { keepSession: true }).then(function (p) {
      composePrefs = (p && p.prefs) || p || {};
    }, function () {});
    var p2 = api("/api/status").then(function (st) {
      if (st && st.domain) composeDomain = st.domain;
    }, function () {});
    return Promise.all([p1, p2]);
  }
  ensureComposeMeta();
  document.addEventListener("manage:reset", function () { composePrefs = null; ensureComposeMeta(); });

  // ---- cross-domain event wiring (protocol surface of this module) ----
  document.addEventListener("compose:to", function (ev) {
    composeTo(((ev.detail || {}).address));
  });
  document.addEventListener("compose:reply", function (ev) {
    var d = ev.detail || {};
    composeReply(d.to, d.subject, d.parentId);
  });
  document.addEventListener("compose:reply-self", function (ev) {
    composeReplyAsSelf((ev.detail || {}).m);
  });
  document.addEventListener("compose:forward", function (ev) {
    composeForward((ev.detail || {}).m);
  });
  document.addEventListener("compose:entered", function () {
    ensureComposeAccounts();
    loadComposeThread();
    ensureComposeShowcaseVisibility();
  });
})();
