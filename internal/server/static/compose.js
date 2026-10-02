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
    imIrtPaint();
  }

  // boss 09-29: the ＋ panel's irt line - display + cancel for the anchor
  // (boss asked to SEE the irt parameter and clear it; it now stays empty
  // unless set explicitly). Painted from renderInReplyTo and the 400ms tick.
  // boss 09-30 v2: the line carries BOTH parameters - it shows while an
  // anchor is set OR the subject field holds a non-no-information subject,
  // and × clears both (the capsule actions refill them). The subject half
  // reads the live field, so the line predicts exactly what the next send
  // carries (same honesty rule as the send-title above the bar).
  function imIrtPaint() {
    var line = document.getElementById("im-irt-line");
    if (!line) return;
    var sec = document.getElementById("tab-compose");
    // boss 09-30 (corrected): the line rides ABOVE the input line; the
    // empty state hides it entirely (the full form's own irt row takes
    // over when the form owns the page).
    var subjEl = document.getElementById("compose-subject");
    var subjLive = !!(subjEl && !noSubjectInfo(subjEl.value));
    var show = !!(sec && sec.classList.contains("im") && (composeInReplyTo || subjLive));
    line.classList.toggle("hidden", !show);
    var val = document.getElementById("im-irt-val");
    if (val) {
      var sv = subjEl ? (subjEl.value || "").trim() : "";
      var v = (composeInReplyTo || "\u2014") + "|" + (noSubjectInfo(sv) ? "" : sv);
      if (val.textContent !== v) val.textContent = v;
    }
    var x = document.getElementById("im-irt-x");
    if (x) x.classList.toggle("hidden", !show);
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
      (res[0].messages || []).forEach(function (m) { items.push({ id: m.id || m.message_id, subj: noSubjectInfo(m.subject) ? t("thread.noSubject") : m.subject, dir: "←", ts: m.received_at || 0 }); });
      (res[1].messages || []).forEach(function (m) { items.push({ id: m.id || m.message_id, subj: noSubjectInfo(m.subject) ? t("thread.noSubject") : m.subject, dir: "→", ts: m.received_at || 0 }); });
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
    // 0.3.5 件1: a Compose entry loads the peer's draft bucket (bucketing
    // supersedes the old unconditional clear — the bucket IS the leftover
    // draft now, keyed by recipient).
    draftReconcile();
    navActivateCompose();
    loadComposeThread();
  }

  // composeReply jumps to Compose with To = the sender and Subject =
  // "Re: " + the original subject. Stacking is deliberate (superior ruling
  // B, 01M14EHTY): anti-stacking lets repeated replies produce identical
  // subjects; every reply adds one more Re:, Gmail/Outlook chain style.
  function composeReply(toAddress, subject, parentId, body) {
    composeInReplyTo = parentId || null;
    renderInReplyTo();
    $("#compose-to").value = toAddress || "";
    var subj = quoteSubjectBase(subject, body);
    $("#compose-subject").value = subj ? "Re: " + subj : "";
    // Reply never prefills the body (To/Subject only — reviewer's model);
    // it anchors the peer's bucket so the user's edits store into it.
    $("#compose-body").value = "";
    draftAnchor(toAddress);
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

  // ---- 0.3.5 件1: per-recipient draft buckets (boss design, both ends) ----
  // The textarea is only a view; the stored truth is one bucket per
  // normalized To (trim + lowercase + sorted multi-address join; cc never
  // enters the key). Switching recipients stores the text back into the old
  // bucket and loads the new one (no bucket -> empty, spec); a sent letter
  // clears its bucket; Reply/Forward prefill anchors the bucket so the
  // user's later edits store into it too.
  function draftKey(toRaw) {
    var parts = String(toRaw || "").split(",").map(function (s) { return s.trim().toLowerCase(); })
      .filter(Boolean).sort();
    return parts.length ? "am_draft_body:" + parts.join(",") : null;
  }
  var draftPrevKey = null, draftTimer = null, draftSwitchTimer = null;
  function draftSaveNow() {
    var key = draftKey($("#compose-to").value);
    if (!key) return; // no recipient: the text stays on screen only
    try {
      var body = $("#compose-body").value;
      if (body) localStorage.setItem(key, body);
      else localStorage.removeItem(key);
    } catch (e) { /* private mode / quota — the textarea keeps the text */ }
  }
  function draftNoteTyping() {
    clearTimeout(draftTimer);
    draftTimer = setTimeout(draftSaveNow, 400);
  }
  function draftReconcile() {
    var key = draftKey($("#compose-to").value);
    if (key === draftPrevKey) return;
    var bodyEl = $("#compose-body");
    if (draftPrevKey) {
      try {
        if (bodyEl.value) localStorage.setItem(draftPrevKey, bodyEl.value);
        else localStorage.removeItem(draftPrevKey);
      } catch (e) {}
    }
    var saved = null;
    if (key) { try { saved = localStorage.getItem(key); } catch (e) {} }
    bodyEl.value = saved || "";
    draftPrevKey = key;
    syncImBar();
  }
  // Reply/Follow-up/Forward write To/Subject/the anchor themselves and
  // leave the body alone (boss 09-30 v2) - they anchor the bucket instead
  // of reconciling (spec: 锚定后用户的手改也进桶).
  function draftAnchor(toRaw) {
    draftPrevKey = draftKey(toRaw);
    clearTimeout(draftTimer);
    syncImBar();
  }

  // ---- 0.3.5 件2: mobile IM mode (boss-approved v3 preview) ----
  // With a recipient set on a phone the compose page reads like a chat: the
  // thread list renders inline in IM order (oldest -> newest, own letters
  // right, received left with the accent bar) and the one-line bar writes
  // the body; subject and the reply anchor derive from the conversation on
  // send. PC and recipient-less compose keep the full form (CSS gates
  // everything under #tab-compose.im).
  var threadNewest = null;
  var ccMoveBack = null; // wireImBar assigns: the Cc row's ride-home (module handle)
  var sheetHomeRestore = null; // wireImBar assigns: the panel's ride-home
  var sheetIntoCard = null; // wireImBar assigns: panel + attachment chips live in the card (v6)
  var attHomeRestore = null; // wireImBar assigns: the attachment chips' ride-home
  function imMode() { return window.innerWidth <= 800; }
  function imInputGrow(el) {
    if (!el || el.tagName !== "TEXTAREA") return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 88) + "px";
  }
  function syncImBar() {
    var bar = document.getElementById("im-input");
    var bodyEl = $("#compose-body");
    if (bar && bodyEl && bar.value !== bodyEl.value) { bar.value = bodyEl.value; imInputGrow(bar); }
  }
  function imPeerText() {
    return t("compose.recentConv") + " · " + (($("#compose-to").value || "").trim() || "…");
  }
  // The single source of the IM send-title: the user's subject wins; empty
  // inherits "Re: <newest subject>" from the conversation (same rule the
  // send-time derive applies — the line predicts exactly what will go out).
  // boss 09-29: conversation mode scrolls in #thread-holder (the inner list
  // has no overflow) - seat BOTH to the bottom wherever we are in the move.
  function scrollImThreadBottom() {
    if (!imMode()) return;
    var holder = document.getElementById("thread-holder");
    if (holder) holder.scrollTop = holder.scrollHeight;
    var t = document.getElementById("compose-thread");
    if (t) t.scrollTop = t.scrollHeight;
  }

  function predictedImSubject() {
    var s = ($("#compose-subject").value || "").trim();
    if (s) return { text: s, auto: false };
    // boss 09-29: no auto-anchoring and no phantom inherit - the send path
    // stamps the no-information subject word when empty, so the line shows
    // exactly that (the line predicts what will go out).
    return { text: t("compose.noSubjectWord"), auto: true };
  }
  function imPaintHead() {
    var peer = document.getElementById("im-peer");
    if (peer && peer.textContent !== imPeerText()) peer.textContent = imPeerText();
    var subjLine = document.getElementById("im-subject");
    if (subjLine) {
      var ps = predictedImSubject();
      var want = ps.text ? (t("compose.imSubject") + ps.text + (ps.auto ? t("compose.imSubjectAuto") : ""))
                         : (t("compose.imSubject") + t("compose.imSubjectNone"));
      if (subjLine.textContent !== want) {
        subjLine.textContent = "";
        var lbl = document.createElement("span");
        lbl.textContent = t("compose.imSubject");
        var val = document.createElement("span");
        val.className = "im-subj-v";
        val.textContent = ps.text ? ps.text + (ps.auto ? t("compose.imSubjectAuto") : "")
                                  : t("compose.imSubjectNone");
        subjLine.appendChild(lbl);
        subjLine.appendChild(val);
      }
    }
    var fullBtn = document.getElementById("im-full");
    if (fullBtn) {
      var want = t(document.getElementById("tab-compose").classList.contains("im-full")
        ? "compose.imBack" : "compose.imFull");
      if (fullBtn.textContent !== want) fullBtn.textContent = want;
    }
  }
  function syncImMode() {
    var sec = document.getElementById("tab-compose");
    if (!sec) return;
    var on = imMode() && !!($("#compose-to").value || "").trim();
    var was = sec.classList.contains("im");
    sec.classList.toggle("im", on);
    // boss 09-30 bug 2: the mode itself moves the page geometry (main's top
    // margin dies with .im), so a fit measured for the other state is stale
    // by exactly that margin - the box stops 16px short of the nav. Refit on
    // every flip; the fit reads the live geometry.
    if (on !== was) fitComposeOneScreen();
    if (!on) {
      sec.classList.remove("im-cc-open");
      sec.classList.remove("im-full"); // stale full-form state dies with IM mode
      if (ccMoveBack) ccMoveBack();
      if (sheetHomeRestore) sheetHomeRestore();
      if (attHomeRestore) attHomeRestore();
      // boss 09-30 bug 3 (PC thread panel sometimes completely blank):
      // leaving the mode must also UNPARK the node. The on-path seats it in
      // #thread-holder (display:none off-phones), and this branch used to
      // reconcile classes only - so a window that crossed 800px with a
      // recipient set and came back kept the PC split rail empty until a
      // reload (bench: A split visible -> B 700 im inHolder:true -> C 1280
      // still inHolder:true). Markup home is right after the holder.
      var t3 = document.getElementById("compose-thread");
      var h3 = document.getElementById("thread-holder");
      var d3 = document.getElementById("thread-modal");
      var drawerOwns3 = !!(d3 && !d3.classList.contains("hidden") && d3.contains(t3));
      if (t3 && h3 && h3.contains(t3) && !drawerOwns3) {
        h3.parentNode.insertBefore(t3, h3.nextSibling);
      }
      return;
    }
    // The inline list lives in #thread-holder; if the drawer owns the node,
    // take it back - EXCEPT while the drawer is open (boss test-server
    // report: the drawer opens from the full form, which lives INSIDE im
    // mode since 0.3.5; this recovery used to yank the thread list out of
    // the open drawer within one 400ms tick, leaving the modal empty).
    var thread = document.getElementById("compose-thread");
    var holder = document.getElementById("thread-holder");
    var drawer = document.getElementById("thread-modal");
    var drawerOwns = !!(drawer && !drawer.classList.contains("hidden") && drawer.contains(thread));
    if (!drawerOwns && thread && holder && !holder.contains(thread)) {
      holder.appendChild(thread);
      if (imMode()) scrollImThreadBottom(); // entering IM: land on the latest
    }
    // Full-form owns the page: the chips stay in the form until the card
    // returns (syncImMode re-runs on many beats and would yank them back).
    if (sheetIntoCard && !sec.classList.contains("im-full")) sheetIntoCard();
    if (!was) loadComposeThread(); // re-render in IM order + scroll to latest
  }
  // The reply anchor wires to the newest letter unless the user picked one
  // explicitly (only EMPTY fields are derived). The SUBJECT is never
  // derived from the thread: boss - putting "Re: <latest>" on the envelope
  // is a weird design; the conversation page stamps the no-information
  // subject word instead (see the send path).
  // noSubjectInfo: Felix's legacy set (zh 短信/消息/空, en SMS/Message/—,
  // trim + case-insensitive) marks subjects the OLD UI actually filled as
  // placeholders - they render exactly like an empty subject (one
  // localized no-subject face; the set is a legacy normalizer only).
  function noSubjectInfo(s) {
    var v = (s || "").trim();
    if (!v) return true;
    return /^(短信|消息|sms|message|—)$/i.test(v);
  }

  // 1048 (boss): quoting a no-information subject reads the body instead -
  // the capsule display already shows the body line, the quote side now
  // matches: prefix + truncated single-line body.
  function quoteSubjectBase(subject, body) {
    if (!noSubjectInfo(subject)) return (subject || "").trim();
    var text = (body == null ? "" : String(body)).replace(/\s+/g, " ").trim();
    if (!text) return "";
    return text.length > 40 ? text.slice(0, 40) + "…" : text;
  }

  // autoDeriveForIm retired (boss 09-29): in-reply-to stays EMPTY unless it
  // is set explicitly - a thread capsule anchors that letter, the panel's
  // irt line shows and clears it. Nothing derives it from the thread.

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
    // IM panel: the pull-out button returns the moment the row dissolves
    // (send clears the chips) and stays hidden while the row resides.
    var imBtn = document.getElementById("im-cc");
    var imSheet = document.getElementById("im-sheet");
    if (imBtn && imSheet && imSheet.contains(row)) {
      imBtn.classList.toggle("hidden", !row.classList.contains("hidden"));
    }
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
    draftAnchor(null); // a forward is a new letter to no one yet
    var subj = quoteSubjectBase(m.subject, m.body != null ? m.body : m.preview);
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
      // Flow states (over-limit / compressing / preview / uploading) render
      // as a stacked column card — inline styles only, so this face carries
      // zero style.css coupling. The name gets nowrap+ellipsis so it can
      // never stack one-char-per-line in the narrow flex row.
      const stacked = a.compressing || a.uploading || a.readyPreview || a.overLimit;
      const nameHtml = '<span class="attach-name" style="' + (stacked ?
        'overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:100%;' : '') + '">' +
        esc(a.filename) + "</span>";
      let body;
      if (a.compressing) {
        body = '<span class="attach-size">' + esc(t("attach.compressing")) + "</span>";
      } else if (a.uploading) {
        body = '<span class="attach-size">' + esc(t("attach.uploading")) + "</span>";
      } else if (a.readyPreview) {
        // Compressed & pending upload (boss 0.2.5): the fullscreen lightbox
        // is the quality-check surface; the card keeps a 预览 re-open button.
        body = '<span class="attach-size" title="' + esc(t("attach.exifNote")) + '">' +
          esc(t("attach.compressed", { from: fmtBytes(a.compressedFrom), to: fmtBytes(a.size) })) + "</span>" +
          '<button type="button" class="row-action" data-pv="' + i + '" style="margin-top:6px;width:100%;">' + esc(t("attach.openPreview")) + "</button>";
      } else if (a.overLimit) {
        body = '<span class="attach-size" style="color:#c0392b;font-size:12px;">' + esc(t("attach.overLimit")) + "</span>" +
          '<button type="button" class="row-action" data-compress="' + i + '" style="margin-top:6px;width:100%;">' + esc(t("attach.compressAndUpload")) + "</button>";
      } else if (a.error) {
        body = '<span class="attach-size">' + esc(a.error) + "</span>";
      } else if (a.compressedFrom) {
        body = '<span class="attach-size" title="' + esc(t("attach.exifNote")) + '">' +
          esc(t("attach.compressed", { from: fmtBytes(a.compressedFrom), to: fmtBytes(a.size) })) + "</span>";
      } else {
        body = '<span class="attach-size">' + esc(fmtBytes(a.size)) + "</span>";
      }
      const cardStyle = stacked ? ' style="display:flex;flex-direction:column;align-items:stretch;min-width:0;"' : "";
      return '<div class="attach-card' + (a.error ? " attach-error" : "") + '"' + cardStyle + '>' +
        '<span class="attach-clip">📎</span>' +
        nameHtml +
        body +
        '<button type="button" class="attach-x" data-rm="' + i + '" title="Remove">×</button>' +
        "</div>";
    }).join("");
    $$("[data-rm]", wrap).forEach(function (btn) {
      btn.addEventListener("click", function () {
        const i = +btn.dataset.rm;
        const it = composeAttachmentItems[i];
        if (it && it.previewUrl) URL.revokeObjectURL(it.previewUrl);
        composeAttachmentItems.splice(i, 1);
        composeAttachmentIds = composeAttachmentItems.filter(function (a) { return a.id; }).map(function (a) { return a.id; });
        renderComposeAttachments(composeAttachmentItems);
      });
    });
    // 压缩后上传 (boss 0.2.5): compress → show the quality-check preview.
    $$("[data-compress]", wrap).forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const i = +btn.dataset.compress;
        const item = composeAttachmentItems[i];
        if (!item || !item.rawFile || item.compressing) return;
        btn.disabled = true;
        item.overLimit = false;
        item.compressing = true;
        renderComposeAttachments(composeAttachmentItems);
        try {
          const blob = await compressImageFile(item.rawFile);
          const base = item.rawFile.name.replace(/\.[^.]+$/, "");
          item.blob = blob;
          item.compressedFrom = item.rawFile.size;
          item.size = blob.size; // preview shows the compressed size as "to"
          item.previewUrl = URL.createObjectURL(blob);
          item.readyPreview = true;
          item.filename = base + ".jpg";
        } catch (_) {
          item.compressFailed = true;
        }
        item.compressing = false;
        renderComposeAttachments(composeAttachmentItems);
        if (item.readyPreview) openCompressLightbox(item);
      });
    });
    // 预览 re-open: the fullscreen lightbox with the bottom 上传 button.
    $$("[data-pv]", wrap).forEach(function (btn) {
      btn.addEventListener("click", function () {
        const i = +btn.dataset.pv;
        const item = composeAttachmentItems[i];
        if (item && item.readyPreview) openCompressLightbox(item);
      });
    });
    // 上传 (boss 0.2.5): only this button puts the compressed file on the wire.
    $$("[data-up]", wrap).forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const i = +btn.dataset.up;
        const item = composeAttachmentItems[i];
        if (!item || !item.blob || item.uploading) return;
        btn.disabled = true;
        item.readyPreview = false;
        item.uploading = true;
        renderComposeAttachments(composeAttachmentItems);
        const up = new File([item.blob], item.filename, { type: "image/jpeg" });
        await uploadItem(item, up);
      });
    });
  }

  let composeAttachmentItems = [];

  // ---- image over-limit flow (0.2.5, boss-directed): an image over the
  // server's 1 MiB gate is NOT auto-uploaded — the card shows the over-limit
  // note with a 压缩后上传 button; compressing reveals a quality-check
  // preview with an explicit 上传 button that performs the actual upload.
  // Canvas re-encode also strips EXIF (incl. GPS) — surfaced in the card.
  const IMG_TRIGGER = 1048576; // 1 MiB — mirrors the server byte gate
  const IMG_TARGET = 972800;   // 950 KiB — ~5% headroom under the gate

  async function compressImageFile(file) {
    // Text-heavy PNGs (screenshots) get the high-quality lane per design.
    const q0 = file.type === "image/png" ? 0.9 : 0.82;
    const img = await createImageBitmap(file, { imageOrientation: "from-image" });
    const cv = document.createElement("canvas");
    const cx = cv.getContext("2d");
    for (const maxEdge of [2560, 2048, 1600]) {
      const k = Math.min(1, maxEdge / Math.max(img.width, img.height));
      cv.width = Math.max(1, Math.round(img.width * k));
      cv.height = Math.max(1, Math.round(img.height * k));
      cx.drawImage(img, 0, 0, cv.width, cv.height);
      let q = q0;
      for (let round = 0; round < 5; round++) {
        const blob = await new Promise(function (res) { cv.toBlob(res, "image/jpeg", q); });
        if (blob && blob.size <= IMG_TARGET) return blob;
        q -= 0.08;
      }
    }
    throw new Error("uncompressible");
  }

  async function uploadItem(item, file) {
    try {
      const fd = new FormData();
      fd.append("file", file, file.name);
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
      if (item.previewUrl) { URL.revokeObjectURL(item.previewUrl); item.previewUrl = null; }
    } catch (e) {
      item.error = (e.message || "").indexOf("too large") >= 0 ? t("attach.tooLarge") : t("attach.upFailed");
    }
    item.uploading = false;
    renderComposeAttachments(composeAttachmentItems);
  }

  // Fullscreen quality-check preview (boss 0.2.5): same lightbox surface the
  // read-side image attachments use — zoomable fullscreen image with a
  // bottom action bar. Here the action is 上传 (uploads the compressed
  // file); Escape / click-outside just closes (the card keeps a 预览
  // button to re-open).
  function openCompressLightbox(item) {
    closeImageLightbox();
    const lb = document.createElement("div");
    lb.className = "img-lightbox";
    const im = document.createElement("img");
    im.src = item.previewUrl;
    im.alt = item.filename || "";
    im.addEventListener("click", function (ev) { ev.stopPropagation(); });
    // Zoom, same mechanics as the read-side lightbox (manage.js, superior
    // 01M1B6J5W): wheel / double-click / pinch (1-6x, pointer-anchored),
    // drag to pan once zoomed; close stays on backdrop / Esc.
    let lbScale = 1, lbTx = 0, lbTy = 0;
    const lbApply = function () {
      im.style.transform = "translate(" + lbTx + "px," + lbTy + "px) scale(" + lbScale + ")";
      im.style.cursor = lbScale > 1 ? "grab" : "zoom-in";
    };
    const lbZoomAt = function (cx, cy, factor) {
      const ns = Math.min(6, Math.max(1, lbScale * factor));
      if (ns === lbScale) return;
      const r = im.getBoundingClientRect();
      lbTx += (cx - r.left) * (1 - ns / lbScale);
      lbTy += (cy - r.top) * (1 - ns / lbScale);
      lbScale = ns;
      if (lbScale === 1) { lbTx = 0; lbTy = 0; }
      lbApply();
    };
    im.style.transformOrigin = "0 0";
    im.style.touchAction = "none";
    lb.style.touchAction = "none";
    lb.addEventListener("wheel", function (ev) {
      ev.preventDefault();
      lbZoomAt(ev.clientX, ev.clientY, ev.deltaY < 0 ? 1.2 : 1 / 1.2);
    }, { passive: false });
    lb.addEventListener("dblclick", function (ev) {
      ev.preventDefault();
      lbZoomAt(ev.clientX, ev.clientY, lbScale > 1 ? 1 / lbScale : 2.5);
    });
    let lbPinch = null;
    let lbDrag = null;
    lb.addEventListener("touchstart", function (ev) {
      if (ev.touches.length === 2) {
        lbPinch = { d: Math.hypot(ev.touches[0].clientX - ev.touches[1].clientX,
                                  ev.touches[0].clientY - ev.touches[1].clientY) };
        lbDrag = null;
      } else if (ev.touches.length === 1) {
        lbDrag = { x: ev.touches[0].clientX, y: ev.touches[0].clientY, bx: lbTx, by: lbTy, moved: false };
      }
    }, { passive: true });
    lb.addEventListener("touchmove", function (ev) {
      ev.preventDefault();
      if (lbPinch && ev.touches.length === 2) {
        const d = Math.hypot(ev.touches[0].clientX - ev.touches[1].clientX,
                             ev.touches[0].clientY - ev.touches[1].clientY);
        lbZoomAt((ev.touches[0].clientX + ev.touches[1].clientX) / 2,
                 (ev.touches[0].clientY + ev.touches[1].clientY) / 2, d / lbPinch.d);
        lbPinch.d = d;
        return;
      }
      if (lbDrag && ev.touches.length === 1 && lbScale > 1) {
        const dx = ev.touches[0].clientX - lbDrag.x, dy = ev.touches[0].clientY - lbDrag.y;
        if (Math.abs(dx) + Math.abs(dy) > 6) lbDrag.moved = true;
        lbTx = lbDrag.bx + dx;
        lbTy = lbDrag.by + dy;
        lbApply();
      }
    }, { passive: false });
    lb.appendChild(im);
    const info = document.createElement("div");
    info.className = "img-lightbox-info";
    info.textContent = t("attach.compressed", { from: fmtBytes(item.compressedFrom), to: fmtBytes(item.size) }) +
      " · " + t("attach.exifNote");
    lb.appendChild(info);
    const up = document.createElement("button");
    up.className = "img-lightbox-dl";
    up.type = "button";
    up.textContent = t("attach.uploadNow");
    up.addEventListener("click", function (ev) {
      ev.stopPropagation();
      closeImageLightbox();
      item.readyPreview = false;
      item.uploading = true;
      renderComposeAttachments(composeAttachmentItems);
      const upFile = new File([item.blob], item.filename, { type: "image/jpeg" });
      uploadItem(item, upFile);
    });
    lb.appendChild(up);
    lb.addEventListener("click", closeImageLightbox);
    document.addEventListener("keydown", closeImageLightbox);
    document.body.appendChild(lb);
  }

  $("#btn-attach").addEventListener("click", function () {
    $("#compose-file-input").click();
  });

  $("#compose-file-input").addEventListener("change", async function () {
    const files = Array.from(this.files || []);
    this.value = "";
    for (const f of files) {
      const item = { filename: f.name, size: f.size, rawFile: f };
      composeAttachmentItems.push(item);
      renderComposeAttachments(composeAttachmentItems);
      if (/^image\//.test(f.type || "") && f.size > IMG_TRIGGER) {
        // Boss flow (0.2.5): an oversized image waits for the explicit
        // 压缩后上传 → preview → 上传 sequence; nothing auto-fires.
        item.overLimit = true;
        renderComposeAttachments(composeAttachmentItems);
        continue;
      }
      await uploadItem(item, f);
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
    var subj = quoteSubjectBase(m.subject, m.body != null ? m.body : m.preview);
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
    // Boss doctrine (the contract): the envelope may not go out empty -
    // the conversation page stamps a no-information subject from the
    // agreed set; the display layer normalizes it back to the no-subject
    // face. The pair only works because both ends speak the same set.
    // (The old autoDeriveForIm pre-pass is retired - boss 09-29: irt
    // defaults to empty, nothing derives it from the thread.)
    if (imMode() && !$("#compose-subject").value.trim()) {
      $("#compose-subject").value = t("compose.noSubjectWord");
    }
    const toRaw = $("#compose-to").value.trim();
    const subject = $("#compose-subject").value.trim();
    const bodyText = $("#compose-body").value;
    const status = $("#compose-status");

    if (!toRaw) { status.textContent = t("compose.needTo"); return; }
    // Boss: the conversation page sends by the no-content-subject doctrine
    // - an empty subject goes out as-is. The full form keeps its gate.
    if (!subject && !imMode()) { status.textContent = t("compose.needSubject"); return; }
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
      // Accounts page listens: refreshes activity so the recipient tops the list.
      document.dispatchEvent(new CustomEvent("compose:sent", { detail: { to: $("#compose-to").value, subject: ($("#compose-subject").value || "").trim() } }));

      // Clear subject/body but keep To (so the thread reloads for the same contact).
      $("#compose-subject").value = "";
      $("#compose-body").value = "";
      // 0.3.5 件1: a sent letter clears its draft bucket (boss addition).
      try { var dk = draftKey(toRaw); if (dk) localStorage.removeItem(dk); } catch (dkE) {}
      $("#compose-cc").value = "";
      syncImBar();
      composeCcChips = [];
      renderComposeCc();
      syncCcVisibility(); // collapse the now-empty Cc field back
      composeAttachmentItems = [];
      composeAttachmentIds = [];
      renderComposeAttachments(composeAttachmentItems);
      loadComposeThread();
      // boss 1001 incremental: the sent letter also rides into a PARKED
      // slot for this peer - switching back paints it instantly instead of
      // one round-trip stale (the live list just reloaded above). Preview
      // mirrors the server's rune-100 cut; the verify pull corrects any
      // sub-second timestamp skew on the next visit.
      (function () {
        var runes = Array.from(bodyText || "");
        threadPoolMerge(toRaw.toLowerCase(), {
          id: res.message_id,
          dir: "out",
          subject: subject,
          preview: runes.length > 100 ? runes.slice(0, 100).join("") : (bodyText || ""),
          ts: Math.floor(Date.now() / 1000),
        });
      })();
    } catch (e) {
      status.textContent = t("common.error", { msg: e.message });
      // v0.2.8.1 (1021): the server's plain-text reason (e.g. "too many
      // recipients: N given, limit is M") belongs in the toast — the
      // user's point of gaze — instead of a generic send-failed line.
      const msg = String((e && e.message) || "");
      if (/too many (recipients|cc)/i.test(msg) || (/limit/i.test(msg) && /\d/.test(msg))) {
        toast(msg, "error");
      } else {
        toast(t("toast.sendFailed"), "error");
      }
    }
  });

  // Refresh button + To-field blur both reload the thread.
  $("#btn-refresh-thread").addEventListener("click", loadComposeThread);

  // Load the conversation between admin and the address in "To".
  // Combines admin's sent-to-that-address + that-address's mail-to-admin.
  // Both are read-only and rely on the admin Basic auth already cached.
  // boss 1001: a small LRU pool of rendered per-peer thread fragments.
  // Re-entering compose for ANY of the last N peers paints its stored
  // fragment instantly (a true cache, not just the current holder); a
  // deferred silent refresh then confirms with the server. Pool entries
  // keep their scroll position; event listeners (newmail/inbox:read)
  // refresh both the live list and any parked entry for that peer.
  // boss 1001: the size is a device display preference (the pool itself
  // lives in page memory), stored browser-local like the theme.
  var THREAD_POOL_MAX = 50; // boss 1002: default raised from 10, no upper bound
  function threadPoolMax() {
    var v = parseInt(localStorage.getItem("compose_thread_pool_max") || "0", 10);
    return (v >= 1) ? v : THREAD_POOL_MAX; // boss 1002: any positive size - no upper bound
  }
  var threadPool = new Map(); // peer(lowercase) -> {html, scrollTop, savedAt}

  // boss 1002: the pool is page memory, so closing the tab used to wipe
  // it. A save now also mirrors the slot into localStorage (the drafts'
  // store); a cold pool hydrates from that mirror on first use, and the
  // usual server-truth verify pass refreshes whatever came back stale.
  // Quota overflow just skips the mirror - the memory pool carries on.
  var POOL_LS_PREFIX = "compose_thread_pool_slot:";
  // boss 1002 速修 (avatars all broken on a reopened compose page): avatar
  // <img> tags point at blob: object URLs that die with the page session,
  // and a mirrored slot re-paints them with the done flag still on - the
  // refill path then skips them and every capsule shows a broken image.
  // The localStorage copy is sanitized at write time (boxes reset to
  // pristine pending state), and a restore prunes any blob tag that is
  // not alive in THIS session (heals mirrors written by older builds).
  function poolSanAvatars(html) {
    var c = document.createElement("div");
    c.innerHTML = html;
    $$("img[src^='blob:']", c).forEach(function (im) {
      var box = im.closest("[data-avremote]") || im.parentElement;
      if (box) { while (box.firstChild) box.removeChild(box.firstChild); box.removeAttribute("data-avdone"); }
    });
    return c.innerHTML;
  }
  function poolMirrorPut(k, e) {
    try { localStorage.setItem(POOL_LS_PREFIX + k, JSON.stringify({
      html: poolSanAvatars(e.html), scrollTop: e.scrollTop, im: e.im, savedAt: e.savedAt,
    })); } catch (err) {}
  }
  function poolMirrorDrop(k) {
    try { localStorage.removeItem(POOL_LS_PREFIX + k); } catch (err) {}
  }
  function poolMirrorHydrate() {
    var keys = [];
    try {
      for (var i = 0; i < localStorage.length; i++) {
        var key = localStorage.key(i);
        if (key && key.indexOf(POOL_LS_PREFIX) === 0) keys.push(key);
      }
    } catch (err) { return; }
    var slots = [];
    keys.forEach(function (key) {
      try {
        var e = JSON.parse(localStorage.getItem(key));
        if (!e || !e.html) throw 0;
        slots.push({ key: key, e: e });
      } catch (err) { poolMirrorDrop(key.slice(POOL_LS_PREFIX.length)); }
    });
    slots.sort(function (a, b) { return (b.e.savedAt || 0) - (a.e.savedAt || 0); });
    slots.forEach(function (r, idx) {
      var k = r.key.slice(POOL_LS_PREFIX.length);
      if (idx < threadPoolMax() && !threadPool.has(k)) threadPool.set(k, r.e);
      else poolMirrorDrop(k); // over the LRU cap (or duplicate) - prune
    });
  }

  function threadPoolGet(peer) {
    var k = String(peer || "").toLowerCase();
    if (k && !threadPool.size) poolMirrorHydrate(); // reopen after close: pool comes back
    if (!k || !threadPool.has(k)) return null;
    var e = threadPool.get(k);
    threadPool.delete(k);
    threadPool.set(k, e); // LRU touch
    return e;
  }
  function threadPoolSave(peer, threadEl) {
    var k = String(peer || "").toLowerCase();
    if (!k || !threadEl || !threadEl.querySelector(".thread-item")) return;
    // boss 1001 correctness rule: a slot may only hold content whose own
    // data-peer IS the key - if the list and the key ever disagree, bail
    // instead of stowing mixed content (boss: 无论如何不要混杂).
    if (threadEl.getAttribute("data-peer") !== k) return;
    if (threadPool.has(k)) threadPool.delete(k);
    var holder = document.getElementById("thread-holder");
    threadPool.set(k, {
      html: threadEl.innerHTML,
      scrollTop: (imMode() && holder && threadEl.parentElement === holder) ? holder.scrollTop : 0,
      im: imMode(), // boss 1001 incremental: merge insertion end (im = newest last, list = newest first)
      savedAt: Date.now(),
    });
    while (threadPool.size > threadPoolMax()) {
      var evict = threadPool.keys().next().value;
      threadPool.delete(evict);
      poolMirrorDrop(evict);
    }
    if (threadPool.has(k)) poolMirrorPut(k, threadPool.get(k));
  }
  function threadPoolDrop(peer) {
    var k = String(peer || "").toLowerCase();
    threadPool.delete(k);
    poolMirrorDrop(k); // an invalidated slot must not survive a reload either
  }
  // boss 1001 incremental (display first, server truth verifies after):
  // a newmail beat that carries the letter's own summary MERGES it into a
  // parked slot instead of dropping the page - switching back paints the
  // arrival with zero requests. Id-dedupe keeps a replayed beat from
  // doubling the item; the slot's saved render order decides which end
  // the letter joins (im: newest last, list: newest first).
  function threadPoolMerge(peer, letter) {
    var k = String(peer || "").toLowerCase();
    if (!k || !letter || !letter.id || !threadPool.has(k)) return;
    var e = threadPool.get(k);
    var probe = document.createElement("div");
    probe.innerHTML = e.html;
    if (probe.querySelector('.thread-item[data-mid="' + letter.id + '"]')) return; // dedupe
    var dir = letter.dir === "out" ? "out" : "in";
    var m = { dir: dir, id: letter.id, subject: letter.subject || "", preview: letter.preview || "",
      ts: letter.ts || 0, peer: k, from: dir === "in" ? k : undefined, unread: dir === "in" };
    probe.insertAdjacentHTML(e.im ? "beforeend" : "afterbegin", threadItemHtml(m, !!e.im, threadSelfAddr()));
    e.html = probe.innerHTML;
    poolMirrorPut(k, e);
  }
  function threadSelfAddr() {
    var c = getSession();
    return c && !c.is_admin ? (c.address || "") : ("admin@" + composeDomain);
  }
  // One thread capsule, as rendered by the list - shared by the full
  // render, the incremental paints and the parked-slot merges so every
  // letter on the field is the same markup.
  function threadItemHtml(m, imOrder, selfAddr) {
    const arrow = m.dir === "out" ? t("thread.sentLabel") : t("thread.receivedLabel"); // 历史残留收编 i18n（boss）
    const cls = m.dir === "out" ? "thread-out" : "thread-in";
    const unreadMark = (m.dir === "in" && m.unread) ? '<span class="unread-dot" title="unread">●</span>' : "";
    const subjCls = (m.dir === "in" && m.unread) ? " thread-subj-unread" : "";
    // Quick action button: "Reply" for received, "Follow up" for sent.
    // Clicking merges the peer into To and sets the in-reply-to anchor;
    // the subject field stays untouched (boss: it lives in the body).
    const actionLabel = m.dir === "in" ? t("thread.reply") : t("thread.followUp");
    const actionTarget = m.dir === "in" ? (m.from || m.peer) : m.peer;
    const actionKind = m.dir === "in" ? "re" : "fwd";
    const actionBtn = '<span class="thread-action" data-target="' + esc(actionTarget) +
      '" data-mid="' + esc(m.id) + '" data-act="' + actionKind +
      '" data-subj="' + esc(quoteSubjectBase(m.subject, m.body != null ? m.body : m.preview)) + '">' + actionLabel + '</span>';
    // 0.3.4.2 capsule avatars: own letters show the self address,
    // incoming show the actual sender (falls back to the peer).
    const avAddr = m.dir === "in" ? (m.from || m.peer) : selfAddr;
    const avBox = imOrder ? '<div class="thread-av" data-av="' + esc(avAddr) + '" data-avremote="1">' +
      esc((String(avAddr)[0] || "?").toUpperCase()) + '</div>' : "";
    // 1053b: the server caps previews at 100 runes with NO ellipsis -
    // a full-cap preview is the reliable "this letter is longer"
    // signal, stamped here for the measure pass.
    // 1054 (boss): letters WITH attachments always keep the expand
    // button - a preview can never show an attachment.
    const nFiles = (m.files && m.files.length) ? m.files.length : (m.files || 0);
    return '<div class="thread-item ' + cls + '" data-mid="' + esc(m.id) + '" data-ts="' + (m.ts || 0) + '" data-loaded="0" data-prevlen="' + esc(String(Array.from(m.preview || "").length)) + '" data-files="' + esc(String(nFiles)) + '">' +
      avBox +
      '<div class="thread-card">' +
      // 1039 (boss, 1001): in IM conversation mode the direction arrow
      // ("← 收信 · " / "→ 已发 · ") drops from the capsule meta - the
      // left/right placement already tells direction there; the clock
      // stays. Non-IM thread view keeps the arrow as before.
      '<div class="thread-meta">' + (imOrder ? "" : "<b>" + arrow + "</b> · ") + "<small>" + fmtTime(m.ts) + "</small>" +
      ' <span class="thread-toggle">' + esc(t("thread.expand")) + '</span> ' + actionBtn + '</div>' +
      (noSubjectInfo(m.subject)
        ? // boss 09-30: a no-information subject gets NO redundant (no
          // subject) label - the preview line carries the unread dot.
          '<div class="thread-prev' + subjCls + ' thread-prev-multi">' + unreadMark + esc(m.preview || "") + "</div>"
        : '<div class="thread-subj' + subjCls + '">' + esc(m.subject) + "</div>" +
          '<div class="thread-prev">' + esc(m.preview || "") + "</div>") +
      '<div class="thread-full hidden"></div>' +
      '</div>' +
      "</div>";
  }
  // Wire ONE freshly created capsule (reply/follow-up + click-to-expand).
  // Every paint site (full render, pool repaint, incremental insert) calls
  // this on its fresh nodes - a node is wired exactly once at birth.
  function threadWireItem(item) {
    var btn = $(".thread-action", item);
    if (btn) btn.addEventListener("click", function (e) {
      e.stopPropagation(); // don't trigger the item's expand toggle
      // Boss 09-30 v2: capsule reply/follow-up restore the PC behavior
      // on the conversation page too - To snaps to the one peer, the
      // in-reply-to anchor is set, and the SUBJECT field is filled
      // again (prefix always prepends; an anchorless letter leaves the
      // field empty so the send still stamps the no-info word). The
      // old body-prefix cue is retired - the field carries it now, the
      // irt line shows it, and the bar's × clears both.
      $("#compose-to").value = btn.dataset.target;
      composeInReplyTo = btn.dataset.mid || null;
      var pfx = btn.dataset.act === "fwd" ? t("compose.followUpPrefix") : "Re:";
      var s2 = (btn.dataset.subj || "").trim();
      $("#compose-subject").value = s2 ? (pfx + " " + s2) : "";
      renderInReplyTo();
      imPaintHead();
      if (imMode()) { syncImBar(); $("#im-input").focus(); }
      else $("#compose-body").focus();
      $("#compose-status").textContent = "Replying to " + btn.dataset.target;
      syncComposeSplit();
    });
    const full = $(".thread-full", item);
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
  }
  // 1046 per item: a letter the capsule already shows in full gets NO
  // expand toggle and its header click won't expand. "Fully shown" is
  // measurable after layout (rAF) so fonts/avatars don't skew the pass.
  function threadMeasureNofull(item) {
    const prev = $(".thread-prev", item);
    const tg = $(".thread-toggle", item);
    if (!prev || !tg) return;
    // 1053b final ruling (boss): anything over 0.6x the preview cap
    // (60 runes) may be a truncation - keep the toggle. At or below,
    // the preview is genuinely short: the old fit check decides.
    const capped = parseInt(item.dataset.prevlen || "0", 10) > 60;
    const hasFiles = parseInt(item.dataset.files || "0", 10) > 0;
    const cut = capped || hasFiles || (prev.classList.contains("thread-prev-multi")
      ? prev.scrollHeight > prev.clientHeight + 1
      : prev.scrollWidth > prev.clientWidth + 1);
    if (!cut) {
      tg.style.display = "none";
      item.dataset.nofull = "1";
    } else {
      // 1053 (boss bug): the verdict is width/font dependent, so a
      // re-measure (pooled restore, late font swap) may flip it - the
      // toggle comes back and the stale nofull stamp clears, or a
      // truncated letter would sit there with no way to expand.
      tg.style.display = "";
      delete item.dataset.nofull;
    }
    if (!cut) {
      // boss 1046 round 2: hiding the toggle must NOT skip the
      // read-on-open fetch - the detail GET is what clears the
      // unread dot. Fully-shown capsules pull it silently once,
      // right here (same endpoint contract as toggleThreadItem).
      // boss 10-01 dot consistency: the pull is only meaningful for an
      // UNREAD capsule (read ones are already consumed - the GET was
      // pure waste), and it must converge like read-on-open does -
      // dispatch the read event and erase this capsule's own dot - or
      // the nav badge clears on the next poll while the accounts row
      // keeps its dot until the next activity pull (the boss-reported
      // row-on/nav-off divergence).
      if (item.querySelector(".unread-dot") || item.querySelector(".thread-subj-unread")) {
        const mid = item.dataset.mid;
        const cur = getSession();
        const peerAddr = ((item.parentElement || {}).getAttribute || function () { return ""; })("data-peer") || "";
        if (cur && !cur.is_admin) {
          api("/api/message?id=" + encodeURIComponent(mid), { keepSession: true })
            .then(function () {
              document.dispatchEvent(new CustomEvent("inbox:read", { detail: { from: peerAddr, src: "thread" } }));
              var d5 = item.querySelector(".unread-dot");
              if (d5) d5.remove();
              var s5 = item.querySelector(".thread-subj-unread");
              if (s5) s5.classList.remove("thread-subj-unread");
            })
            .catch(function () {});
        } else {
          api("/admin/message?id=" + encodeURIComponent(mid)).catch(function () {});
        }
      }
    }
  }
  // boss 1001 no-churn verify support: the DOM-side fingerprint of what is
  // painted (ids, directions, read state, timestamps - render order is the
  // sequence). Equal to the fetch-side fingerprint means the truth shows
  // EXACTLY what the field shows and the swap is skipped wholesale.
  function threadDomFp(el) {
    var out = [];
    $$(".thread-item", el).forEach(function (item) {
      out.push((item.getAttribute("data-mid") || "") + ":" +
        (item.classList.contains("thread-out") ? "out" : "in") + ":" +
        ((item.querySelector(".unread-dot") || item.querySelector(".thread-subj-unread")) ? 1 : 0) + ":" +
        (item.getAttribute("data-ts") || "0"));
    });
    return out.join("|");
  }
  // boss 1001 incremental: the OPEN conversation paints the beat's letter
  // with ZERO requests (display first); the verify fetch that follows the
  // same tick swaps in server truth only when something actually differs.
  function threadPaintIncremental(letter, from2) {
    if (!letter || !letter.id || !from2) return;
    var threadEl = $("#compose-thread");
    var to = ($("#compose-to").value || "").trim().toLowerCase();
    if (!threadEl || !to || threadEl.getAttribute("data-peer") !== to) return;
    if (!threadEl.querySelector(".thread-item")) return; // nothing rendered yet - the load path owns the first paint
    if (threadEl.querySelector('.thread-item[data-mid="' + letter.id + '"]')) return; // dedupe
    var m = { dir: "in", id: letter.id, subject: letter.subject || "", preview: letter.preview || "",
      ts: letter.ts || 0, peer: from2, from: from2, unread: true };
    var im2 = imMode();
    threadEl.insertAdjacentHTML(im2 ? "beforeend" : "afterbegin",
      threadItemHtml(m, im2, threadSelfAddr()));
    var node = im2 ? threadEl.lastElementChild : threadEl.firstElementChild;
    if (node && node.classList.contains("thread-item")) {
      threadWireItem(node); // fresh node, wired at birth like every render
      // boss 1002 production report: the incremental letter rendered WITHOUT
      // its avatar - every other paint path (full render, pool repaint) runs
      // the avatar hydration pass; the incremental insert missed it.
      if (window.__avRestore) {
        window.__avRestore(node, null);
        if (window.__avHydrate) window.__avHydrate(node);
        if (window.__avRemoteHydrate) window.__avRemoteHydrate(node);
      }
      requestAnimationFrame(function () { threadMeasureNofull(node); });
      threadReadOnOpen([m], threadEl);
      if (im2) scrollImThreadBottom(); // land on the arrival
    }
  }
  // 0.3.4 IM semantics (boss 09-29): opening the conversation reads it -
  // each unread incoming letter is fetched once (the detail GET marks it
  // read server-side), so the next accounts poll clears the dots
  // everywhere. Self-limiting: afterwards there is nothing to fetch.
  // Regular accounts only (admin previews never write state). Runs on
  // BOTH the full render and the no-churn skip - unread letters must be
  // consumed even when the verify finds nothing to change.
  function threadReadOnOpen(list, threadEl) {
    var c = getSession();
    if (!c || c.is_admin) return;
    // 0.3.5 (boss "5,6,7 quick"): reading happens when the user can
    // SEE - the read-on-open body-fetch must not run for a hidden
    // compose page, or the peer's letter is consumed before the badge
    // ever lights (reddot A1/A2 reproduced on the shipped bytes). The
    // letters keep their dots; the next VISIBLE render reads them.
    var tabEl = document.getElementById("tab-compose");
    var seen = !!tabEl && !tabEl.classList.contains("hidden") && document.visibilityState === "visible";
    list.forEach(function (m) {
      if (m.dir !== "in" || !m.unread) return;
      if (!seen) return;
      api("/api/message?id=" + encodeURIComponent(m.id), { keepSession: true })
        .then(function () {
          // 0.3.5 (boss staging note): the accounts unread dot clears
          // the same tick the letter is consumed - no 5s pull wait.
          // boss 1001: src:"thread" marks the thread's OWN consume - it
          // converges its capsule dots right below, so the audit #6b
          // listener must not fire a redundant full reload on it.
          document.dispatchEvent(new CustomEvent("inbox:read", { detail: { from: m.from || "", src: "thread" } }));
          // boss 1001 dot audit #1: the render above stamped this
          // capsule unread and nothing re-renders here - strip the
          // capsule's own dot/bold the same tick or it stays on a
          // letter that is already read server-side.
          var it2 = threadEl.querySelector('.thread-item[data-mid="' + m.id + '"]');
          if (it2) {
            var d2 = it2.querySelector(".unread-dot");
            if (d2) d2.remove();
            var s3 = it2.querySelector(".thread-subj-unread");
            if (s3) s3.classList.remove("thread-subj-unread");
          }
        })
        .catch(function () {});
    });
  }
  // boss 1001 correctness rule: only the LATEST load may paint. Two
  // overlapping loads (peer switched mid-fetch) used to race the DOM -
  // the loser's render would stamp its own data-peer over the winner's
  // content, the exact mixing the boss forbids. Stale loads abandon.
  var threadLoadSeq = 0;

  async function loadComposeThread(opts) {
    const to = ($("#compose-to").value || "").trim();
    const threadEl = $("#compose-thread");
    const titleEl = $("#thread-title");
    var mySeq = ++threadLoadSeq;
    // boss 1001: a deferred re-entry refresh rides the scroll through the
    // silent swap (captured before any DOM wipe) instead of re-seating.
    var keepScrollTop = (opts && opts.keepScroll && imMode())
      ? ((document.getElementById("thread-holder") || {}).scrollTop || 0)
      : null;
    // boss 1001 pool: before parking the live fragment, stow it under its
    // peer so switching conversations preserves each rendered list.
    var livePeer = threadEl.getAttribute("data-peer") || "";
    if (livePeer && livePeer !== to.toLowerCase()) threadPoolSave(livePeer, threadEl);
    if (!to) {
      titleEl.textContent = t("compose.recentConv");
      threadEl.className = "thread-list muted";
      threadEl.textContent = "Fill in \"To\" to load the thread.";
      threadEl.setAttribute("data-peer", "");
      return;
    }
    titleEl.textContent = t("compose.recentConv");
    threadEl.className = "thread-list";
    // boss 09-30: a same-peer refresh (the newmail beat) must not blank the
    // visible list into "loading" - the fresh render swaps in silently when
    // the data arrives. A peer CHANGE still shows the loading placeholder.
    // 0.3.4.2 follow-up (089c43b): harvest BEFORE any wipe - the wipe
    // destroys the rendered boxes and a post-wipe harvest finds nothing.
    // The bank feeds the render site's avRestore (the silent path would
    // re-harvest the very same nodes, so one early harvest serves both).
    var avBankT = window.__avHarvest ? window.__avHarvest(threadEl) : null;
    // boss 1001 pool: a stored fragment for THIS peer paints before the
    // fetch (real cache hit - switching conversations feels instant too);
    // the fetch still runs and swaps in fresh data when it lands.
    // boss 1001 pool: a stored fragment for THIS peer paints before the
    // fetch (real cache hit - switching conversations feels instant too);
    // the fetch still runs and swaps in fresh data when it lands.
    // NOTE (precedence fix): the old form `opts && opts.pool !== false ?
    // Get(to) : null` evaluated falsy for EVERY plain call (opts undefined)
    // - the pool never painted on the To-switch path at all. Only an
    // explicit opt-OUT ({pool:false}) disables the hit.
    // The hit serves PEER SWITCHES only: when the field already shows this
    // peer's rendered list, repainting from the slot would discard live UI
    // state (an expanded capsule, a scroll position mid-read) that the
    // silent-swap path is contractually preserving through the fetch.
    var poolHit = (opts && opts.pool === false ||
      threadEl.getAttribute("data-peer") === to.toLowerCase()) ? null : threadPoolGet(to);
    if (poolHit) {
      threadEl.setAttribute("data-peer", to.toLowerCase());
      threadEl.innerHTML = poolHit.html;
      if (imMode()) {
        var h5 = document.getElementById("thread-holder");
        // boss 10-01: a merged arrival rides in the slot as an UNREAD
        // capsule - restoring the saved scroll would park the viewport on
        // the old reading position while the new letter sits below the
        // fold (and a no-change verify then skips any re-seat). New
        // content seats to the newest; no new content keeps the position.
        if (threadEl.querySelector(".unread-dot") || threadEl.querySelector(".thread-subj-unread")) {
          scrollImThreadBottom();
          requestAnimationFrame(scrollImThreadBottom);
          setTimeout(scrollImThreadBottom, 250);
        } else if (h5) {
          h5.scrollTop = poolHit.scrollTop || 0;
        }
      }
      // boss 1002 速修: a slot restored across page sessions can still carry
      // blob: urls written by an older build (or kept in memory from this
      // one) - prune the foreign ones so the refill below re-fetches them;
      // same-session urls are alive and stay untouched (no refill flash).
      if (window.__avPruneForeignBlobs) window.__avPruneForeignBlobs(threadEl);
      if (window.__avRestore) {
        window.__avRestore(threadEl, null);
        if (window.__avHydrate) window.__avHydrate(threadEl);
        if (window.__avRemoteHydrate) window.__avRemoteHydrate(threadEl);
      }
      // boss 1001: the repainted pool nodes are FRESH - wire them at birth.
      // The verify swap used to rewire within a round-trip anyway; with the
      // no-churn skip the repaint may become the long-lived DOM, and dead
      // reply/expand buttons must not survive it.
      $$(".thread-item", threadEl).forEach(threadWireItem);
      // 1053 (boss bug): the nofull verdict rode in with the saved HTML,
      // but it was decided under the OLD width/fonts - re-measure the
      // restored items or a truncated letter can sit there with no
      // expand toggle at all.
      requestAnimationFrame(function () { $$(".thread-item", threadEl).forEach(threadMeasureNofull); });
    }
    if (threadEl.getAttribute("data-peer") !== to.toLowerCase()) threadEl.textContent = t("common.loading");

    try {
      // Server-side thread endpoint (v0.5.2): server merges both directions
      // per peer — replaces the old "fetch 50 inbox + 50 sent, filter
      // client-side" approach, which missed conversations with low-frequency
      // contacts that fell outside the 50-message windows.
      const cur = getSession();
      const isRegular = cur && !cur.is_admin;
      // 0.3.4.2 capsule avatars: own letters show the self address,
      // incoming show the actual sender (falls back to the peer).
      const selfAddr = isRegular ? (cur.address || "") : ("admin@" + composeDomain);
      // Boss: on the conversation page a letter has exactly ONE recipient.
      // A multi-value To (allowed on the full form) is CLIPPED to its first
      // address the moment the conversation view loads.
      const firstPeer = to.split(",")[0].trim();
      // boss 09-29 field report: the clip is CONVERSATION-PAGE semantics only.
      // Gating it on imMode - on the PC full form a second To address the
      // user just typed must survive (the field feeds the send envelope).
      if (imMode() && firstPeer && firstPeer !== to) $("#compose-to").value = firstPeer;
      const threadRes = isRegular
        ? await api("/api/thread?with=" + encodeURIComponent(firstPeer || to) + "&limit=50")
        : await api("/admin/thread?account=" + encodeURIComponent("admin@" + composeDomain) +
            "&with=" + encodeURIComponent(firstPeer || to) + "&limit=50");
      // boss 1001 correctness rule: a load superseded by a newer one must
      // not paint - its content belongs to a peer that is no longer on the
      // field (the mix the boss forbids). The pool-hit paint above is
      // synchronous (no interleave); everything after this await is not.
      if (mySeq !== threadLoadSeq) return;
      const all = (threadRes.messages || []).map(function (m) {
        return m.dir === "out"
          ? { dir: "out", id: m.id, subject: m.subject, preview: m.preview, ts: m.received_at, peer: firstPeer || to }
          : { dir: "in", id: m.id, subject: m.subject, preview: m.preview, ts: m.received_at,
              peer: firstPeer || to, from: m.from, unread: m.unread };
      }).sort(function (a, b) { return b.ts - a.ts; });
      threadNewest = all.length ? all[0] : null; // auto anchor + quote source
      var imOrder = imMode();
      if (imOrder) all.reverse(); // IM reading order: oldest top, latest bottom
      if (!all.length) {
        threadEl.className = "thread-list muted";
        threadEl.textContent = "No conversation with " + to + " yet.";
        // boss 1001 correctness rule: the stamp must ALWAYS follow the
        // painted content - the empty face used to leave the previous
        // peer's stamp, so field and list could disagree until a reload.
        threadEl.setAttribute("data-peer", to.toLowerCase());
        // boss 09-29 addendum: a peer with NO conversation lands in the full
        // compose form - the conversation view has nothing to show. The
        // explicit back button still works (no reload runs on exit).
        if (imOrder) {
          var secN = document.getElementById("tab-compose");
          if (secN && secN.classList.contains("im")) secN.classList.add("im-full");
        }
        return;
      }
      // boss 1001 no-churn verify (真值校验无变更不得抖动页面): when the
      // fetched truth is EXACTLY what the field already shows (same peer,
      // same letters, same read state, same stamps), the swap is skipped
      // wholesale - no node churn, no avatar re-hydration, no re-seating,
      // expanded capsules stay expanded. Only the read-on-open pass still
      // runs, so unread letters are consumed even on a skipped swap.
      //
      // boss 1001 range rule (拉取只与其覆盖时间范围内的缓存比较): the fetch
      // covers the newest N letters of the peer; letters already on the
      // field that are OLDER than that window are the fetch's blind spot,
      // not its contradiction - they are retained (node identity and all),
      // so the view ACCUMULATES its tail instead of shedding it each time
      // fresh arrivals slide the window forward.
      var fpFetch = all.map(function (m) {
        return m.id + ":" + m.dir + ":" + (m.unread ? 1 : 0) + ":" + m.ts;
      }).join("|");
      var fetchIds = {};
      var fetchMinTs = 0;
      all.forEach(function (m) { fetchIds[m.id] = 1; });
      if (all.length) fetchMinTs = all[all.length - 1].ts; // desc-sorted: last entry is the oldest
      var samePeerLive = threadEl.getAttribute("data-peer") === to.toLowerCase();
      var tailNodes = []; // older-than-range nodes, DETACHED only right before the wipe
      var rangeFp = [];
      if (samePeerLive) {
        $$(".thread-item", threadEl).forEach(function (item) {
          var ts = parseInt(item.getAttribute("data-ts") || "0", 10);
          var mid = item.getAttribute("data-mid") || "";
          if (ts && fetchMinTs && ts < fetchMinTs && !fetchIds[mid]) {
            tailNodes.push(item);
          } else {
            rangeFp.push(mid + ":" + (item.classList.contains("thread-out") ? "out" : "in") + ":" +
              ((item.querySelector(".unread-dot") || item.querySelector(".thread-subj-unread")) ? 1 : 0) + ":" + ts);
          }
        });
      }
      if (samePeerLive && threadEl.querySelector(".thread-item") && rangeFp.join("|") === fpFetch) {
        threadReadOnOpen(all, threadEl);
        threadPoolSave(to, threadEl); // keep the parked slot as fresh as the field
        return;
      }
      var html = all.map(function (m) {
        return threadItemHtml(m, imOrder, selfAddr); // shared renderer (also feeds the incremental paints)
      }).join("");
      // Range rule, wipe side: park the tail nodes (listeners/expansion ride
      // along - they are moved, not re-created), then re-seat them at the
      // OLDER end of the fresh render: im order is oldest-top so the tail
      // goes back on top; list order is newest-top so it goes back below.
      var tailFrag = [];
      tailNodes.forEach(function (node) {
        tailFrag.push(node.parentNode ? node.parentNode.removeChild(node) : node);
      });
      threadEl.setAttribute("data-peer", to.toLowerCase()); // same-peer refreshes swap silently
      // 0.3.4.2: same decode-free recycle as 06586e1 - polls re-render this
      // list constantly, harvested avatar boxes keep their decoded bitmaps.
      if (imOrder && window.__avRestore) {
        threadEl.innerHTML = html;
        window.__avRestore(threadEl, avBankT);
        if (window.__avHydrate) window.__avHydrate(threadEl);
        if (window.__avRemoteHydrate) window.__avRemoteHydrate(threadEl);
      } else {
        threadEl.innerHTML = html;
      }
      // Range rule, re-seat side: the detached tail nodes go back at the
      // older end (im: above the fresh block; list: below it), keeping
      // their listeners, expansion state and avatar bitmaps intact.
      if (tailFrag.length) {
        if (imOrder) {
          for (var ti = tailFrag.length - 1; ti >= 0; ti--) {
            threadEl.insertBefore(tailFrag[ti], threadEl.firstChild);
          }
        } else {
          tailFrag.forEach(function (node) { threadEl.appendChild(node); });
        }
      }

      // boss 1002 (earliest bubbles lose their avatars): the avatar bank
      // harvests ALL done boxes pre-fetch - including the tail nodes this
      // swap re-seats - and avRestore MOVES their children out into the
      // fresh render. A drained tail box keeps data-avdone, so every fill
      // path (which guards on that flag) skips it forever. Strip the flag
      // from childless boxes after the re-seat and refill from the cached
      // objectURL registry - one pass, no re-decode.
      if (tailFrag.length && window.__avRemoteHydrate) {
        $$(".thread-av[data-avdone]", threadEl).forEach(function (box) {
          if (!box.firstChild) box.removeAttribute("data-avdone");
        });
        window.__avRemoteHydrate(threadEl);
      }
      // 1046 (boss, 1001): a letter the capsule already shows in full gets
      // NO expand toggle and its header click won't expand. "Fully shown"
      // is measurable after layout: a single-line preview overflows in
      // scrollWidth, a multi-line one in scrollHeight - no overflow means
      // the body is completely on screen. Measured post-layout (rAF) so
      // fonts/avatars don't skew the first pass.
      if (imOrder) {
        requestAnimationFrame(function () {
          $$(".thread-item", threadEl).forEach(threadMeasureNofull);
        });
      }
      // boss 1001 pool: the fresh render re-stows under its peer (LRU).
      threadPoolSave(to, threadEl);
      // 0.3.4 IM semantics (boss 09-29): opening the conversation reads
      // it (regular accounts only; admin previews never write state) -
      // extracted so the no-churn skip path runs it too.
      if (isRegular) threadReadOnOpen(all, threadEl);
      // Wire Reply/Follow-up buttons + click-to-expand: every freshly
      // rendered capsule is wired at birth (shared with the incremental
      // paints so a skipped swap never leaves dead buttons).
      $$(".thread-item", threadEl).forEach(threadWireItem);
      if (imOrder) {
        // boss 09-29: entering from the accounts row must land on the latest
        // letter. The HOLDER is the scroller in conversation mode (the inner
        // list has no overflow), and the im-mode node move/class flip can
        // land after this render - seat now and re-seat on the next frame
        // and once more after the tick's node move.
        // boss 1001: the deferred re-entry refresh keeps the user's scroll
        // instead (the swap must not yank the view back to the latest).
        if (keepScrollTop != null) {
          var h4 = document.getElementById("thread-holder");
          if (h4) h4.scrollTop = keepScrollTop;
        } else {
          scrollImThreadBottom();
          requestAnimationFrame(scrollImThreadBottom);
          setTimeout(scrollImThreadBottom, 250);
        }
      } // start at the latest
    } catch (e) {
      threadEl.className = "thread-list";
      threadEl.textContent = "Error loading thread: " + e.message;
    }
  }

  // Reload the thread when the user leaves the To field (covers typing a peer
  // manually then tabbing away); the draft bucket reconciles alongside —
  // immediately on leave, debounced while typing (spec: 失焦/停顿防抖).
  $("#compose-to").addEventListener("change", function () {
    clearTimeout(draftSwitchTimer);
    draftReconcile();
    loadComposeThread();
  });
  $("#compose-to").addEventListener("input", function () {
    clearTimeout(draftSwitchTimer);
    draftSwitchTimer = setTimeout(function () { draftReconcile(); loadComposeThread(); }, 600);
  });

  // v0.2.8 compose two-column (boss-approved): PC only (CSS gates <961px).
  // Recipient present -> split (left form / right thread); empty -> solo
  // (right column hidden, form centered).
  function syncComposeSplit() {
    var sec = document.getElementById("tab-compose");
    if (!sec) return;
    var has = !!($("#compose-to").value || "").trim();
    // v0.2.8.2 merge (boss cursor idea + alice ruling): focusing subject or
    // body also opens the two-column view — the caret itself is a trigger,
    // so the wide layout is up before the first keystroke lands.
    var fa = document.activeElement;
    var focused = !!fa && (fa.id === "compose-subject" || fa.id === "compose-body");
    var split = has || focused;
    sec.classList.toggle("split", split);
    sec.classList.toggle("solo", !split);
  }
  $("#compose-to").addEventListener("input", syncComposeSplit);
  syncComposeSplit();
  // 0.3.5 件1: every keystroke in the body drafts into the current bucket.
  $("#compose-body").addEventListener("input", function () {
    syncImBar();
    draftNoteTyping();
  });
  // Flush the bucket when the tab hides/closes mid-typing (mobile switches).
  window.addEventListener("pagehide", draftSaveNow);
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "hidden") draftSaveNow();
  });

  // v0.2.8.2 (boss live feedback): input events alone missed transitions
  // sometimes (value changed by autofill/other code paths). The split state
  // now reconciles against the To value on a fixed tick — the value is the
  // single source of truth, events only make it instant.
  setInterval(function () { syncComposeSplit(); syncImMode(); syncImBar(); imPaintHead(); imIrtPaint(); }, 400);
  document.addEventListener("focusin", syncComposeSplit);
  document.addEventListener("focusout", function () { setTimeout(syncComposeSplit, 0); });

  // Toggle a thread item's full body (lazy-load the message on first expand).
  // Admins read via /admin/message (any account's mail); regular accounts read
  // their own mail via /api/message. The thread only shows mail to/from the
  // current user, so /api/message works for both roles for the viewer's own
  // messages — and regular accounts CANNOT call /admin/* (401 → session reset).
  async function toggleThreadItem(item) {
    // 1046: a capsule that already shows the whole letter doesn't expand.
    if (item.dataset.nofull === "1") return;
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
      // boss 1001 dot audit #3: a regular consume must say so - the
      // automatic read-on-open of this very letter clears the accounts dot
      // (inbox:read), the manual expand used to leave it lit until the
      // next accounts pull. Admin expansion is read-only, emits nothing.
      const cur2 = getSession();
      if (cur2 && !cur2.is_admin) {
        document.dispatchEvent(new CustomEvent("inbox:read", { detail: { from: ($("#compose-to").value || "").split(",")[0].trim() } }));
        document.dispatchEvent(new CustomEvent("badge:refresh"));
      }
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

  // PDF attachments (superior, point-to-point): same lazy "Read PDF" button
  // as the manage view — blob fetched on click, dimmed fullscreen reader.
  const ATTACH_PDF_RE = /\.pdf$/i;
  function attachIsPdf(a) {
    return !!(a && a.filename && ATTACH_PDF_RE.test(a.filename));
  }

  // Markdown attachments (superior, point-to-point): .md renders inline as
  // formatted prose (vendored marked + DOMPurify; images/styles inside the
  // markdown are stripped by the sanitizer). Falls back to plain <pre> when
  // the vendor libs are missing (stale cached index.html).
  const ATTACH_MD_RE = /\.(md|markdown)$/i;
  function attachIsMd(a) {
    return !!(a && a.filename && ATTACH_MD_RE.test(a.filename));
  }

  // Text attachments (superior, point-to-point): lightbox-only preview —
  // no inline window, the ⛶ button opens the plain-text reader.
  const ATTACH_TXT_RE = /\.(txt|text)$/i;
  function attachIsTxt(a) {
    return !!(a && a.filename && ATTACH_TXT_RE.test(a.filename));
  }

  // renderMd: markdown text -> sanitized .md-body element (marked +
  // DOMPurify, img/style/audio/video stripped). Plain-<pre> fallback when
  // the vendor libs are missing. Shared by the inline preview and the
  // fullscreen lightbox.
  function renderMd(text) {
    if (window.marked && window.DOMPurify) {
      try {
        const html = DOMPurify.sanitize(marked.parse(text), {
          FORBID_TAGS: ["img", "style", "audio", "video"],
          FORBID_ATTR: ["style"],
        });
        const box = document.createElement("div");
        box.className = "md-body";
        box.innerHTML = html;
        return box;
      } catch (_) { /* fall through to raw */ }
    }
    const pre = document.createElement("pre");
    pre.className = "md-body md-body-raw";
    pre.textContent = text;
    return pre;
  }

  // openMdLightbox (superior): fullscreen dimmed reader for markdown —
  // same shell as the PDF lightbox (mobile gets the bottom ×/download bar).
  function openMdLightbox(text, filename, raw) {
    closeMdLightbox();
    const lb = document.createElement("div");
    lb.className = "pdf-lightbox md-lightbox";
    const frame = document.createElement("div");
    frame.className = "pdf-lightbox-frame md-lightbox-frame";
    if (raw) {
      const pre = document.createElement("pre");
      pre.className = "md-body md-body-raw";
      pre.textContent = text;
      frame.appendChild(pre);
    } else {
      frame.appendChild(renderMd(text));
    }
    const url = URL.createObjectURL(new Blob([text], { type: "text/markdown" }));
    const x = document.createElement("button");
    x.className = "pdf-lightbox-x";
    x.type = "button";
    x.textContent = "×";
    x.setAttribute("aria-label", "close");
    x.addEventListener("click", function (ev) { ev.stopPropagation(); closeMdLightbox(); });
    const dl = document.createElement("button");
    dl.className = "img-lightbox-dl";
    dl.type = "button";
    dl.textContent = t("attach.download");
    dl.addEventListener("click", function (ev) {
      ev.stopPropagation();
      const a = document.createElement("a");
      a.href = url;
      a.download = filename || "attachment.md";
      document.body.appendChild(a);
      a.click();
      a.remove();
    });
    lb.appendChild(frame);
    if (window.innerWidth <= 800) {
      const bar = document.createElement("div");
      bar.className = "pdf-lightbox-bar";
      bar.appendChild(x);
      bar.appendChild(dl);
      lb.appendChild(bar);
    } else {
      lb.appendChild(x);
      lb.appendChild(dl);
    }
    lb.addEventListener("click", function (ev) {
      if (ev.target === lb) closeMdLightbox();
    });
    document.addEventListener("keydown", closeMdLightbox);
    document.body.appendChild(lb);
    setTimeout(function () { URL.revokeObjectURL(url); }, 10 * 60 * 1000);
  }
  function closeMdLightbox() {
    $$(".md-lightbox").forEach(function (el) { el.remove(); });
    document.removeEventListener("keydown", closeMdLightbox);
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
      const isImg = attachIsImage(a), isAud = attachIsAudio(a), isPdf = attachIsPdf(a), isMd = attachIsMd(a), isTxt = attachIsTxt(a);
      const preview = (isImg || isAud || isMd) ? '<div class="attach-preview" data-pv="' + i + '"></div>' : "";
      // superior 09-05: no PDF preview button on mobile (no inline PDF
      // viewer there; canInlinePdf() already gates the lightbox) - the
      // download button and fallback card remain the paths.
      const readBtn = (isPdf && canInlinePdf()) ? '<button class="row-action" data-pdf="' + i + '">' + esc(t("attach.readPdf")) + "</button>" : "";
      const mdBtn = isMd ? '<button class="row-action" data-md="' + i + '" title="' + esc(t("attach.expandMd")) + '" aria-label="' + esc(t("attach.expandMd")) + '">⛶</button>' : "";
      const txtBtn = isTxt ? '<button class="row-action" data-txt="' + i + '">' + esc(t("attach.previewTxt")) + "</button>" : "";
      const actions = '<span class="attach-actions"><button class="row-action" data-dl="' + i + '">' + esc(t("attach.download")) + "</button>" + readBtn + mdBtn + txtBtn + "</span>";
      return '<div class="attach-card attach-card-' + (isImg ? "img" : isAud ? "audio" : isPdf ? "pdf" : isMd ? "md" : isTxt ? "txt" : "file") + '">' +
        '<span class="attach-clip">📎</span>' +
        '<span class="attach-name">' + esc(a.filename) + "</span>" +
        '<span class="attach-size">' + esc(fmtBytes(a.size)) + "</span>" +
        attachTTLBadge(a) +
        actions +
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

  // openPdfLightbox (superior, point-to-point): module-local twin of
  // manage.js's — dimmed fullscreen, reader window one size smaller than
  // the viewport, octet-stream download MIME rebuilt to application/pdf.
  // Feedback 09-04: phones (Android/iOS, coarse pointer / narrow shell)
  // have no inline PDF viewer — iframes come up blank white.
  function canInlinePdf() {
    try {
      const coarse = window.matchMedia && window.matchMedia("(pointer: coarse)").matches;
      const mobileUA = /Android|iPhone|iPad|Mobile/i.test(navigator.userAgent || "");
      return !(coarse || mobileUA);
    } catch (_) { return true; }
  }
  function openPdfLightbox(url, filename) {
    closePdfLightbox();
    const lb = document.createElement("div");
    lb.className = "pdf-lightbox";
    const frame = document.createElement("div");
    frame.className = "pdf-lightbox-frame";
    // Feedback 09-04: mobile browsers (Android/iOS) render no inline PDF
    // in an iframe — the reader window shows as blank white. Offer an
    // open-in-viewer + download card instead on those devices.
    if (canInlinePdf()) {
      const fr = document.createElement("iframe");
      fr.src = url;
      fr.type = "application/pdf";
      fr.title = filename || "";
      frame.appendChild(fr);
    } else {
      const fb = document.createElement("div");
      fb.className = "pdf-fallback";
      const ic = document.createElement("div");
      ic.className = "pdf-fallback-ic";
      ic.textContent = "📄";
      const nm = document.createElement("div");
      nm.className = "pdf-fallback-name";
      nm.textContent = filename || "PDF";
      const hint = document.createElement("div");
      hint.className = "pdf-fallback-hint";
      hint.textContent = t("attach.pdfHint");
      const op = document.createElement("button");
      op.className = "row-action pdf-fallback-open";
      op.type = "button";
      op.textContent = t("attach.open");
      op.addEventListener("click", function (ev) {
        ev.stopPropagation();
        window.open(url, "_blank");
      });
      fb.appendChild(ic); fb.appendChild(nm); fb.appendChild(hint); fb.appendChild(op);
      frame.appendChild(fb);
    }
    const x = document.createElement("button");
    x.className = "pdf-lightbox-x";
    x.type = "button";
    x.textContent = "×";
    x.setAttribute("aria-label", "close");
    x.addEventListener("click", function (ev) { ev.stopPropagation(); closePdfLightbox(); });
    const dl = document.createElement("button");
    dl.className = "img-lightbox-dl";
    dl.type = "button";
    dl.textContent = t("attach.download");
    dl.addEventListener("click", function (ev) {
      ev.stopPropagation();
      const a = document.createElement("a");
      a.href = url;
      a.download = filename || "attachment.pdf";
      document.body.appendChild(a);
      a.click();
      a.remove();
    });
    lb.appendChild(frame);
    // On phones (same 800px breakpoint as the tab layout) the top-right ×
    // is out of thumb reach — group it with the download button in a
    // bottom-center bar instead. Desktop keeps × top-right, download bottom.
    if (window.innerWidth <= 800) {
      const bar = document.createElement("div");
      bar.className = "pdf-lightbox-bar";
      bar.appendChild(x);
      bar.appendChild(dl);
      lb.appendChild(bar);
    } else {
      lb.appendChild(x);
      lb.appendChild(dl);
    }
    lb.addEventListener("click", function (ev) {
      if (ev.target === lb) closePdfLightbox();
    });
    document.addEventListener("keydown", closePdfLightbox);
    document.body.appendChild(lb);
  }
  function closePdfLightbox() {
    $$(".pdf-lightbox").forEach(function (el) { el.remove(); });
    document.removeEventListener("keydown", closePdfLightbox);
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
    // PDF reader buttons are lazy: the blob is fetched on the first click,
    // so a big PDF costs nothing until it is actually opened.
    $$("[data-pdf]", root).forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const a = list[+btn.dataset.pdf];
        if (!a) return;
        btn.disabled = true;
        try {
          const res = await fetch("/api/files/" + encodeURIComponent(a.id) + "/download?code=" + encodeURIComponent(a.access_code), {
            headers: { Authorization: basicAuth() },
          });
          if (!res.ok) throw new Error(res.status);
          const blob = new Blob([await res.arrayBuffer()], { type: "application/pdf" });
          openPdfLightbox(URL.createObjectURL(blob), a.filename);
        } catch (e) {
          toast(t("attach.dlFailed") + e.message, "error");
        }
        btn.disabled = false;
      });
    });
    // ⛶: fetch the md text and open it in the lightbox.
    $$("[data-md]", root).forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const a = list[+btn.dataset.md];
        if (!a) return;
        btn.disabled = true;
        try {
          const res = await fetch("/api/files/" + encodeURIComponent(a.id) + "/download?code=" + encodeURIComponent(a.access_code), {
            headers: { Authorization: basicAuth() },
          });
          if (!res.ok) throw new Error(res.status);
          openMdLightbox(await res.text(), a.filename);
        } catch (e) {
          toast(t("attach.dlFailed") + e.message, "error");
        }
        btn.disabled = false;
      });
    });
    // ⛶ (txt): fetch the text and open it in the plain-text lightbox.
    $$("[data-txt]", root).forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const a = list[+btn.dataset.txt];
        if (!a) return;
        btn.disabled = true;
        try {
          const res = await fetch("/api/files/" + encodeURIComponent(a.id) + "/download?code=" + encodeURIComponent(a.access_code), {
            headers: { Authorization: basicAuth() },
          });
          if (!res.ok) throw new Error(res.status);
          openMdLightbox(await res.text(), a.filename, true);
        } catch (e) {
          toast(t("attach.dlFailed") + e.message, "error");
        }
        btn.disabled = false;
      });
    });
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
        // Markdown branch (superior): render inline as sanitized HTML —
        // images/styles/audio/video are stripped by the sanitizer, so an
        // .md attachment cannot pull remote assets or inject markup.
        if (attachIsMd(a)) {
          // Mobile skip is SCOPED to this thread capsule (superior): the
          // pane is too narrow on phones for the inline md window to be
          // readable — the card's ⛶ lightbox is the reader there. Other
          // surfaces (manage view etc.) keep the inline preview on mobile.
          if (window.innerWidth <= 800) { holder.remove(); return; }
          holder.appendChild(renderMd(await res.text()));
          return;
        }
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
    composeReply(d.to, d.subject, d.parentId, d.body);
  });
  // Superior 01M1AWXF: follow-up on own sent mail — recipients unchanged,
  // irt wired, subject prefixed 跟进/Follow-up instead of Re.
  document.addEventListener("compose:followUp", function (ev) {
    var d = ev.detail || {};
    composeInReplyTo = d.parentId || null;
    renderInReplyTo();
    $("#compose-to").value = d.to || "";
    $("#compose-subject").value = (() => { var s = quoteSubjectBase(d.subject, d.body); return s ? t("compose.followUpPrefix") + " " + s : ""; })();
    $("#compose-body").value = "";
    draftAnchor(d.to);
    navActivateCompose();
    loadComposeThread();
    $("#compose-body").focus();
  });
  document.addEventListener("compose:reply-self", function (ev) {
    composeReplyAsSelf((ev.detail || {}).m);
  });
  document.addEventListener("compose:forward", function (ev) {
    composeForward((ev.detail || {}).m);
  });
  document.addEventListener("compose:entered", function () {
    ensureComposeAccounts();
    draftReconcile(); // re-entering loads the peer's bucket if untouched
    // boss 1001: re-entry must feel like a SWITCH, not a load. The event
    // bus (peer-filtered newmail, inbox:read) keeps the rendered thread
    // fresh even while the page is hidden, so a same-peer rendered list
    // paints as-is (no reload, no scroll reset) and a deferred silent
    // refresh confirms with the server, scroll riding through the swap.
    var t0 = $("#compose-thread");
    var v0 = ($("#compose-to").value || "").trim();
    if (v0 && t0 && t0.getAttribute("data-peer") === v0.toLowerCase() && t0.querySelector(".thread-item")) {
      setTimeout(function () {
        var v1 = ($("#compose-to").value || "").trim();
        if (v1 && v1.toLowerCase() === v0.toLowerCase()) loadComposeThread({ keepScroll: true });
      }, 60);
    } else {
      // boss 1001 pool: no live same-peer list - a parked fragment for the
      // To peer still paints instantly; no entry at all means the plain
      // network load path (first visit for that conversation).
      loadComposeThread();
    }
    ensureComposeShowcaseVisibility();
    fitComposeOneScreen();
    setTimeout(fitComposeOneScreen, 250); // second pass: late fonts/layout
  });
  // boss 0.3.4 ask #3: a letter arriving while the user sits on the
  // compose page must surface on its own. The "new mail" beat (the badge
  // poll's signal - the same instant the accounts-row dots light up)
  // re-pulls the open peer's thread. No-to state: loadComposeThread
  // no-ops into its placeholder, so the listener stays dumb.
  document.addEventListener("inbox:newmail", function (ev) {
    var from2 = String((ev.detail || {}).from || "").toLowerCase();
    var letter = (ev.detail && ev.detail.letter) || null;
    // boss 1001 incremental (display first, server truth verifies after):
    // a beat that carries the letter's own summary MERGES it into a PARKED
    // slot instead of dropping the page - switching back paints the arrival
    // with zero requests. A summary-less beat (older shape) keeps the old
    // conservative drop.
    if (from2 && letter) threadPoolMerge(from2, letter);
    else if (from2 && threadPool.has(from2)) threadPoolDrop(from2);
    // boss 09-30: a bystander's letter must not flash the open conversation.
    // Reload only when the new mail is FROM the open peer (either side of a
    // display-name form); a no-to state keeps the placeholder, not a reload.
    var to2 = ($("#compose-to").value || "").trim().toLowerCase();
    if (!to2) return;
    if (from2 && to2.indexOf(from2) < 0 && from2.indexOf(to2) < 0) return;
    // boss 1001 incremental: the open conversation paints the beat's letter
    // with ZERO requests, then the verify pull confirms - and a verify that
    // finds nothing changed does not touch the DOM at all (no churn).
    if (letter) threadPaintIncremental(letter, from2 || to2);
    loadComposeThread();
  });
  // boss 1001 dot audit #6b: a consume elsewhere (inbox detail, mark-all,
  // mail-tab read) must converge the open thread's capsule dots the same
  // tick - the thread used to keep its dots until some unrelated reload.
  // boss 1001 refinement: only reload when there is something to converge -
  // the thread's own read-on-open converges itself (src:"thread") and a
  // reload with no markers left would just churn node identity (the
  // convrefresh ① invariant).
  document.addEventListener("inbox:read", function (ev) {
    var det = ev.detail || {};
    var threadEl = $("#compose-thread");
    if (det.src === "thread") return;
    var hasMarkers = !!(threadEl && threadEl.querySelector(".unread-dot, .thread-subj-unread"));
    if (det.all) { if (hasMarkers) loadComposeThread(); return; }
    var to3 = ($("#compose-to").value || "").trim().toLowerCase();
    if (!to3 || !hasMarkers) return;
    var f3 = String(det.from || "").toLowerCase();
    if (f3 && (to3.indexOf(f3) >= 0 || f3.indexOf(to3) >= 0)) loadComposeThread();
  });

  // ---- 0.3.5 件2: the one-line IM bar + ＋ panel (boss-approved v3) ----
  (function wireImBar() {
    var sec = document.getElementById("tab-compose");
    var bar = document.getElementById("im-bar");
    var input = document.getElementById("im-input");
    var send = document.getElementById("im-send");
    var plus = document.getElementById("im-plus");
    var sheet = document.getElementById("im-sheet");
    var subjLine = document.getElementById("im-subject");
    window.__canary = { sec: !!sec, bar: !!bar, input: !!input, send: !!send, plus: !!plus, sheet: !!sheet, subjLine: !!subjLine };
    if (!sec || !bar || !input || !send || !plus || !sheet || !subjLine) return;
    window.__canary.passed = true;
    input.addEventListener("input", function () {
      $("#compose-body").value = input.value;
      draftNoteTyping();
      imInputGrow(input);
    });
    // Chat semantics: on a PHYSICAL keyboard Enter sends, Shift+Enter
    // makes the newline. On touch the soft keyboard has no shift -
    // every "newline" attempt would fire a letter (boss), so there
    // Enter falls through to the textarea and the ➤ button owns
    // sending.
    var PHYS_KB = !(window.matchMedia && window.matchMedia("(hover: none) and (pointer: coarse)").matches);
    input.addEventListener("keydown", function (ev) {
      if (PHYS_KB && ev.key === "Enter" && !ev.shiftKey) { ev.preventDefault(); send.click(); }
    });
    send.addEventListener("click", function () { $("#btn-send").click(); });
    function closeSheet() { setSheet(false); }
    // Boss: while the panel is open the plus morphs to a minus; tapping it
    // (or anything outside) collapses again. One switch owns the glyph.
    function setSheet(open) {
      sheet.classList.toggle("hidden", !open);
      plus.textContent = open ? "\u2212" : "\uff0b";
      if (open) {
        input.blur(); // drop the soft keyboard before the panel opens
        // Opening the panel shrinks the list; without this the clip edge
        // slices a capsule mid-line and reads as occlusion (boss). Snap to
        // the latest so the cut falls below the newest letter, chat-style.
        var holderEl = document.getElementById("thread-holder");
        if (holderEl) holderEl.scrollTop = holderEl.scrollHeight;
      }
    }

    document.getElementById("im-attach").addEventListener("click", function () {
      closeSheet();
      $("#btn-attach").click();
    });
    // Boss freeze-round feedback: the ＋ panel carries Cc — the validated cc
    // row (chips + autocomplete) un-hides in place; emptying the chips
    // re-hides it via syncCcVisibility (dropping the im-cc-open exception).
    // Boss: the Cc input must not float at the top of the page — it anchors
    // with the writing zone (inside the sheet, above the bar). The node moves
    // (listeners ride along, house pattern); moving back restores the form.
    var ccHome = document.getElementById("compose-cc-row");
    var ccHomeParent = ccHome ? ccHome.parentNode : null;
    var ccHomeNext = ccHome ? ccHome.nextSibling : null;
    var sheetHomeParent = sheet.parentNode;
    var sheetHomeNext = sheet.nextSibling;
    var attHome = document.getElementById("compose-attachments");
    var attHomeParent = attHome ? attHome.parentNode : null;
    var attHomeNext = attHome ? attHome.nextSibling : null;    attHomeRestore = function () { // ride home whenever IM mode is off
      if (attHome && attHomeParent && attHome.parentNode !== attHomeParent) {
        attHomeParent.insertBefore(attHome, attHomeNext);
      }
    };
    sheetIntoCard = function () { // v6: panel rows + attachment chips live in the card
      if (sheet && bar && !bar.contains(sheet)) bar.appendChild(sheet);
      if (attHome && bar && !bar.contains(attHome)) bar.insertBefore(attHome, sheet);
    };
    sheetHomeRestore = function () {
      if (sheet && sheetHomeParent && sheet.parentNode !== sheetHomeParent) {
        sheetHomeParent.insertBefore(sheet, sheetHomeNext);
      }
    };
        ccMoveBack = function () { // module handle: syncImMode calls it off-mode
      if (ccHome && ccHomeParent && ccHome.parentNode !== ccHomeParent) {
        ccHomeParent.insertBefore(ccHome, ccHomeNext);
      }
    };

    // Boss semantics (v2 correction): ＋ pops the buttons; tapping Cc pulls
    // the row OUT to reside by the bar (发信后即消 - a send dissolves it);
    // while the row exists its button hides. One reconciler keeps the pair
    // honest wherever the row state changes.
    // 委托式接线（v6）：bar/面板节点在模式切换里会被搬移/重排，直接挂点
    // 有被换元素的风险——document 级委托对任何 DOM 身份变化免疫。
    document.addEventListener("click", function (e) {
      if (e.target.closest("#im-plus")) {
        setSheet(sheet.classList.contains("hidden"));
        return;
      }
      if (e.target.closest("#im-irt-x")) {
        // boss 09-30 v2: the bar carries irt + subject, so × clears BOTH
        // (the line dies with the pair; a capsule tap refills them).
        composeInReplyTo = null;
        $("#compose-subject").value = "";
        renderInReplyTo();
        imPaintHead();
        return;
      }
      if (e.target.closest("#im-cc")) {
        if (ccHome) {
          sheet.appendChild(ccHome);
          ccHome.classList.remove("hidden");
        }
        imSyncCcUi();
        var ccInput = $("#compose-cc");
        if (ccInput) ccInput.focus();
        return;
      }
      if (!sheet.classList.contains("hidden") && !e.target.closest("#im-sheet") &&
          !e.target.closest("#im-back") && !e.target.closest("#im-full")) closeSheet();
    });
    function imSyncCcUi() {
      var btn = document.getElementById("im-cc");
      if (btn && ccHome && sheet && sheet.contains(ccHome)) {
        btn.classList.toggle("hidden", !ccHome.classList.contains("hidden"));
      }
    }

    document.getElementById("im-refresh").addEventListener("click", function () {
      loadComposeThread();
    });
    // Boss: the subject must be visible and reachable, never silent — the
    // line above the bar opens the full form with the subject focused.
    subjLine.addEventListener("click", function () {
      enterFullForm();
      var subjEl = $("#compose-subject");
      if (subjEl) subjEl.focus();
    });
    // Boss: the switch is lossless BOTH ways - body text, Cc, in-reply-to
    // and attachments all ride along (shared nodes/values; only the chrome
    // moves). The card sleeps while the full form owns the page; the 回来
    // button (compose.imBack) brings the conversation back.
    function enterFullForm() {
      sec.classList.add("im-full");
      input.blur(); // drop the soft keyboard
      ccMoveBack(); // cc row home (chips + value ride with the node)
      attHomeRestore(); // attachment chips home into the form
      closeSheet();
      imPaintHead();
    }
    function exitFullForm() {
      sec.classList.remove("im-full");
      // boss 0930 v3: the irt anchor now SURVIVES the round trip (retires
      // the 0.3.4.2 clearing - its "bar's attachment line" rationale died
      // with that bar design; the current irt line shows and × clears the
      // anchor, so silently dropping it here only lost replies).
      renderInReplyTo();
      // Boss: the conversation page is single-To - a multi-value To the
      // user typed on the full form clips to its first address here too.
      var toEl = $("#compose-to");
      var first = (toEl.value || "").split(",")[0].trim();
      if (first && first !== toEl.value) toEl.value = first;
      syncImBar(); // body text rides back into the bar
      sheetIntoCard(); // chips back into the card
      // Cc with content stays resident in the card (what the form showed,
      // the bar keeps showing); emptied cc leaves just the button.
      if (composeCcChips.length > 0 && ccHome) {
        if (!sheet.contains(ccHome)) sheet.appendChild(ccHome);
        ccHome.classList.remove("hidden");
        setSheet(true);
      }
      imSyncCcUi();
      imPaintHead();
    }
    document.getElementById("im-full").addEventListener("click", function () {
      if (sec.classList.contains("im-full")) exitFullForm();
      else enterFullForm();
    });
    var backBtn = document.getElementById("im-back");
    if (backBtn) backBtn.addEventListener("click", exitFullForm);
    plus.title = t("compose.imPlus");
    plus.setAttribute("aria-label", t("compose.imPlus"));
    send.setAttribute("aria-label", t("compose.send"));
    document.getElementById("im-refresh").setAttribute("aria-label", t("compose.refreshThread"));
    document.addEventListener("i18n:change", function () {
      plus.title = t("compose.imPlus");
      plus.setAttribute("aria-label", t("compose.imPlus"));
      send.setAttribute("aria-label", t("compose.send"));
      document.getElementById("im-refresh").setAttribute("aria-label", t("compose.refreshThread"));
      imPaintHead();
    });
    // Tap outside the panel closes it (the ＋ button toggles itself).
    document.addEventListener("click", function (e) {
      if (sheet.classList.contains("hidden")) return;
      if (!e.target.closest("#im-sheet") && !e.target.closest("#im-plus") &&
          !e.target.closest("#im-back") && !e.target.closest("#im-full")) closeSheet();
    });
  })();
  // ---- mobile one-screen compose (superior 09-02): the recent-
  // conversation list folds into a fullscreen modal; the node is MOVED in
  // and out (listeners ride along), everything else fits the viewport.
  function fitComposeOneScreen() {
    var tab = document.getElementById("tab-compose");
    if (!tab || tab.classList.contains("hidden")) return;
    if (window.innerWidth > 800) { tab.style.removeProperty("--compose-1s"); return; }
    var top = tab.getBoundingClientRect().top;
    if (top <= 0) return;
    var h = window.innerHeight - (window.__fixedNavInset ? window.__fixedNavInset() : 0) - Math.max(top, 0);
    if (h < 240) h = 240;
    tab.style.setProperty("--compose-1s", h + "px");
    var over = document.documentElement.scrollHeight - window.innerHeight;
    if (over > 0) tab.style.setProperty("--compose-1s", Math.max(h - over, 240) + "px");
    // 0015 (boss rc10 retest: compose still had scroll room on shorter
    // viewports): the form's min content - the body textarea, whose rows
    // attribute alone sets ~230px regardless of the 140px CSS floor - can
    // exceed the measured tab, and .compose-form scrolls internally. Give
    // the body exactly what the fixed rows leave (60px abs min); typed
    // overflow scrolls inside the textarea, standard behavior.
    // 0020 (boss rc15 field report: with the soft keyboard open the body
    // crushed to a ~2-line sliver): while the keyboard is up - the body is
    // focused, or the visual viewport sits far below the layout one - skip
    // the compression and let the form scroll internally instead; the next
    // refit after the keyboard closes restores the static compression.
    var form = tab.querySelector(".compose-form");
    var body = document.getElementById("compose-body");
    if (form && body) {
      var ae = document.activeElement;
      var kbOpen = (ae && (ae.tagName === "TEXTAREA" || ae.tagName === "INPUT")) ||
        (window.visualViewport && window.visualViewport.height < window.innerHeight - 120);
      body.style.minHeight = "";
      body.style.height = "";
      if (!kbOpen) {
        var over2 = form.scrollHeight - form.clientHeight;
        if (over2 > 0) {
          var bh = body.getBoundingClientRect().height;
          body.style.height = Math.max(60, Math.round(bh - over2)) + "px";
          body.style.minHeight = "0";
        }
      }
    }
  }
  window.addEventListener("resize", fitComposeOneScreen);
  (function wireThreadModal() {
    var btn = document.getElementById("btn-thread-pane");
    var thread = document.getElementById("compose-thread");
    var modal = document.getElementById("thread-modal");
    var holder = document.getElementById("thread-holder");
    var bodyBox = document.getElementById("thread-modal-body");
    var tools = document.getElementById("thread-modal-tools");
    var refresh = document.getElementById("btn-refresh-thread");
    var refreshHome = refresh ? refresh.parentNode : null;
    if (!btn || !thread || !modal || !holder || !bodyBox) return;
    // boss 09-30: the drawer is a 往来邮件 LIST - newest at TOP (like the PC
    // rail and the pre-0.3.5 full page). The conversation view keeps chat
    // order (newest at bottom), so the shared node flips on open and flips
    // back on close; the drawer body then rests on its top edge.
    function flipThreadOrder() {
      var kids = Array.prototype.slice.call(thread.children);
      for (var i = kids.length - 1; i >= 0; i--) thread.appendChild(kids[i]);
    }
    function open() {
      bodyBox.appendChild(thread); // move the node in — listeners ride along
      if (refresh && tools) tools.appendChild(refresh); // 刷新会话 lives in the drawer
      if (imMode()) flipThreadOrder();
      modal.classList.remove("hidden");
      bodyBox.scrollTop = 0; // newest-first list starts at its top
      fitComposeOneScreen();
    }
    function close() {
      if (imMode()) flipThreadOrder(); // restore chat order for the inline view
      holder.appendChild(thread); // back to the (hidden) inline anchor
      modal.classList.add("hidden");
    }
    btn.addEventListener("click", open);
    document.getElementById("btn-thread-close").addEventListener("click", close);
    modal.addEventListener("click", function (e) { if (e.target === modal) close(); });
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      if (!modal.classList.contains("hidden")) close();
    });
  })();
})();
