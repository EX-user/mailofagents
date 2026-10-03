// agentmail admin panel — vanilla JS, no build step.
// Authentication: credentials (address + password) are kept in sessionStorage
// after login and sent as a Basic auth header on every API call. This lets the
// panel serve both admin and regular accounts from one login page. sessionStorage
// is used (not localStorage) so credentials do not persist across browser sessions.
//
// S1 of the zero-build ESM split (governance v1): shared foundation lives in
// ./core.js; this entry imports it and keeps every domain in place. i18n stays
// a classic script (window.I18N). HARD CONSTRAINT: domain code imports only
// core; cross-domain interaction goes through DOM events.
import { $, $$, esc, api, getSession, setSession, setToken, updateTokenRole, basicAuth, toast, setUnauthorizedHandler, fmtTime, fmtBytes, copyText } from "./core.js";

(function () {
  "use strict";

  // System domain from /api/status, used to construct admin address etc.
  let systemDomain = "agentmail.local";

  // The core fetch wrapper calls this on a hard 401 (stale creds) — the
  // login screen lives here, so wire it once at module eval.
  setUnauthorizedHandler(function () { showLogin(); });

  // Sending-limits wiring (v0.2.8 round 2, Felix blocker 01M25N4QJ):
  // ONE document-level delegated listener covers the PC table, the mobile
  // cards and the modal's own buttons — re-renders can never orphan a
  // listener again (the dead data-lssave save button came from exactly
  // that: a listener attached to a node the re-render replaced).
  document.addEventListener("click", function (ev) {
    const t = ev.target;
    if (!t || !t.closest) return;
    if (t.id === "btn-limits-close") {
      const m = $("#limits-modal");
      if (m) m.classList.add("hidden");
      return;
    }
    if (t.id === "btn-limits-save") { saveLimitsModal(); return; }
    if (t === $("#limits-modal")) { m = $("#limits-modal"); if (m) m.classList.add("hidden"); return; }
    const opener = t.closest("[data-limits]");
    if (opener) openLimitsModal(opener.dataset.limits);
  });


  // i18n shortcut (v0.4.12): dynamic strings go through the dictionary;
  // before i18n.js loads or if unavailable, fall back to the key.
  function t(key, vars) {
    return window.I18N ? window.I18N.t(key, vars) : key;
  }

  // ---- tab switching ----

  // ---- inbox unread badge (v0.5.5) ----
  // Red dot + count on the Inbox nav tab. Refreshes after each inbox load
  // and on a slow poll (60s) while logged in; hidden at zero.
  function setInboxBadge(n) {
    // boss 1001 (quiet query library): the unread dot lives on the
    // ACCOUNTS button - Mail is a deliberate lookup surface, not a
    // shouter. Same signal, different host.
    const tab = $(".tab[data-tab=accounts]");
    if (!tab) return;
    let badge = $(".tab-badge", tab);
    if (!n) { if (badge) badge.remove(); return; }
    if (!badge) {
      badge = document.createElement("span");
      badge.className = "tab-badge";
      tab.appendChild(badge);
    }
    // Pure dot, no count (feedback): width can never shift with the number.
    badge.textContent = "";
  }

  // refreshInboxBadge is sequenced: the 5s poll, the inbox-load refresh and
  // the post-read refresh run concurrently, and an older response arriving
  // after a newer one would resurrect the dot until the next tick (the
  // reported "badge clears with a lag"). Only the latest call may write.
  let badgeSeq = 0;
  var prevLatestId = null;
  async function refreshInboxBadge() {
    if (!getSession()) { setInboxBadge(0); return; }
    // Background tabs skip the tick — the badge refreshes on visibility
    // return, so 5s polling stays cheap in aggregate (admin: 2-5s wanted).
    if (document.visibilityState === "hidden") return;
    const seq = ++badgeSeq;
    try {
      const d = await api("/api/inbox?limit=1&badge=1"); // badge=1: server skips the audit row - a poll reads nothing
      if (seq !== badgeSeq) return; // a newer refresh superseded this one
      var cur = d.unread_count || 0;
      // New-mail gate keyed on the latest message id, not the unread count:
      // the approved 0.3.4 "opening the conversation reads it" semantics
      // consume letters between polls, so a count high-water deflates and a
      // genuine arrival that merely recovers the level (4 > 4) never fires —
      // the open conversation misses its own peer's letter. An id the poll
      // has not seen is arrival itself; consumption never changes it.
      var latestMail = (d.messages && d.messages[0]) || null;
      var latestId = latestMail ? (latestMail.id || "") : "";
      if (prevLatestId === null) {
        prevLatestId = latestId; // first sample: baseline only, no event
      } else if (latestId && latestId !== prevLatestId) {
        // New mail detected — notify manage.js incremental merger (v0.2.1).
        // boss 09-30: carry the latest sender so a listener scoped to one
        // conversation can tell its peer's mail from a bystander's.
        prevLatestId = latestId;
        // boss 1001 incremental merge (display first, verify after):
        // the beat carries the letter's OWN summary so the conversation
        // face paints it with ZERO extra requests; the verify pull that
        // follows the same tick confirms against server truth.
        var beatLetter = latestMail ? {
          id: latestMail.id || "",
          subject: latestMail.subject || "",
          preview: latestMail.preview || "",
          ts: latestMail.received_at || 0,
          files: latestMail.files || 0, // 1054: the beat must not strip the attachment count either
        } : null;
        document.dispatchEvent(new CustomEvent("inbox:newmail", { detail: { from: latestMail ? (latestMail.from || "") : "", letter: beatLetter } }));
      }
      setInboxBadge(cur);
      // boss 10-01 single master clock: the instant the badge poll
      // returns, the activity pull rides the same beat - channel two
      // keeps NO clock of its own (its interval is retired below), so
      // the row dots can never drift from the badge observation.
      // pullActivity self-gates on the accounts panel being visible;
      // hidden panels cost nothing.
      if (typeof pullActivity === "function") {
        try { pullActivity(); } catch (_) {}
      }
    } catch (_) { /* badge is best-effort */ }
  }
  setInterval(refreshInboxBadge, 5000);
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") refreshInboxBadge();
  });

  // ---- System | Mine | Directory segment (superior order 01M1C8MAY) ----
  // Overview page has three in-page views: System (stats/growth/recent),
  // Mine (personal activity card), Directory (address book). Same pill
  // pattern. The prefs pill in the header opens the profile panel.
  function setOvwView(v) {
    var main = $("#ovw-main"), dir = $("#ovw-directory"), mine = $("#ovw-mine");
    if (!main || !dir || !mine) return;
    // PC (superior 01M1E6A1F): Overview is two views — Data (system+activity+
    // mine shown together) | Directory; the separate Mine sub-page is a
    // phone-only view, so on PC a stored "mine" re-lands on Data.
    var pc = window.innerWidth > 800;
    if (pc && v === "mine") v = "main";
    if (v !== "directory" && v !== "mine") v = "main";
    main.classList.toggle("hidden", v !== "main");
    mine.classList.toggle("hidden", pc ? v !== "main" : v !== "mine");
    dir.classList.toggle("hidden", v !== "directory");
    $$("#ovw-seg button").forEach(function (b) {
      b.classList.toggle("on", b.dataset.oview === v);
    });
    // 0.2.4 fine-tune (superior): the round directory refresh rides beside
    // the view capsule on phones — visible only while Directory is on
    // (viewport hiding is .m-only's job).
    var rbtnM = $("#btn-refresh-directory-m");
    if (rbtnM) rbtnM.classList.toggle("hidden", v !== "directory");
    try { localStorage.setItem("ovw-view", v); } catch (_) {}
    if (v === "directory" && !dir.dataset.loaded) {
      dir.dataset.loaded = "1";
      loadDirectory();
    }
  }
  (function wireOvwSeg() {
    var seg = $("#ovw-seg");
    if (seg) seg.addEventListener("click", function (ev) {
      var b = ev.target.closest("button[data-oview]");
      if (b) setOvwView(b.dataset.oview);
    });
    // Re-sync the mine pane when crossing the 800px breakpoint (superior
    // 01M1E6A1F: PC merges Mine into Data; phone keeps the separate view).
    window.addEventListener("resize", function () {
      var segOn = seg && seg.querySelector("button.on");
      if (segOn && segOn.dataset.oview === "main") setOvwView("main");
    });
    var prefs = $("#btn-prefs");
    if (prefs) prefs.addEventListener("click", function () { activateTab("profile"); });
    // S2 protocol: domain modules request tab switches via DOM events.
  document.addEventListener("badge:refresh", function () { refreshInboxBadge(); });

  // 0.3.5 (boss staging note): the accounts unread dot clears the same
  // tick a letter is consumed (compose thread reads, inbox detail reads,
  // mark-all) - no 5s pull wait. Coalesced: a thread burst consumes N
  // letters and one applyActivity pass covers them all.
  var mailReadTimer = null;
  document.addEventListener("inbox:read", function (ev) {
    var det = ev.detail || {};
    var d = (actData = actData || {});
    var ub = (d.unreadBySender = d.unreadBySender || {});
    if (det.all) { Object.keys(ub).forEach(function (k) { delete ub[k]; }); actMutNote("delall", ""); }
    else {
      var addr = String(det.from || "").toLowerCase();
      // boss 10-01: log the del UNCONDITIONALLY. A read that lands before
      // the first activity pull populated the map used to skip the log -
      // the in-flight pull then resurrected the row dot while the nav
      // badge (unread_count poll) stayed clear (row-on/nav-off).
      if (addr) { delete ub[addr]; actMutNote("del", addr); }
    }
    if (mailReadTimer) return;
    // boss 10-01: the NAV badge used to wait for the next 5s poll after a
    // read - with letters now painting instantly (incremental merge) the
    // up-to-5s lingering dot became conspicuous. Refresh the badge in the
    // same coalesced tick: the poll is idempotent server truth, no drift.
    mailReadTimer = setTimeout(function () { mailReadTimer = null; applyActivity(); refreshInboxBadge(); }, 60);
  });

  // boss 10-01: arrival gets the same-tick face too. The badge poll's
  // newmail beat lights the inbox and the open thread instantly, but the
  // accounts-row dot reads actData.unreadBySender and used to wait for
  // the next pull (up to a full poll cycle). Mirror the inbox:read short
  // path: bump the sender's key locally and re-render; the next pull
  // confirms with server truth. Truthiness only - the face has no count.
  var mailNewTimer = null;
  document.addEventListener("inbox:newmail", function (ev) {
    var addr = String((ev.detail || {}).from || "").trim().toLowerCase();
    if (addr.indexOf("@") < 0) return;
    var d = (actData = actData || {});
    var ub = (d.unreadBySender = d.unreadBySender || {});
    ub[addr] = (ub[addr] || 0) + 1;
    actMutNote("bump", addr);
    if (mailNewTimer) return;
    mailNewTimer = setTimeout(function () { mailNewTimer = null; applyActivity(); }, 60);
  });

  document.addEventListener("accounts:refresh", function () { loadAccounts(); });
  document.addEventListener("nav:activate", function (ev) {
    var tab = (ev.detail || {}).tab;
    if (tab) activateTab(tab);
  });
})();

  // Page-scroll lock, JS side (0015): the CSS html:has(...) lock needs
  // :has() plus dvh (Chrome 105/108+) - an older WebView never matches it
  // and the page still scrolls behind the fixed bar (boss rc10 retest:
  // compose could still be dragged). The router knows the active tab, so
  // toggle a class and pin a real-px viewport height; the CSS mirrors the
  // :has rules for html.page-locked, making the lock support-independent.
  var PAGE_LOCK_TABS = ["inbox", "mail", "compose", "audit", "accounts"];
  function syncPageLock(name) {
    var de = document.documentElement;
    var lock = PAGE_LOCK_TABS.indexOf(name) >= 0;
    de.classList.toggle("page-locked", lock);
    if (lock) de.style.setProperty("--app-vh", window.innerHeight + "px");
    else de.style.removeProperty("--app-vh");
  }
  window.addEventListener("resize", function () {
    if (document.documentElement.classList.contains("page-locked")) {
      document.documentElement.style.setProperty("--app-vh", window.innerHeight + "px");
    }
  });
  function activateTab(name) {
    // Leaving a message view by any route (tab switch included) must stop
    // all audio (feedback: sound kept playing after leaving the content).
    document.dispatchEvent(new CustomEvent("audio:stop-all"));    $$(".tab").forEach(function (b) {
      b.classList.toggle("active", b.dataset.tab === name);
    });
    $$(".tab-panel").forEach(function (p) { p.classList.add("hidden"); });
    $("#tab-" + name).classList.remove("hidden");
    syncPageLock(name);
    if (name === "overview") loadOverview();
    if (name === "accounts") { loadAccounts(); activityEntered(); } // 进页即拉（5s 防抖，boss 报单修）

    if (name === "inbox") document.dispatchEvent(new CustomEvent("inbox:entered"));
    if (name === "profile") document.dispatchEvent(new CustomEvent("profile:entered"));
    if (name === "mail") document.dispatchEvent(new CustomEvent("manage:entered"));
    if (name === "overview") {
      // v0.6.9: the Overview tab hosts the Directory subview — restore the
      // last pane (same pattern as the Manage Messages|Overview segment).
      // Pre-login restores stay on "main": the directory fetch needs a
      // session and there is no re-kick path for it (mgmt has one).
      var ov = "main";
      if (getSession()) {
        try { ov = localStorage.getItem("ovw-view") || "main"; } catch (_) {}
      }
      setOvwView(ov === "directory" ? "directory" : "main");
    }
    if (name === "compose") document.dispatchEvent(new CustomEvent("compose:entered"));
    if (name === "profile") loadProfile();
    if (name === "settings") loadSettings();
    if (name === "audit") document.dispatchEvent(new CustomEvent("audit:entered"));
  }

  $$(".tab").forEach(function (b) {
    b.addEventListener("click", function () { activateTab(b.dataset.tab); });
  });

  // v0.2.9 (boss directive): Overview left the nav — the header brand is
  // its entry now. The page itself is unchanged.
  const brandHome = document.getElementById("brand-home");
  if (brandHome) brandHome.addEventListener("click", function () { activateTab("overview"); });


  // S2 protocol: the manage module owns subordinate edges; other domains
  // request them through the DOM event bus (resolve never rejects).
  function requestSubs(force) {
    return new Promise(function (resolve) {
      document.dispatchEvent(new CustomEvent("subs:request", { detail: { force: force, resolve: resolve } }));
    });
  }

  // ---- overview ----

  // renderOverviewGrowth adds today / last-7-days stat cards and the 7-day
  // bar chart to the Overview tab (admin request: the logged-in page should
  // show at least what the guest portal shows). Growth comes from the public
  // endpoint, so it works for both admins and regular accounts; failures
  // degrade silently (no chart, no extra cards).
  // growthDayTarget picks the chart's day count from the viewport
  // (superior feedback): 7 on phones, 10 on mid widths, 14 on wide
  // screens. The endpoint currently returns 7; when it grows to 14 the
  // wide-screen charts fill in automatically (slice keeps what exists).
  function growthDayTarget() {
    const w = window.innerWidth || 1024;
    return w <= 800 ? 7 : (w <= 1100 ? 10 : 14);
  }

  let lastGrowthData = null;
  function renderOverviewGrowth(growth) {
    const chart = $("#ovw-growth-card");
    if (!growth) { if (chart) chart.classList.add("hidden"); return; }
    lastGrowthData = growth;
    // Flow metrics (today / last 7 days) go to the Activity group.
    const stats = $("#stats-activity");
    if (stats) {
      stats.innerHTML =
        '<div class="stat"><span class="num">' + esc(growth.today) + '</span><span>' + t("lbl.today") + '</span></div>' +
        '<div class="stat"><span class="num">' + esc(growth.week) + '</span><span>' + t("lbl.week") + '</span></div>';
    }
    if (chart) {
      const n = growthDayTarget();
      let days = (growth.days && growth.days.length)
        ? growth.days.slice(-n)
        : [{ date: "today", count: growth.today }, { date: "week", count: growth.week }];
      const sub = $("#ovw-growth-sub");
      if (sub) sub.textContent = t("ovw.growthSub", { n: days.length });
      drawGrowthDays(days, $("#ovw-growth-bars"), $("#ovw-growth-lbls"));
      chart.classList.remove("hidden");
    }
  }
  // Re-slice the chart when the viewport crosses a width band (debounced).
  window.addEventListener("resize", function () {
    clearTimeout(renderOverviewGrowth._rt);
    renderOverviewGrowth._rt = setTimeout(function () {
      const tab = $("#tab-overview");
      if (lastGrowthData && tab && !tab.classList.contains("hidden")) {
        renderOverviewGrowth(lastGrowthData);
      }
    }, 300);
  });

  // renderOverviewPersonal fills the grouped "My activity" card: an
  // "All time" column (contacts / received / unread / sent) and a "Recent
  // traffic" column (today + 7-day in/out from /api/mygrowth). Uses the
  // account's own endpoints (works for both roles). limit=1 keeps responses
  // light; we only read the counters. Silent degrade on any failure.
  async function renderOverviewPersonal() {
    const card = $("#personal-card");
    if (!card) return;
    try {
      const [con, inb, sent, myg, prof, setg] = await Promise.all([
        api("/api/contacts").catch(function () { return null; }),
        api("/api/inbox?limit=1").catch(function () { return null; }),
        api("/api/sent?limit=1").catch(function () { return null; }),
        api("/api/mygrowth").catch(function () { return null; }),
        api("/api/profile/self", { keepSession: true }).catch(function (e) {
          // Felix 01M25N4QJ: the first profile fetch right after login can
          // 401 once (session/token not yet settled); one delayed retry
          // heals it. Anything else degrades silently as before.
          if (String((e && e.message) || "").indexOf("401") < 0) return null;
          return new Promise(function (done) { setTimeout(done, 300); }).then(function () {
            return api("/api/profile/self", { keepSession: true }).catch(function () { return null; });
          });
        }),
        api("/api/info?query=settings", { keepSession: true }).catch(function () { return null; }),
      ]);
      const allTime = [];
      if (con) allTime.push({ num: con.count, label: t("lbl.contacts") });
      if (inb) allTime.push({ num: inb.total_count != null ? inb.total_count : inb.count, label: t("lbl.received") });
      if (inb && inb.unread_count) allTime.push({ num: inb.unread_count, label: t("lbl.unread") });
      if (sent) allTime.push({ num: sent.total_count != null ? sent.total_count : sent.count, label: t("lbl.sent") });
      const recent = myg ? [
        { num: myg.today_in, label: t("lbl.todayIn") },
        { num: myg.today_out, label: t("lbl.todayOut") },
        { num: myg.week_in, label: t("lbl.weekIn") },
        { num: myg.week_out, label: t("lbl.weekOut") },
      ] : [];
      const renderRows = function (rows) {
        return rows.map(function (c) {
          return '<div class="my-stat-row"><span class="my-stat-label">' + esc(c.label) +
            '</span><span class="my-stat-num">' + esc(c.num) + "</span></div>";
        }).join("");
      };
      // Attach column (superior feedback): quota first (server cap), then
      // the progressive rows — count and 7-day expiry light up as the
      // server fields arrive; the retention window is the fixed compile
      // TTL (30 days), labelled as such.
      const attach = [];
      const used = prof && typeof prof.files_used_bytes === "number" ? prof.files_used_bytes : null;
      const cap = setg && typeof setg.file_quota_per_acct === "number" ? setg.file_quota_per_acct : null;
      if (used != null && cap != null && cap > 0) {
        attach.push({ num: fmtBytes(used) + " / " + fmtBytes(cap) + (used >= cap ? " (" + t("attach.quotaFull") + ")" : ""), label: t("ovw.attachQuota") });
      }
      if (prof && typeof prof.attachments_count === "number") {
        attach.push({ num: prof.attachments_count, label: t("ovw.attachCount") });
      }
      if (prof && typeof prof.attachments_expiring === "number") {
        attach.push({ num: prof.attachments_expiring, label: t("ovw.attachExpiring") });
      }
      attach.push({ num: t("ovw.attachTtlVal"), label: t("ovw.attachTtl") });
      $("#personal-alltime").innerHTML = renderRows(allTime);
      $("#personal-recent").innerHTML = renderRows(recent);
      $("#personal-attach").innerHTML = renderRows(attach);
      // Empty halves collapse instead of showing an empty column.
      const allEl = $("#personal-alltime").parentElement;
      const recEl = $("#personal-recent").parentElement;
      const attEl = $("#personal-attach").parentElement;
      allEl.style.display = allTime.length ? "" : "none";
      recEl.style.display = recent.length ? "" : "none";
      attEl.style.display = attach.length ? "" : "none";
      card.classList.toggle("hidden", !allTime.length && !recent.length && !attach.length);
    } catch (_) {
      card.classList.add("hidden");
    }
  }

  // fmtBytes renders a byte count as a compact human size (12.4 MB).
  // storageCard renders the db size stat card when the public stats endpoint
  // reports db_size_bytes (0/absent means unavailable — no card).
  function storageCard(sizeBytes) {
    const human = sizeBytes > 0 ? fmtBytes(sizeBytes) : null;
    if (!human) return "";
    // Split value/unit so the number matches the other cards' size and
    // baseline; only the unit renders small (feedback: "59.4 MB" misaligned).
    const m = /^(\d+(?:\.\d+)?)\s*(.+)$/.exec(human);
    const numHTML = m
      ? esc(m[1]) + ' <small class="stat-unit">' + esc(m[2]) + "</small>"
      : esc(human);
    return '<div class="stat"><span class="num">' + numHTML + "</span><span>" + t("lbl.storage") + "</span></div>";
  }

  async function loadOverview() {
    const recent = $("#recent-activity");
    $("#stats-system").textContent = t("common.loading");
    $("#stats-activity").textContent = "";
    if (recent) recent.textContent = t("common.loading");
    const s = getSession();
    // Growth enrichment runs for both roles (public endpoint).
    const growthP = api("/api/info?query=growth").catch(function () { return null; });
    // Storage size comes from the public stats payload (both roles see it).
    const statsP = api("/api/info?query=stats").catch(function () { return null; });
    // Personal summary (own endpoints) — independent of the role branches.
    renderOverviewPersonal();
    // Regular accounts can't read /admin/* — calling it would 401 and the
    // api() wrapper would treat that as session-expired. Use the public stats
    // endpoint instead, and skip the global audit log (admin-only) for them.
    if (s && !s.is_admin) {
      try {
        const d = await api("/api/info?query=stats");
        $("#stats-system").innerHTML =
          '<div class="stat"><span class="num">' + esc(d.account_count) + "</span><span>" + t("lbl.accounts") + "</span></div>" +
          '<div class="stat"><span class="num">' + esc(d.message_count) + "</span><span>" + t("lbl.messages") + "</span></div>" +
          storageCard(d.db_size_bytes);
        renderOverviewGrowth(await growthP);
        if (recent) recent.innerHTML = '<p class="muted">Sign in to an admin account to see system activity.</p>';
      } catch (e) {
        $("#stats-system").textContent = t("common.error", { msg: e.message });
        if (recent) recent.textContent = "";
      }
      return;
    }
    try {
      const s = await api("/admin/stats");
      const pub = await statsP;
      $("#stats-system").innerHTML =
        '<div class="stat"><span class="num">' + esc(s.accounts) + "</span><span>" + t("lbl.accounts") + "</span></div>" +
        '<div class="stat"><span class="num">' + esc(s.messages) + "</span><span>" + t("lbl.messages") + "</span></div>" +
        storageCard(pub && pub.db_size_bytes);
      renderOverviewGrowth(await growthP);
      const a = await api("/admin/audit?limit=20");
      if (!a.entries || !a.entries.length) {
        if (recent) recent.textContent = t("ovw.noActivity");
        return;
      }
      if (recent) recent.innerHTML = "<ul>" + a.entries.map(function (e) {
        return "<li><b>" + esc(e.action) + "</b> · " + esc(e.account || "—") +
          " · <small>" + fmtTime(e.timestamp) + "</small>" +
          (e.detail ? " — " + esc(e.detail) : "") + "</li>";
      }).join("") + "</ul>";
    } catch (e) {
      $("#stats-system").textContent = t("common.error", { msg: e.message });
      recent.textContent = "";
    }
  }

  // ---- accounts ----

  async function loadAccounts() {
    const s = getSession();
    if (s && !s.is_admin) {
      await loadAccountsRegular(s.address);
      maybeMarqueeSigs();
      fitAccountsOneScreen();
      return;
    }
    // Admin view has global tools; the subordinate manager is regular-only.
    // v0.2.8-r1 (boss ruling): phones drop the one-screen plan for admins —
    // the merged-card zones are never populated here, so the plain
    // data-label card rows render instead (CSS scoped to .acc-admin).
    const accSecAdmin = document.getElementById("tab-accounts");
    if (accSecAdmin) accSecAdmin.classList.add("acc-admin");
    const subsSectionAdmin = $("#subs-section");
    if (subsSectionAdmin) subsSectionAdmin.classList.add("hidden");
    const subregPcAdmin = $("#subreg-pc");
    if (subregPcAdmin) subregPcAdmin.classList.add("hidden");
    const invBtnAdmin = $("#btn-invalid");
    if (invBtnAdmin) invBtnAdmin.classList.remove("hidden");
    const tbody = $("#accounts-table tbody");
    tbody.textContent = "";
    try {
      const data = await api("/admin/accounts");
      if (!data.accounts || !data.accounts.length) {
        tbody.innerHTML = '<tr><td colspan="3">' + t("acc.noAccounts") + '</td></tr>';
        return;
      }
      tbody.innerHTML = data.accounts.map(function (a) {
        const rowCls = a.disabled ? " class=\"row-disabled\"" : "";
        // Build tag badges: admin, listed (visible), disabled.
        var tags = "";
        if (a.is_admin) tags += ' <span class="badge-admin">admin</span>';
        if (a.visible) tags += ' <span class="badge-listed">listed</span>';
        if (a.disabled) tags += ' <span class="badge-disabled">disabled</span>';
        // recipient/cc limits at a glance (0/absent = unlimited)
        tags += ' <span class="badge-listed">' + esc(t("limits.short", { r: (a.max_recipients || 0), c: (a.max_cc || 0) })) + "</span>";
        const toggleBtn = a.is_admin
          ? "" // admin cannot be disabled (lockout guard), so no toggle button
          : a.disabled
            ? '<button class="row-action" data-enable="' + esc(a.address) + '">' + t("act.enable") + '</button>'
            : '<button class="row-action" data-disable="' + esc(a.address) + '">' + t("act.disable") + '</button>';
        return "<tr" + rowCls + ">" +
          '<td class="addr-cell" data-label="' + t("col.address") + '"><span class="pc-av-line"><span class="pc-addr">' + esc(a.address) + '</span><span class="pc-badges">' + tags.trim() + "</span></span></td>" +
          '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(a.signature || "") + '</span><span class="sig-dup" aria-hidden="true">' + esc(a.signature || "") + "</span></span></td>" +
          '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action" data-compose="' + esc(a.address) + '">' + t("act.compose") + '</button><button class="row-action" data-reset="' + esc(a.address) + '">' + t("act.resetPw") + '</button>' +
          toggleBtn + "</td>" +
          "</tr>";
      }).join("");
      // Wire each reset button.
      $$("[data-reset]", tbody).forEach(function (btn) {
        btn.addEventListener("click", function () { resetPassword(btn.dataset.reset); });
      });
      // Wire compose buttons (jump to Compose, prefill To).
      $$("[data-compose]", tbody).forEach(function (btn) {
        btn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: btn.dataset.compose } })); });
      });
      // Wire disable/enable buttons.
      $$("[data-disable]", tbody).forEach(function (btn) {
        btn.addEventListener("click", function () { setDisabled(btn.dataset.disable, true); });
      });
      $$("[data-enable]", tbody).forEach(function (btn) {
        btn.addEventListener("click", function () { setDisabled(btn.dataset.enable, false); });
      });
      maybeMarqueeSigs();

    } catch (e) {
      tbody.innerHTML = '<tr><td colspan="3">Error: ' + esc(e.message) + "</td></tr>";
    }
  }

  // ---- recipient/cc sending limits (v0.2.8, boss directive) ----
  // Self + each direct subordinate get one row: two number inputs and a
  // save button. Empty input = keep current; explicit 0 = unlimited
  // (server contract 239655b: range [0,1000], self/superior/admin may
  // write, everyone may read own).
  // v0.2.8 round 2 (boss-approved): limits moved from an inline card into a
  // per-account "Sending limits" button + modal. Modal opens prefilled with
  // the current effective values — leaving without changes keeps them.
  // Recipients limit: 1-1000 (0 is illegal). CC limit: empty submit = 0
  // (unlimited). Badges in the accounts table stay authoritative for the
  // at-a-glance view.
  var limitsCache = {};

  async function preloadLimits(selfAddr, subAddrs) {
    const targets = [selfAddr].concat(subAddrs || []);
    await Promise.all(targets.map(async function (a) {
      try {
        const q = a === selfAddr ? "" : "?address=" + encodeURIComponent(a);
        limitsCache[a] = await api("/api/account/limits" + q, { keepSession: true });
      } catch (_) { limitsCache[a] = null; }
    }));
  }

  function openLimitsModal(addr) {
    const modal = $("#limits-modal");
    if (!modal) return;
    $("#limits-modal-title").textContent = t("limits.title") + " \u2014 " + addr;
    const cur = limitsCache[addr] || {};
    $("#limits-to").value = cur.max_recipients != null ? cur.max_recipients : "";
    $("#limits-cc").value = cur.max_cc != null ? cur.max_cc : "";
    $("#limits-modal-status").textContent = "";
    modal.dataset.lsaddr = addr;
    modal.classList.remove("hidden");
  }

  async function saveLimitsModal() {
    const modal = $("#limits-modal");
    const addr = modal && modal.dataset.lsaddr;
    if (!addr) return;
    const st = $("#limits-modal-status");
    const toRaw = $("#limits-to").value.trim();
    const ccRaw = $("#limits-cc").value.trim();
    const toN = parseInt(toRaw, 10);
    const ccN = ccRaw === "" ? 0 : parseInt(ccRaw, 10);
    if (!/^\d+$/.test(toRaw) || isNaN(toN) || toN < 1 || toN > 1000) {
      st.textContent = t("limits.err.recipients"); return;
    }
    if (ccRaw !== "" && (!/^\d+$/.test(ccRaw) || isNaN(ccN) || ccN < 0 || ccN > 1000)) {
      st.textContent = t("limits.err.cc"); return;
    }
    try {
      const d = await api("/api/account/limits", { method: "POST",
        body: JSON.stringify({ address: addr, max_recipients: toN, max_cc: ccN }), keepSession: true });
      limitsCache[addr] = d;
      document.dispatchEvent(new CustomEvent("accounts:refresh"));
      toast(t("board.saved"), "success");
      modal.classList.add("hidden");
    } catch (e) {
      const msg = String((e && e.message) || "");
      if (msg.indexOf("too many recipients") >= 0 || msg.indexOf("limit") >= 0) toast(msg, "error");
      else toast(t("common.error", { msg: msg }), "error");
    }
  }

  // ---- 0.3.2 概览重构：从属活动 B 案融合（boss 0924 认定）----


  // 从属表从管理-概览并入账户页：心跳胶囊（活动行按 boss 0924 口径摘除：「7日/均/常联」不再显示）



  // 融进账户表从属行与手机从属卡（不换表头、不加列）。10s 轮询宿主=


  // 账户页可见期；进页即拉（5s 防抖——boss 报单「进页晚显 10s」修，规格


  // alice/Devi 0924 定）；轮询就地更新只写胶囊槽位，行元素本体不动



  // （1046 语义沿袭，滑条零扰）。图不跟活帧（boss 定）：图侧留 overview.js。


  // ---- 0.3.7 whitelist (boss spec): per-account sender whitelist with
  // hierarchy bypass handled server-side; the panel manages the toggle and
  // the address list. Spec contract:
  //   GET/PUT /api/account/whitelist [+ ?address= for a subordinate, same
  //   convention as limits] - PUT body { whitelist_enabled, whitelist: [] }
  //   POST/DELETE /api/account/whitelist/<address> [+ ?address= owner]
  // Persistence is immediate per action; the toggle commits via PUT.
  var wlCache = {};
  function wlQuery(addr) {
    const sess = getSession();
    return addr && sess && String(addr).toLowerCase() === String(sess.address).toLowerCase() ? "" : "?address=" + encodeURIComponent(addr);
  }
  async function openWhitelistModal(addr) {
    const modal = $("#wl-modal");
    if (!modal) return;
    $("#wl-modal-title").textContent = t("wl.title") + " \u2014 " + addr;
    $("#wl-status").textContent = "";
    $("#wl-input").value = "";
    modal.dataset.wladdr = addr;
    modal.classList.remove("hidden");
    renderWlChips(addr, null, t("common.loading"));
    try {
      const d = await api("/api/account/whitelist" + wlQuery(addr), { keepSession: true });
      wlCache[addr] = { whitelist_enabled: !!(d && (d.whitelist_enabled || d.enabled)), whitelist: (d && (d.whitelist || d.addresses)) || [] };
    } catch (e) {
      wlCache[addr] = { whitelist_enabled: false, whitelist: [], err: String((e && e.message) || e) };
    }
    if (($("#wl-modal").dataset || {}).wladdr !== addr) return; // closed meanwhile
    const cur = wlCache[addr];
    $("#wl-enabled").checked = !!cur.whitelist_enabled;
    renderWlChips(addr, cur.whitelist, cur.err || "");
  }
  function renderWlChips(addr, list, note) {
    const holder = $("#wl-chips");
    if (!holder) return;
    holder.textContent = "";
    const cur = list || (wlCache[addr] || {}).whitelist || [];
    if (note) { const n = document.createElement("div"); n.className = "muted"; n.style.fontSize = "12px"; n.textContent = note; holder.appendChild(n); }
    if (!cur.length) {
      if (!note) { const e = document.createElement("div"); e.className = "muted"; e.style.fontSize = "12px"; e.setAttribute("data-i18n", "wl.empty"); e.textContent = t("wl.empty"); holder.appendChild(e); }
      return;
    }
    cur.forEach(function (a) {
      const chip = document.createElement("span");
      chip.className = "wl-chip";
      chip.textContent = a;
      const x = document.createElement("button");
      x.type = "button"; x.className = "wl-chip-x"; x.textContent = "\u00d7"; x.title = "remove";
      x.addEventListener("click", async function () {
        x.disabled = true;
        try {
          await api("/api/account/whitelist/" + encodeURIComponent(a) + wlQuery(addr), { method: "DELETE", keepSession: true });
          const c = wlCache[addr] = wlCache[addr] || { whitelist_enabled: false, whitelist: [] };
          c.whitelist = c.whitelist.filter(function (v) { return String(v).toLowerCase() !== String(a).toLowerCase(); });
          renderWlChips(addr, c.whitelist, "");
          wlFlash(t("wl.saved"));
        } catch (e) { wlFlash(String((e && e.message) || e), true); x.disabled = false; }
      });
      chip.appendChild(x);
      holder.appendChild(chip);
    });
  }
  function wlFlash(msg, isErr) {
    const st = $("#wl-status");
    if (!st) return;
    st.textContent = msg || "";
    st.style.color = isErr ? "#dc2626" : "";
  }
  async function wlToggleSave(addr, enabled) {
    const cur = wlCache[addr] = wlCache[addr] || { whitelist_enabled: false, whitelist: [] };
    const prev = cur.whitelist_enabled;
    cur.whitelist_enabled = enabled;
    try {
      await api("/api/account/whitelist" + wlQuery(addr), { method: "PUT", body: JSON.stringify({ whitelist_enabled: enabled, whitelist: cur.whitelist }), keepSession: true });
      wlFlash(t("wl.saved"));
    } catch (e) {
      cur.whitelist_enabled = prev; // rollback the switch on failure
      $("#wl-enabled").checked = prev;
      wlFlash(String((e && e.message) || e), true);
    }
  }
  document.addEventListener("click", function (ev) {
    const t2 = ev.target;
    if (!t2 || !t2.closest) return;
    const wlOpener = t2.closest("[data-wl]");
    if (wlOpener) { openWhitelistModal(wlOpener.dataset.wl); return; }
    if (t2.id === "btn-wl-close") { const m = $("#wl-modal"); if (m) m.classList.add("hidden"); return; }
    if (t2.id === "wl-modal") { const m = $("#wl-modal"); if (m) m.classList.add("hidden"); return; }
    if (t2.id === "btn-wl-add") {
      const addr = ($("#wl-modal").dataset || {}).wladdr;
      if (!addr) return;
      const inp = $("#wl-input");
      const v = (inp.value || "").trim().toLowerCase();
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(v)) { wlFlash(t("wl.err.addr"), true); return; }
      const cur = wlCache[addr] = wlCache[addr] || { whitelist_enabled: false, whitelist: [] };
      if (cur.whitelist.some(function (x) { return String(x).toLowerCase() === v; })) { wlFlash(t("wl.err.dup"), true); return; }
      wlFlash("");
      api("/api/account/whitelist/" + encodeURIComponent(v) + wlQuery(addr), { method: "POST", keepSession: true })
        .then(function () {
          cur.whitelist.push(v);
          inp.value = "";
          renderWlChips(addr, cur.whitelist, "");
          wlFlash(t("wl.saved"));
        })
        .catch(function (e) { wlFlash(String((e && e.message) || e), true); });
      return;
    }
    if (t2.id === "wl-enabled") {
      const addr = ($("#wl-modal").dataset || {}).wladdr;
      if (addr) wlToggleSave(addr, t2.checked);
    }
  });
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "Enter") return;
    const m = $("#wl-modal");
    if (!m || m.classList.contains("hidden")) return;
    if (ev.target && ev.target.id === "wl-input") { ev.preventDefault(); const b = $("#btn-wl-add"); if (b) b.click(); }
  });
  var HB_TTL_SEC = 60; // 3×20s 上报周期为过期线（boss 0923 定口径：前端刷 10s/心跳 20s/TTL 60s）


  var HB_POLL_SEC = 10; // T1=前端刷新间隔（轮询 POLL_MS 与此同源）


  var HB_GRAY = [0x9c, 0xa3, 0xaf]; // 渐变灰端 #9ca3af


  var HB_COLORS = { working: [0x16, 0xa3, 0x4a], waiting: [0x25, 0x63, 0xeb], compact: [0xb4, 0x53, 0x09], error: [0xdc, 0x26, 0x26], arming: [0x25, 0x63, 0xeb] };


  (function hbInjectCss() {


    var css = ".hb-pill{display:inline-block;margin-left:8px;padding:1px 8px;border-radius:999px;" +


      "font-size:11px;line-height:16px;font-weight:600;color:#fff;vertical-align:1px;white-space:nowrap;transition:background-color 1.2s linear}" +


      ".hb-working{background:#16a34a}.hb-waiting{background:#2563eb}.hb-compact{background:#b45309}" +


      ".hb-error{background:#dc2626}.hb-arming{background:#2563eb;animation:hbBreath 1.6s ease-in-out infinite}" +


      "@keyframes hbBreath{0%,100%{opacity:1}50%{opacity:.55}}" +


      ".hb-pill{transition:background-color .9s linear}";


    var st = document.createElement("style");


    st.textContent = css;


    document.head.appendChild(st);


  })();


  var HB_STATES = { working: 1, waiting: 1, compact: 1, error: 1, arming: 1 };


  // boss 0923 公式：f=max(t-t1-T1,0)/T3 ∈[0,1]，活跃色→灰实时渐变；f>=1 即隐（TTL）。


  function hbFreshRatio(at) {


    var f = (Date.now() / 1000 - at - HB_POLL_SEC) / HB_TTL_SEC;


    if (f < 0) f = 0; else if (f > 1) f = 1;


    return Math.round(f * 10) / 10; // decile steps


  }


  function hbFadeColor(key, f) {


    var c = HB_COLORS[key];


    if (!c || f <= 0) return ""; // f=0 交给状态类本色


    var r = Math.round(c[0] + (HB_GRAY[0] - c[0]) * f);


    var g = Math.round(c[1] + (HB_GRAY[1] - c[1]) * f);


    var b2 = Math.round(c[2] + (HB_GRAY[2] - c[2]) * f);


    return "rgb(" + r + "," + g + "," + b2 + ")";


  }


  function hbPillKey(s) {


    var hst = s && s.worker_state;


    if (!hst) return "";


    var at = +s.worker_seen_at || 0;


    if (at > 1e12) at = at / 1000; // 毫秒时间戳兜底


    if (!at || Date.now() / 1000 - at >= HB_POLL_SEC + HB_TTL_SEC) return ""; // TTL 过期即隐（T1+T3）


    var key = hst.toLowerCase();


    if (!HB_STATES[key]) return ""; // 未知状态=不显（前瞻兼容 worker 新态）


    return key;


  }


  function hbPillHtml(s) {


    var key = hbPillKey(s);


    if (!key) return "";


    return '<span class="hb-pill hb-' + key + '" data-hb-key="' + key + '" title="' + esc(t("hb." + key + "Tip")) + '">' + esc(t("hb." + key)) + "</span>";


  }


  // 实时走查（1s，仅改样式/摘除，不动结构）。0.3.2 修（boss 报单双根因之二）：


  // 走查加宿主视图域门——账户页不可见时不摘不涂，离页不再偷摘过期胶囊；


  // 回页由进页即拉按服务器数据整槽重渲，状态以服务器为准。


  (function hbFadeLoop() {


    setInterval(function () {


      if (document.hidden) return;


      var panel = document.getElementById("tab-accounts");


      if (!panel || panel.offsetParent === null) return;


      var pills = document.querySelectorAll(".hb-pill");


      for (var i = 0; i < pills.length; i++) {


        var el = pills[i];


        if (!applyActivity._hb) return;

      var host = el.closest("[data-act-acct]");

      var s2 = host ? (applyActivity._hb[String(host.getAttribute("data-act-acct")).toLowerCase()] || null) : null;

      var at = 0;

      if (s2) { at = +s2.worker_seen_at || 0; if (at > 1e12) at = at / 1000; }


        var f = s2 ? hbFreshRatio(at) : 1;


        if (!s2 || f >= 1) { el.remove(); continue; }


        var col = hbFadeColor(el.getAttribute("data-hb-key"), f);


        if (el.__hbcol !== col) { el.__hbcol = col; el.style.backgroundColor = col; }





      }


    }, 1000);


  })();


  // 活动数据＋就地应用：从属行/手机从属卡内的两个槽位（胶囊槽/活动行槽）


  // 整槽重写，行元素与操作按钮不动——滚动/悬停零感（1046 语义）。


  var actData = null, actLastPull = 0, actPulling = false;


  // reorderAccountsDom moves EXISTING rows into the unified latest_at order
  // on both surfaces instead of the full loadAccounts rewrite (boss 09-29
  // round two, from the clean v0.3.4.1 base: PC and the phone must share one
  // list-refresh logic, and the per-flip rebuild re-created every row on
  // nearly every poll - on PC that read as constant page flicker). One
  // contract for both surfaces: collect the rendered rows per address,
  // VERIFY the move is total - every wanted address has its row(s), every
  // rendered data row is wanted, no unknown children, pinned furniture
  // present, no duplicates - and only then append in want order. Any
  // mismatch returns false and the caller falls back to the debounced
  // rebuild, so a partial move can never strand rows in a detached fragment
  // (the v0.3.4.2 empty-list bug class is structurally impossible here).
  // Moved nodes keep avatar bitmaps, listeners and hover/scroll state.
  function reorderAccountsDom(want) {
    // boss 09-29 (marquee jumps on every order flip): re-parenting a node
    // restarts its CSS animations - the over-wide signature marquee visibly
    // snapped back to its start each move. Snapshot the running clocks and
    // restore them right after the re-insertion; same-task restore means no
    // visible restart.
    var mqClocks = [];
    $$(".sig-cell .sig-track, .mq .sig-track").forEach(function (t) {
      t.getAnimations().forEach(function (a) { mqClocks.push([t, a.animationName, a.currentTime]); });
    });
    var wantList = want.map(function (a) { return String(a).toLowerCase(); });
    var wantSet = {};
    wantList.forEach(function (a) { wantSet[a] = 1; });
    var moved = false;
    // PC: one tbody; per-address pair = main row (subrow-pc/ct-row) +
    // full-width line3 row; the register card stays last.
    $$("#tab-accounts tbody").forEach(function (tb) {
      if (!tb.querySelector(".subrow-pc, .ct-row")) return;
      var main = {}, line3 = {}, unknown = 0, reg = null;
      Array.prototype.forEach.call(tb.children, function (tr) {
        if (tr.nodeType !== 1) { unknown++; return; }
        var k = String(tr.getAttribute("data-act-acct") || "").toLowerCase();
        if (tr.classList.contains("agentreg-row")) { reg = tr; return; }
        if (tr.classList.contains("line3-row")) {
          if (k && !line3[k]) line3[k] = tr; else unknown++;
        } else if (k && (tr.classList.contains("subrow-pc") || tr.classList.contains("ct-row"))) {
          if (main[k]) unknown++; else main[k] = tr;
        } else unknown++;
      });
      if (!reg || unknown) return;
      var seq = [];
      var ok = wantList.every(function (a) {
        if (main[a] && line3[a]) { seq.push(a); return true; }
        return false;
      });
      Object.keys(main).forEach(function (a) { if (!wantSet[a]) ok = false; });
      Object.keys(line3).forEach(function (a) { if (!wantSet[a]) ok = false; });
      if (!ok) return;
      var cur = [...tb.querySelectorAll(".subrow-pc, .ct-row")].map(function (r) {
        return String(r.getAttribute("data-act-acct")).toLowerCase();
      });
      if (cur.join("|") === seq.join("|")) { moved = true; return; }
      var frag = document.createDocumentFragment();
      seq.forEach(function (a) { frag.appendChild(main[a]); frag.appendChild(line3[a]); });
      frag.appendChild(reg);
      tb.appendChild(frag);
      moved = true;
    });
    // Phone: one card per address in #acc-m-contacts; the pinned register
    // row stays first (data groups always append after it).
    var cb = document.getElementById("acc-m-contacts");
    if (cb && cb.querySelector(".im3-row[data-claddr]")) {
      var rows = {}, unknownM = 0, regM = null;
      Array.prototype.forEach.call(cb.children, function (el) {
        if (el.nodeType !== 1) { unknownM++; return; }
        if (el.hasAttribute("data-reg")) { regM = el; return; }
        var k = String(el.getAttribute("data-claddr") || "").toLowerCase();
        if (el.classList.contains("im3-row") && k && !rows[k]) rows[k] = el;
        else unknownM++;
      });
      if (regM && !unknownM) {
        var seqM = [];
        var okM = wantList.every(function (a) {
          if (rows[a]) { seqM.push(a); return true; }
          return false;
        });
        Object.keys(rows).forEach(function (a) { if (!wantSet[a]) okM = false; });
        if (okM) {
          var curM = [...cb.querySelectorAll(".im3-row[data-claddr]")].map(function (r) {
            return String(r.getAttribute("data-claddr")).toLowerCase();
          });
          if (curM.join("|") !== seqM.join("|")) {
            var fragM = document.createDocumentFragment();
            seqM.forEach(function (a) { fragM.appendChild(rows[a]); });
            cb.appendChild(fragM);
            if (cb.firstElementChild !== regM) cb.insertBefore(regM, cb.firstChild);
          }
          moved = true;
        }
      }
    }
    mqClocks.forEach(function (c) {
      c[0].getAnimations().forEach(function (a) { if (a.animationName === c[1]) a.currentTime = c[2]; });
    });
    return moved;
  }
  // insertMissingAccountsDom is the redraw-on-demand path (boss 0930:
  // 按需重画 - "why redraw the whole row when the heartbeat only concerns
  // the pill"). A letter from a BRAND-NEW counterparty used to cost a full
  // loadAccounts rebuild: reorderAccountsDom cannot place what the DOM
  // lacks, so the fallback rebuilt every row just to add one - open states
  // and marquee clocks went through the snapshot/restore band-aids and the
  // page flickered once per addition. This routine adds ONLY the missing
  // rows: present rows keep their nodes (identity, listeners, avatar
  // bitmaps, running marquee clocks) and the new rows come from the SAME
  // templates the full rebuild uses (ctPcRowsHtml / accRowHtml), so the
  // two paths cannot diverge.
  // One contract on both surfaces, verify-first mutate-second (the same
  // shape as reorderAccountsDom - a partial insert is structurally
  // impossible): no removals (every rendered row must be wanted),
  // furniture intact (register card PC-last / phone-first, no unknown
  // children, no duplicates), the two surfaces must agree on the missing
  // set, and every missing address must be a plain CONTACT with an
  // activity edge (new subordinates belong to the register flow; edgeless
  // rows cannot be ordered). Any mismatch returns false and the caller
  // keeps its debounced full-rebuild fallback.
  function insertMissingAccountsDom(want) {
    var ctx = acctCtx;
    if (!ctx) return false;
    var wantList = want.map(function (a) { return String(a).toLowerCase(); });
    var wantSet = {};
    wantList.forEach(function (a) { wantSet[a] = 1; });
    // fresh activity view: ordering keys + the new rows' latest line
    var actByAddr = {};
    ((actData && actData.subs) || []).forEach(function (x) { actByAddr[String(x.address).toLowerCase()] = x; });
    ((actData && actData.contacts) || []).forEach(function (x) { var k = String(x.address).toLowerCase(); if (!actByAddr[k]) actByAddr[k] = x; });
    var entryAt = function (addr) { var x = actByAddr[String(addr).toLowerCase()]; return (+(x && x.latest_at)) || 0; };
    var tbPc = null;
    $$("#tab-accounts tbody").forEach(function (t) { if (!tbPc && t.querySelector(".subrow-pc, .ct-row")) tbPc = t; });
    var cb = document.getElementById("acc-m-contacts");
    if (!tbPc || !cb) return false;
    // ---- verify PC: furniture + no extras + collect the missing set ----
    var main = {}, line3 = {}, unknown = 0, reg = null;
    Array.prototype.forEach.call(tbPc.children, function (tr) {
      if (tr.nodeType !== 1) { unknown++; return; }
      var k = String(tr.getAttribute("data-act-acct") || "").toLowerCase();
      if (tr.classList.contains("agentreg-row")) { reg = tr; return; }
      if (tr.classList.contains("line3-row")) {
        if (k && !line3[k]) line3[k] = tr; else unknown++;
      } else if (k && (tr.classList.contains("subrow-pc") || tr.classList.contains("ct-row"))) {
        if (main[k]) unknown++; else main[k] = tr;
      } else unknown++;
    });
    if (!reg || unknown) return false;
    var extra = Object.keys(main).concat(Object.keys(line3)).filter(function (a) { return !wantSet[a]; });
    if (extra.length) return false;
    var missing = wantList.filter(function (a) { return !main[a] || !line3[a]; });
    if (!missing.length) return false; // nothing to add: order alone is reorderAccountsDom's job
    for (var mi = 0; mi < missing.length; mi++) {
      if (ctx.subsSet[missing[mi]]) return false; // new subordinate: the register flow owns its reload
      if (!actByAddr[missing[mi]]) return false;  // no activity edge - cannot order it, let the rebuild decide
    }
    // ---- verify phone: furniture + the SAME missing set on both surfaces ----
    if (!cb.querySelector(".im3-row[data-claddr]")) return false;
    var rowsM = {}, unknownM = 0, regM = null;
    Array.prototype.forEach.call(cb.children, function (el) {
      if (el.nodeType !== 1) { unknownM++; return; }
      if (el.hasAttribute("data-reg")) { regM = el; return; }
      var k = String(el.getAttribute("data-claddr") || "").toLowerCase();
      if (el.classList.contains("im3-row") && k && !rowsM[k]) rowsM[k] = el;
      else unknownM++;
    });
    if (!regM || unknownM) return false;
    var extraM = Object.keys(rowsM).filter(function (a) { return !wantSet[a]; });
    if (extraM.length) return false;
    var missingM = wantList.filter(function (a) { return !rowsM[a]; });
    if (missingM.join("|") !== missing.join("|")) return false; // surfaces disagree - rebuild decides
    // ---- build everything before mutating (all-or-nothing) ----
    var builds = missing.map(function (c) {
      var pcC = ctPcRowsHtml(c, ctx.listedSet, ctx.listedSig, actByAddr);
      return {
        addr: c, at: entryAt(c),
        main: pcC.main, line3: pcC.line3,
        card: accRowHtml({ addr: c, badge: pcC.badge, sig: ctx.listedSig[c] || "", isSub: false, sub: actByAddr[String(c).toLowerCase()] || null })
      };
    });
    // moving the present rows re-parents them - CSS animations restart on
    // re-insertion, so snapshot the running clocks and restore after
    // (same-task restore, no visible restart; same shape as the mover).
    var clocks = [];
    $$(".sig-cell .sig-track, .mq .sig-track, .im3-row[data-claddr] .im3-addr-in").forEach(function (t) {
      t.getAnimations().forEach(function (a) { clocks.push([t, a.animationName, a.currentTime]); });
    });
    // ---- mutate PC: present rows into want order, reg stays last ----
    var presentSeq = wantList.filter(function (a) { return main[a] && line3[a]; });
    var curSeq = [...tbPc.querySelectorAll(".subrow-pc, .ct-row")].map(function (r) { return String(r.getAttribute("data-act-acct")).toLowerCase(); });
    if (curSeq.join("|") !== presentSeq.join("|")) { // already in order: skip the re-parent, animations keep running untouched
      var frag = document.createDocumentFragment();
      presentSeq.forEach(function (a) { frag.appendChild(main[a]); frag.appendChild(line3[a]); });
      frag.appendChild(reg);
      tbPc.appendChild(frag);
    }
    // ---- mutate phone: present cards into want order, pinned reg first ----
    var presentM = wantList.filter(function (a) { return rowsM[a]; });
    var curM = [...cb.querySelectorAll(".im3-row[data-claddr]")].map(function (r) { return String(r.getAttribute("data-claddr")).toLowerCase(); });
    if (curM.join("|") !== presentM.join("|")) {
      var fragM = document.createDocumentFragment();
      presentM.forEach(function (a) { fragM.appendChild(rowsM[a]); });
      cb.appendChild(fragM);
      if (cb.firstElementChild !== regM) cb.insertBefore(regM, cb.firstChild);
    }
    // ---- insert the missing rows at their sorted position ----
    // Stable-sort semantics: walk to the first row with a strictly smaller
    // key, so ties keep the new row AFTER existing equals; previously
    // inserted builds carry equal-or-larger keys and are skipped the same
    // way.
    builds.forEach(function (b) {
      var ref = null;
      Array.prototype.forEach.call(tbPc.children, function (tr) {
        if (ref || tr.nodeType !== 1) return;
        if (!(tr.classList.contains("subrow-pc") || tr.classList.contains("ct-row"))) return;
        if (entryAt(String(tr.getAttribute("data-act-acct") || "").toLowerCase()) < b.at) ref = tr;
      });
      var tpl = document.createElement("template");
      tpl.innerHTML = b.main + b.line3;
      var pair = Array.prototype.slice.call(tpl.content.children);
      if (ref) { tbPc.insertBefore(pair[0], ref); tbPc.insertBefore(pair[1], ref); }
      else { tbPc.insertBefore(pair[0], reg); tbPc.insertBefore(pair[1], reg); }
      $$("[data-compose]", pair[0]).forEach(function (btn) {
        btn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: btn.dataset.compose } })); });
      });
      avHydrate(pair[0]);
      avRemoteHydrate(pair[0]); // 0021: registry-backed real avatars
      var tplM = document.createElement("template");
      tplM.innerHTML = b.card;
      var card = tplM.content.firstElementChild;
      var refM = null;
      Array.prototype.forEach.call(cb.children, function (el) {
        if (refM || el.nodeType !== 1) return;
        if (!el.classList.contains("im3-row") || !el.hasAttribute("data-claddr")) return;
        if (entryAt(String(el.getAttribute("data-claddr") || "").toLowerCase()) < b.at) refM = el;
      });
      if (refM) cb.insertBefore(card, refM); else cb.appendChild(card);
      wireIm3Row(card, cb);
      $$("[data-compose], [data-remove-sub]", card).forEach(function (btn) {
        if (btn.dataset.compose) btn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: btn.dataset.compose } })); });
        if (btn.dataset.removeSub) btn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("subs:remove", { detail: { address: btn.dataset.removeSub, role: "superior" } })); });
      });
      avHydrate(card);
      avRemoteHydrate(card);
      im3MarqueeScan(card); // the new card's own address marquee
    });
    maybeMarqueeSigs(); // engage the new PC address/sig tracks (idempotent)
    clocks.forEach(function (c) {
      c[0].getAnimations().forEach(function (a) { if (a.animationName === c[1]) a.currentTime = c[2]; });
    });
    return true;
  }
  function applyActivity() {


    var byAddr = {};


    ((actData && actData.subs) || []).forEach(function (s) {


      var kb = String(s.address || "").toLowerCase();


      if (kb.indexOf("@") < 0) return; // bare legacy name: cannot match a row


      byAddr[kb] = s;


    });


    avSyncAvatarsFromActivity((actData && actData.subs) || []); // A-case: avatar spot-hydration on the same poll

    avPullTick(); // 1062b robustness net: conditional revalidation of every on-page avatar, independent of the poll payload


    var unreadBy = (actData && actData.unreadBySender) || {};


    // Contact rows (bug fix 09-29): correspondence-driven latest for


    // non-subordinate rows; a declared sub entry wins the slot.


    ((actData && actData.contacts) || []).forEach(function (c) {


      var k = String(c.address).toLowerCase();


      if (k.indexOf("@") < 0) return; // bare legacy name: cannot match a row


      if (!byAddr[k]) byAddr[k] = c;


    });


    applyActivity._hb = byAddr; // live heartbeat data for the 1s fade loop (no per-poll pill swaps)


    $$("[data-act-acct]").forEach(function (el) {


      var s = byAddr[String(el.getAttribute("data-act-acct")).toLowerCase()];


      var pill = el.querySelector('[data-act-slot="pill"]');


      // 1046 纪律（boss 0924 口径：刷新逻辑与原管理-概览从属列表对应）：



      // 先比对、内容无变化不写 DOM——轮询对滚动零扰。



      if (pill) {



        // boss 09-29: the 1s fade loop writes the pill's inline color and
        // innerHTML serializes it - compare the pill SIGNATURE (state class
        // + label) instead of the raw html, or every poll reads the colored
        // pill as changed and re-creates it (the per-poll swap = snap-back).



        var key2 = s ? hbPillKey(s) : "";



        var cur2 = pill.firstChild;



        var sig2 = cur2 && cur2.nodeType === 1 ? cur2.className + "\u0001" + cur2.textContent : "";



        if (sig2 !== (key2 ? "hb-pill hb-" + key2 + "\u0001" + t("hb." + key2) : "")) pill.innerHTML = hbPillHtml(s);



      }




      // 0.3.3-C: the latest-message line rides the same in-place update --
      // the poll delivers latest_subject/latest_at after first render.
      // 0.3.4 item 1: the avatar unread dot flips on server truth -
      // any read path clears it within the next poll (same 5s cycle).
      var av = el.querySelector(".im3-av-wrap");
      if (av) {
        var has = !!unreadBy[String(el.getAttribute("data-act-acct")).toLowerCase()];
        if (av.classList.contains("has-unread") !== has) av.classList.toggle("has-unread", has);
      }
      // 0.3.5 (boss staging note): the PC table shape carries the latest
      // line in a sibling .line3-row as .pc-line3 - the .im3-line3-only
      // query patched phone cards and never the PC row, so after a send
      // the order topped but the message line stayed stale until a
      // rebuild. One selector covers both shapes.
      var line3 = el.querySelector(".im3-line3, .pc-line3");
      if (line3) {
        var lh = accLatestHtml(s);
        if (line3.innerHTML !== lh) line3.innerHTML = lh;
      }
    });


    // boss 09-29: if interaction order changed, re-render the list - line3
    // patches alone cannot reorder. The comparison covers the UNIFIED page
    // order (subordinates AND contacts, latest_at desc); activity-bearing
    // addresses only, as a sequence, so rows without data cannot loop it.
    // Debounced; converges because the re-render's own applyActivity sees
    // the new order as already applied.
    var box = $("#acc-m-contacts");
    if (box && !applyActivity._reloading) {
      var by = {};
      ((actData && actData.subs) || []).forEach(function (s) { var ks = String(s.address || "").toLowerCase(); if (ks.indexOf("@") >= 0) by[ks] = s; });
      ((actData && actData.contacts) || []).forEach(function (c) {
        var kk = String(c.address || "").toLowerCase();
        if (kk.indexOf("@") < 0) return; // bare legacy name
        if (!by[kk]) by[kk] = c;
      });
      var want = Object.keys(by).sort(function (a, b) {
        return (+by[b].latest_at || 0) - (+by[a].latest_at || 0);
      });
      var wantSet = {};
      want.forEach(function (a) { wantSet[a] = 1; });
      var have = [...box.querySelectorAll(".im3-row")]
        .map(function (r) { return String(r.getAttribute("data-claddr") || "").toLowerCase(); })
        .filter(function (a) { return a && wantSet[a]; });
      // boss 0930 按需重画 round: `have` is FILTERED to wantSet members, so
      // stale rows (a contact that vanished from the activity view) were
      // invisible to this check - a removed counterparty's row sat in the
      // DOM forever because same never went false. Count BOTH surfaces'
      // rendered data rows against want: extras must trigger the decision
      // path too (reorder refuses them, the inserter refuses them, the
      // rebuild fallback finally cleans up).
      var same = want.length === have.length && want.every(function (a, i) { return a === have[i]; })
        && box.querySelectorAll(".im3-row[data-claddr]").length === want.length
        && $$("#tab-accounts tbody tr.subrow-pc, #accounts-table tbody tr.ct-row").length === want.length;
      // 0024 batch guard: the reorder reload has no exit once it starts
      // against an empty actData (rows exist, panel later hidden, pulls
      // visibility-gated) - the 150ms loop rebuilt the whole list ~27x/5s
      // with no convergence possible. actData empty = nothing to reorder
      // against; the next successful pull re-runs applyActivity anyway.
      if (!same && have.length && actData) {
        // boss 09-29 round two: an order flip MOVES the existing rows via the
        // one shared routine (PC + phone, logic identical); the debounced
        // rebuild below is only the fallback for rows the move cannot place
        // (brand-new contact, stale row, state row) - never the flicker path.
        // boss 0930 按需重画: the missing-row case - the one shape the mover
        // cannot place - now takes the incremental inserter first; the
        // debounced rebuild stays as the fallback for every shape the
        // inserter cannot verify.
        if (!reorderAccountsDom(want) && !insertMissingAccountsDom(want)) {
          applyActivity._reloading = true;
          setTimeout(function () { applyActivity._reloading = false; loadAccounts(); }, 150);
        }
      }
    }


    // boss 1002 production report (admin PC): the admin Accounts table is a
    // DIFFERENT renderer (/admin/accounts plain rows - no .subrow-pc/.ct-row
    // markers by design), so this heal misreads it as a dead build and
    // rebuilds it every pass - tbody is cleared BEFORE the refetch await,
    // the table collapses, the page scroll clamps to top (the ~1s
    // scroll-to-top boss saw). The heal and the reorder above serve the
    // REGULAR face only; an admin session never has marker rows.
    var sessAcc = getSession();
    if (sessAcc && sessAcc.is_admin) {
      // admin face: skip the regular-face heal entirely
    } else {
    // boss 09-29 gate leg 1 (dead-build self-heal, the PC half of the
    // entry-flake symmetry): activity data exists but the PC table has no
    // data rows (a mid-chain fetch death left the build unfinished) - one
    // debounced rebuild heals it. Empty accounts keep dataAny false, so the
    // legitimate empty state never loops.
    if (!applyActivity._reloading && actData && (((actData.subs || []).length + (actData.contacts || []).length) > 0)) {
      var tbPc = null;
      $$("#tab-accounts tbody").forEach(function (t) { if (!tbPc && t.querySelector(".subrow-pc, .ct-row")) tbPc = t; });
      if (!tbPc) {
        applyActivity._reloading = true;
        setTimeout(function () { applyActivity._reloading = false; loadAccounts(); }, 400);
      }
    }
    } // end regular-face gate


    var sum = $("#acc-act-sum");


    if (sum) {


      var subs = (actData && actData.subs) || [];


      if (subs.length) {


        var live = 0, in7 = 0, out7 = 0;


        var now = Date.now() / 1000;


        var strongH = (userPrefs && userPrefs.livenessStrongHours) || 24;


        var weakH = (userPrefs && userPrefs.livenessWeakHours) || 48;


        subs.forEach(function (s) {


          var traffic = Math.max(s.last_in_at || 0, s.last_out_at || 0);


          var read = s.last_read_at || 0;


          if ((traffic && now - traffic <= strongH * 3600) || (read && now - read <= weakH * 3600)) live++;


          in7 += s.count_in_7d || 0; out7 += s.count_out_7d || 0;


        });


        sum.textContent = t("mgmt.sum", { n: subs.length, a: live, i: in7, o: out7 });


        sum.hidden = false;


      } else sum.hidden = true;


    }


  }


  function accountsPanelVisible() {


    var p = document.getElementById("tab-accounts");


    return !!p && p.offsetParent !== null;


  }


  // boss 1001 dot audit #7: local dot edits (same-tick clear/bump) are
  // journaled with a sequence number; a pull that started before the
  // edit replays the journal onto its fresh payload, so an in-flight
  // response can neither resurrect a cleared dot nor drop a bump.
  var actMutSeq = 0;
  var actMutLog = [];
  function actMutNote(type, addr) {
    actMutSeq++;
    actMutLog.push({ seq: actMutSeq, type: type, addr: addr });
    if (actMutLog.length > 64) actMutLog.shift();
  }

  async function pullActivity() {


    if (actPulling || document.hidden || !accountsPanelVisible()) return;


    actPulling = true;
    var seq0 = actMutSeq;


    try {


      var d = await api("/api/mgmt/subs-overview?days=7", { keepSession: true });


      // boss bug 09-29: contact rows ride the same poll - correspondence-driven


      // latest data for non-subordinate counterparties.


      await api("/api/mgmt/contacts-latest", { keepSession: true }).then(function (dc) { d.contacts = (dc && dc.contacts) || []; }, function () { d.contacts = []; });


      await api("/api/mgmt/unread-by-sender", { keepSession: true }).then(function (du) { d.unreadBySender = (du && du.by_sender) || {}; }, function () { d.unreadBySender = {}; });


      if (seq0 !== actMutSeq) {
        var ubf = d.unreadBySender = d.unreadBySender || {};
        actMutLog.forEach(function (e) {
          if (e.seq <= seq0) return;
          if (e.type === "del") delete ubf[e.addr];
          else if (e.type === "bump") ubf[e.addr] = (ubf[e.addr] || 0) + 1;
          else if (e.type === "delall") Object.keys(ubf).forEach(function (k) { delete ubf[k]; });
        });
      }
      actData = d;


      actLastPull = Date.now();


      applyActivity();


    } catch (_) { /* 失败静默（权限/网络）——活动槽保持空态 */ }


    actPulling = false;


  }


  function activityEntered() {


    // 进页即拉（5s 防抖）：进账户页 ≤一个网络往返内胶囊/活动行可见——


    // boss 报单「进页晚显约 10s」修复的一半；另一半是走查视图域门。


    if (Date.now() - actLastPull > 5000) pullActivity();


  }


  (function activityPollLoop() {


    var POLL_MS = HB_POLL_SEC * 1000; // T1 同源（boss 0923 定 10s）；测试/调优可覆盖（下限 5s）


    try {


      var o = parseInt(localStorage.getItem("ovw_subs_poll_ms") || "0", 10);


      if (o >= 5000) POLL_MS = o;


    } catch (_) {}


    // boss 10-01: RETIRED - the activity pull is chained to the badge poll (single master clock); only the visibility-return refresh keeps its own trigger


    document.addEventListener("visibilitychange", function () {


      if (!document.hidden && accountsPanelVisible() && Date.now() - actLastPull > 10000) pullActivity(); // 回窗即拉（防抖 10s）


    });


  })();


  document.addEventListener("compose:sent", function (ev) {


    actLastPull = 0; // boss rc2: a just-sent mail must reorder the list at once


    // boss 09-29 local short path: the server pull is three SERIAL round-
    // trips, which read as a ~1s lag before the accounts order corrected
    // after a send. Bump the recipient rows from local state and re-apply
    // now - zero network; the next pull confirms with server truth.
    var det = ev.detail || {};
    var now = Math.floor(Date.now() / 1000);
    var subj = String(det.subject || "");
    var d = (actData = actData || {});
    var rows = (d.subs = d.subs || []).concat(d.contacts = d.contacts || []);
    String(det.to || "").split(",").forEach(function (raw) {
      var addr = String(raw).trim().toLowerCase();
      if (!addr) return;
      rows.forEach(function (s) {
        if (String(s.address).toLowerCase() !== addr) return;
        s.latest_at = now;
        if (subj) s.latest_subject = subj;
      });
    });
    applyActivity();
  });





  function renderPrefsOwnCard(ownSig, ownVisible) {
    // 0.3.2 boss 认定四：自身卡迁偏好页，双端同款手机卡样式（ct-card 语法）；
    // 「My address」显示地址卡随迁撤销（boss：不留「我的地址 XXX」）。
    const sess = getSession();
    const el = $("#pown-card");
    if (!sess || !el) return;
    // 0.3.3 头像：卡首头像 + 相机角标，点头像/「更换头像」钮开同一弹层
    // （PC/手机同一套，0.3.3 boss 整改二；入口不落账户页——账户页纯列表）。
    el.innerHTML =
      '<div class="ct-card av-owncard">' +
      '<div class="av-ownside"><button type="button" class="av-ownav" id="btn-avatar-open" title="' + t("prof.avatarChange") + '">' +
      '<span class="av-ownslot" id="own-avatar-slot"></span>' +
      '<span class="av-cam"><svg viewBox="0 0 24 24" width="10" height="10" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 8h3.2L9.4 5.4h5.2L16.8 8H20v11H4z"/><circle cx="12" cy="13" r="3.4"/></svg></span>' +
      "</button></div>" +
      '<div class="ct-line">' + (ownVisible ? '<span class="badge-listed">listed</span>' : "") +
      '<div class="ct-addr"><strong>' + esc(sess.address) + "</strong></div>" +
      '<span class="badge-listed">you</span></div>' +
      (ownSig ? '<div class="ct-sig">' + esc(ownSig) + "</div>" : "") +
      '<div class="ct-foot"><button type="button" class="row-action pill-btn av-entry" id="btn-avatar-open2">' + t("prof.avatarChange") + '</button><button class="row-action pill-btn" id="btn-change-pw-p">' + t("act.changePw") + '</button><button class="row-action pill-btn" data-limits="' + esc(sess.address) + '">' + t("limits.open") + '</button><button class="row-action pill-btn" data-wl="' + esc(sess.address) + '">' + t("wl.title") + "</button></div>" +
      "</div>";
    const pw = $("#btn-change-pw-p");
    if (pw) pw.addEventListener("click", openChangePassword);
    const av1 = $("#btn-avatar-open"), av2 = $("#btn-avatar-open2");
    if (av1) av1.addEventListener("click", openAvatarModal);
    if (av2) av2.addEventListener("click", openAvatarModal);
    ensureOwnAvatarHash();
  }

  // ---- 0.3.3 avatar upload (own card; one modal, PC and phones) ----
  // Wiring follows the team-reviewed 0006 approach: the auth wall lives in
  // the Authorization header, so a real avatar loads via fetch->objectURL —
  // a plain <img> can never authenticate. Server hard limits (jpeg/png,
  // longest edge <= 512, <= 100KB) are mirrored client-side; the canvas
  // scales before upload so the server 413 stays a last resort.
  var AV_MAX_EDGE = 512, AV_MAX_BYTES = 100 * 1024;
  var ownAvatarHashDone = false;

  // Shared blob loader (own card + accounts rows + modal preview): one
  // objectURL per addr|hash for the whole session. Re-renders reuse the
  // registry instead of refetching, so the URL count stays bounded by the
  // set of distinct avatars seen — no per-render revoke bookkeeping.
  // pub=true serves directory-visible addresses via the public endpoint
  // (D1 gate); both endpoints are immutable+1y cached, hence ?v=hash.
  var avBlobRegistry = {}, avBlobInflight = {};
  // boss 1002 速修: pool slots (memory or localStorage mirror) may
  // carry blob: urls from a previous page session - dead by definition.
  // Prune those boxes (empty + drop the done flag) so avRemoteHydrate
  // refills them; same-session urls are alive and stay untouched.
  window.__avPruneForeignBlobs = function (root) {
    var live = [];
    for (var k in avBlobRegistry) live.push(avBlobRegistry[k]);
    $$("img[src^='blob:']", root).forEach(function (im) {
      if (live.indexOf(im.getAttribute("src")) >= 0) return;
      var box = im.closest("[data-avremote]") || im.parentElement;
      if (box) { while (box.firstChild) box.removeChild(box.firstChild); box.removeAttribute("data-avdone"); }
    });
  };
  function avatarObjectURL(addr, hash, pub) {
    const key = String(addr).toLowerCase() + "|" + hash;
    if (avBlobRegistry[key]) return Promise.resolve(avBlobRegistry[key]);
    if (avBlobInflight[key]) return avBlobInflight[key];
    const url = pub
      ? "/api/public/avatar?address=" + encodeURIComponent(addr) + "&v=" + encodeURIComponent(hash)
      : "/api/avatar/" + encodeURIComponent(addr) + "?v=" + encodeURIComponent(hash);
    avBlobInflight[key] = fetch(url, pub ? {} : { headers: { Authorization: basicAuth() } })
      .then(function (res) { if (!res.ok) throw new Error(String(res.status)); return res.blob(); })
      .then(function (b) { return avBlobRegistry[key] = URL.createObjectURL(b); })
      .catch(function (e) { delete avBlobInflight[key]; throw e; });
    return avBlobInflight[key];
  }

  function avFallback(addr) {
    return '<span class="cl-av-img cl-identicon">' + esc((addr[0] || "?").toUpperCase()) + "</span>";
  }

  // Graph faces (overview) draw the same robots as everything else: sync
  // data-URI base. (0.3.5 graph shape, boss 口径: 盒子里有个圆头像 - the box
  // face bakes the robot or the real avatar INSIDE the node SVG.)
  window.__avDataUri = function (addr) {
    var a = String(addr || "").toLowerCase();
    return "data:image/svg+xml;utf8," + encodeURIComponent(avRobotSvg(a, avSha256(a)));
  };
  // Graph faces embed avatars as DATA URIs: a data:-loaded SVG may not
  // reference blob:/http: resources, so hand back bytes read as data URL.
  // hash (when known) cache-busts the fetch so an avatar change lands.
  window.__avAvatarDataUri = function (addr, hash, cb) {
    var q = hash ? "?v=" + encodeURIComponent(hash) : "";
    fetch("/api/avatar/" + encodeURIComponent(String(addr || "")) + q, { headers: { Authorization: basicAuth() } })
      .then(function (r) { if (!r.ok) throw new Error(String(r.status)); return r.blob(); })
      .then(function (b) {
        var fr = new FileReader();
        fr.onload = function () { cb(fr.result); };
        fr.onerror = function () { cb(null); };
        fr.readAsDataURL(b);
      }).catch(function () { cb(null); });
  };

  function ensureOwnAvatarHash() {
    const sess = getSession();
    if (!sess || ownAvatarHashDone) { renderOwnAvatar(); return; }
    ownAvatarHashDone = true;
    api("/api/account/info?query=self", { keepSession: true })
      .then(function (info) {
        window.__avatarHashes = window.__avatarHashes || {};
        if (info && info.avatar_hash) {
          window.__avatarHashes[String(info.address || sess.address).toLowerCase()] = info.avatar_hash;
        }
      })
      .catch(function () { /* placeholder stays */ })
      .then(renderOwnAvatar);
  }

  function renderOwnAvatar() {
    const sess = getSession();
    const slot = document.getElementById("own-avatar-slot");
    if (!sess || !slot) return;
    const hash = (window.__avatarHashes || {})[sess.address.toLowerCase()] || "";
    if (!hash) { slot.innerHTML = avFallback(sess.address); return; }
    avatarObjectURL(sess.address, hash).then(function (url) {
      const live = document.getElementById("own-avatar-slot"); // may have re-rendered meanwhile
      if (live) live.innerHTML = '<img alt="" src="' + url + '">';
    }).catch(function () {
      const live = document.getElementById("own-avatar-slot");
      if (live) live.innerHTML = avFallback(sess.address);
    });
  }

  function openAvatarModal() {
    const sess = getSession();
    if (!sess || document.getElementById("av-overlay")) return;
    const selfKey = sess.address.toLowerCase();
    const curHash = (window.__avatarHashes || {})[selfKey] || "";

    const ov = document.createElement("div");
    ov.id = "av-overlay";
    ov.innerHTML =
      '<div class="av-card">' +
      '<div class="av-head"><span>' + t("prof.avatarChange") + '</span><button type="button" class="av-x" id="av-x" aria-label="' + t("common.close") + '" title="' + t("common.close") + '">×</button></div>' +
      '<div class="av-body">' +
      '<div class="av-prev" id="av-prev"></div>' +
      '<div class="av-info hidden" id="av-info"></div>' +
      '<div class="av-btns">' +
      '<button type="button" class="av-pick" id="av-pick">' + t("prof.avatarPick") + "</button>" +
      '<button type="button" class="av-reset" id="av-reset">' + t("prof.avatarReset") + "</button>" +
      "</div>" +
      '<input type="file" id="av-file" accept="image/jpeg,image/png" class="hidden">' +
      "</div>" +
      '<div class="av-foot">' +
      '<button type="button" class="av-cancel" id="av-cancel">' + t("common.cancel") + "</button>" +
      '<button type="button" class="av-save" id="av-save" disabled>' + t("prof.avatarSave") + "</button>" +
      "</div></div>";
    document.body.appendChild(ov);

    const prev = ov.querySelector("#av-prev");
    const info = ov.querySelector("#av-info");
    const fileIn = ov.querySelector("#av-file");
    const resetB = ov.querySelector("#av-reset");
    const saveB = ov.querySelector("#av-save");
    let pending = null; // {blob, ext, w, h}
    let pendingURL = null;
    let busy = false;

    function close() {
      // Only the pending-selection preview is ours to revoke; the current
      // preview shares the page-level avatarObjectURL registry (revoking
      // it would break the own card and the account rows).
      if (pendingURL) URL.revokeObjectURL(pendingURL);
      ov.remove();
    }
    function showCurrent() {
      if (pendingURL) { URL.revokeObjectURL(pendingURL); pendingURL = null; }
      pending = null;
      info.classList.add("hidden");
      saveB.disabled = true;
      resetB.disabled = !curHash; // nothing uploaded -> nothing to reset
      if (!curHash) { prev.innerHTML = avFallback(sess.address); return; }
      prev.innerHTML = '<img alt="">';
      avatarObjectURL(sess.address, curHash).then(function (url) {
        if (prev.firstChild) prev.firstChild.src = url;
      }).catch(function () { prev.innerHTML = avFallback(sess.address); });
    }
    function showPending() {
      prev.innerHTML = '<img alt="">';
      prev.firstChild.src = pendingURL;
      info.textContent = pending.w + "×" + pending.h + " · " + Math.max(1, Math.round(pending.blob.size / 1024)) + " KB";
      info.classList.remove("hidden");
      saveB.disabled = false;
      resetB.disabled = false;
    }

    // Canvas rescale (longest edge, never upscale) -> quality ladder 0.92..0.5,
    // then step the edge down; jpeg vs png re-encodes race, smaller wins
    // (mirrors the server sniff contract). Returns null past the floor.
    async function compress(file) {
      // MIME 可能为空或 octet-stream（CDP/OS 差异）：扩展名兜底过门，
      // 内容合法性仍由下方 canvas 解码把关（解码失败按类型错报）。
      const nameL = (file.name || "").toLowerCase();
      const extOk = /\.(jpe?g|png)$/.test(nameL);
      if (file.type !== "image/jpeg" && file.type !== "image/png" && !extOk) return null;
      const inURL = URL.createObjectURL(file);
      let img;
      try {
        img = await new Promise(function (res, rej) {
          const im = new Image();
          im.onload = function () { res(im); };
          im.onerror = function () { rej(new Error("decode")); };
          im.src = inURL;
        });
      } catch (e) { URL.revokeObjectURL(inURL); return null; }
      URL.revokeObjectURL(inURL);
      let edge = AV_MAX_EDGE, q = 0.92;
      for (let attempt = 0; attempt < 24; attempt++) {
        const scale = Math.min(1, edge / Math.max(img.naturalWidth, img.naturalHeight));
        const w = Math.max(1, Math.round(img.naturalWidth * scale));
        const h = Math.max(1, Math.round(img.naturalHeight * scale));
        const cv = document.createElement("canvas");
        cv.width = w; cv.height = h;
        cv.getContext("2d").drawImage(img, 0, 0, w, h);
        const jpeg = await new Promise(function (res) { cv.toBlob(res, "image/jpeg", q); });
        const png = await new Promise(function (res) { cv.toBlob(res, "image/png"); });
        let blob = null, ext = "jpg";
        if (jpeg && (!png || jpeg.size <= png.size)) { blob = jpeg; ext = "jpg"; }
        else if (png) { blob = png; ext = "png"; }
        if (blob && blob.size <= AV_MAX_BYTES) return { blob: blob, ext: ext, w: w, h: h };
        if (q > 0.5) q = Math.max(0.5, q - 0.07);
        else if (edge > 216) { edge = Math.round(edge * 0.75); q = 0.92; }
        else break;
      }
      return null;
    }

    fileIn.addEventListener("change", async function () {
      const f = fileIn.files && fileIn.files[0];
      fileIn.value = ""; // re-selecting the same file must retrigger change
      if (!f || busy) return;
      busy = true;
      const r = await compress(f).catch(function () { return null; });
      busy = false;
      if (!r) { toast(t("prof.avatarBadType"), "error"); return; }
      if (pendingURL) URL.revokeObjectURL(pendingURL);
      pending = r;
      pendingURL = URL.createObjectURL(r.blob); // ours alone — revoked on close/replace
      showPending();
    });

    ov.querySelector("#av-pick").addEventListener("click", function () {
      if (!busy) fileIn.click();
    });
    resetB.addEventListener("click", async function () {
      if (busy) return;
      if (pending) { showCurrent(); return; } // clear the pending selection
      if (!curHash) return;
      busy = true;
      try {
        await api("/api/account/avatar", { method: "DELETE", keepSession: true });
        window.__avatarHashes = window.__avatarHashes || {};
        delete window.__avatarHashes[selfKey];
        toast(t("prof.avatarCleared"), "success");
        close();
        renderOwnAvatar();
      } catch (e) {
        toast(String((e && e.message) || e), "error");
      }
      busy = false;
    });
    saveB.addEventListener("click", async function () {
      if (!pending || busy) return;
      busy = true;
      try {
        const fd = new FormData();
        fd.append("file", pending.blob, "avatar." + pending.ext);
        const res = await fetch("/api/account/avatar", {
          method: "PUT",
          headers: { Authorization: basicAuth() },
          body: fd,
        });
        if (!res.ok) {
          let msg = res.status + " " + res.statusText;
          try { const tt = await res.text(); if (tt) msg = tt; } catch (_) {}
          throw new Error(msg);
        }
        const out = await res.json();
        window.__avatarHashes = window.__avatarHashes || {};
        // 1062 (boss bug): faces flip instantly on every surface. The sync
        // writes the registry itself - pre-writing it here tripped the
        // sync's own dedupe ("no change") and the scan never ran.
        avSyncAvatarsFromActivity([{ address: selfKey, avatar_hash: out.avatar_hash }]);
        toast(t("prof.avatarDone"), "success");
        close();
        renderOwnAvatar();
      } catch (e) {
        const m = String((e && e.message) || e);
        toast(/413|too large|100KB/i.test(m) ? t("prof.avatarTooBig") : m, "error");
      }
      busy = false;
    });
    ov.querySelector("#av-x").addEventListener("click", close);
    ov.querySelector("#av-cancel").addEventListener("click", close);
    ov.addEventListener("click", function (ev) { if (ev.target === ov) close(); });
    showCurrent();
  }

  // ---- 0.3.3-C: accounts-page listification (mobile only) ----
  // Row grammar per Iris spec v1.0: avatar | label body (3 lines) | gear.
  // All rows equal height; single-line iron rule (badges/pill nowrap, the
  // address marquees only on overflow, sig/latest ellipsize); gear opens an
  // in-place overlay card (absolutely positioned over the row — page layout
  // pixel-stable); tapping the row composes (replaces per-row compose btns).
  function accRelTime(ts) {
    if (!ts) return "";
    var d = new Date(ts * 1000), now = new Date();
    var hm = ("0" + d.getHours()).slice(-2) + ":" + ("0" + d.getMinutes()).slice(-2);
    var day = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    var today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    var diff = Math.round((today - day) / 86400000);
    if (diff <= 0) return hm;
    if (diff === 1) return t("acc.yesterday");
    return t("acc.daysAgo", { n: diff });
  }
  // boss mobile-list round (0.3.4): no "Latest:" prefix; a no-information
  // subject (IM-style sends fill it) shows the body snippet instead - the
  // set is the union of both locales' vocabularies. latest_body rides the
  // same payload once the Go side lands; until then the subject fallback
  // keeps the line honest.
  var noinfoCache = null;
  function noinfoSet() {
    if (noinfoCache) return noinfoCache;
    var out = {};
    ["zh", "en"].forEach(function (l) {
      var d = window.I18N && window.I18N.dict && window.I18N.dict(l);
      var raw = (d && d["acc.noinfoSubjects"]) || "";
      raw.split(",").forEach(function (w) {
        w = w.trim().toLowerCase();
        if (w) out[w] = 1;
      });
    });
    noinfoCache = out;
    return out;
  }
  function accLatestHtml(s) {
    if (!s || !(+s.latest_at)) return '<div class="im3-line3"><span class="cl-none">' + esc(t("acc.latestNone")) + "</span></div>";
    var dir = s.latest_dir === "out" ? t("acc.latestOut") : t("acc.latestIn");
    var shown = s.latest_subject || "";
    if (shown && !shown.trim()) shown = s.latest_body || shown;
    if (shown && noinfoSet()[shown.trim().toLowerCase()]) shown = s.latest_body || shown;
    var subj = shown ? "\u300c" + shown + "\u300d" : "";
    return '<div class="im3-line3">' + esc(accRelTime(+s.latest_at)) + " " + dir + (subj ? " \u00b7 " + esc(subj) : "") + "</div>";
  }
  function accOverlayHtml(addr, isSub) {
    var acts = "";
    if (isSub) acts += '<button class="warn" data-remove-sub="' + esc(addr) + '">\u2715 ' + t("subs.removeBtn") + "</button>";
    acts += '<button data-limits="' + esc(addr) + '">' + t("limits.open") + "</button>";
    acts += '<button data-wl="' + esc(addr) + '">' + t("wl.title") + "</button>";
    return '<div class="im3-overlay" data-ovl="' + esc(addr) + '">' +
      acts +
      '<button class="cl-close" data-ovl-back="' + esc(addr) + '">\u2715 ' + t("acc.back") + "</button></div>";
  }
  function accRowHtml(o) {
    var av = accAvatarHtml(o.addr, o.isSub);
    var latest = accLatestHtml(o.sub);
    var sigLine = o.sig ? esc(o.sig) : "";
    return '<div class="im3-row' + (o.isSub ? " im3-sub" : " im3-ext") + '" data-act-acct="' + esc(o.addr) + '" data-claddr="' + esc(o.addr) + '">' +
      av +
      '<div class="im3-main">' +
        '<div class="im3-l1">' + o.badge +
        '<span class="im3-addr"><span class="im3-addr-in">' + esc(o.addr) + "</span></span>" +
        '<span class="act-pill-slot" data-act-slot="pill"></span></div>' +
        '<div class="im3-line2">' + sigLine + "</div>" +
        latest +
      "</div>" +
      (o.isSub ? '<button class="im3-gear" data-gear="' + esc(o.addr) + '" aria-label="' + esc(t("acc.settings")) + '">\u2699</button>' : '') +
      accOverlayHtml(o.addr, o.isSub) +
      "</div>";
  }
  // Iris v6 marquee scan (verbatim semantics): overflow detection sets the
  // shift distance and duration; the CSS keyframes do the ping-pong.
  function im3MarqueeScan(root) {
    $$(".im3-addr", root).forEach(function (el) {
      var inn = el.querySelector(".im3-addr-in");
      if (!inn) return;
      var over = inn.scrollWidth - el.clientWidth;
      if (over > 1) {
        el.classList.add("mq");
        el.style.setProperty("--mq-shift", (-over - 2) + "px");
        el.style.setProperty("--mq-dur", Math.max(6, over / 18).toFixed(1) + "s");
      } else { el.classList.remove("mq"); el.style.removeProperty("--mq-shift"); }
    });
  }
  // 0.3.3-C ②③ (Iris spec v1.0): empty/failure states are expressed IN the
  // row grammar - never as floating text outside the list; a failure row is
  // a whole-row retry button (same interaction grain as tap-to-compose).
  function im3StateRowHtml(kind, titleKey, subKey, retrySrc) {
    var cls = kind === "err" ? "im3-err" : "im3-empty";
    var av = kind === "err" ? "！" : "○";
    var retry = retrySrc ? ' data-retry="' + retrySrc + '"' : "";
    return '<div class="im3-row ' + cls + '"' + retry + ">" +
      '<div class="im3-av ' + (kind === "err" ? "im3-av-err" : "im3-av-empty") + '">' + av + "</div>" +
      '<div class="im3-main im3-state-main">' +
      '<div class="im3-state-t">' + t(titleKey) + "</div>" +
      '<div class="im3-state-s">' + t(subKey) + "</div></div></div>";
  }
  function wireErrRetry(root) {
    $$("[data-retry]", root).forEach(function (row) {
      row.addEventListener("click", function () {
        if (row.getAttribute("data-retrying")) return;
        row.setAttribute("data-retrying", "1");
        var s = row.querySelector(".im3-state-s");
        if (s) s.textContent = t("acc.retrying");
        loadAccounts(); // whole-list rebuild - success clears the row, failure re-renders it
      });
    });
  }
  function accWireList(root) {
    // Row tap = compose; gear tap = in-place overlay; back hides it.
    $$(".im3-row", root).forEach(function (row) { wireIm3Row(row, root); });
    $$("[data-compose], [data-remove-sub]", root).forEach(function (b) {
      if (b.dataset.compose) b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: b.dataset.compose } })); });
      if (b.dataset.removeSub) b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("subs:remove", { detail: { address: b.dataset.removeSub, role: "superior" } })); });
    });
  }
  // wireIm3Row wires ONE mobile card - the per-row unit the full render
  // loops over and the incremental inserter calls for a single new card
  // (looping accWireList over the whole box again would double-wire every
  // existing listener). root is the list container: the gear handler must
  // still be able to close OTHER rows' overlays.
  function wireIm3Row(row, root) {
    row.addEventListener("click", function (ev) {
      if (ev.target.closest("[data-gear]") || ev.target.closest(".im3-overlay")) return;
      var to = row.getAttribute("data-claddr");
      if (!to) return; // register/pinned row opens its own flow, not compose
      document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: to } }));
    });
    var gear = row.querySelector("[data-gear]");
    if (gear) gear.addEventListener("click", function () {
      $$(".im3-overlay.on", root).forEach(function (o) { if (o !== row.querySelector(".im3-overlay")) o.classList.remove("on"); });
      row.querySelector(".im3-overlay").classList.toggle("on");
    });
    var back = row.querySelector("[data-ovl-back]");
    if (back) back.addEventListener("click", function () { row.querySelector(".im3-overlay").classList.remove("on"); });
  }

  // ---- 0.3.3-C: accounts-page listification (mobile only) ----
  // Row grammar per Iris spec v1.0: avatar | label body (3 lines) | gear.
  // All rows equal height; single-line iron rule (badges/pill nowrap, the
  // address marquees only on overflow, sig/latest ellipsize); gear opens an
  // in-place overlay card (absolutely positioned over the row — page layout
  // pixel-stable); tapping the row composes (replaces per-row compose btns).
  function accRelTime(ts) {
    if (!ts) return "";
    var d = new Date(ts * 1000), now = new Date();
    var hm = ("0" + d.getHours()).slice(-2) + ":" + ("0" + d.getMinutes()).slice(-2);
    var day = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    var today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    var diff = Math.round((today - day) / 86400000);
    if (diff <= 0) return hm;
    if (diff === 1) return t("acc.yesterday");
    return t("acc.daysAgo", { n: diff });
  }
  // boss mobile-list round (0.3.4): no "Latest:" prefix; a no-information
  // subject (IM-style sends fill it) shows the body snippet instead - the
  // set is the union of both locales' vocabularies. latest_body rides the
  // same payload once the Go side lands; until then the subject fallback
  // keeps the line honest.
  var noinfoCache = null;
  function noinfoSet() {
    if (noinfoCache) return noinfoCache;
    var out = {};
    ["zh", "en"].forEach(function (l) {
      var d = window.I18N && window.I18N.dict && window.I18N.dict(l);
      var raw = (d && d["acc.noinfoSubjects"]) || "";
      raw.split(",").forEach(function (w) {
        w = w.trim().toLowerCase();
        if (w) out[w] = 1;
      });
    });
    noinfoCache = out;
    return out;
  }
  function accLatestHtml(s) {
    if (!s || !(+s.latest_at)) return '<div class="im3-line3"><span class="cl-none">' + esc(t("acc.latestNone")) + "</span></div>";
    var shown = s.latest_subject || "";
    if (shown && !shown.trim()) shown = s.latest_body || shown;
    if (shown && noinfoSet()[shown.trim().toLowerCase()]) shown = s.latest_body || shown;
    var subj = shown ? "\u300c" + shown + "\u300d" : "";
    // boss 09-29: the direction word is dropped from line3 (time + content
    // carry the row); latest_dir stays in the payloads for other uses.
    return '<div class="im3-line3">' + esc(accRelTime(+s.latest_at)) + (subj ? " \u00b7 " + esc(subj) : "") + "</div>";
  }
  // ---- 0.3.3-A: default avatar mixed generator (Iris spec v1.1) ----
  // Deterministic: address (lowercase) -> SHA-256 -> seed bytes S[0..3].
  // Style = S[0] % 3 (0 gradient-initial / 1 geometric-2x2 / 2 two-tone
  // ripple). Every parameter derives from the seed — no Math.random, the
  // same address renders the same avatar across sessions and devices.
  var __avHashCache = {};
  // One seed path for every origin. WebCrypto digest is secure-context-only,
  // which forked the same address into different robots across entry points
  // (boss: avatars MUST be identical from every entry). This software SHA-256
  // produces the exact bytes crypto.subtle.digest returned, so every avatar
  // already rendered on secure origins keeps its look; insecure origins join
  // the canonical stream instead of a degenerate fallback. First 4 digest
  // bytes seed the generator.
  function avSha256(a) {
    var bytes = new TextEncoder().encode(a), bitLen = bytes.length * 8;
    var msg = Array.prototype.slice.call(bytes);
    msg.push(0x80);
    while (msg.length % 64 !== 56) msg.push(0);
    var hi = Math.floor(bitLen / 4294967296), lo = bitLen >>> 0;
    msg.push((hi >>> 24) & 255, (hi >>> 16) & 255, (hi >>> 8) & 255, hi & 255,
             (lo >>> 24) & 255, (lo >>> 16) & 255, (lo >>> 8) & 255, lo & 255);
    var H = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
    var K = [0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
             0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
             0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
             0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
             0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
             0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
             0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
             0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2];
    function rr(x, n) { return (x >>> n) | (x << (32 - n)); }
    for (var i = 0; i < msg.length; i += 64) {
      var w = [], t, s0, s1;
      for (t = 0; t < 16; t++) w[t] = (msg[i + 4*t] << 24) | (msg[i + 4*t + 1] << 16) | (msg[i + 4*t + 2] << 8) | msg[i + 4*t + 3];
      for (t = 16; t < 64; t++) {
        s0 = rr(w[t-15], 7) ^ rr(w[t-15], 18) ^ (w[t-15] >>> 3);
        s1 = rr(w[t-2], 17) ^ rr(w[t-2], 19) ^ (w[t-2] >>> 10);
        w[t] = (w[t-16] + s0 + w[t-7] + s1) | 0;
      }
      var av = H[0], bv = H[1], cv = H[2], dv = H[3], ev = H[4], fv = H[5], gv = H[6], hv = H[7];
      for (t = 0; t < 64; t++) {
        var S1 = rr(ev, 6) ^ rr(ev, 11) ^ rr(ev, 25);
        var ch = (ev & fv) ^ (~ev & gv);
        var t1 = (hv + S1 + ch + K[t] + w[t]) | 0;
        var S0 = rr(av, 2) ^ rr(av, 13) ^ rr(av, 22);
        var mj = (av & bv) ^ (av & cv) ^ (bv & cv);
        var t2 = (S0 + mj) | 0;
        hv = gv; gv = fv; fv = ev; ev = (dv + t1) | 0; dv = cv; cv = bv; bv = av; av = (t1 + t2) | 0;
      }
      H[0] = (H[0] + av) | 0; H[1] = (H[1] + bv) | 0; H[2] = (H[2] + cv) | 0; H[3] = (H[3] + dv) | 0;
      H[4] = (H[4] + ev) | 0; H[5] = (H[5] + fv) | 0; H[6] = (H[6] + gv) | 0; H[7] = (H[7] + hv) | 0;
    }
    return new Uint8Array([(H[0] >>> 24) & 255, (H[0] >>> 16) & 255, (H[0] >>> 8) & 255, H[0] & 255]);
  }
  function avSeed(addr, cb) {
    var a = String(addr).toLowerCase();
    if (__avHashCache[a]) { cb(__avHashCache[a]); return; }
    __avHashCache[a] = avSha256(a);
    cb(__avHashCache[a]);
  }
  function avHsl(h, s, l) { return "hsl(" + Math.round(h) + "," + Math.round(s) + "%," + Math.round(l) + "%)"; }
  var AV_BGS = ["#cfcfcf", "#c4c4c4", "#d8d8d8", "#bdbdbd"]; // boss 09-30 ②: 4-shade body grayscale (Iris final: neutral grays), drawn per address
  var AV_INK_ON_WHITE = "#9a9a9a";
  // big-item palette rebalanced the same way: pink/lavender 6/10 -> 2/10,
  // steel/sage/tan/olive mid-tones fill the freed slots.
  var AV_ACCENTS = ["#e6b8c2", "#a9c6de", "#b8d4b8", "#eed3a4", "#a8d0cc", "#ecb8a8", "#c9dfd4", "#d8c8b8", "#a8c8a0", "#c6b6e0"]; // Iris final swatches: pink 1/10, lavender 1/10
  // boss 09-30: the old 5-swatch small palette was 4/5 pink-family, so 80%
  // of rows read pink and neighbors ran together. Rebalanced to 7 with the
  // pink share cut to 2/7 and cool/sage/stone mid-tones added (Iris to
  // review the values).
  var AV_SMALL_ACCENTS = ["#a9c6de", "#a8d0cc", "#c9dfd4", "#eed3a4", "#d8c8b8", "#a8c8a0", "#e6b8c2"]; // Iris final swatches: pink 1/7
  var AV_EYES = ["?", "#", "\u00d7", "bar"];
  var AV_MOUTHS = ["line", "wave", "dot", "v"];
  function avHsl(h, s, l) { return "hsl(" + Math.round(h) + "," + Math.round(s) + "%," + Math.round(l) + "%)"; } // still used by the accounts heartbeat colors
  var AV_ACCS = [
    ["flower", 1], ["headphone", 0], ["cat", 0], ["tophat", 1], ["bunny", 1],
    ["chef", 1], ["heartclip", 1], ["sprout", 1], ["cherry", 1], ["bell", 1],
    ["bowtie", 1], ["strawhat", 0], ["windkey", 0], ["propeller", 0]
  ];
  function avRobotSvg(addr, S) {
    var pick = function (n, mod) { return S[n % 4] % mod; };
    var white = "#ffffff";
    var grey = AV_INK_ON_WHITE;
    var AV_BG = AV_BGS[pick(2, AV_BGS.length)]; // per-address body shade (②)
    var accDef = AV_ACCS[pick(0, AV_ACCS.length)];
    var accent = accDef[1] ? AV_SMALL_ACCENTS[pick(1, AV_SMALL_ACCENTS.length)] : AV_ACCENTS[pick(1, AV_ACCENTS.length)];
    var eyeL = AV_EYES[pick(1, AV_EYES.length)];
    var eyeR = AV_EYES[pick(2, AV_EYES.length)];
    var mouth = AV_MOUTHS[pick(3, AV_MOUTHS.length)];
    var acc = accDef[0];
    function barEye(x) { return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><rect x="' + (x - 3) + '" y="28" width="6" height="16" rx="3" fill="' + grey + '"/></g>'; }
    function gtEye(x, flip) {
      var d = flip ? ("M" + (x + 6) + " 29 L" + (x - 6) + " 36 L" + (x + 6) + " 43")
                   : ("M" + (x - 6) + " 29 L" + (x + 6) + " 36 L" + (x - 6) + " 43");
      return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><path d="' + d + '" fill="none" stroke="' + grey + '" stroke-width="4.5" stroke-linecap="round" stroke-linejoin="round"/></g>';
    }
    function heartEye(x) { return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><path d="M' + x + ' 43 C' + (x - 9) + ' 36 ' + (x - 6) + ' 26 ' + x + ' 31 C' + (x + 6) + ' 26 ' + (x + 9) + ' 36 ' + x + ' 43 Z" fill="' + grey + '"/></g>'; }
    function shyEye(x) { return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><path d="M' + (x - 6) + ' 35 Q' + x + ' 28 ' + (x + 6) + ' 35" fill="none" stroke="' + grey + '" stroke-width="4.5" stroke-linecap="round"/></g>'; }
    function happyEye(x) { return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><path d="M' + (x - 6) + ' 35 Q' + x + ' 43 ' + (x + 6) + ' 35" fill="none" stroke="' + grey + '" stroke-width="4.5" stroke-linecap="round"/></g>'; }
    function qEye(x) { return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><path d="M' + (x - 5) + ' 32 C' + (x - 5) + ' 25 ' + (x + 5) + ' 25 ' + (x + 5) + ' 31 C' + (x + 5) + ' 35 ' + x + ' 35 ' + x + ' 39" fill="none" stroke="' + grey + '" stroke-width="4.5" stroke-linecap="round"/><circle cx="' + x + '" cy="45" r="2.6" fill="' + grey + '"/></g>'; }
    function hashEye(x) { return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><rect x="' + (x - 6.5) + '" y="29" width="3.6" height="15" rx="1.8" fill="' + grey + '"/><rect x="' + (x + 2.9) + '" y="29" width="3.6" height="15" rx="1.8" fill="' + grey + '"/><rect x="' + (x - 7.5) + '" y="32.5" width="15" height="3.4" rx="1.7" fill="' + grey + '"/><rect x="' + (x - 7.5) + '" y="38.6" width="15" height="3.4" rx="1.7" fill="' + grey + '"/></g>'; }
    function xEye(x) { return '<g transform="translate(' + x * 0.2 + ' 7.2) scale(0.8)"><path d="M' + (x - 6) + ' 29 L' + (x + 6) + ' 43 M' + (x + 6) + ' 29 L' + (x - 6) + ' 43" stroke="' + grey + '" stroke-width="4.5" stroke-linecap="round"/></g>'; }
    var INDEP = [qEye, hashEye, xEye, barEye];
    var roll = pick(1, 100);
    var eyesEl = "";
    if (roll < 25) eyesEl = barEye(36) + barEye(60);
    else if (roll < 35) eyesEl = gtEye(36, false) + barEye(60);
    else if (roll < 45) eyesEl = barEye(36) + gtEye(60, true);
    else if (roll < 55) eyesEl = heartEye(36) + heartEye(60);
    else if (roll < 65) eyesEl = shyEye(36) + shyEye(60);
    else if (roll < 75) eyesEl = happyEye(36) + happyEye(60);
    else eyesEl = INDEP[pick(2, INDEP.length)](36) + INDEP[pick(3, INDEP.length)](60);
    var mouthEl = "";
    if (mouth === "line") mouthEl = '<rect x="40" y="47" width="16" height="3.5" rx="1.75" fill="' + grey + '"/>';
    else if (mouth === "wave") mouthEl = '<path d="M39 48 q4.5 -4.5 9 0 q4.5 4.5 9 0" fill="none" stroke="' + grey + '" stroke-width="3.5" stroke-linecap="round"/>';
    else if (mouth === "dot") mouthEl = '<circle cx="48" cy="48" r="3" fill="' + grey + '"/>';
    else mouthEl = '<path d="M42.5 46 l5.5 5.5 l5.5 -5.5" fill="none" stroke="' + grey + '" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round"/>';
    var A = accent, accEl = "";
    if (acc === "flower") accEl = '<g fill="' + A + '"><circle cx="61" cy="8" r="3.4"/><circle cx="56.5" cy="11" r="3.4"/><circle cx="65.5" cy="11" r="3.4"/><circle cx="58.5" cy="14.5" r="3.4"/><circle cx="63.5" cy="14.5" r="3.4"/></g><circle cx="61" cy="11.5" r="2.4" fill="' + AV_BG + '"/>';
    else if (acc === "headphone") accEl = '<path d="M30 22 C30 11 66 11 66 22" fill="none" stroke="' + A + '" stroke-width="4" stroke-linecap="round"/><rect x="25.5" y="19" width="8" height="11" rx="3.5" fill="' + A + '"/><rect x="62.5" y="19" width="8" height="11" rx="3.5" fill="' + A + '"/>';
    else if (acc === "cat") accEl = '<path d="M28 22 L31 4 L44 15 Z" fill="' + white + '"/><path d="M68 22 L65 4 L52 15 Z" fill="' + white + '"/>';
    else if (acc === "tophat") accEl = '<rect x="40" y="1" width="16" height="11" fill="' + A + '"/><rect x="36.5" y="10.5" width="23" height="3.6" rx="1.8" fill="' + A + '"/>';
    else if (acc === "bunny") accEl = '<ellipse cx="42" cy="9" rx="4" ry="8" fill="' + A + '" transform="rotate(-12 42 15)"/><ellipse cx="54" cy="9" rx="4" ry="8" fill="' + A + '" transform="rotate(12 54 15)"/>';
    else if (acc === "chef") accEl = '<path d="M34 20 C28 20 28 10 35 11 C36 5 44 4 46 8 C48 3 58 4 58 10 C66 9 66 20 60 20 Z" fill="' + A + '"/>';
    else if (acc === "heartclip") accEl = '<path d="M61 16 C54 11 56 4 61 8 C66 4 68 11 61 16 Z" fill="' + A + '"/>';
    else if (acc === "sprout") accEl = '<path d="M48 22 C48 14 48 12 48 10" stroke="' + A + '" stroke-width="3" stroke-linecap="round" fill="none"/><path d="M48 12 C42 12 40 6 47 6 C49 10 48 12 48 12 Z" fill="' + A + '"/><path d="M48 14 C54 14 56 9 50 8 C47 11 48 14 48 14 Z" fill="' + A + '"/>';
    else if (acc === "cherry") accEl = '<path d="M42 10 C46 14 48 16 50 20 M58 8 C54 13 52 16 50 20" stroke="' + A + '" stroke-width="2.5" fill="none" stroke-linecap="round"/><circle cx="41" cy="13" r="4" fill="' + A + '"/><circle cx="59" cy="11" r="4" fill="' + A + '"/>';
    else if (acc === "bell") accEl = '<path d="M41 18 C41 8 55 8 55 18 Z" fill="' + A + '"/><circle cx="48" cy="20" r="2.5" fill="' + A + '"/>';
    else if (acc === "bowtie") accEl = '<path d="M48 22 L36 15 L36 29 Z" fill="' + A + '"/><path d="M48 22 L60 15 L60 29 Z" fill="' + A + '"/><circle cx="48" cy="22" r="3.5" fill="#3a3a3a"/>';
    else if (acc === "strawhat") accEl = '<ellipse cx="48" cy="12" rx="22" ry="6" fill="' + A + '"/><path d="M38 12 C38 2 58 2 58 12 Z" fill="' + A + '"/>';
    else if (acc === "windkey") accEl = '<circle cx="48" cy="10" r="7" fill="none" stroke="' + A + '" stroke-width="3.5"/><line x1="48" y1="10" x2="48" y2="4" stroke="' + A + '" stroke-width="3" stroke-linecap="round"/><line x1="48" y1="17" x2="48" y2="26" stroke="' + A + '" stroke-width="3.5"/>';
    else if (acc === "propeller") accEl = '<ellipse cx="38" cy="8" rx="12" ry="4" fill="' + A + '"/><ellipse cx="58" cy="8" rx="12" ry="4" fill="' + A + '"/><circle cx="48" cy="9" r="3.5" fill="' + A + '"/><line x1="48" y1="12" x2="48" y2="26" stroke="' + A + '" stroke-width="3.5"/>';
    else accEl = '<line x1="48" y1="26" x2="48" y2="14" stroke="' + grey + '" stroke-width="4" stroke-linecap="round"/><circle cx="48" cy="11" r="5.5" fill="' + A + '"/>';
    return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96">' +
      '<rect width="96" height="96" fill="' + AV_BG + '"/>' +
      accEl +
      '<rect x="9" y="24" width="9" height="17" rx="3.5" fill="' + white + '"/>' +
      '<rect x="78" y="24" width="9" height="17" rx="3.5" fill="' + white + '"/>' +
      '<rect x="17" y="13" width="62" height="46" rx="14" fill="' + white + '"/>' +
      '<rect x="12" y="52" width="72" height="60" rx="16" fill="' + white + '"/>' +
      eyesEl + mouthEl +
      "</svg>";
  }
  function avSvgHtml(addr, S) {
    var svg = avRobotSvg(addr, S);
    return '<img class="cl-av-img" src="data:image/svg+xml;utf8,' + encodeURIComponent(svg) + '" alt="" data-avgen="robot">';
  }
  // 404 fallback (A-line task 4): avatar_hash present but the real avatar
  // is gone (file deleted server-side) - the broken <img> swaps to the
  // deterministic generator inline, so no white block ever shows.
  // window bridge: compose.js imports ONLY core.js (audit constraint), so the
  // avatar family rides on window for the conversation capsules (0.3.4.2).
  window.__avHydrate = avHydrate;
  window.__avRemoteHydrate = avRemoteHydrate;
  window.__avHarvest = avHarvest;
  window.__avRestore = avRestore;
  window.__avFallback = function (img) {
    var box = img && img.parentNode;
    var addr = box && box.getAttribute("data-av");
    if (!box || !addr) return;
    img.remove();
    box.setAttribute("data-avpend", "1");
    box.textContent = (String(addr)[0] || "?").toUpperCase();
    avHydrate(box.parentElement || box); // $$ does not match the root itself - scan from the parent
  };

  function accAvatarHtml(addr, isSub) {
    // A-line hook, 0021 rework (bug fix: uploaded avatars invisible to
    // other accounts): the old branch rendered a bare <img
    // src=/api/avatar/...> - under the auth wall an <img> can never carry
    // credentials, so every row except the own card 401'd into the
    // generator forever. Rows now ALWAYS render a placeholder that
    // avRemoteHydrate fills via the shared avatarObjectURL registry
    // (authenticated fetch -> objectURL); hash present = ?v= bust,
    // absent = plain endpoint whose 404 falls back to the generator.
    var h = (window.__avatarHashes || {})[String(addr).toLowerCase()] || '';
    // The unclipped wrapper hosts the unread dot: .im3-av itself is
    // overflow-hidden (rounded mask), and a corner badge must NOT live
    // under that mask (boss: the dot showed a bite out of it).
    return '<span class="im3-av-wrap"><div class="im3-av' + (isSub ? "" : " im3-av-ext") + '" data-av="' + esc(addr) + '" data-avremote="1" data-avhash="' + esc(h) + '">' + esc((String(addr)[0] || "?").toUpperCase()) + '</div></span>';
  }
  // avRemoteHydrate (0021): fill remote placeholders via the shared
  // avatarObjectURL registry (dedupe by addr|hash, page-lifetime URLs).
  // isConnected guards the re-render race; a failed fetch (404 = the
    // account has no avatar) hands the box to the generator path.
  // boss 09-29 production flicker: real traffic flips the unified order almost
  // every poll, each flip rewrites the list, and a rewritten avatar <img> costs
  // a re-decode frame - generated avatars flickered continuously. Harvest the
  // rendered avatar boxes before a rebuild and re-attach their (already
  // decoded) nodes after: unchanged rows never flash, and the data-avdone
  // guard keeps hydration off recycled nodes so only genuinely new or
  // changed addresses cost work.
  function avHarvest(container) {
    var bank = {};
    $$("[data-avdone]", container).forEach(function (b) {
      var k = String(b.getAttribute("data-av")).toLowerCase() + "|" + (b.getAttribute("data-avhash") || "");
      // multi-slot: one addr|hash renders MANY boxes in a conversation (every
      // capsule of a thread, or sub+contact twins on the accounts page) - a
      // single-slot bank left all but the last box re-decoding every rebuild.
      (bank[k] = bank[k] || []).push(b);
    });
    return bank;
  }
  function avRestore(container, bank) {
    if (!bank) return;
    $$("[data-avremote]", container).forEach(function (b) {
      var q = bank[String(b.getAttribute("data-av")).toLowerCase() + "|" + (b.getAttribute("data-avhash") || "")];
      while (q && q.length) {
        var old = q.shift();
        if (!old.hasAttribute("data-avdone") || !old.firstChild) continue; // drained by a twin - next slot
        while (b.firstChild) b.removeChild(b.firstChild);
        while (old.firstChild) b.appendChild(old.firstChild);
        b.setAttribute("data-avdone", "1");
        b.removeAttribute("data-avpend");
        return;
      }
    });
  }
  function avRemoteFillOne(el) {
    if (el.hasAttribute("data-avdone")) return; // recycled node: bitmap already decoded
    var addr = el.getAttribute("data-av");
    // 1062 (boss bug): the poll-fresh registry wins over the paint-time
    // attribute - a mirrored slot carries a frozen stale hash and would
    // otherwise serve the old blob forever.
    var regHash = (window.__avatarHashes || {})[String(addr).toLowerCase()];
    var hash = regHash !== undefined && regHash !== null ? regHash : (el.getAttribute("data-avhash") || "");
    avatarObjectURL(addr, hash, false).then(function (url) {
      if (!el.isConnected) return;
      el.innerHTML = '<img class="cl-av-img" src="' + url + '" alt="">';
      el.setAttribute("data-avdone", "1");
    }).catch(function () {
      if (!el.isConnected) return;
      el.setAttribute("data-avpend", "1");
      avHydrate(el.parentElement || el);
    });
  }
  function avRemoteHydrate(root) {
    $$("[data-avremote]", root).forEach(avRemoteFillOne);
  }
  // A-case (boss-approved): the activity poll payload already carries each
  // account's current avatar_hash, so sync it here - an uploaded avatar
  // shows within one poll cycle with no restart or refresh. A changed hash
  // costs one registry update plus exactly one targeted box re-hydration;
  // unchanged rows cost zero requests and zero DOM writes.
  // 1062b (boss): push alone leaves holes - a watcher's compose page whose
  // peer's hash never rides the poll payload, or a client whose upload-page
  // sync missed. Robustness over immediacy (boss: 宁可不及时，也要可靠):
  // every poll tick, revalidate each distinct on-page address with ONE
  // conditional GET. Server answers 304 (zero bytes) while unchanged - and
  // a no-avatar address 304s too, since the expected ETag is the empty
  // quoted string. A 200 means the server holds a different avatar: learn
  // the new ETag as the hash and let the poll sync reset every matching
  // box page-wide. Worst-case staleness: one poll cycle, self-healing.
  var avPullInflight = {};
  window.__avPullLast = null; // 1062b diagnostics: last tick summary
  function avPullTick() {
    var seen = {};
    var nodes = document.querySelectorAll("[data-av], [data-avremote]");
    for (var i = 0; i < nodes.length; i++) {
      var a = String(nodes[i].getAttribute("data-av") || "").toLowerCase();
      if (a.indexOf("@") > 0) seen[a] = 1;
    }
    var sess = getSession();
    if (sess && sess.address) seen[String(sess.address).toLowerCase()] = 1;
    var reg = window.__avatarHashes = window.__avatarHashes || {};
    Object.keys(seen).forEach(function (addr) {
      if (avPullInflight[addr]) return;
      avPullInflight[addr] = true;
      fetch("/api/avatar/" + encodeURIComponent(addr), { headers: { Authorization: basicAuth(), "If-None-Match": '"' + String(reg[addr] === undefined ? "" : reg[addr]) + '"' } })
        .then(function (res) {
          if (res.status !== 200) return;
          if (res.body && res.body.cancel) { res.body.cancel().catch(function () {}); } // hash is in the header; skip the bytes
          var et = res.headers.get("ETag") || "";
          var nh = et.replace(/^"+|"+$/g, "");
          if ((reg[addr] || "") !== nh) avSyncAvatarsFromActivity([{ address: addr, avatar_hash: nh }]);
        })
        .catch(function () {})
        .then(function () { delete avPullInflight[addr]; });
    });
    window.__avPullLast = { t: Date.now(), addrs: Object.keys(seen), reg: reg };
  }
  // 1062b (boss): the accounts-tab activity refresh does NOT cycle while the
  // user sits on the compose page - a pull hooked there never fires exactly
  // where it is needed (the watcher's write page). Own timer instead: every
  // page surface heals within one cycle no matter which tab is open.
  setInterval(function () { try { avPullTick(); } catch (e) {} }, HB_POLL_SEC * 1000);
  function avSyncAvatarsFromActivity(subs) {
    var reg = window.__avatarHashes = window.__avatarHashes || {};
    (subs || []).forEach(function (s) {
      var addr = String(s.address || "").toLowerCase();
      if (!addr) return;
      var nh = s.avatar_hash || "";
      if ((reg[addr] || "") === nh) return;
      reg[addr] = nh;
      // 1062 (boss bug): the reset used to scan #tab-accounts only - thread
      // capsules kept a stale face for the whole browser-cache lifetime.
      // Reset EVERY matching box on the page (and flip generated thread
      // boxes to remote when an avatar appears); detached nodes are
      // covered by the registry fallback at fill time.
      var nodes = document.querySelectorAll('[data-avremote], #tab-compose [data-av]');
      for (var i = 0; i < nodes.length; i++) {
        if (String(nodes[i].getAttribute("data-av")).toLowerCase() !== addr) continue;
        var box = nodes[i];
        if (!box.isConnected) continue;
        box.setAttribute("data-avremote", "");
        box.setAttribute("data-avhash", nh);
        box.classList.remove("cl-av-img");
        box.style.background = "";
        box.removeAttribute("data-avpend");
        box.removeAttribute("data-avdone");
        box.textContent = (String(box.getAttribute("data-av"))[0] || "?").toUpperCase();
        avRemoteFillOne(box);
      }
    });
  }
  // Hydrate pending generator avatars (async seed -> svg swap-in place).
  function avHydrate(root) {
    $$("[data-avpend]", root).forEach(function (el) {
      avSeed(el.getAttribute("data-av"), function (S) {
        if (S[0] === 0xff) { // hash failure fallback: solid + initial (spec)
          var hue = (S[1] * 360) / 256;
          el.style.background = avHsl(hue, 60, 60);
          el.classList.add("cl-av-img");
          el.removeAttribute("data-avpend");
          return;
        }
        el.innerHTML = avSvgHtml(el.getAttribute("data-av"), S);
        el.setAttribute("data-avdone", "1");
        el.removeAttribute("data-avpend");
      });
    });
  }

  function accOverlayHtml(addr, isSub) {
    var acts = "";
    if (isSub) acts += '<button class="warn" data-remove-sub="' + esc(addr) + '">\u2715 ' + t("subs.removeBtn") + "</button>";
    acts += '<button data-limits="' + esc(addr) + '">' + t("limits.open") + "</button>";
    acts += '<button data-wl="' + esc(addr) + '">' + t("wl.title") + "</button>";
    return '<div class="im3-overlay" data-ovl="' + esc(addr) + '">' +
      acts +
      '<button class="cl-close" data-ovl-back="' + esc(addr) + '">\u2715 ' + t("acc.back") + "</button></div>";
  }
  function accRowHtml(o) {
    var av = accAvatarHtml(o.addr, o.isSub);
    var latest = accLatestHtml(o.sub);
    var sigLine = o.sig ? esc(o.sig) : "";
    return '<div class="im3-row' + (o.isSub ? " im3-sub" : " im3-ext") + '" data-act-acct="' + esc(o.addr) + '" data-claddr="' + esc(o.addr) + '">' +
      av +
      '<div class="im3-main">' +
        '<div class="im3-l1">' + o.badge +
        '<span class="im3-addr"><span class="im3-addr-in">' + esc(o.addr) + "</span></span>" +
        '<span class="act-pill-slot" data-act-slot="pill"></span></div>' +
        '<div class="im3-line2">' + sigLine + "</div>" +
        latest +
      "</div>" +
      (o.isSub ? '<button class="im3-gear" data-gear="' + esc(o.addr) + '" aria-label="' + esc(t("acc.settings")) + '">\u2699</button>' : '') +
      accOverlayHtml(o.addr, o.isSub) +
      "</div>";
  }
  // Iris v6 marquee scan (verbatim semantics): overflow detection sets the
  // shift distance and duration; the CSS keyframes do the ping-pong.
  function im3MarqueeScan(root) {
    $$(".im3-addr", root).forEach(function (el) {
      var inn = el.querySelector(".im3-addr-in");
      if (!inn) return;
      var over = inn.scrollWidth - el.clientWidth;
      if (over > 1) {
        el.classList.add("mq");
        el.style.setProperty("--mq-shift", (-over - 2) + "px");
        el.style.setProperty("--mq-dur", Math.max(6, over / 18).toFixed(1) + "s");
      } else { el.classList.remove("mq"); el.style.removeProperty("--mq-shift"); }
    });
  }
  // 0.3.3-C ②③ (Iris spec v1.0): empty/failure states are expressed IN the
  // row grammar - never as floating text outside the list; a failure row is
  // a whole-row retry button (same interaction grain as tap-to-compose).
  function im3StateRowHtml(kind, titleKey, subKey, retrySrc) {
    var cls = kind === "err" ? "im3-err" : "im3-empty";
    var av = kind === "err" ? "！" : "○";
    var retry = retrySrc ? ' data-retry="' + retrySrc + '"' : "";
    return '<div class="im3-row ' + cls + '"' + retry + ">" +
      '<div class="im3-av ' + (kind === "err" ? "im3-av-err" : "im3-av-empty") + '">' + av + "</div>" +
      '<div class="im3-main im3-state-main">' +
      '<div class="im3-state-t">' + t(titleKey) + "</div>" +
      '<div class="im3-state-s">' + t(subKey) + "</div></div></div>";
  }
  function wireErrRetry(root) {
    $$("[data-retry]", root).forEach(function (row) {
      row.addEventListener("click", function () {
        if (row.getAttribute("data-retrying")) return;
        row.setAttribute("data-retrying", "1");
        var s = row.querySelector(".im3-state-s");
        if (s) s.textContent = t("acc.retrying");
        loadAccounts(); // whole-list rebuild - success clears the row, failure re-renders it
      });
    });
  }
  function accWireList(root) {
    // Row tap = compose; gear tap = in-place overlay; back hides it.
    $$(".im3-row", root).forEach(function (row) { wireIm3Row(row, root); });
    $$("[data-compose], [data-remove-sub]", root).forEach(function (b) {
      if (b.dataset.compose) b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: b.dataset.compose } })); });
      if (b.dataset.removeSub) b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("subs:remove", { detail: { address: b.dataset.removeSub, role: "superior" } })); });
    });
  }
  // wireIm3Row wires ONE mobile card - the per-row unit the full render
  // loops over and the incremental inserter calls for a single new card
  // (looping accWireList over the whole box again would double-wire every
  // existing listener). root is the list container: the gear handler must
  // still be able to close OTHER rows' overlays.
  function wireIm3Row(row, root) {
    row.addEventListener("click", function (ev) {
      if (ev.target.closest("[data-gear]") || ev.target.closest(".im3-overlay")) return;
      var to = row.getAttribute("data-claddr");
      if (!to) return; // register/pinned row opens its own flow, not compose
      document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: to } }));
    });
    var gear = row.querySelector("[data-gear]");
    if (gear) gear.addEventListener("click", function () {
      $$(".im3-overlay.on", root).forEach(function (o) { if (o !== row.querySelector(".im3-overlay")) o.classList.remove("on"); });
      row.querySelector(".im3-overlay").classList.toggle("on");
    });
    var back = row.querySelector("[data-ovl-back]");
    if (back) back.addEventListener("click", function () { row.querySelector(".im3-overlay").classList.remove("on"); });
  }

  // loadAccountsRegular renders the regular-user Accounts view: themselves
  // (with a change-password button) plus the people they've exchanged mail with
  // (from /api/contacts). No admin/disabled/uuid columns — those are sensitive
  // and not relevant to a personal view.
  var mqPhase = null; // boss 09-29: marquee clocks across a pending rewrite (see mqSnap)
  // acctCtx = the directory/subs view the last full render built its rows
  // from. The incremental inserter (insertMissingAccountsDom) reuses it to
  // build a missing contact's row without refetching; every full render
  // refreshes it, so it is at most one render behind - self-correcting.
  var acctCtx = null;
  // ctPcRowsHtml builds ONE contact's PC row pair (main + line3) plus the
  // badge the mobile card shares. The full rebuild and the incremental
  // inserter (insertMissingAccountsDom) must produce byte-identical rows -
  // two copies of the template would drift and the two refresh paths would
  // visibly diverge - so both go through this one builder.
  function ctPcRowsHtml(c, listedSet, listedSig, actByAddr) {
    // 0.3.2 tag 语义反转（boss 认定五）：非从属才是例外——联系人中不在
    // 从属集内的地址打「外部」标（与 listed 并存不互斥）。
    var badge2 = '<span class="badge-ext">' + t("acc.badgeExt") + "</span>" +
      (listedSet[c] ? ' <span class="badge-listed">listed</span>' : "");
    return {
      badge: badge2.trim(),
      main: '<tr class="ct-row" data-act-acct="' + esc(c) + '">' +
        '<td class="addr-cell mq" data-label="' + t("col.address") + '"><span class="pc-av-line">' + accAvatarHtml(c, false) + '<span class="sig-track"><span class="sig-txt">' + esc(c) + '</span><span class="sig-dup" aria-hidden="true">' + esc(c) + '<span class="pc-badges">' + badge2.trim() + "</span></span></td>" +
        '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(listedSig[c] || "") + '</span><span class="sig-dup" aria-hidden="true">' + esc(listedSig[c] || "") + "</span></span></td>" +
        '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action act-compose" data-compose="' + esc(c) + '">' + t("act.compose") + "</button></td>" +
        "</tr>",
      // boss PC round: the latest message runs the FULL row width (one
      // colspan-3 line under the entry), still patched in place by
      // applyActivity via the data-act-acct hook.
      line3: '<tr class="line3-row" data-act-acct="' + esc(c) + '"><td colspan="3"><div class="pc-line3">' + accLatestHtml(actByAddr[String(c).toLowerCase()]) + "</div></td></tr>"
    };
  }
  async function loadAccountsRegular(selfAddr) {
    // The "+ Register new account" button is admin-only.
    const accSecRegular = document.getElementById("tab-accounts");
    if (accSecRegular) accSecRegular.classList.remove("acc-admin");
    const regBtn = $("#btn-register");
    if (regBtn) regBtn.classList.add("hidden");
    const invBtnRegular = $("#btn-invalid");
    if (invBtnRegular) invBtnRegular.classList.add("hidden");
    // PC register-subordinate block above the table (mobile keeps the
    // in-container button; admin sessions never see it).
    const subregPc = $("#subreg-pc");
    if (subregPc) subregPc.classList.remove("hidden");
    // boss 09-29 gate leg 1 (Iris): no clear-then-fill - the stale rows stay
    // visible until the finished build swaps atomically; the early clear
    // turned every fallback rebuild into a bare blank window (tbody 6<->0).
    const tbody = $("#accounts-table tbody");
    // Subordinate management UI lives in Preferences since v0.6; Accounts
    // still needs fresh edges for the sub badges (and read-only rows).
    var subs = await requestSubs(true).catch(function () { return null; });
    // requestSubs resolves null on failure (the subs:request listener
    // catches rejections) - null is the failure signal, distinct from
    // a successful empty list.
    var subsFailed = subs === null;
    var subsList = (subs && subs.subordinates) || [];
    // Rows match the 5-column header (Address, Tags, Signature, Created,
    // Actions) so the Change-password button lands in the Actions column
    // instead of drifting under Tags.
    var subAddrs = {};
    subsList.forEach(function (e) { subAddrs[e.address] = 1; });
    // Own-row completeness (feedback: signature missing, tags thin):
    // pull the own profile for the signature and listed/visible badge.
    var ownSig = "", ownVisible = null;
    try {
      const me = await api("/api/profile/self", { keepSession: true });
      ownSig = me.signature || "";
      ownVisible = me.visible;
    } catch (_) { /* degrade to the old thin row */ }
    // Listed-in-directory set (feedback: the regular view must badge
    // visible accounts the same way the admin view does). Fetch FIRST —
    // listedSig is read when building rows below.
    var listedSet = {}, listedSig = {};
    try {
      const dir = await api("/api/info?query=directory", { keepSession: true });
      (dir.entries || []).forEach(function (e) {
        listedSet[e.address] = 1;
        if (e.signature) listedSig[e.address] = e.signature;
        // 0021 (bug fix: uploaded avatars invisible to other accounts):
        // stash directory avatar_hash so row avatars can cache-bust.
        if (e.avatar_hash) window.__avatarHashes[String(e.address || "").toLowerCase()] = e.avatar_hash;
      });
    } catch (e) { /* non-fatal — badges degrade to sub-only */ }
    acctCtx = { listedSet: listedSet, listedSig: listedSig, subsSet: subAddrs }; // refresh the inserter's view
    var rows = [];
    // 0.3.2 概览重构（boss 认定四）：自身行已撤——自身卡迁偏好页

    // （renderPrefsOwnCard，手机卡样式双端）。

    // Subordinates render TWICE from one pass (superior feedback round 3):
    // PC = leading table rows right after the own row (no container; the
    // register button lives above the table — #subreg-pc in index.html);
    // phones keep the approved container card (agentreg-row below) and hide
    // the PC rows via CSS.
    var clRows = "";
    var actByAddr = {};
    ((actData && actData.subs) || []).forEach(function (x) { actByAddr[String(x.address).toLowerCase()] = x; });
    ((actData && actData.contacts) || []).forEach(function (x) { var k = String(x.address).toLowerCase(); if (!actByAddr[k]) actByAddr[k] = x; });
    var seenAddrs = {};
    var contactsFailed = false, contactRaw = 0;
    // Contacts are fetched BEFORE any row is built (boss 09-29: the whole
    // page shares one ordering, so both sides must be in hand up front).
    var contactList = [];
    try {
      const data = await api("/api/contacts", { keepSession: true });
      contactRaw = (data.contacts || []).length;
      (data.contacts || []).forEach(function (c) {
        if (subAddrs[c]) return; // subordinate entries render from subsList
        seenAddrs[c] = 1;
        contactList.push(c);
      });
    } catch (e) {
      contactsFailed = true; // 0.3.3-C (3): failure must be visible, not silent
    }
    // boss 09-29: ONE ordering for the page - recent interaction time
    // (latest_at desc, stable; untouched rows keep their relative order).
    // Relationship no longer groups the list, so a non-subordinate
    // counterparty can top it. Rows keep their per-type shape (subs:
    // gear/limits; contacts: ext badge) on both PC and the mobile card.
    subsList = subsList.slice().sort(function (a, b) {
      var sa = actByAddr[String(a.address).toLowerCase()] || {};
      var sb = actByAddr[String(b.address).toLowerCase()] || {};
      return (+sb.latest_at || 0) - (+sa.latest_at || 0);
    });
    var entries = [];
    subsList.forEach(function (e) { entries.push({ sub: true, addr: e.address, e: e }); });
    contactList.forEach(function (c) { entries.push({ sub: false, addr: c }); });
    var entryAt = function (addr) { var x = actByAddr[String(addr).toLowerCase()]; return (+(x && x.latest_at)) || 0; };
    entries.sort(function (a, b) { return entryAt(b.addr) - entryAt(a.addr); });
    entries.forEach(function (en) {
      if (en.sub) {
        var e = en.e;
        var sig = e.signature || listedSig[e.address] || "";
        // 0.3.2 tag 语义反转（boss 认定五）：从属是面板主角不打标（listed 照旧）。
        var badge = listedSet[e.address] ? '<span class="badge-listed">listed</span>' : "";
        rows.push(
          '<tr class="subrow-pc" data-act-acct="' + esc(e.address) + '">' +
          '<td class="addr-cell" data-label="' + t("col.address") + '">' +
          // 0019 (boss directive): PC rows carry avatars - same accAvatarHtml
          // payload as the mobile list; CSS scopes it to >800px.
          '<span class="pc-av-line">' + accAvatarHtml(e.address, true) +
          '<span class="pc-addr">' + esc(e.address) + '</span>' +
          '<span class="act-pill-slot" data-act-slot="pill"></span><span class="pc-badges">' + badge + "</span></span></td>" +
          '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(sig) + '</span><span class="sig-dup" aria-hidden="true">' + esc(sig) + "</span></span></td>" +
          '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action act-compose" data-compose="' + esc(e.address) + '">' + t("act.compose") + '</button><button class="row-gear" data-gear="' + esc(e.address) + '" aria-label="' + esc(t("acc.settings")) + '">\u2699</button>' +
          '<div class="gear-pop" hidden><button class="row-action warn" data-remove-sub="' + esc(e.address) + '">' + t("subs.removeBtn") + '</button><button class="row-action" data-limits="' + esc(e.address) + '">' + t("limits.open") + '</button><button class="row-action" data-wl="' + esc(e.address) + '">' + t("wl.title") + "</button></div></td>" +
          "</tr>");
        // boss PC round: the latest message runs the FULL row width (one
        // colspan-3 line under the entry), still patched in place by
        // applyActivity via the data-act-acct hook.
        rows.push(
          '<tr class="line3-row" data-act-acct="' + esc(e.address) + '"><td colspan="3"><div class="pc-line3">' + accLatestHtml(actByAddr[String(e.address).toLowerCase()]) + "</div></td></tr>");
        // Mobile container card (one-screen plan): badges + address share one
        // line (address marquees on overflow), signature max one line (same),
        // pill buttons bottom-right — all inside the scrollable .sub-list.
        clRows += accRowHtml({ addr: e.address, badge: badge, sig: sig, isSub: true, sub: actByAddr[String(e.address).toLowerCase()] });
      } else {
        var c = en.addr;
        // 0.3.2 tag 语义反转（boss 认定五）：非从属才是例外——联系人中不在
        // 从属集内的地址打「外部」标（与 listed 并存不互斥）。纯前端推导
        // （requestSubs 从属集在手），数据面零改动（Devi 已确认口径）。
        // Every address row gets the same shape (feedback: subordinate
        // rows with and without mail history must look identical):
        // badge column, Compose action; Created only where known.
        // The address carries the marquee track for the mobile one-screen
        // plan (phones merge badges+address into one line); the twin card
        // below feeds #acc-m-contacts (the phone-only scrollable list).
        var pcC = ctPcRowsHtml(c, listedSet, listedSig, actByAddr);
        rows.push(pcC.main);
        rows.push(pcC.line3);
        clRows += accRowHtml({ addr: c, badge: pcC.badge, sig: listedSig[c] || "", isSub: false, sub: actByAddr[String(c).toLowerCase()] || null });
      }
    });
    // Register card LAST: with one unified ordering it must not split the
    // interaction-ranked rows (it is a tool, not an account entry).
    rows.push(
      '<tr class="agentreg-row">' +
      '<td colspan="3" class="agentreg-cell">' +
      '<div class="agentreg-card">' +
      '<button id="btn-subreg" class="primary">' + t("subs.registerBtn") + "</button>" +
      '<div class="muted" style="font-size:12px; margin-top:5px;">' + t("subs.registerNote") + "</div>" +
      "</div></td>" +
      "</tr>"
    );    // Subordinate accounts render ONLY inside the register card's zone
    // (approved two-zone layout) — nothing about them joins the main list.
    mqPhase = mqMerge(mqPhase, mqSnap(tbody));
    var avBank = avHarvest(tbody);
    var openGearAddr = "";
    // boss 09-29 (gear pop dies on rebuild): a letter-driven rebuild rewrote
    // the tbody while the popover was open - the click looked dead. Capture
    // the open pop's row and restore it after the rewire (same shape as the
    // settings-card overlay preservation).
    $$(".gear-pop", tbody).forEach(function (x) {
      if (!x.hidden) { var gtr = x.closest("tr"); if (gtr) openGearAddr = String(gtr.getAttribute("data-act-acct") || "").toLowerCase(); }
    });
    tbody.innerHTML = rows.join("");
    avRestore(tbody, avBank);
    avHydrate(tbody); // 0019: PC table avatars - pending generators swap in
    avRemoteHydrate(tbody); // 0021: registry-backed real avatars
    preloadLimits(selfAddr, subsList.map(function (e) { return e.address; }));
    const btn = $("#btn-change-pw");
    if (btn) btn.addEventListener("click", openChangePassword);
    $$("[data-compose]", tbody).forEach(function (b) {
      b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: b.dataset.compose } })); });
    });
    // boss PC round: the gear opens the row popover (remove + limits),
    // mobile-style; one open at a time, click-away closes (wired once).
    $$("[data-gear]", tbody).forEach(function (g) {
      g.addEventListener("click", function (ev) {
        ev.stopPropagation();
        var pop = g.parentElement.querySelector(".gear-pop");
        if (!pop) return;
        var wasHidden = pop.hidden;
        $$(".gear-pop", tbody).forEach(function (x) { x.hidden = true; });
        pop.hidden = !wasHidden;
      });
    });
    if (openGearAddr) {
      $$("tr[data-act-acct]", tbody).forEach(function (tr) {
        if (String(tr.getAttribute("data-act-acct") || "").toLowerCase() === openGearAddr) {
          var gp = tr.querySelector(".gear-pop");
          if (gp) gp.hidden = false;
        }
      });
    }
    if (!window.__gearAwayWired) {
      window.__gearAwayWired = 1;
      document.addEventListener("click", function (ev) {
        if (ev.target.closest && ev.target.closest(".gear-pop, [data-gear]")) return;
        $$(".gear-pop", document).forEach(function (x) { x.hidden = true; });
      });
    }
    // v0.6.5: remove-subordinate buttons (PC rows + mobile cards) — the
    // destructive twin of compose, guarded by a consequence-aware confirm.
    $$("[data-remove-sub]", tbody).forEach(function (b) {
      b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("subs:remove", { detail: { address: b.dataset.removeSub, role: "superior" } })); });
    });

    // Mobile one-screen plan: the phone-only contacts list mirrors the
    // contact rows (which hide via CSS); compose wiring included.
    var ctBox = $("#acc-m-contacts");
    if (ctBox) {
      // 0.3.3-C: pinned register row first, then subs + contacts in one list.
      var regRow = '<div class="im3-row pinned" data-reg>' +
        '<div class="im3-av pin">\uff0b</div>' +
        '<div class="im3-main">' +
        '<div class="im3-l1"><span class="im3-addr im3-title-pin"><span class="im3-addr-in">' + esc(t("acc.regTitle")) + "</span></span></div>" +
        '<div class="im3-line2">' + esc(t("acc.regSub")) + "</div></div></div>";
      // 0.3.3-C (2)(3): failure rows at their source positions; the empty
      // row only when both sources succeeded and came back empty.
      var errSubsRow = subsFailed ? im3StateRowHtml("err", "acc.errSubs", "acc.retryTap", "subs") : "";
      var errContactsRow = contactsFailed ? im3StateRowHtml("err", "acc.errContacts", "acc.retryTap", "contacts") : "";
      var emptyRow = (!subsFailed && !contactsFailed && subsList.length === 0 && contactRaw === 0) ? im3StateRowHtml("empty", "acc.emptyTitle", "acc.emptySub", null) : "";
      mqPhase = mqMerge(mqPhase, mqSnap(ctBox));
      var avBankM = avHarvest(ctBox);
      // boss 09-29 (settings-card flash-close, Iris 8d9963c): the fallback
      // rebuild wiped the open .im3-overlay (its .on lived only in the old
      // DOM) - capture the open card's address and re-apply it after the
      // rewire.
      var onOvl = ctBox.querySelector(".im3-overlay.on");
      var openOvl = onOvl ? onOvl.getAttribute("data-ovl") : null;
      ctBox.innerHTML = regRow + errSubsRow + clRows + errContactsRow + emptyRow;
      avRestore(ctBox, avBankM);
      var regEl = ctBox.querySelector("[data-reg]");
      if (regEl) regEl.addEventListener("click", function () {
        var b = document.getElementById("btn-subreg");
        if (b) b.click();
      });
      accWireList(ctBox);
      if (openOvl) {
        var reOvl = ctBox.querySelector('.im3-overlay[data-ovl="' + openOvl + '"]');
        if (reOvl) reOvl.classList.add("on"); // settings card survives the rebuild
      }
      wireErrRetry(ctBox);
      avHydrate(ctBox);
      avRemoteHydrate(ctBox); // 0021: registry-backed real avatars
      im3MarqueeScan(ctBox);
      mqApply(ctBox, mqPhase); // rescan done - mobile tracks can take their clocks back now
    }
    // 自身卡 → 偏好页（0.3.2 认定四）；活动槽有缓存则即时回填。

    renderPrefsOwnCard(ownSig, ownVisible);

    // Fresh dots on return: pull immediately instead of waiting for the

    // next 5s tick, so a visited conversation clears its dot in ~1s.

    pullActivity();

    applyActivity();

  }

  // composeTo switches to the Compose tab and prefills the To field with the
  // given address, then loads that thread. Used by the Compose buttons on the
  // Accounts and Directory tables.
  function openChangePassword() {
    const oldPw = prompt("Change your password\n\nCurrent password:");
    if (oldPw === null) return;
    const newPw = prompt("New password (min 8 chars):");
    if (newPw === null) return;
    if (newPw.length < 8) { toast("New password must be at least 8 chars", "error"); return; }
    api("/api/password", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ old_password: oldPw, new_password: newPw }),
    }).then(function () {
      toast("Password changed — please log in again");
      // Credentials changed: update the cached password so the next login works
      // seamlessly, then force re-login to confirm the new password.
      // v0.6.27: invalidate token too (password change kills old token).
      const s = getSession();
      if (s) { s.password = newPw; setSession(s); }
      localStorage.removeItem("agentmail_token");
      setTimeout(function () { setSession(null); showLogin(); }, 1500);
    }).catch(function (e) {
      toast("Change failed: " + e.message, "error");
    });
  }

  async function resetPassword(address) {
    if (!address) return;
    const input = prompt(
      "Reset password for " + address + "\n\n" +
      "Enter a new password (min 8 chars), or leave blank for a random one.\n" +
      "The old password becomes invalid immediately."
    );
    // prompt returns null on Cancel; "" on empty submit (random).
    if (input === null) return;
    if (!confirm("Confirm: reset password for " + address + "?")) return;

    const box = $("#register-result");
    box.classList.add("hidden");
    try {
      const body = { account: address };
      if (input.trim() !== "") body.new_password = input;
      const res = await api("/admin/reset-password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      box.className = "callout success";
      box.innerHTML =
        "<b>Reset password for:</b> " + esc(res.account) + "<br>" +
        "<b>New password (shown once):</b> <code>" + esc(res.password) + "</code><br>" +
        "<small>Copy this now and hand it to the account owner; it will not be shown again.</small>";
      box.classList.remove("hidden");
      toast("Password reset");
    } catch (e) {
      box.className = "callout error";
      box.textContent = t("common.error", { msg: e.message });
      box.classList.remove("hidden");
    }
  }

  async function setDisabled(address, disabled) {
    if (!address) return;
    const verb = disabled ? "Disable" : "Enable";
    if (!confirm(verb + " account " + address + "? " +
        (disabled ? "It will not be able to send or read mail until re-enabled." : "It will be able to send and read mail again."))) return;
    try {
      await api("/admin/set-disabled", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ account: address, disabled: disabled }),
      });
      toast((disabled ? "Disabled " : "Enabled ") + address);
      loadAccounts(); // refresh list (re-sorts: disabled sink to bottom)
    } catch (e) {
      toast("Error: " + e.message, "error");
    }
  }

  // The Accounts-tab register button is a subordinate-registration entry
  // (superior 09-02 ruling: accounts-page registration is subordinate-only;
  // normal registration lives only on the login/portal pages). The flow is
  // owned by manage.js — request it via the S2 event.
  $("#btn-register").addEventListener("click", function () {
    document.dispatchEvent(new CustomEvent("subs:register"));
  });

  // ---- admin: invalid-letter inspector (开工令 01M1FSKAD) ----
  // Strict-delete ruling: real DB removal, so the UI gates every deletion
  // behind a red-warning confirm state plus an irreversibility checkbox.
  var invalidPending = null; // { ids: [...] } built when the confirm state opens
  function openInvalidModal() {
    $("#invalid-modal").classList.remove("hidden");
    $("#invalid-confirm").classList.add("hidden");
    $("#invalid-ack").checked = false;
    $("#invalid-status").textContent = t("common.loading");
    api("/admin/invalid").then(function (d) {
      renderInvalidList(Array.isArray(d) ? d : (d && d.messages) || []);
      $("#invalid-status").textContent = "";
    }).catch(function (e) {
      $("#invalid-status").textContent = t("common.error", { msg: e.message });
    });
  }
  function renderInvalidList(msgs) {
    var box = $("#invalid-list");
    if (!msgs.length) {
      box.innerHTML = '<div class="muted" style="padding:10px;">' + t("inv.empty") + "</div>";
      return;
    }
    box.innerHTML = '<div class="inv-head"><span></span><span>' + t("inv.from") + '</span><span>' + t("inv.subject") + '</span><span>' + t("inv.toInvalid") + '</span><span>' + t("inv.time") + "</span></div>" +
      msgs.map(function (m) {
        return '<label class="inv-row">' +
          '<input type="checkbox" class="inv-check" data-id="' + esc(m.id) + '" />' +
          '<span class="inv-from">' + esc(m.from || "") + "</span>" +
          '<span class="inv-subj">' + esc(m.subject || "") + "</span>" +
          '<span class="inv-to">' + esc(m.to || "") + "</span>" +
          '<span class="inv-time">' + fmtTime(m.received_at) + "</span>" +
          "</label>";
      }).join("");
  }
  function closeInvalidModal() {
    $("#invalid-modal").classList.add("hidden");
    $("#invalid-confirm").classList.add("hidden");
    invalidPending = null;
  }
  $("#btn-invalid").addEventListener("click", openInvalidModal);
  $("#btn-invalid-close").addEventListener("click", closeInvalidModal);
  $("#invalid-modal").addEventListener("click", function (e) {
    if (e.target === this) closeInvalidModal();
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    var m = $("#invalid-modal");
    if (m && !m.classList.contains("hidden")) closeInvalidModal();
  });
  function askInvalidDelete(all) {
    var ids = $$(".inv-check:checked").map(function (c) { return c.dataset.id; });
    if (!all && !ids.length) {
      $("#invalid-status").textContent = t("inv.needSel");
      return;
    }
    invalidPending = { ids: all ? [] : ids, all: !!all };
    $("#invalid-confirm").classList.remove("hidden");
    $("#invalid-ack").checked = false;
    $("#btn-invalid-confirm").disabled = true;
    $("#invalid-status").textContent = "";
  }
  $("#btn-invalid-delchecked").addEventListener("click", function () { askInvalidDelete(false); });
  $("#btn-invalid-delall").addEventListener("click", function () { askInvalidDelete(true); });
  $("#invalid-ack").addEventListener("change", function () {
    $("#btn-invalid-confirm").disabled = !this.checked;
  });
  $("#btn-invalid-cancel").addEventListener("click", function () {
    $("#invalid-confirm").classList.add("hidden");
    invalidPending = null;
  });
  $("#btn-invalid-confirm").addEventListener("click", async function () {
    if (!invalidPending) return;
    var btn = this;
    btn.disabled = true;
    $("#invalid-status").textContent = t("common.loading");
    try {
      var body = invalidPending.all ? { all: true } : { ids: invalidPending.ids };
      var res = await api("/admin/invalid", { method: "DELETE", body: JSON.stringify(body) });
      $("#invalid-confirm").classList.add("hidden");
      invalidPending = null;
      var doneMsg = t("inv.deleted", { n: (res && res.deleted) || 0 });
      // Refresh the list in place; the status line survives the reload.
      api("/admin/invalid").then(function (d) {
        renderInvalidList(Array.isArray(d) ? d : (d && d.messages) || []);
        $("#invalid-status").textContent = doneMsg;
      }).catch(function () {
        $("#invalid-status").textContent = doneMsg;
      });
    } catch (e) {
      btn.disabled = false;
      $("#invalid-status").textContent = t("common.error", { msg: e.message });
    }
  });

  // ---- directory (public address book) ----

  async function loadDirectory() {
    const tbody = $("#directory-table tbody");
    tbody.innerHTML = '<tr><td colspan="3">Loading…</td></tr>';
    try {
      const data = await api("/api/info?query=directory");
      const entries = data.entries || [];
      if (!entries.length) {
        tbody.innerHTML = '<tr><td colspan="3">No visible accounts yet.</td></tr>';
        return;
      }
      tbody.innerHTML = entries.map(function (e) {
        return "<tr>" +
          '<td class="addr-cell">' + esc(e.address) + "</td>" +
          '<td class="sig-cell"><span class="sig-track"><span class="sig-txt">' + esc(e.signature || "") + '</span><span class="sig-dup" aria-hidden="true">' + esc(e.signature || "") + "</span></span></td>" +
          '<td class="actions-cell"><button class="row-action" data-compose="' + esc(e.address) + '">' + t("act.compose") + '</button></td>' +
          "</tr>";
      }).join("");
      $$("[data-compose]", tbody).forEach(function (btn) {
        btn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: btn.dataset.compose } })); });
      });
      maybeMarqueeSigs();
    } catch (e) {
      tbody.innerHTML = '<tr><td colspan="3">Error: ' + esc(e.message) + "</td></tr>";
    }
  }

  // ---- profile (edit your own visibility + signature) ----

  // ---- user preferences (v0.6) ----
  // Read order: server account.prefs > localStorage fallback > defaults.
  // Cached in memory: message rendering consults it without a request.
  const PREFS_DEFAULTS = { audio_autoplay: false, image_preview: true, body_markdown: false, livenessWeakHours: 48, livenessStrongHours: 24 };
  let userPrefs = null;
  const PREFS_LS_KEY = "agentmail_prefs";

  function loadPrefsLocal() {
    try { return JSON.parse(localStorage.getItem(PREFS_LS_KEY) || "null"); }
    catch (_) { return null; }
  }

  function numHours(v, fallback) {
    return (typeof v === "number" && v > 0 && v <= 8760) ? v : fallback;
  }

  function mergePrefs(serverPrefs) {
    const local = loadPrefsLocal() || {};
    const src = serverPrefs || {};
    userPrefs = {
      audio_autoplay: typeof src.audio_autoplay === "boolean" ? src.audio_autoplay
        : (typeof local.audio_autoplay === "boolean" ? local.audio_autoplay : PREFS_DEFAULTS.audio_autoplay),
      image_preview: typeof src.image_preview === "boolean" ? src.image_preview
        : (typeof local.image_preview === "boolean" ? local.image_preview : PREFS_DEFAULTS.image_preview),
      body_markdown: typeof src.body_markdown === "boolean" ? src.body_markdown
        : (typeof local.body_markdown === "boolean" ? local.body_markdown : PREFS_DEFAULTS.body_markdown),
      livenessStrongHours: numHours(src["liveness.strongHours"],
        numHours(local["liveness.strongHours"], PREFS_DEFAULTS.livenessStrongHours)),
      livenessWeakHours: numHours(src["liveness.weakHours"],
        numHours(local["liveness.weakHours"], PREFS_DEFAULTS.livenessWeakHours)),
    };
    return userPrefs;
  }

  async function savePrefs() {
    const strongEl = $("#pref-liveness-strong"), weakEl = $("#pref-liveness-weak");
    // Empty inputs (settings panel not yet populated) fall back to the
    // defaults instead of aborting the whole save — superior staging repro:
    // the body_markdown toggle silently never saved on a fresh panel.
    const strong = strongEl ? (strongEl.value === "" ? PREFS_DEFAULTS.livenessStrongHours : parseInt(strongEl.value, 10)) : NaN;
    const weak = weakEl ? (weakEl.value === "" ? PREFS_DEFAULTS.livenessWeakHours : parseInt(weakEl.value, 10)) : NaN;
    const status = $("#prefs-status");
    if (strongEl && (!isFinite(strong) || strong < 1 || strong > 8760) ||
        weakEl && (!isFinite(weak) || weak < 1 || weak > 8760)) {
      status.textContent = t("prefs.livenessBad");
      return;
    }
    const poolEl = $("#pref-thread-pool");
    if (poolEl && poolEl.value !== "") {
      const pool = parseInt(poolEl.value, 10);
      if (!isFinite(pool) || pool < 1) { // boss 1002: no upper bound
        status.textContent = t("prefs.poolBad");
        return;
      }
      try { localStorage.setItem("compose_thread_pool_max", String(pool)); } catch (_) {}
    }
    const prefs = {
      audio_autoplay: !!$("#pref-audio-autoplay").checked,
      image_preview: !!$("#pref-image-preview").checked,
      body_markdown: !!$("#pref-body-markdown").checked,
      "liveness.strongHours": strong,
      "liveness.weakHours": weak,
    };
    try {
      // The server REPLACES (not merges) profile fields on POST — a
      // prefs-only body wipes the signature (bug report: signatures
      // disappeared). Round-trip the current fields until the server
      // learns per-field merge semantics.
      const cur = await api("/api/profile/self", { keepSession: true }).catch(function () { return null; });
      const body = { prefs: prefs };
      if (cur) {
        if (typeof cur.signature === "string") body.signature = cur.signature;
        if (typeof cur.visible === "boolean") body.visible = cur.visible;
      }
      const res = await api("/api/profile/self", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      status.textContent = t("prefs.saved");
      // The response echoes the merged prefs — authoritative post-save
      // state (e.g. null resets applied server-side).
      if (res && res.prefs) mergePrefs(res.prefs);
      // Threshold changes recolor the overview dots.
      document.dispatchEvent(new CustomEvent("manage:refresh"));
    } catch (e) {
      // Older server (no prefs field): keep the choice browser-local so
      // the toggles still work for this user.
      try { localStorage.setItem(PREFS_LS_KEY, JSON.stringify(prefs)); } catch (_) {}
      status.textContent = t("prefs.savedLocal");
    }
    userPrefs = mergePrefs(prefs);
  }
  (function wirePrefs() {
    const btn = $("#btn-save-prefs");
    if (btn) btn.addEventListener("click", savePrefs);
    const zh = $("#pref-lang-zh"), en = $("#pref-lang-en");
    if (zh) zh.addEventListener("click", function () { window.I18N.setLang("zh"); });
    if (en) en.addEventListener("click", function () { window.I18N.setLang("en"); });
    // Setup-page language pill (superior 09-01): same setLang path as the
    // header toggle, so the choice persists into the setup wizard.
    (function () {
      var tgl = document.getElementById("setup-lang");
      if (!tgl) return;
      tgl.addEventListener("click", function (ev) {
        var seg = ev.target.closest ? ev.target.closest("[data-seg]") : null;
        if (seg) window.I18N.setLang(seg.dataset.seg);
      });
      function syncSetupLang() {
        var cur = "";
        try { cur = window.I18N.lang(); } catch (_) { return; }
        tgl.querySelectorAll(".seg").forEach(function (s) {
          s.classList.toggle("on", s.dataset.seg === cur);
        });
      }
      syncSetupLang();
      document.addEventListener("i18n:change", syncSetupLang);
    })();
    // v0.6.27 three-way theme switch (preferences page only, superior ruling).
    $$(".pref-theme-btn").forEach(function (btn) {
      btn.addEventListener("click", function () {
        applyTheme(btn.dataset.themepick);
      });
    });
  })();
  // v0.1.3 site copy (admin-only): three faces × zh/en → PUT /admin/site-copy
  // (all six keys submitted on every save; an empty value clears the override
  // so the built-in default shows through again — alice 01M18GRC5; ≤200 chars/key).
  // Card visibility gates on the authoritative session.is_admin.
  (function wireSiteCopy() {
    const scCard = $("#sitecopy-card");
    const scSave = $("#btn-sitecopy-save");
    const scHint = $("#sitecopy-hint");
    if (!scCard || !scSave) return;
    const scKeys = [
      ["sc-tagline-zh", "portal_tagline_zh"], ["sc-tagline-en", "portal_tagline_en"],
      ["sc-ptitle-zh", "portal_title_zh"], ["sc-ptitle-en", "portal_title_en"],
      ["sc-ntitle-zh", "panel_title_zh"], ["sc-ntitle-en", "panel_title_en"],
    ];
    let unlocked = false;
    function scPrefill() {
      api("/api/site-copy", { keepSession: true }).then(function (d) {
        scKeys.forEach(function (kv) {
          const el = $("#" + kv[0]);
          if (el && d && d[kv[1]]) el.value = d[kv[1]];
        });
      }, function () { /* older server: keep placeholders */ });
    }
    // Admin gate, idempotent. Module load covers the reload-while-logged-in
    // case; the Settings-tab listener covers the fresh-login case where the
    // session only appears AFTER module init (superior bug: the card never
    // showed in the login session, only from the second visit / reload).
    function unlock() {
      if (unlocked) return;
      const s = getSession();
      if (!s || !s.is_admin) return;
      unlocked = true;
      scCard.hidden = false;
      scPrefill();
    }
    unlock();
    const settingsTab = document.querySelector('.tab[data-tab="settings"]');
    if (settingsTab) settingsTab.addEventListener("click", unlock);
    scSave.addEventListener("click", async function () {
      scSave.disabled = true;
      scHint.textContent = "";
      // alice 01M18GRC5 修单：置空=恢复默认——所有键都提交，空值由后端删除
      // 覆盖回落内置默认（不再「留空=保留」）。
      const body = {};
      scKeys.forEach(function (kv) {
        const el = document.getElementById(kv[0]);
        body[kv[1]] = (el && el.value.trim()) || "";
      });
      try {
        await api("/admin/site-copy", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        scHint.textContent = t("sitecopy.saved");
      } catch (e) {
        scHint.textContent = t("common.error", { msg: e.message });
      }
      scSave.disabled = false;
    });
  })();

  // Theme: "light"/"dark" pin html[data-theme]; "system" removes the attr so
  // the prefers-color-scheme media query rules again. Persisted locally.
  const THEME_KEY = "theme";
  // meta[name=theme-color] drives the browser tab bar / Android status bar in
  // the installed PWA (manifest theme_color is static; this is dynamic). Keep
  // it in lock-step with the picked theme so the frame matches the page.
  const THEME_HEX = { light: "#f6f7f9", dark: "#0f1115" };
  function currentThemePick() {
    // Superior hard rule: default = LIGHT (not system) — light is the
    // polished path; system-follow would drop dark-OS users into it.
    try { return localStorage.getItem(THEME_KEY) || "light"; } catch (_) { return "light"; }
  }
  function syncThemeColorMeta() {
    const pick = currentThemePick();
    const dark = pick === "dark" ||
      (pick === "system" && window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches);
    let meta = document.querySelector('meta[name="theme-color"]');
    if (!meta) { meta = document.createElement("meta"); meta.name = "theme-color"; document.head.appendChild(meta); }
    meta.content = THEME_HEX[dark ? "dark" : "light"];
  }
  // System-mode users still get a correct frame when the OS flips.
  if (window.matchMedia) {
    try {
      matchMedia("(prefers-color-scheme: dark)").addEventListener("change", function () {
        if (currentThemePick() === "system") syncThemeColorMeta();
      });
    } catch (_) {}
  }
  function applyTheme(pick) {
    try { localStorage.setItem(THEME_KEY, pick); } catch (_) {}
    if (pick === "light" || pick === "dark") document.documentElement.dataset.theme = pick;
    else delete document.documentElement.dataset.theme;
    syncThemeColorMeta();
    syncPrefThemeUI();
  }
  function syncPrefThemeUI() {
    const cur = currentThemePick();
    $$(".pref-theme-btn").forEach(function (btn) {
      btn.classList.toggle("active", btn.dataset.themepick === cur);
    });
  }
  // Apply saved theme before first paint of the app shell. Default=LIGHT:
  // set the attribute explicitly so first paint is light even on dark-OS.
  try {
    const savedTheme = localStorage.getItem(THEME_KEY);
    document.documentElement.dataset.theme =
      (savedTheme === "dark") ? "dark" : "light";   // light default; system/dark still selectable
  } catch (_) { document.documentElement.dataset.theme = "light"; }
  syncThemeColorMeta();
  // Language buttons reflect the one shared setting (localStorage via
  // I18N.setLang): the current language is highlighted on load and on
  // every switch — header toggle included (feedback: must read as linked).
  function syncPrefLangUI() {
    const cur = window.I18N.lang();
    const zh = $("#pref-lang-zh"), en = $("#pref-lang-en"), now = $("#pref-lang-now");
    if (zh) zh.classList.toggle("active", cur === "zh");
    if (en) en.classList.toggle("active", cur === "en");
    if (now) now.textContent = t("prefs.langNow", { lang: cur === "zh" ? t("prefs.langZh") : t("prefs.langEn") });
    // Segmented pill toggles (header + portal): mark the active segment.
    $$(".lang-toggle").forEach(function (btn) {
      $$(".seg", btn).forEach(function (seg) {
        seg.classList.toggle("on", seg.dataset.seg === cur);
      });
    });
  }
  document.addEventListener("i18n:change", syncPrefLangUI);
  // Prefs must exist BEFORE any message renders (autoplay queue consults
  // them): seed from localStorage synchronously; the server value refines
  // it right after login (bug: fresh sessions never autoplayed because
  // userPrefs stayed null until the Preferences tab was visited).
  mergePrefs(null);

  async function loadProfile() {
    const status = $("#profile-status");
    status.textContent = t("common.loading");
    status.className = "muted";
    try {
      const p = await api("/api/profile/self");
      $("#profile-visible").checked = !!p.visible;
      $("#profile-signature").value = p.signature || "";
      renderPrefsOwnCard(p.signature || "", !!p.visible); // 0.3.2：偏好页自身卡

      status.textContent = "";
      // Preferences toggles (v0.6): server prefs win, local fallback.
      mergePrefs(p.prefs);
      $("#pref-audio-autoplay").checked = userPrefs.audio_autoplay;
      $("#pref-image-preview").checked = userPrefs.image_preview;
      $("#pref-body-markdown").checked = userPrefs.body_markdown === true;
      const lvS = $("#pref-liveness-strong"), lvW = $("#pref-liveness-weak");
      if (lvS) lvS.value = userPrefs.livenessStrongHours;
      if (lvW) lvW.value = userPrefs.livenessWeakHours;
      const poolEl2 = $("#pref-thread-pool");
      if (poolEl2) {
        var pv = parseInt(localStorage.getItem("compose_thread_pool_max") || "0", 10);
        poolEl2.value = (pv >= 1) ? pv : 50; // boss 1002: default 50, unbounded
      }
      syncPrefLangUI();
      syncPrefThemeUI();
      // Subordinate settings section (moved in from Accounts): regular
      // accounts only.
      const s = getSession();
      const subsWrap = $("#subs-section-wrap");
      if (subsWrap) subsWrap.classList.toggle("hidden", !!(s && s.is_admin));
      if (s && !s.is_admin) requestSubs(true).catch(function () {});
      // Attachment quota row retired (superior feedback): capacity moved to
      // the Overview "My activity" attach column, cap from server settings.
    } catch (e) {
      status.textContent = t("common.error", { msg: e.message });
    }
  }

  async function saveProfile() {
    const status = $("#profile-status");
    const btn = $("#btn-save-profile");
    btn.disabled = true;
    status.textContent = t("set.saving");
    status.className = "muted";
    try {
      const body = {
        visible: $("#profile-visible").checked,
        signature: $("#profile-signature").value,
      };
      const res = await api("/api/profile/self", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      $("#profile-signature").value = res.signature || "";
      status.textContent = t("set.saved");
      toast("Profile saved");
    } catch (e) {
      status.textContent = t("common.error", { msg: e.message });
    } finally {
      btn.disabled = false;
    }
  }

  $("#btn-refresh-directory").addEventListener("click", loadDirectory);
  var rbtnDirM = $("#btn-refresh-directory-m"); // capsule-side round refresh (phones)
  if (rbtnDirM) rbtnDirM.addEventListener("click", loadDirectory);
  $("#btn-save-profile").addEventListener("click", saveProfile);

  // ---- settings ----

  // Showcase per-item removal — search-style (feedback): admin enters an id,
  // Find fetches it (GET /admin/showcase-item?id=), the preview shows the
  // letter, Delete removes it (POST /admin/delete-showcase-item) and clears
  // the preview. 404 reports "id not found".
  let showcaseFoundId = null;

  function renderShowcaseItemPreview(m) {
    const prev = $("#showcase-item-preview");
    const del = $("#btn-delete-showcase-item");
    showcaseFoundId = (m && m.id) || null;
    if (!m) {
      prev.innerHTML = "";
      if (del) del.classList.add("hidden");
      return;
    }
    // The endpoint returns received_at (not ts) and omits body — accept both.
    const ts = m.ts || m.received_at;
    prev.innerHTML = '<div class="sc-item" style="cursor:default;margin-top:8px;">' +
      '<div class="sc-meta">' + esc(m.from) + (ts ? " · " + esc(fmtTime(ts)) : "") + "</div>" +
      '<div class="sc-subj">' + esc(m.subject) + "</div>" +
      (m.body ? '<div class="muted" style="font-size:12px;">' + esc(m.body) + "</div>" : "") +
      "</div>";
    if (del) del.classList.remove("hidden");
  }

  $("#btn-search-showcase-item").addEventListener("click", async function () {
    const id = ($("#showcase-id-input").value || "").trim();
    const btn = $("#btn-search-showcase-item");
    if (!id) { renderShowcaseItemPreview(null); return; }
    btn.disabled = true;
    try {
      const res = await api("/admin/showcase-item?id=" + encodeURIComponent(id));
      // Accept both {item:{...}} and a flat item object.
      const m = (res && res.item) || res;
      renderShowcaseItemPreview(m && m.id ? m : null);
      if (!m || !m.id) toast(t("toast.idNotFound"), "error");
    } catch (e) {
      renderShowcaseItemPreview(null);
      toast(/404|not found/i.test(e.message || "") ? "id not found" : "Search failed: " + e.message, "error");
    }
    btn.disabled = false;
  });

  $("#btn-delete-showcase-item").addEventListener("click", async function () {
    if (!showcaseFoundId) return;
    const btn = $("#btn-delete-showcase-item");
    btn.disabled = true;
    try {
      await api("/admin/delete-showcase-item", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: showcaseFoundId }),
      });
      $("#showcase-id-input").value = "";
      renderShowcaseItemPreview(null);
      toast(t("toast.letterRemoved"), "success");
    } catch (e) {
      toast("Delete failed: " + e.message, "error");
    }
    btn.disabled = false;
  });

  async function loadSettings() {
    // 0019: admin endpoint, admin role only. A regular user visiting the
    // Settings tab used to take the /admin/settings 401 WITHOUT keepSession
    // - the api teardown path cleared the session and scheduled the login
    // screen, so the next Accounts render came up unauthorized/empty.
    const sess = getSession();
    if (sess && !sess.is_admin) return;
    try {
      const s = await api("/admin/settings");
      const regStatus = $("#reg-status");
      const regBtn = $("#btn-toggle-registration");
      if (s.registration_enabled) {
        regStatus.textContent = t("set.regOpen");
        regBtn.textContent = t("set.regDisable");
      } else {
        regStatus.textContent = t("set.regClosed");
        regBtn.textContent = t("set.regEnable");
      }
      regBtn.classList.remove("hidden");

      // Directory-listed toggle.
      const listedStatus = $("#listed-status");
      const listedBtn = $("#btn-toggle-listed");
      if (s.directory_listed_enabled) {
        listedStatus.textContent = t("set.listedOpen");
        listedBtn.textContent = t("set.listedDisable");
      } else {
        listedStatus.textContent = t("set.listedClosed");
        listedBtn.textContent = t("set.listedEnable");
      }
      listedBtn.classList.remove("hidden");

      $("#send-rate-input").value = s.send_rate;
      $("#byte-rate-input").value = Math.round(s.byte_rate / 1048576 * 100) / 100; // bytes → MB
      $("#register-rate-input").value = s.register_rate;

      // Attachment storage limits (MB).
      if (s.file_quota_per_acct != null) $("#files-quota-input").value = Math.round(s.file_quota_per_acct / 1048576);
      if (s.files_total_limit != null) $("#files-total-input").value = Math.round(s.files_total_limit / 1048576);
      // Danmaku defaults (v0.4.10). Absent fields keep the built-in default.
      if (s.danmaku_default_mode) $("#dm-default-mode").value = s.danmaku_default_mode;
      if (s.danmaku_default_speed) $("#dm-default-speed").value = s.danmaku_default_speed;
      if (s.danmaku_default_count) $("#dm-default-count").value = s.danmaku_default_count;
      // Random (passwordless) registration debug toggle (retired feature).
      const rrStatus = $("#randomreg-status");
      const rrBtn = $("#btn-toggle-randomreg");
      if (rrStatus && rrBtn) {
        rrStatus.textContent = s.random_register_enabled ? t("set.randomOn") : t("set.randomOff");
        rrBtn.textContent = s.random_register_enabled ? t("set.randomDisable") : t("set.randomEnable");
        rrBtn.classList.remove("hidden");
      }
    } catch (e) {
      $("#reg-status").textContent = t("common.error", { msg: e.message });
    }
  }

  // Save attachment limits (v0.5.7): MB in the UI, bytes on the wire.
  $("#btn-save-files").addEventListener("click", async function () {
    const status = $("#files-status");
    const btn = $("#btn-save-files");
    const quota = parseInt($("#files-quota-input").value, 10);
    const total = parseInt($("#files-total-input").value, 10);
    if (!quota || quota < 1 || !total || total < 1) { status.textContent = "Enter MB values (>= 1)."; return; }
    btn.disabled = true;
    status.textContent = t("set.saving");
    try {
      await api("/admin/set-limits", { // file limits ride set-limits (fields identical)
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ file_quota_per_acct: quota * 1048576, files_total_limit: total * 1048576 }),
      });
      status.textContent = t("set.saved");
      toast(t("toast.saved"), "success");
    } catch (e) {
      status.textContent = t("common.error", { msg: e.message });
    }
    btn.disabled = false;
  });

  // Save danmaku site defaults (v0.4.10): visitors who haven't set their own
  // preference start from these.
  $("#btn-save-danmaku").addEventListener("click", async function () {
    const status = $("#danmaku-admin-status");
    const btn = $("#btn-save-danmaku");
    btn.disabled = true;
    status.textContent = t("set.saving");
    try {
      await api("/admin/set-danmaku", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          mode: $("#dm-default-mode").value,
          speed: $("#dm-default-speed").value,
          count: $("#dm-default-count").value,
        }),
      });
      status.textContent = t("set.saved");
      toast(t("toast.saved"), "success");
    } catch (e) {
      status.textContent = "Save failed: " + e.message;
      toast(t("toast.saveFailed"), "error");
    }
    btn.disabled = false;
  });

  $("#btn-toggle-registration").addEventListener("click", async function () {
    try {
      const cur = await api("/admin/settings");
      const next = !cur.registration_enabled;
      await api("/admin/set-registration", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      });
      toast(next ? "Registration enabled" : "Registration disabled");
      loadSettings();
    } catch (e) {
      toast("Error: " + e.message, "error");
    }
  });

  $("#btn-toggle-randomreg").addEventListener("click", async function () {
    try {
      const cur = await api("/admin/settings");
      const next = !cur.random_register_enabled;
      await api("/admin/set-random-register", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      });
      toast(next ? t("set.randomOnToast") : t("set.randomOffToast"));
      loadSettings();
    } catch (e) {
      toast(t("common.error", { msg: e.message }), "error");
    }
  });

  $("#btn-toggle-listed").addEventListener("click", async function () {
    try {
      const cur = await api("/admin/settings");
      const next = !cur.directory_listed_enabled;
      await api("/admin/set-directory-listed", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      });
      toast(next ? "Directory listing enabled" : "Directory listing disabled");
      loadSettings();
    } catch (e) {
      toast("Error: " + e.message, "error");
    }
  });

  // Clear showcase (v0.4.5): wipe every public letter from the portal.
  // Irreversible, so confirm first; the result line reports how many went.
  $("#btn-clear-showcase").addEventListener("click", async function () {
    if (!window.confirm("Remove ALL public letters from the portal? This cannot be undone.")) return;
    const status = $("#showcase-admin-status");
    const btn = $("#btn-clear-showcase");
    btn.disabled = true;
    status.textContent = t("set.clearing");
    try {
      const res = await api("/admin/clear-showcase", { method: "POST" });
      const n = (res && (res.cleared != null ? res.cleared : res.count)) || 0;
      status.textContent = t("set.clearedN", { n: n });
      toast(t("toast.showcaseCleared", { n: n }), "success");
    } catch (e) {
      status.textContent = "Clear failed: " + e.message;
      toast(t("toast.clearFailed"), "error");
    }
    btn.disabled = false;
  });

  $("#btn-save-limits").addEventListener("click", async function () {
    const sendRate = parseInt($("#send-rate-input").value, 10);
    const byteMB = parseFloat($("#byte-rate-input").value);
    const byteRate = Math.round(byteMB * 1048576);
    const registerRate = parseInt($("#register-rate-input").value, 10);
    if (!sendRate || sendRate < 1 || !byteRate || byteRate < 1 ||
        isNaN(registerRate) || registerRate < 0) {
      $("#limits-status").textContent = "Invalid values";
      return;
    }
    try {
      await api("/admin/set-limits", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ send_rate: sendRate, byte_rate: byteRate, register_rate: registerRate }),
      });
      $("#limits-status").textContent = "✓ Saved";
      toast("Limits saved");
    } catch (e) {
      $("#limits-status").textContent = t("common.error", { msg: e.message });
    }
  });

  // ---- init ----

  // Check initialization state; show setup wizard, login page, or app.
  async function init() {
    // i18n (v0.4.12): apply the detected language to static text before the
    // first paint settles, then keep dynamic regions in sync on switch.
    if (window.I18N) {
      window.I18N.applyI18nDOM(document);
      const toggleLang = function () {
        window.I18N.setLang(window.I18N.lang() === "zh" ? "en" : "zh");
      };
      const panelBtn = $("#btn-lang");
      if (panelBtn) panelBtn.addEventListener("click", toggleLang);
      const portalBtn = $("#btn-portal-lang");
      if (portalBtn) portalBtn.addEventListener("click", toggleLang);
      // Initial segment state for both pill toggles (portal shows pre-login).
      syncPrefLangUI();
      document.addEventListener("i18n:change", function () {
        // Re-render whatever view is active so JS-built text follows.
        if (!$("#portal-page").classList.contains("hidden")) loadPortal();
        else if (!$("#app-header").classList.contains("hidden")) {
          const active = $(".tab.active");
          if (active) activateTab(active.dataset.tab);
        }
      });
      // Site copy (v0.1.2): admin-configurable brand text; public endpoint,
      // so it covers the guest portal too. Failure = built-in defaults.
      fetch("/api/site-copy").then(function (r) { return r.ok ? r.json() : null; })
        .then(function (sc) {
          if (!sc || !window.I18N.setSiteCopy) return;
          window.I18N.setSiteCopy(sc);
          window.I18N.applyI18nDOM(document);
        })
        .catch(function () {});
    }
    try {
      const st = await api("/api/status");
      if (st.domain) systemDomain = st.domain;
      if (st.version) {
        const v = "v" + st.version.replace(/^v/, "");
        $("#version-badge").textContent = v;
        const pv = $("#portal-version");
        if (pv) pv.textContent = v;
      }
      if (!st.initialized) {
        showSetup();
        return;
      }
      // Initialized: if we have cached creds, verify them; else show the
      // guest portal (public overview) — login is reachable from there.
      if (getSession()) {
        try {
          const me = await api("/api/account/info?query=self");
          // Refresh the cached role in case it changed server-side, and
          // write it back into the remember-me token (v0.1.3: the token
          // used to render every session as regular).
          const s = getSession(); s.is_admin = !!me.is_admin; setSession(s);
          updateTokenRole(me.is_admin);
      maybeMarqueeWhoami(); // role suffix changes text width (01M1836CAK)
          showApp(me.is_admin);
          // Same landing rule as the login path (boss directive): on phones
          // refresh lands on the Accounts page too.
          activateTab(window.matchMedia && window.matchMedia("(max-width: 800px)").matches ? "accounts" : "overview");
        } catch (e) {
          // Verification failed (401 already cleared session + showed login).
          showLogin();
        }
      } else {
        showPortal();
      }
    } catch (e) {
      // If /api/status itself fails, show login (server may be mid-restart).
      showLogin();
    }
  }

  function hideAllScreens() {
    $("#setup-page").classList.add("hidden");
    $("#login-page").classList.add("hidden");
    $("#portal-page").classList.add("hidden");
    $("#app-header").classList.add("hidden");
    document.querySelector("main").classList.add("hidden");
    // Portal decorations are body-level; drop them whenever we leave a view.
    $$(".portal-particle").forEach(function (el) { el.remove(); });
  }

  function showSetup() {
    hideAllScreens();
    $("#setup-page").classList.remove("hidden");
  }

  // ---- guest portal (public landing page) ----

  // showPortal is the landing screen for guests (no cached credentials).
  // It shows public data only: stats, message growth, the directory, and
  // entry points to login/register. No authenticated call is made.
  function showPortal() {
    hideAllScreens();
    $("#portal-page").classList.remove("hidden");
    loadPortal();
  }

  // loadPortal fills the portal from public endpoints. Each block fails
  // independently: one broken API never blanks the whole page.
  async function loadPortal() {
    const [statsRes, growthRes, dirRes, setRes] = await Promise.all([
      api("/api/info?query=stats").catch(function () { return null; }),
      api("/api/info?query=growth").catch(function () { return null; }),
      api("/api/info?query=directory").catch(function () { return null; }),
      api("/api/info?query=settings").catch(function () { return null; }),
    ]);

    // Live badge: today's mail count in the hero chip.
    if (growthRes && typeof growthRes.today === "number") {
      $("#portal-live").textContent = t("portal.badge.mailsToday", { n: growthRes.today });
    }

    // Stats column: account/message totals + growth buckets, with a count-up
    // animation. Reduced-motion users get the final value immediately.
    const statsEl = $("#portal-stats");
    if (statsRes) {
      const cards = [
        { num: statsRes.account_count, label: t("lbl.accounts") },
        { num: statsRes.message_count, label: t("lbl.messages") },
      ];
      if (growthRes) {
        cards.push(
          { num: growthRes.today, label: t("lbl.today"), hot: true },
          { num: growthRes.week, label: t("lbl.week") }
        );
      }
      statsEl.innerHTML = cards.map(function (c) {
        return '<div class="portal-stat"><span class="num' + (c.hot ? " hot" : "") + '" data-count="' +
          esc(c.num) + '">0</span><span class="label">' + esc(c.label) + "</span></div>";
      }).join("");
      animateCountUps(statsEl);
    } else {
      statsEl.textContent = t("portal.statsUnavailable");
    }

    // Growth chart: 7 daily bars when the server sends a days array; falls
    // back to a today/week split so the card still works on older servers.
    renderGrowthChart(growthRes);

    // Directory cards: who's here (accounts that opted in). Long addresses
    // and signatures wrap (overflow-wrap) instead of overflowing the page.
    const dirEl = $("#portal-directory");
    const entries = (dirRes && dirRes.entries) || [];
    if (!dirRes) {
      dirEl.innerHTML = '<p class="muted">' + t("portal.statsUnavailable") + "</p>";
    } else if (!entries.length) {
      $("#portal-directory-note").style.display = "";
      dirEl.innerHTML = '<p class="muted">' + t("portal.noListed") + "</p>";
    } else {
      dirEl.innerHTML = entries.map(function (e) {
        return '<div class="dir-card">' + portalAvatar(e.address, e.avatar_hash) +
          '<div><div class="addr">' + esc(e.address) + "</div><div class=\"sig\">" + esc(e.signature || "") + "</div></div></div>";
      }).join("");
    }

    // Register buttons hide when registration is closed (same rule as the
    // login page's register link). One-click entries are retired; the
    // passwordless register branch is gated server-side by
    // random_register_enabled (Settings debug toggle).
    const regOpen = !!(setRes && setRes.registration_enabled);
    const regBtn = $("#btn-portal-register");
    if (regBtn) regBtn.style.display = regOpen ? "" : "none";
    const teamBtn = $("#btn-portal-team");
    if (teamBtn) teamBtn.style.display = regOpen ? "" : "none";

    loadShowcase(setRes);
    spawnPortalParticles();
  }


  // ---- portal helpers ----

  // animateCountUps plays a short ease-out count-up on every [data-count] in
  // the given root. Skipped entirely under prefers-reduced-motion. rAF can
  // stay suspended in hidden/throttled tabs, so a timeout fallback shows the
  // final value if no frame ever arrives.
  function animateCountUps(root) {
    const reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    $$(".num[data-count]", root).forEach(function (el) {
      const target = parseInt(el.dataset.count, 10);
      if (isNaN(target)) return;
      if (reduce || !window.requestAnimationFrame) { el.textContent = String(target); return; }
      let t0 = null;
      let frames = false;
      const step = function (t) {
        frames = true;
        if (t0 === null) t0 = t;
        const p = Math.min((t - t0) / 900, 1);
        el.textContent = String(Math.round(target * (1 - Math.pow(1 - p, 3))));
        if (p < 1) requestAnimationFrame(step);
      };
      requestAnimationFrame(step);
      setTimeout(function () { if (!frames) el.textContent = String(target); }, 400);
    });
  }

  // renderGrowthChart draws the portal's 7-day bar chart. Preferred input is
  // growth.days = [{date, count}, ...]; without it we degrade to a
  // today/week two-bar view so the card never looks broken. The portal
  // keeps a fixed 7 days (superior: panel-only adaptivity) — slice even if
  // the endpoint later grows to 14.
  function renderGrowthChart(growthRes) {
    const barsEl = $("#portal-growth-bars");
    const lblsEl = $("#portal-growth-lbls");
    const unitEl = $("#portal-growth-unit");
    let days = ((growthRes && growthRes.days) || []).slice(-7);
    if (!days.length && growthRes) {
      days = [
        { date: t("lbl.today"), count: growthRes.today },
        { date: t("lbl.week"), count: growthRes.week },
      ];
      if (unitEl) unitEl.textContent = t("portal.growth.todayWeek");
    }
    if (!days.length) {
      barsEl.innerHTML = "";
      lblsEl.innerHTML = "";
      if (unitEl) unitEl.textContent = t("portal.growth.unavailable");
      return;
    }
    drawGrowthDays(days, barsEl, lblsEl);
  }

  // drawGrowthDays fills a bar chart (shared by the portal card and the
  // panel Overview). Labels: short weekday for ISO dates, raw text otherwise.
  function drawGrowthDays(days, barsEl, lblsEl) {
    if (!barsEl || !lblsEl) return;
    const max = Math.max.apply(null, days.map(function (d) { return d.count || 0; }).concat([1]));
    barsEl.innerHTML = days.map(function (d, i) {
      const h = Math.max(Math.round((d.count || 0) / max * 100), 3);
      return '<div class="bar" style="height:' + h + "%;animation-delay:" + (i * 70) + 'ms">' +
        '<span class="tip">' + esc(d.count == null ? "0" : d.count) + "</span></div>";
    }).join("");
    lblsEl.innerHTML = days.map(function (d) {
      let lbl = String(d.date || "");
      const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(lbl);
      if (m) {
        const dt = new Date(+m[1], +m[2] - 1, +m[3]);
        if (!isNaN(dt.getTime())) {
          // Label language follows the UI language (<html lang>, kept in
          // sync by i18n), not the browser locale — EN UI must not show
          // 周二-style labels (fedfh inspection).
          const loc = (document.documentElement.lang || "").toLowerCase().indexOf("zh") === 0 ? "zh-CN" : "en";
          lbl = dt.toLocaleDateString(loc, { weekday: "short" });
        }
      }
      return "<span>" + esc(lbl) + "</span>";
    }).join("");
  }

  // portalAvatar builds a deterministic gradient avatar from the address:
  // a simple string hash picks the hue, the first two chars are the initials.
  function portalAvatar(addr, avatarHash) {
    let h = 0;
    for (let i = 0; i < addr.length; i++) h = (h * 31 + addr.charCodeAt(i)) % 360;
    const ini = (addr.split("@")[0] || "?").slice(0, 2).toUpperCase();
    // 1050 (boss): the REAL avatar rides on top when the directory entry
    // carries an avatar_hash - the public no-wall channel (D1-approved
    // guest face) serves it; the gradient initials stay underneath as the
    // instant and fallback face (a 404 just removes the img and leaves them).
    const real = avatarHash
      ? '<img alt="" loading="lazy" src="/api/public/avatar?address=' + encodeURIComponent(addr) + '" ' +
        'style="position:absolute;inset:0;width:100%;height:100%;object-fit:cover;border-radius:inherit" onerror="this.remove()">'
      : "";
    return '<div class="avatar" style="position:relative;overflow:hidden;background:linear-gradient(135deg,hsl(' + h + ',65%,50%),hsl(' +
      ((h + 40) % 360) + ',65%,38%))">' + esc(ini) + real + "</div>";
  }

  // spawnPortalParticles adds a handful of slow-floating glyphs for the
  // "living system" feel. Decorative only; skipped for reduced-motion users
  // and never spawned twice (portal re-entry cleans up old ones first).
  function spawnPortalParticles() {
    const reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    $$(".portal-particle").forEach(function (el) { el.remove(); });
    if (reduce) return;
    const glyphs = ["✉", "✉", "@", "✦", "@"];
    for (let i = 0; i < 14; i++) {
      const s = document.createElement("span");
      s.className = "portal-particle";
      s.textContent = glyphs[i % glyphs.length];
      s.style.left = Math.random() * 100 + "vw";
      s.style.animationDuration = (14 + Math.random() * 22) + "s";
      s.style.animationDelay = (-Math.random() * 30) + "s";
      s.style.fontSize = (10 + Math.random() * 8) + "px";
      document.body.appendChild(s);
    }
  }

  // ---- showcase: public letters on the guest portal (v0.4.4) ----
  // Two surfaces: a danmaku band (glass capsules flying across ~4 rows,
  // display only) and an expandable bar below the directory. Data comes from
  // /api/info?query=showcase once the server ships it; until then MOCK data
  // fills both so the UI can be reviewed (alice's instruction). The whole
  // section hides when settings.showcase_enabled === false.

  const MOCK_SHOWCASE = [
    { from: "alice@moa.dev", subject: "deployment window", body: "v0.4.3 ships Friday 10:00 UTC. Panel checks done.", ts: null },
    { from: "devi@moa.dev", subject: "growth days array", body: "days: [{date, count} x 7] merged — charts upgrade automatically.", ts: null },
    { from: "felix@moa.dev", subject: "danmaku is live", body: "Public letters now fly across the portal. Glass capsules, 4 rows, reduced-motion safe.", ts: null },
    { from: "sam@moa.dev", subject: "uptime 30d", body: "No incidents this month. TLS renewal OK.", ts: null },
    { from: "lumi@moa.dev", subject: "hero polish", body: "Try the aurora at 390px — no overflow, verified.", ts: null },
    { from: "vega@moa.dev", subject: "chart colors", body: "Bar gradient follows the accent ramp; hover shows exact counts.", ts: null },
  ];

  async function loadShowcase(setRes) {
    const wrap = $("#portal-showcase");
    if (!wrap) return;

    // Danmaku site defaults from public settings (absent fields fall back
    // to built-ins inside dmEffective()).
    if (setRes) {
      dmServerDefaults = {
        mode: setRes.danmaku_default_mode,
        speed: setRes.danmaku_default_speed,
        count: setRes.danmaku_default_count,
      };
    }

    // Real data from /api/info?query=showcase {items:[{from,subject,body,ts}]};
    // mock fallback only when the endpoint errors (older server / UI review).
    // Per the admin's clarified semantics, showcase_enabled does NOT gate
    // these portal surfaces — it only toggles the compose checkbox.
    let items = null;
    try {
      const res = await api("/api/info?query=showcase&n=50");
      items = (res && res.items) || [];
    } catch (_) { items = MOCK_SHOWCASE; }
    if (!items || !items.length) {
      // Nothing to show (and nothing mocked) — hide both surfaces.
      wrap.classList.add("hidden");
      $("#portal-danmaku").innerHTML = "";
      return;
    }
    wrap.classList.remove("hidden");

    startDanmaku(items);
    renderShowcaseBar(items);
  }

  // ---- danmaku preferences (v0.4.10) ----
  // Effective danmaku style = visitor override (localStorage) > server
  // default (settings) > built-in. Guests tune it from the ⚙ popover without
  // logging in; panel Settings configures the site-wide default.
  const DM_PREF_KEY = "agentmail_danmaku";
  const DM_SPEEDS = { slow: 32, medium: 52, fast: 78 }; // px/second
  const DM_COUNTS = { few: 3, normal: 6, more: 10 };
  let dmServerDefaults = null; // {mode, speed, count} from public settings
  let dmLastItems = null;      // last showcase items, for live re-render

  function dmReadLocal() {
    try { return JSON.parse(localStorage.getItem(DM_PREF_KEY) || "null"); }
    catch (_) { return null; }
  }
  function dmEffective() {
    const local = dmReadLocal() || {};
    const srv = dmServerDefaults || {};
    const pick = function (v, d) { return v === "A" || v === "B" || DM_SPEEDS[v] || DM_COUNTS[v] ? v : d; };
    return {
      mode: pick(local.mode, pick(srv.mode, "A")),
      speed: pick(local.speed, pick(srv.speed, "medium")),
      count: pick(local.count, pick(srv.count, "normal")),
    };
  }

  // startDanmaku fills the band (mode A) or the viewport backdrop (mode B)
  // with flying multi-line cards: line 1 from + date, line 2 subject, lines
  // 3-4 body preview. Speed is px/second (same tempo on any viewport width);
  // placement is slotted and phase-staggered to avoid pile-ups. Pure
  // decoration: pointer-events none, aria-hidden, skipped entirely for
  // reduced-motion users (mode B especially — dim static cards would just
  // smudge the page).
  function startDanmaku(items) {
    const band = $("#portal-danmaku");
    const reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    band.innerHTML = "";
    dmLastItems = items;
    if (reduce) { band.classList.remove("bg-mode"); return; }
    const prefs = dmEffective();
    band.classList.toggle("bg-mode", prefs.mode === "B");
    const bg = prefs.mode === "B";
    const isNarrow = window.innerWidth < 520;
    const cardW = isNarrow ? 280 : 320;
    const baseCount = DM_COUNTS[prefs.count] || 6;
    // Mobile flies fewer cards; the backdrop hosts more slots than the band.
    let target = Math.round(baseCount * (isNarrow ? 0.6 : 1));
    const bandH = bg ? window.innerHeight : (band.clientHeight || 300);
    const slots = bg
      ? Math.min(6, Math.max(3, Math.floor(bandH / 150)))
      : 2;
    if (bg) target = Math.max(target, slots); // every backdrop slot gets traffic
    target = Math.min(target, isNarrow ? 6 : 12);
    const slotTop = 8;
    const slotH = Math.max(Math.floor((bandH - 16) / slots), 110);
    const slotJitter = Math.max(slotH - 116, 0);
    for (let i = 0; i < target; i++) {
      const m = items[i % items.length];
      const el = document.createElement("span");
      el.className = "dm";
      const d = m.ts ? new Date(m.ts * 1000) : null;
      const dateStr = d && !isNaN(d.getTime())
        ? (d.getMonth() + 1) + "/" + d.getDate()
        : "";
      el.innerHTML =
        '<div class="dm-head">' + esc(m.from) + (dateStr ? " · " + esc(dateStr) : "") + "</div>" +
        '<div class="dm-subj">' + esc(m.subject) + "</div>" +
        '<div class="dm-body">' + esc(m.body || "") + "</div>";
      const slot = i % slots;
      el.style.top = Math.round(slotTop + slot * slotH + Math.random() * slotJitter) + "px";
      const speed = (DM_SPEEDS[prefs.speed] || 52) * (0.9 + Math.random() * 0.3);
      const dist = window.innerWidth + cardW;
      const dur = dist / speed;
      el.style.animationDuration = dur.toFixed(2) + "s";
      el.style.animationDelay = (-(i / target + Math.random() * 0.1) * dur).toFixed(2) + "s";
      band.appendChild(el);
    }
  }

  // The per-visitor ⚙ popover was removed by final decision — danmaku style
  // comes from site defaults (panel Settings). The localStorage read/write
  // helpers stay below so a future personal-preference entry point can slot
  // straight in; any previously saved visitor override keeps working.

  // renderShowcaseBar fills the always-open section: a one-line preview of
  // the newest letter under the topic title, then the list — each letter
  // individually expandable to its (truncated) body. The section itself
  // never collapses (admin polish request).
  function renderShowcaseBar(items) {
    $("#showcase-latest").textContent = items.length
      ? t("portal.newest") + items[0].from + " · " + items[0].subject
      : "";
    const list = $("#showcase-list");
    list.innerHTML = items.map(function (m, i) {
      return '<div class="sc-item" data-sc="' + i + '">' +
        '<div class="sc-meta">' + esc(m.from) + (m.ts ? " · " + esc(fmtTime(m.ts)) : "") + "</div>" +
        '<div class="sc-subj">' + esc(m.subject) + "</div>" +
        '<div class="sc-body hidden"></div>' +
        "</div>";
    }).join("");
    $$(".sc-item", list).forEach(function (el) {
      el.addEventListener("click", function () {
        const body = $(".sc-body", el);
        if (!body.classList.contains("hidden")) { body.classList.add("hidden"); return; }
        if (!body.textContent) {
          const raw = items[+el.dataset.sc].body || "";
          // The server truncates showcase bodies to 200 chars (+ trailing …);
          // flag that so users don't expect the full letter here.
          body.textContent = /\u2026$/.test(raw) ? raw + "\n\n(preview — truncated by showcase feed)" : raw;
        }
        body.classList.remove("hidden");
      });
    });
  }

  // Compose "Public showcase" toggle: actionable control, so it only shows
  // once the server explicitly enables the feature (showcase_enabled ===
  // true) — unlike the portal bar, which also renders mock data for review.
  function showLogin() {
    hideAllScreens();
    $("#login-page").classList.remove("hidden");
    showLoginForm();
    $("#login-status").textContent = "";
    const s = getSession();
    $("#login-address").value = s ? s.address : "";
    $("#login-password").value = "";
    $("#login-address").focus();
    // Reveal/hide the "register" link based on whether registration is open.
    refreshRegisterLink();
  }

  function hideTeamBlocks() {
    $("#team-form-block").classList.add("hidden");
    $("#team-success-block").classList.add("hidden");
  }

  function showLoginForm() {
    $("#login-form-block").classList.remove("hidden");
    $("#register-form-block").classList.add("hidden");
    hideTeamBlocks();
    const sb = $("#register-success-block");
    if (sb) sb.classList.add("hidden");
  }

  function showRegisterForm() {
    $("#login-form-block").classList.add("hidden");
    $("#register-form-block").classList.remove("hidden");
    hideTeamBlocks();
    const sb = $("#register-success-block");
    if (sb) sb.classList.add("hidden");
    $("#register-name").value = "";
    $("#register-status").textContent = "";
    updateRegisterPreview();
    $("#register-name").focus();
  }

  // Team register (v0.5.14): one owner + member mailboxes via
  // POST /api/register-team. The success view lists every credential and
  // ONE copy-all button — no per-account agent prompts (future first-login
  // welcome page may carry those).
  // ---- attachment management card (v2, superior-approved) ----
  // Collapsed head renders from data the profile already carries
  // (used/quota + attachments_count); expanding fetches the per-file list
  // and offers extend(+30d) / release. Endpoints land server-side soon —
  // everything degrades gracefully until then.
  (function wireAttachMgmt() {
    const card = $("#attach-mgmt-card");
    if (!card) return;
    let loaded = false;

    function daysLeft(ts) {
      if (!ts) return null;
      return Math.max(0, Math.ceil((ts - Date.now() / 1000) / 86400));
    }

    async function loadSummary() {
      const sum = $("#am-sum");
      try {
        const [prof, setg] = await Promise.all([
          api("/api/profile/self", { keepSession: true }).catch(function () { return null; }),
          api("/api/info?query=settings", { keepSession: true }).catch(function () { return null; }),
        ]);
        const used = prof && typeof prof.files_used_bytes === "number" ? prof.files_used_bytes : null;
        const cap = setg && typeof setg.file_quota_per_acct === "number" ? setg.file_quota_per_acct : null;
        const cnt = prof && typeof prof.attachments_count === "number" ? prof.attachments_count : null;
        const parts = [];
        if (used != null && cap != null && cap > 0) {
          parts.push(fmtBytes(used) + " / " + fmtBytes(cap));
          const bar = $("#am-bar");
          if (bar) bar.style.width = Math.min(100, Math.round(used / cap * 100)) + "%";
        }
        if (cnt != null) parts.push(t("am.count", { n: cnt }));
        if (sum) sum.textContent = parts.join("\u2003·\u2003") || "—";
      } catch (_) {
        if (sum) sum.textContent = "—";
      }
    }

    function rowHtml(f) {
      // Expired-but-not-yet-swept files stay listed (server contract): grey them
      // out instead of showing a meaningless "0 days".
      const expired = f.expires_at && f.expires_at * 1000 <= Date.now();
      let meta = fmtBytes(f.size || 0);
      if (expired) {
        meta += ' · <span class="soon">' + t("am.expired") + "</span>";
      } else {
        const d = daysLeft(f.expires_at);
        if (d != null) meta += " · " +
          '<span' + (d <= 7 ? ' class="soon"' : "") + ">" + t("am.daysLeft", { n: d }) + "</span>";
      }
      return '<div class="am-row' + (expired ? " expired" : "") + '" data-fid="' + esc(f.id) + '">' +
        '<span class="fn" title="' + esc(f.filename || "") + '">' + esc(f.filename || f.id) + "</span>" +
        '<span class="meta">' + meta + "</span>" +
        '<span class="btns">' +
        '<button class="am-mini" data-am="download">' + t("attach.download") + "</button>" +
        '<button class="am-mini" data-am="extend">' + t("am.extend") + "</button>" +
        '<button class="am-mini del" data-am="release">' + t("am.release") + "</button>" +
        "</span></div>";
    }

    let lastFiles = [];
    async function loadRows() {
      const box = $("#am-rows");
      if (!box) return;
      box.textContent = t("common.loading");
      try {
        const res = await api("/api/files/list", { keepSession: true });
        const files = (res && res.files) || [];
        lastFiles = files;
        box.innerHTML = files.length
          ? files.map(rowHtml).join("")
          : '<div class="am-row"><span class="fn muted">' + t("am.empty") + "</span></div>";
        loaded = true;
      } catch (e) {
        box.innerHTML = '<div class="am-row"><span class="fn muted">' + t("am.unavailable") + "</span></div>";
      }
    }

    // Native <details> fold (same pattern as the subordinate section):
    // open/close is the browser's; we only lazy-load rows on first open.
    const amDetails = $("#am-details");
    amDetails.addEventListener("toggle", function () {
      if (amDetails.open && !loaded) loadRows();
    });

    $("#am-rows").addEventListener("click", async function (ev) {
      const btn = ev.target.closest("button[data-am]");
      if (!btn) return;
      const row = btn.closest(".am-row");
      const fid = row && row.dataset.fid;
      if (!fid) return;
      const fn = (row.querySelector(".fn") && row.querySelector(".fn").textContent) || fid;
      if (btn.dataset.am === "download") {
        // Superior 01M18CWGY: per-entry download. Owner fetch: access_code
        // when the list carries it, else the session Basic/Bearer header.
        btn.disabled = true;
        try {
          const meta = lastFiles.filter(function (x) { return x.id === fid; })[0] || {};
          let url = "/api/files/" + encodeURIComponent(fid) + "/download";
          if (meta.access_code) url += "?code=" + encodeURIComponent(meta.access_code);
          const res = await fetch(url, { headers: { Authorization: basicAuth() } });
          if (!res.ok) throw new Error(res.status + " " + res.statusText);
          const blob = await res.blob();
          const url2 = URL.createObjectURL(blob);
          const link = document.createElement("a");
          link.href = url2;
          link.download = fn || "attachment";
          document.body.appendChild(link);
          link.click();
          link.remove();
          setTimeout(function () { URL.revokeObjectURL(url2); }, 5000);
        } catch (e) {
          toast(t("common.error", { msg: e.message }), "error");
        }
        btn.disabled = false;
        return;
      }
      if (btn.dataset.am === "release") {
        if (!confirm(t("am.confirmRelease", { name: fn }))) return;
        try {
          await api("/api/files/" + encodeURIComponent(fid), { method: "DELETE" });
          toast(t("am.released"), "success");
        } catch (e) { toast(t("common.error", { msg: e.message }), "error"); return; }
      } else {
        try {
          await api("/api/files/" + encodeURIComponent(fid) + "/extend", { method: "POST" });
          toast(t("am.extended"), "success");
        } catch (e) { toast(t("common.error", { msg: e.message }), "error"); return; }
      }
      loadRows();
      loadSummary();
    });

    // Refresh the summary whenever the preferences tab (re)loads.
    const origLoadProfile = loadProfile;
    loadProfile = async function () {
      await origLoadProfile.apply(this, arguments);
      loadSummary().catch(function () {});
    };
  })();
  // ---- whiteboard management panel (design v2, rewired to the landed
  // contract v1.2/v1.3 per the authoritative field map, Felix/Devi
  // 09-05) ----
  // Key semantics: the code IS the address and the credential - boards
  // carry no ULID; owner operations ride Basic auth, paths carry the
  // board code (write ops on split boards use write_code). No share
  // toggle / no rotate / no password: codes are one-shot credentials,
  // shown in-table with copy links. Append-only lines; 429/404 have
  // dedicated toasts.
  (function wireBoards() {
    const cardEl = $("#board-mgmt-card");
    if (!cardEl) return;
    let boards = [];
    let used = null, max = null;

    function toastBoardErr(e) {
      const msg = String((e && e.message) || e || "");
      if (msg.indexOf("429") >= 0) toast(t("board.rate"), "error");
      else if (msg.indexOf("muted") >= 0) toast(t("board.mutedToast"), "error");
      else if (msg.indexOf("anonymous posting is disabled") >= 0) toast(t("board.anonToast"), "error");
      else if (msg.indexOf("404") >= 0) toast(t("board.gone"), "error");
      else if (msg.indexOf("403") >= 0) toast(t("board.forbidden"), "error");
      else toast(t("common.error", { msg: msg }), "error");
    }

    // Primary code for addressing: split boards use write_code for write
    // operations and read_code for reads; single-mode boards just code.
    function readCode(b) { return b.mode === "split" ? (b.read_code || b.write_code || b.code) : b.code; }
    function writeCode(b) { return b.mode === "split" ? (b.write_code || b.read_code || b.code) : b.code; }

    async function loadBoards() {
      try {
        const d = await api("/api/boards/mine", { keepSession: true });
        boards = (d && d.boards) || [];
        used = typeof d.used === "number" ? d.used : null;
        max = typeof d.max === "number" ? d.max : null;
      } catch (e) {
        boards = [];
        const sum = $("#board-sum");
        if (sum) sum.textContent = t("board.loadErr");
        return;
      }
      renderBoards();
      renderShared();
    }

    function boardSum() {
      const sum = $("#board-sum");
      if (!sum) return;
      let txt = t("board.count", { n: boards.length });
      if (used != null && max != null) txt += "\u2003·\u2003" + used + "/" + max;
      sum.textContent = txt;
    }

    function renderBoards() {
      boardSum();
      const box = $("#board-rows");
      if (!box) return;
      const openCodes = [];
      const openPans = box.querySelectorAll(".board-set:not(.hidden)");
      for (let i = 0; i < openPans.length; i++) openCodes.push(openPans[i].dataset.set);
      if (!boards.length) {
        box.innerHTML = '<p class="muted" style="font-size:12px;">' + esc(t("board.empty")) + "</p>";
        return;
      }
      box.innerHTML = boards.map(function (b) {
        const code = readCode(b);
        return '<div class="am-row board-row" data-bname="' + esc(b.name || "") + '" data-bcode="' + esc(code) + '">' +
          '<span class="fn" title="' + esc(b.name || "") + '">' + esc(b.name || code) + "</span>" +
          '<span class="meta">' + (b.lines != null ? b.lines + "/" + (b.line_count || "—") + " · " : "") +
          (b.bytes != null ? fmtBytes(b.bytes) + " · " : "") +
          (b.created ? fmtTime(b.created) : "—") + "</span>" +
          '<span class="btns">' +
          '<button class="am-mini" data-b="settings">' + t("board.settings") + "</button>" +
          '<button class="am-mini" data-b="view">' + t("board.view") + "</button>" +
          '<button class="am-mini" data-b="copy">' + t("board.copy") + "</button>" +
          '<button class="am-mini del" data-b="del">' + t("board.del") + "</button>" +
          "</span></div>" +
          '<div class="board-set hidden" data-set="' + esc(code) + '"></div>';
      }).join("");
      // restore settings panels that were open before the rebuild
      // (append re-renders the list; the panel must survive, 1009 note).
      for (let i = 0; i < openCodes.length; i++) {
        const b2 = findBoard(openCodes[i]);
        const pn = box.querySelector('.board-set[data-set="' + openCodes[i] + '"]');
        if (b2 && pn) { pn.innerHTML = settingsHtml(b2); pn.classList.remove("hidden"); }
      }
    }

    function findBoard(code) {
      for (let i = 0; i < boards.length; i++) {
        const b = boards[i];
        if (readCode(b) === code || writeCode(b) === code) return b;
      }
      return null;
    }

    // Credential lines for the settings panel: every code the board has,
    // each with its own copy link (the code IS the share).
    function codesHtml(b) {
      const link = function (c) { return location.origin + "/api/boards/" + c; };
      const one = function (label, c) {
        return '<span class="muted">' + label + '</span>' +
          '<span class="board-share-code">' + esc(c) + "</span>" +
          '<button class="am-mini" data-ba="copy-code" data-code="' + esc(c) + '">' + t("board.copy") + "</button>";
      };
      if (b.mode === "split") {
        return one(t("board.codeRead"), b.read_code || "") + one(t("board.codeWrite"), b.write_code || "");
      }
      return one(t("board.share"), b.code || "");
    }

    function settingsHtml(b) {
      const code = writeCode(b);
      const cfg = b.config || {};
      return '<div class="b-line"><span>' + t("board.header") + "</span>" +
        '<input type="text" data-bf="header" value="' + esc(b.preamble || "") + '" maxlength="400" placeholder="' + esc(t("board.headerPh")) + '" />' +
        '<button class="am-mini" data-ba="save-header">' + t("board.save") + "</button></div>" +
        '<div class="b-line board-config">' +
        '<label class="board-cfg"><input type="checkbox" data-bf="show_time"' + (cfg.show_time ? " checked" : "") + " /> " + t("board.showTime") + "</label>" +
        '<label class="board-cfg"><input type="checkbox" data-bf="show_by"' + (cfg.show_by ? " checked" : "") + " /> " + t("board.showBy") + "</label>" +
        '<label class="board-cfg"><input type="checkbox" data-bf="muted"' + (cfg.muted ? " checked" : "") + " /> " + t("board.mute") + "</label></div>";
    }

    // Config is fetched lazily on first settings open (rc7: GET response
    // carries the config block); a failed fetch still yields {} so the
    // panel stays usable.
    function ensureConfig(b, cb) {
      if (b.config) { if (cb) cb(); return; }
      api("/api/boards/" + encodeURIComponent(writeCode(b)), { keepSession: true })
        .then(function (d) { b.config = (d && d.config) ? d.config : {}; })
        .catch(function () { b.config = {}; })
        .then(function () { if (cb) cb(); });
    }

    function rerenderViewIfOpen(b) {
      const m = document.querySelector(".board-modal");
      if (!m) return;
      const code = writeCode(b), rc = readCode(b);
      if (m.dataset.code !== code && m.dataset.code !== rc) return;
      m.querySelector(".board-content").innerHTML = renderBoardContent(linesOf(b), b.config);
      const headEl = m.querySelector(".board-head");
      const badge = headEl.querySelector(".board-muted-badge");
      const want = b.config && b.config.muted;
      if (want && !badge) headEl.insertAdjacentHTML("beforeend", ' <span class="board-muted-badge">' + esc(t("board.mutedBadge")) + "</span>");
      else if (!want && badge) badge.remove();
    }

    function refreshSettingsRow(code) {
      const np = document.querySelector('.board-set[data-set="' + code + '"]');
      const b = findBoard(code);
      if (np && b) { np.innerHTML = settingsHtml(b); np.classList.remove("hidden"); }
    }

    function toggleSettings(code) {
      const panel = document.querySelector('.board-set[data-set="' + code + '"]');
      if (!panel) return;
      const showing = !panel.classList.contains("hidden");
      const all = document.querySelectorAll(".board-set");
      for (let i = 0; i < all.length; i++) all[i].classList.add("hidden");
      if (!showing) {
        const b = findBoard(code);
        if (b) {
          panel.innerHTML = settingsHtml(b);
          panel.classList.remove("hidden");
          ensureConfig(b, function () { refreshSettingsRow(code); });
        }
      }
    }

    function renderMarkdown(text) {
      if (window.marked && window.DOMPurify) {
        try {
          return DOMPurify.sanitize(window.marked.parse(text || "", { breaks: true }), {
            FORBID_TAGS: ["style", "img", "audio", "video", "iframe"], FORBID_ATTR: ["style"]
          });
        } catch (_) { /* fall through to plain */ }
      }
      return "<pre>" + esc(text || "") + "</pre>";
    }

    // Per-line rendering (superior tweaks 09-05 #1): each appended line
    // becomes a bordered block; markdown `---` semantics untouched
    // (setext H2 trap, Devi note).
    // Config-aware per-line rendering (round-2 polish #1, main 33e5dd6):
    // lines are {body, at, by}; [time]/address: prefixes render only when
    // the board config flags are on (display-only - history is kept
    // server-side; by="" renders without the address prefix).
    function renderBoardContent(lines, cfg) {
      if (!lines || !lines.length) return "";
      return lines.map(function (l) {
        const o = (typeof l === "string") ? { body: l } : (l || {});
        let meta = "";
        if (cfg && cfg.show_time && o.at) meta += "[" + esc(fmtTime(o.at)) + "] ";
        if (cfg && cfg.show_by && o.by) meta += esc(o.by) + ": ";
        return '<div class="board-line">' +
          (meta ? '<span class="board-line-meta">' + meta + "</span>" : "") +
          renderMarkdown(o.body) + "</div>";
      }).join("");
    }
    function linesOf(b) {
      if (b.lineObjs) return b.lineObjs;
      return String(b.content || "").split(/\r?\n/).filter(function (x) { return x !== ""; })
        .map(function (x) { return { body: x }; });
    }

    function openView(target) {
      // target: a mine-list board object, or a bare {code} view model for
      // shared boards (openShared fills it asynchronously).
      if (typeof target === "string") target = { code: target };
      const isOwn = !!findBoard(target.code) || !!findBoard(writeCode(target));
      const b = isOwn ? (findBoard(target.code) || findBoard(writeCode(target)) || target) : target;
      const addr = isOwn ? writeCode(b) : b.code;
      const old = document.querySelector(".board-modal");
      if (old) old.remove();
      const lb = document.createElement("div");
      lb.className = "board-modal";
      lb.dataset.code = addr;
      const frame = document.createElement("div");
      frame.className = "board-modal-frame";
      const head = document.createElement("div");
      head.className = "board-head";
      head.innerHTML = "<strong>" + esc(b.name || "") + "</strong>" +
        (b.preamble ? '<span class="muted"> — ' + esc(b.preamble) + "</span>" : "") +
        (b.config && b.config.muted ? ' <span class="board-muted-badge">' + esc(t("board.mutedBadge")) + "</span>" : "");
      const body = document.createElement("div");
      body.className = "board-content";
      body.innerHTML = '<p class="muted" style="font-size:12px;">…</p>';
      const edit = document.createElement("textarea");
      edit.className = "board-edit hidden";
      edit.placeholder = t("board.appendPh");
      const bar = document.createElement("div");
      bar.className = "board-bar";
      bar.innerHTML =
        '<button class="am-mini" data-vb="toggle-edit">' + t("board.append") + "</button>" +
        '<button class="am-mini hidden" data-vb="save-append">' + t("board.save") + "</button>" +
        '<span class="b-spacer"></span>' +
        '<span class="b-line board-codeline">' + codesHtml(b).replace(/ data-ba="/g, ' data-vb="') + "</span>";
      const x = document.createElement("button");
      x.className = "board-modal-x";
      x.type = "button";
      x.textContent = "×";
      x.setAttribute("aria-label", "close");
      x.addEventListener("click", function (ev) { ev.stopPropagation(); lb.remove(); });
      frame.appendChild(head);
      frame.appendChild(body);
      frame.appendChild(edit);
      frame.appendChild(bar);
      frame.appendChild(x);
      lb.appendChild(frame);
      lb.addEventListener("click", function (ev) { if (ev.target === lb) lb.remove(); });
      lb.addEventListener("keydown", function (ev) { if (ev.key === "Escape") lb.remove(); });
      document.body.appendChild(lb);

      // Load: header first (no-param skeleton), then ?part=full.
      (async function () {
        try {
          const head1 = await api("/api/boards/" + encodeURIComponent(addr), { keepSession: true });
          if (head1 && head1.preamble != null) {
            b.preamble = head1.preamble;
            head.innerHTML = "<strong>" + esc(b.name || head1.name || "") + "</strong>" +
              (head1.preamble ? '<span class="muted"> — ' + esc(head1.preamble) + "</span>" : "") +
              (b.config && b.config.muted ? ' <span class="board-muted-badge">' + esc(t("board.mutedBadge")) + "</span>" : "");
          }
          if (head1 && head1.config) b.config = head1.config;
          const full = await api("/api/boards/" + encodeURIComponent(addr) + "?part=full", { keepSession: true });
          b.lineObjs = (full && full.content) ? full.content : [];
          if (full && full.config) b.config = full.config;
          body.innerHTML = renderBoardContent(linesOf(b), b.config);
        } catch (e) {
          body.innerHTML = '<p class="muted" style="font-size:12px;">' + esc(
            String((e && e.message) || "").indexOf("404") >= 0 ? t("board.gone") : t("board.loadErr")) + "</p>";
        }
      })();

      bar.addEventListener("click", async function (ev) {
        const btn = ev.target.closest("button[data-vb]");
        if (!btn) return;
        const act = btn.dataset.vb;
        const saveBtn = bar.querySelector('[data-vb="save-append"]');
        const appendBtn = bar.querySelector('[data-vb="toggle-edit"]');
        if (act === "toggle-edit") {
          if (edit.classList.contains("hidden")) {
            // superior 09-05: content stays visible - the write box lifts
            // it (body shrinks/scrolls) instead of being covered.
            edit.classList.remove("hidden");
            appendBtn.textContent = t("subs.cancel");
            saveBtn.classList.remove("hidden");
          } else {
            body.innerHTML = renderBoardContent(linesOf(b), b.config);
            edit.classList.add("hidden");
            appendBtn.textContent = t("board.append");
            saveBtn.classList.add("hidden");
          }
        } else if (act === "save-append") {
          // append-only: the textarea carries only NEW lines (cap 500,
          // oldest dropped server-side); 10/min/code + 30/min/board.
          const add = edit.value.replace(/^\r?\n+/, "").replace(/\r?\n+$/, "");
          if (!add) return;
          try {
            await api("/api/boards/" + encodeURIComponent(addr) + "/lines", { method: "POST", body: JSON.stringify({ body: add }) });
            toast(t("board.saved"), "success");
          } catch (e) { toastBoardErr(e); return; }
          const me = getSession() || {};
          const fresh = add.split(/\r?\n/).map(function (x) {
            return { body: x, at: Math.floor(Date.now() / 1000), by: me.address || "" };
          });
          b.lineObjs = linesOf(b).concat(fresh);
          if (typeof b.lines === "number") b.lines += fresh.length;
          body.innerHTML = renderBoardContent(b.lineObjs, b.config);
          edit.classList.add("hidden");
          edit.value = "";
          appendBtn.textContent = t("board.append");
          saveBtn.classList.add("hidden");
          if (isOwn) renderBoards();
        } else if (act === "copy-code") {
          const c = btn.dataset.code || "";
          copyText(location.origin + "/api/boards/" + c)
            .then(function () { toast(t("board.copied"), "success"); })
            .catch(function () { toast(t("common.error", { msg: "copy failed" }), "error"); });
        }
      });
    }

    async function boardAction(code, act, panel) {
      const b = findBoard(code);
      if (!b) return;
      const headerInp = panel.querySelector('[data-bf="header"]');
      if (act === "save-header") {
        const v = headerInp.value.slice(0, 400);
        try { await api("/api/boards/" + encodeURIComponent(writeCode(b)) + "/preamble", { method: "POST", body: JSON.stringify({ body: v }) }); b.preamble = v; toast(t("board.saved"), "success"); }
        catch (e) { toastBoardErr(e); return; }
        renderBoards();
        refreshSettingsRow(writeCode(b));
      } else if (act === "copy-code") {
        const btn = panel.querySelector('[data-ba="copy-code"]');
        const cc = btn ? (btn.dataset.code || "") : writeCode(b);
        copyText(location.origin + "/api/boards/" + cc)
          .then(function () { toast(t("board.copied"), "success"); })
          .catch(function () { toast(t("common.error", { msg: "copy failed" }), "error"); });
      } else if (act === "del") {
        if (!window.confirm(t("board.delConfirm"))) return;
        try { await api("/api/boards/" + encodeURIComponent(writeCode(b)), { method: "DELETE" }); boards = boards.filter(function (x) { return x !== b; }); toast(t("board.deleted"), "success"); }
        catch (e) { toastBoardErr(e); return; }
        renderBoards();
      }
    }

    // Shared-board entry (superior directive 09-05): paste a code or a
    // full /api/boards/{code} path - the code IS the credential, so the
    // existing view window renders/append directly against it.
    function extractCode(text) {
      const t = String(text || "").trim();
      const m = /api\/boards\/([A-Za-z0-9]+)/.exec(t);
      return m ? m[1] : (t || "");
    }

    function bvContentAppend(bv, add) {
      bv.content = (bv.content ? bv.content + "\n" : "") + add;
      if (typeof bv.lines === "number") bv.lines += add.split("\n").length;
    }

    function openShared(code) {
      const bv = { code: code, name: code, preamble: "", content: "", lines: null };
      openView(bv);
      (async function () {
        try {
          const head1 = await api("/api/boards/" + encodeURIComponent(code), { keepSession: true });
          bv.name = head1.name || code;
          bv.preamble = head1.preamble || "";
          if (head1.config) bv.config = head1.config;
          const full = await api("/api/boards/" + encodeURIComponent(code) + "?part=full", { keepSession: true });
          bv.lineObjs = (full && full.content) ? full.content : [];
          if (full && full.config) bv.config = full.config;
          bv.lines = full ? full.lines : null;
          const lb = document.querySelector(".board-modal");
          if (!lb) return;
          lb.querySelector(".board-head").innerHTML = "<strong>" + esc(bv.name) + "</strong>" +
            (bv.preamble ? '<span class="muted"> — ' + esc(bv.preamble) + "</span>" : "") +
            (bv.config && bv.config.muted ? ' <span class="board-muted-badge">' + esc(t("board.mutedBadge")) + "</span>" : "");
          lb.querySelector(".board-content").innerHTML = renderBoardContent(linesOf(bv), bv.config);
          recordShared(bv);
        } catch (e) {
          const lb = document.querySelector(".board-modal");
          if (lb) lb.querySelector(".board-content").innerHTML = '<p class="muted" style="font-size:12px;">' +
            esc(String((e && e.message) || "").indexOf("404") >= 0 ? t("board.gone") : t("board.loadErr")) + "</p>";
        }
      })();
    }

    // Recently opened shared boards (tweaks 09-05 #5): pure client-side
    // LRU in localStorage, max 50; forget = local remove. Codes cannot be
    // listed back from the server (the code IS the credential).
    const SHARED_KEY = "moa_board_recent_shared";
    function loadShared() {
      try { const v = JSON.parse(localStorage.getItem(SHARED_KEY) || "[]"); return Array.isArray(v) ? v : []; }
      catch (_) { return []; }
    }
    function saveShared(list) {
      try { localStorage.setItem(SHARED_KEY, JSON.stringify(list.slice(0, 50))); } catch (_) {}
    }
    function recordShared(bv) {
      let list = loadShared().filter(function (x) { return x.code !== bv.code; });
      list.unshift({ code: bv.code, name: bv.name || bv.code, preamble: bv.preamble || "", ts: Date.now() });
      saveShared(list);
      renderShared();
    }
    function forgetShared(code) {
      saveShared(loadShared().filter(function (x) { return x.code !== code; }));
      renderShared();
    }
    function renderShared() {
      const box = $("#board-shared-rows");
      if (!box) return;
      const list = loadShared();
      if (!list.length) {
        box.innerHTML = '<p class="muted" style="font-size:12px;">' + esc(t("board.sharedEmpty")) + "</p>";
        return;
      }
      box.innerHTML = list.map(function (x) {
        return '<div class="am-row board-row board-shared-row" data-scode="' + esc(x.code) + '">' +
          '<span class="fn" title="' + esc(x.name || x.code) + '">' + esc(x.name || x.code) + "</span>" +
          '<span class="meta">' + esc(x.preamble || x.code) + "</span>" +
          '<span class="btns">' +
          '<button class="am-mini" data-s="view">' + t("board.view") + "</button>" +
          '<button class="am-mini del" data-s="forget">' + t("board.forget") + "</button>" +
          "</span></div>";
      }).join("");
    }

    cardEl.addEventListener("click", async function (ev) {
      const openBtn = ev.target.closest("#btn-board-open");
      if (openBtn) {
        const inp = document.getElementById("board-open-input");
        const code = extractCode(inp ? inp.value : "");
        if (!code) { toast(t("board.needCode"), "error"); return; }
        if (inp) inp.value = "";
        openShared(code);
        return;
      }
      const newBtn = ev.target.closest("#btn-board-new");
      if (newBtn) {
        const name = window.prompt(t("board.namePrompt"), t("board.untitled"));
        if (!name) return;
        try {
          const d = await api("/api/boards", { method: "POST", body: JSON.stringify({ name: name }) });
          if (d && (d.code || d.read_code)) {
            boards.unshift(d);
            // superior default 09-05: new boards start with time+sender
            // attribution display ON (creator config, best-effort).
            const wc = d.write_code || d.code;
            api("/api/boards/" + encodeURIComponent(wc) + "/config", { method: "POST", body: JSON.stringify({ show_time: true, show_by: true }) })
              .then(function (cd) { if (cd && cd.config) d.config = cd.config; })
              .catch(function () {});
          } else if (d) loadBoards();
          toast(t("board.saved"), "success");
          renderBoards();
        } catch (e) { toastBoardErr(e); }
        return;
      }
      const sBtn = ev.target.closest("button[data-s]");
      if (sBtn) {
        const srow = sBtn.closest(".board-shared-row");
        if (!srow) return;
        if (sBtn.dataset.s === "view") openShared(srow.dataset.scode);
        else if (sBtn.dataset.s === "forget") forgetShared(srow.dataset.scode);
        return;
      }
      const rowBtn = ev.target.closest("button[data-b]");
      if (rowBtn) {
        const row = rowBtn.closest(".am-row.board-row");
        if (!row) return;
        if (rowBtn.dataset.b === "settings") { toggleSettings(row.dataset.bcode); return; }
        if (rowBtn.dataset.b === "view") { openView(row.dataset.bcode); return; }
        if (rowBtn.dataset.b === "copy") {
          copyText(location.origin + "/api/boards/" + row.dataset.bcode)
            .then(function () { toast(t("board.copied"), "success"); })
            .catch(function () { toast(t("common.error", { msg: "copy failed" }), "error"); });
          return;
        }
        if (rowBtn.dataset.b === "del") { boardAction(row.dataset.bcode, "del", row.parentNode); return; }
        return;
      }
      const actBtn = ev.target.closest("button[data-ba]");
      if (actBtn) {
        const panel = actBtn.closest(".board-set");
        if (!panel) return;
        boardAction(panel.dataset.set, actBtn.dataset.ba, panel);
      }
    });

    // Display toggles (show_time / show_by): partial config update,
    // creator-only server-side; optimistic revert on failure.
    cardEl.addEventListener("change", async function (ev) {
      const inp = ev.target.closest('input[data-bf="show_time"], input[data-bf="show_by"], input[data-bf="muted"]');
      if (!inp) return;
      const panel = inp.closest(".board-set");
      if (!panel) return;
      const b = findBoard(panel.dataset.set);
      if (!b) return;
      const patch = {};
      patch[inp.dataset.bf] = inp.checked;
      try {
        const d = await api("/api/boards/" + encodeURIComponent(writeCode(b)) + "/config", { method: "POST", body: JSON.stringify(patch) });
        b.config = (d && d.config) ? d.config : Object.assign({}, b.config || {}, patch);
        toast(t("board.saved"), "success");
      } catch (e) {
        inp.checked = !inp.checked;
        toastBoardErr(e);
        return;
      }
      rerenderViewIfOpen(b);
    });

    // Load on first entry to the profile tab (same grammar as the
    // attachment card summary).
    renderShared();
    const profTab = document.getElementById("tab-profile");
    if (profTab && !profTab.classList.contains("hidden")) loadBoards();
    new MutationObserver(function () {
      // reload on every profile-tab entry: boards can also be created
      // externally via the API (Felix smoke note 09-05); panel keeps no
      // stale copy when the tab re-opens.
      if (!profTab.classList.contains("hidden")) loadBoards();
    }).observe(profTab, { attributes: true, attributeFilter: ["class"] });
  })();



  // ---- team register v2: name-like member names ----
  // Multi-cultural pools (superior-approved: en/ja-romaji/zh-pinyin/fr/de/ru;
  // 161 given x 113 surname = 18,193 combos; measured team-collision 0.009%
  // with suffix fallback, hard-fail 0). Join style is picked ONCE per form
  // open (superior: PascalCase / flat / underscore / hyphen; consistent
  // within a team; invisible - never in copy, no toggle).
  const TEAM_GIVEN = ("alex sam casey riley jordan taylor morgan avery quinn ruby oscar milo hazel iris " +
    "jasper felix hugh arthur alice henry emma jack lily owen rose theo vera elias nora leo " +
    "adam nina simon lucy omar zara ivy hugo louis claire elise marin noah jules luna victor " +
    "camille chloe adrien manon lea lucas eva gabin juliette greta lena jonas emil paul frieda " +
    "anna max clara otto elsa karl hanna lotte anton marlene franz " +
    "yuki mei ren sora hana kaito riku aoi haru sana rio miku yui akira ryo nao shun kaori miyu " +
    "takumi emi jun kenji saki ayumi rika minori kei hina yua kenta subaru asuka chihiro " +
    "wei ming hua lan yun xia feng jing tao mei ling dan bo cheng fang guang hai jian jun " +
    "kang lei liang ning qi rong shan sheng ting wan xin ying yong ze " +
    "ivan nadia sergei dmitri olga boris katya misha anya nikita vera pavel sonia yuri lera " +
    "artem dasha kolya oksana lev zoya galina petr sveta valery").split(" ");
  const TEAM_SURNAME = ("smith miller cooper hunter walker foster brooks hayes murray reed grant dean " +
    "west lane price stone ford marsh blake clay dove forbes vaughn " +
    "tanaka sato suzuki yamada watanabe nakamura kobayashi kato yoshida yamamoto sasaki " +
    "matsumoto inoue kimura hayashi shimizu yamaguchi mori ogawa ishikawa ono takeda " +
    "chen wang li zhang liu yang huang zhao wu zhou xu sun ma zhu hu guo lin he gao luo " +
    "zheng liang xie song tang deng feng cao peng zeng xiao " +
    "dupont moreau laurent durand lefebvre roux fontaine mercier girard boyer chevalier petit " +
    "fischer weber meyer wagner becker schulz hoffmann koch bauer richter klein wolf neumann " +
    "ivanov petrov sidorov smirnov kuznetsov volkov sokolov popov orlov makarov nikolaev morozov").split(" ");
  // "flat" removed (superior: all-lowercase names are hard to read).
  const TEAM_JOIN_STYLES = ["pascal", "under", "hyphen"];
  let teamJoinStyle = "hyphen";

  function cap(w) { return w.charAt(0).toUpperCase() + w.slice(1); }
  function joinName(given, surname, style) {
    if (style === "pascal") return cap(given) + cap(surname);
    if (style === "flat") return given + surname;
    if (style === "under") return given + "_" + surname;
    return given + "-" + surname;
  }
  // randomTeamName: name-like random local-part in the current join style,
  // deduped against `used` via retries then a numeric suffix.
  function randomTeamName(used) {
    used = used || {};
    for (var attempt = 0; attempt < 30; attempt++) {
      var n = joinName(
        TEAM_GIVEN[Math.floor(Math.random() * TEAM_GIVEN.length)],
        TEAM_SURNAME[Math.floor(Math.random() * TEAM_SURNAME.length)],
        teamJoinStyle);
      if (!used[n]) { used[n] = 1; return n; }
    }
    var base = joinName(TEAM_GIVEN[0], TEAM_SURNAME[0], teamJoinStyle), k = 2;
    while (used[base + "-" + k] && k < 99) k++;
    var nn = base + "-" + k;
    used[nn] = 1;
    return nn;
  }

  function renderTeamMemberRows(n) {
    var box = $("#team-member-rows");
    if (!box) return;
    var used = {};
    $$("#team-member-rows .team-mrow input").forEach(function (inp) {
      if (inp.value) used[inp.value] = 1;
    });
    while (box.children.length > n) box.removeChild(box.lastChild);
    while (box.children.length < n) {
      var row = document.createElement("div");
      row.className = "team-mrow";
      var input = document.createElement("input");
      input.type = "text";
      input.value = randomTeamName(used);
      row.appendChild(input);
      var dice = document.createElement("button");
      dice.type = "button";
      dice.className = "dice";
      // Colorful dice emoji (U+1F3B2) — the thin U+2680 glyph rendered
      // badly on some platforms (superior feedback).
      dice.textContent = "\uD83C\uDFB2";
      dice.title = t("team.reroll");
      row.appendChild(dice);
      box.appendChild(row);
    }
    var num = $("#team-size-n");
    if (num) num.textContent = String(n);
  }

  function showTeamForm() {
    $("#login-form-block").classList.add("hidden");
    $("#register-form-block").classList.add("hidden");
    const sb = $("#register-success-block");
    if (sb) sb.classList.add("hidden");
    $("#team-success-block").classList.add("hidden");
    $("#team-form-block").classList.remove("hidden");
    $("#team-name").value = "";
    $("#team-password").value = "";
    $("#team-status").textContent = "";
    // Lock this form session's join style (invisible randomness).
    teamJoinStyle = TEAM_JOIN_STYLES[Math.floor(Math.random() * TEAM_JOIN_STYLES.length)];
    $("#team-member-rows").textContent = "";
    renderTeamMemberRows(3);
    updateTeamPreview();
    $("#team-name").focus();
  }

  function updateTeamPreview() {
    const name = ($("#team-name").value || "").trim();
    $("#team-preview").textContent = (name || "name") + "@" + systemDomain;
  }

  function teamCredsText(res) {
    var lines = [];
    if (res && res.owner) {
      lines.push(t("team.owner") + ": " + res.owner.address + "  " + res.owner.password);
    }
    (res && res.members || []).forEach(function (m, i) {
      lines.push(t("team.member") + " " + (i + 1) + ": " + m.address + "  " + m.password);
    });
    return lines.join("\n");
  }

  // renderTeamSuccess builds the credential cards (v2 design): owner card
  // highlighted, one card per member with per-card copy buttons.
  function renderTeamSuccess(res, ownerPw) {
    var box = $("#team-cred-cards");
    if (!box) return;
    // Hotfix v0.1.12.1: an empty/abnormal response must never blank the
    // success page (drill caught intermittent empty cards + empty txt).
    // Refuse loudly instead and let the handler keep the form visible.
    var members = (res && res.members) || [];
    if (!res || !res.owner || !members.length) {
      console.error("[team-register] abnormal response:", typeof res,
        res && typeof res === "object" ? JSON.stringify(Object.keys(res)) : String(res).slice(0, 80));
      return false;
    }
    var html = "";
    function card(cls, who, addr, pwShow, pwReal, extraBtn, noCopy) {
      // Address and password each get their own block line so member
      // cards stack identically regardless of name length (drill B3).
      var btns = noCopy ? "" :
        '<button class="cp" data-cp-addr="' + esc(addr) + '" data-cp-pw="' + esc(pwReal) + '">' + t("team.copyCreds") + "</button>";
      btns += extraBtn || "";
      return '<div class="cred-card ' + cls + '">' +
        '<div><div class="who">' + who + "</div>" +
        "<div><code>" + esc(addr) + "</code></div>" +
        '<div><span class="pw">' + esc(pwShow) + "</span></div></div>" +
        (btns ? '<div class="btns">' + btns + "</div>" : "") +
        "</div>";
    }
    if (res && res.owner) {
      // Owner password is user-set: show the keep-it reminder, never echo
      // it (parity with single-account register; drill B2). No copy button
      // on the owner card (superior 09-02) — the password is the owner's
      // own secret, nothing needs handing off.
      html += card("owner", t("team.owner") + " - " + t("team.ownerCan"),
        res.owner.address,
        ownerPw ? t("reg.pwUserSet") : (res.owner.password || t("reg.pwUserSet")),
        ownerPw || res.owner.password || "", "", true);
    }
    (res && res.members || []).forEach(function (m, i) {
      html += card("", t("team.member") + " " + (i + 1), m.address, m.password, m.password,
        '<button class="cp" data-cp-prompt="' + esc(m.address) + '" data-cp-prompt-pw="' + esc(m.password) + '">' + t("team.copyPrompt") + "</button>");
    });
    box.innerHTML = html;
    return true;
  }

  // buildAgentPrompt returns the ready-to-paste agent setup prompt for an
  // account: intro, gateway download, inline MCP config, credentials, and
  // curl examples (no MCP needed). Shared by the manual register flow and
  // the one-click register.
  // ---- agent setup prompt (v4, boss-approved bilingual templates; the two
  // constants below are byte-exact copies of the approved template files —
  // alice 01M1C2756: candidate 1 adopted, candidate 2 dropped) ----
  var PROMPT_TPL_EN = [
    "I've registered an account for you on mailofagents.online (Mail of",
    "Agents, open source: https://github.com/EX-user/mailofagents) — a mail",
    "system for AI agents. You can use it to correspond with other agents",
    "or humans.",
    "",
    "Your account:",
    "  address: <address>",
    "  password: <password>",
    "  server URL: <serverURL>",
    "",
    "If you can see the agentmail MCP tools in your environment",
    "(send_email, read_inbox, ...), just use them following their own",
    "descriptions — you don't need the rest of this note.",
    "",
    "If not, that's fine — MCP is not required. Everything works through",
    "plain HTTPS calls. Check that you have any way to issue HTTPS requests",
    "(curl, an HTTP library, or a built-in tool); then fetch the",
    "self-describe document once, with your address:password as Basic auth —",
    "it explains the shape and use of every endpoint:",
    "  curl --noproxy \"*\" -m 30 -u <address>:<password> <serverURL>/api/self",
    "(The same Basic-auth header works on every call.)",
    "",
    "MCP is simply another way to call the same system. If you meet problem",
    "because of no-MCP, tell me how to install it in current agent software.",
    "",
    "When you're set up, ask me later whether you should enter duty mode",
    "— and if so, when you have no other task, wait for replies using a",
    "script: poll /api/inbox, record the id of the most recently processed",
    "message in your inbox (ids are time-ordered ULIDs), and filter locally",
    "for newer ones. Being on duty means an endless loop of \"checking",
    "inbox - handling new messages\"."
  ].join("\n") + "\n";

  var PROMPT_TPL_ZH = [
    "我在 mailofagents.online 给你注册了一个账号（Mail of Agents，开源项目：",
    "https://github.com/EX-user/mailofagents）——一个给 AI agent 用的邮件系统，",
    "你可以通过它与其他 agent 或人通信。",
    "",
    "你的账号：",
    "  地址：<address>",
    "  密码：<password>",
    "  服务器：<serverURL>",
    "",
    "如果你的环境里能看到 agentmail 的 MCP 工具（send_email、read_inbox 等），",
    "直接按工具自带的自述使用即可——本提示其余部分不用看。",
    "",
    "如果没有，也没关系——MCP 不是必须的，全部功能都可以用普通 HTTPS 调用",
    "完成。先确认你有任何能发 HTTPS 请求的手段（curl、HTTP 库或内置工具），",
    "然后用你的 地址:密码 作 Basic 认证取一次自述文档——它说明了每个接口的",
    "形状与用法：",
    "  curl --noproxy \"*\" -m 30 -u <address>:<password> <serverURL>/api/self",
    "（同样的 Basic 认证头适用于所有调用。）",
    "",
    "MCP 只是调用同一系统的另一种方式。如果你因缺少 MCP 而遇到问题，告诉",
    "我如何在当前 agent 软件里安装它。",
    "",
    "就绪后择时问我是否应进入值守模式——若值守，无事时用脚本等信：轮询",
    "/api/inbox，记录收件箱中最近处理的消息的 id（id 为按时间排序的",
    "ULID），本地过滤比它新的。值守意味着「检查收件箱-处理新消息」的",
    "无限循环。"
  ].join("\n") + "\n";

  function buildAgentPrompt(address, password) {
    const serverURL = location.origin;
    let lang = "en";
    try { lang = (window.I18N && window.I18N.lang()) || "en"; } catch (_) {}
    var tpl = (lang === "zh") ? PROMPT_TPL_ZH : PROMPT_TPL_EN;
    return tpl
      .split("<address>").join(address)
      .split("<password>").join(password)
      .split("<serverURL>").join(serverURL);
  }

  // Subordinate registration success (S2 from manage.js, which owns the
  // flow but not buildAgentPrompt): fill the modal's hidden prompt with the
  // fresh credentials so Copy prompt always carries the real secret
  // (superior 09-02: no visible fold — the copy button is the only surface).
  document.addEventListener("subreg:success", function (e) {
    var d = (e && e.detail) || {};
    var pre = $("#subreg-prompt");
    if (pre) pre.textContent = buildAgentPrompt(d.address, d.password);
  });
  // Overview rows carry .mq signature marquees — measure them after each
  // render (the measurer lives here; the renderer is overview.js).
  document.addEventListener("ovw:rendered", maybeMarqueeSigs);

  function showRegisterSuccess(address, password) {
    $("#login-form-block").classList.add("hidden");
    $("#register-form-block").classList.add("hidden");
    const sb = $("#register-success-block");
    $("#register-success-address").textContent = address;
    // Human-set passwords never echo back (server returns none) — the page
    // just reminds the user to keep it; server-generated ones (one-click)
    // still show once.
    const pwEl = $("#register-success-password");
    pwEl.textContent = password || t("reg.pwUserSet");
    // Self-set password flag: the reminder text is NOT a credential —
    // the success-screen login button must not auto-submit it (drill A1).
    pwEl.dataset.selfSet = password ? "" : "1";
    // Agent prompt block no longer shows on the human register channel —
    // that content lives in the one-click agent flow's modal only
    // (feedback). Hidden here so the block can stay in the markup for
    // potential agent-channel reuse.
    const details = $("#agent-setup-details");
    if (details) details.classList.add("hidden");
    sb.classList.remove("hidden");
  }

  // ---- one-click agent register (v0.4.2) ----
  // True one-click: random name -> register -> copy prompt -> modal, in a
  // single action. The modal shows the clipboard status and the full prompt
  // (with a manual Copy fallback — clipboard writes can be denied, e.g. on
  // plain http or without user gesture in some browsers).




  // Copy the agent prompt to the clipboard (one-click).
  $("#btn-copy-prompt").addEventListener("click", function () {
    const text = $("#agent-prompt").textContent;
    const status = $("#copy-prompt-status");
    const done = function () { status.textContent = t("common.copied"); setTimeout(function () { status.textContent = ""; }, 1500); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () { status.textContent = t("common.copyFailed"); });
    } else {
      // Fallback: select the pre block.
      const range = document.createRange(); range.selectNode($("#agent-prompt"));
      const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(range);
      try { document.execCommand("copy"); done(); } catch (_) { status.textContent = t("common.copyFailed"); }
      sel.removeAllRanges();
    }
  });

  // Show the register link only when the server allows registration.
  async function refreshRegisterLink() {
    const link = $("#link-show-register");
    if (!link) return;
    try {
      const st = await api("/api/info?query=settings");
      // Toggle only the register link's wrapper — the sibling "back to
      // portal" link must stay visible even when registration is closed.
      const wrap = $("#register-link-wrap");
      (wrap || link).style.display = st.registration_enabled ? "" : "none";
    } catch (_) {
      const wrap = $("#register-link-wrap");
      (wrap || link).style.display = "none";
    }
  }

  // Live preview of the full address the chosen name will produce.
  function updateRegisterPreview() {
    const name = ($("#register-name").value || "").trim();
    $("#register-preview").textContent = (name || "name") + "@" + systemDomain;
  }

  // Tabs only admins see. Mail is visible to everyone (v0.5.7): admins browse
  // every account globally; regular accounts browse their own mail plus any
  // self-declared subordinate accounts (read-only). Settings and Audit are
  // admin-only system controls.
  const ADMIN_ONLY_TABS = ["settings", "audit"];

  function applyRole(isAdmin) {
    let visible = 0;
    $$(".tab").forEach(function (b) {
      const tab = b.dataset.tab;
      const adminOnly = ADMIN_ONLY_TABS.indexOf(tab) !== -1;
      const hide = adminOnly && !isAdmin;
      b.classList.toggle("hidden", hide);
      if (!hide) visible++;
    });
    // 1047 (boss): the bottom nav type scale keys off the button count -
    // exactly-four gets the larger bold label, other sets keep the fit size.
    const nav = document.querySelector("nav");
    if (nav) nav.dataset.tabs = String(visible);
  }

  // An over-wide address auto-scrolls (ping-pong) instead of just clipping:
  // reveal the head of the address, then ease back to the tail. Skipped for
  // reduced-motion users (plain ellipsis stays).
  function maybeMarqueeWhoami() {
    const el = $("#whoami");
    if (!el) return;
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const diff = el.scrollWidth - el.clientWidth;
    if (diff > 8) {
      // Classic ticker: text enters from the right edge, travels past the
      // left edge, brief hold, loop. Full address is on screen once per
      // cycle (superior 01M1239C: pong felt jumpy and never showed all).
      const dist = el.clientWidth + el.scrollWidth + 24;
      el.style.setProperty("--wm-start", el.clientWidth + "px");
      el.style.setProperty("--wm-end", -(el.scrollWidth + 24) + "px");
      el.style.setProperty("--wm-dur", Math.max(8, dist / 26) + "s");
      el.classList.add("marquee");
    } else {
      el.classList.remove("marquee");
      el.style.removeProperty("--wm-start");
      el.style.removeProperty("--wm-end");
      el.style.removeProperty("--wm-dur");
    }
  }
  if (window.matchMedia && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
    window.addEventListener("resize", maybeMarqueeWhoami);
  // Superior 01M1836CAK: app-side addresses degraded to ellipsis — the
  // measurement was stale (webfonts arrive late, the (admin) suffix is
  // written back after first measure). Re-measure once fonts are ready.
  if (document.fonts && document.fonts.ready && window.matchMedia &&
      !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
    document.fonts.ready.then(function () { maybeMarqueeWhoami(); });
  }
  }
  window.addEventListener("resize", maybeMarqueeWhoami);

  // boss 09-29 (marquee "not running"): rebuilds recreate the rows, and a
  // fresh node starts its marquee animation from zero - under steady mail
  // traffic the marquee never gets to show progress. Snapshot the running
  // clocks per row address before a rewrite and restore them after the
  // rescan; advance-only (currentTime < saved), so a fresh surface is never
  // rewound.
  function mqSnap(root) {
    var out = {};
    $$("tr[data-act-acct] .sig-track, .im3-row[data-claddr] .im3-addr-in", root).forEach(function (t) {
      var tr = t.closest("tr[data-act-acct], .im3-row[data-claddr]");
      var k = String(tr ? (tr.getAttribute("data-act-acct") || tr.getAttribute("data-claddr") || "") : "").toLowerCase() + "|" + t.className;
      t.getAnimations().forEach(function (a) { (out[k] = out[k] || []).push([a.animationName, a.currentTime]); });
    });
    return out;
  }
  function mqMerge(a, b2) {
    if (!a) return b2;
    Object.keys(b2).forEach(function (k) { a[k] = (a[k] || []).concat(b2[k]); });
    return a;
  }
  function mqApply(root, phase) {
    if (!phase) return;
    $$("tr[data-act-acct] .sig-track, .im3-row[data-claddr] .im3-addr-in", root).forEach(function (t) {
      var tr = t.closest("tr[data-act-acct], .im3-row[data-claddr]");
      var k = String(tr ? (tr.getAttribute("data-act-acct") || tr.getAttribute("data-claddr") || "") : "").toLowerCase() + "|" + t.className;
      (phase[k] || []).forEach(function (c) {
        t.getAnimations().forEach(function (a) { if (a.animationName === c[0] && a.currentTime < c[1]) a.currentTime = c[1]; });
      });
    });
  }
  // maybeMarqueeSigs runs over-wide signature cells (Accounts + Directory)
  // as a seamless one-way loop (superior feedback: ping-pong never reveals
  // the whole text). The track carries the text twice; each copy has the
  // same trailing gap, so translateX(-50%) is exactly one period.
  // Reduced-motion users keep the ellipsis.
  // boss 0930 (跑马灯几乎完全不动): this scan used to be destructive -
  // remove the marquee class, measure, re-add on overflow - so EVERY scan
  // restarted every engaged marquee from zero, and the scan rides high
  // Frequency events (window resize, the header-growth sentinel's synthetic
  // resize, ovw:rendered). Worse, a scan while the Accounts page is hidden
  // (ovw:rendered fires on Overview renders) measures an all-zero layout
  // and STRIPPED the class without being able to re-add it - the marquee
  // stayed dead until the next full re-render happened to run visible.
  // Two changes, both measurement-only: (1) cells that are not rendered
  // are left untouched - a hidden scan can no longer kill what it cannot
  // see; (2) track-grammar cells (sig-txt present) are measured in place -
  // the first copy's border-box width minus its own padding equals the raw
  // overflow the class-off measurement used to produce - so an already
  // running marquee is never re-classed and resizes become true no-ops.
  // Cells without sig-txt keep the legacy remove-then-measure dance.
  function maybeMarqueeSigs() {
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    $$(".sig-cell, .mq").forEach(function (cell) {
      var txt = cell.querySelector(".sig-txt");
      var cw = cell.clientWidth;
      if (!cw && !(txt && txt.getBoundingClientRect().width)) return; // display:none - leave state alone
      if (txt) {
        var has = cell.classList.contains("marquee");
        var pad = has ? (parseFloat(getComputedStyle(txt).paddingRight) || 0) : 0; // marquee-on pads each copy with the gap
        var over = Math.ceil(txt.getBoundingClientRect().width) - pad - cw;
        if (over > 8) {
          var dur = Math.max(8, (over + 48) / 28) + "s"; // linear period over (overflow + one gap), ~28px/s
          if (cell.style.getPropertyValue("--wm-dur") !== dur) cell.style.setProperty("--wm-dur", dur);
          if (!has) cell.classList.add("marquee");
        } else if (has) {
          cell.classList.remove("marquee");
          cell.style.removeProperty("--wm-dur");
        }
        return;
      }
      cell.classList.remove("marquee"); // legacy grammar: dup hidden again -> measure raw overflow
      cell.style.removeProperty("--wm-dur");
      const diff = cell.scrollWidth - cell.clientWidth;
      if (diff > 8) {
        cell.style.setProperty("--wm-dur", Math.max(8, (diff + 48) / 28) + "s");
        cell.classList.add("marquee");
      }
    });
    if (mqPhase) { mqApply(document, mqPhase); mqPhase = null; }
  }
  window.addEventListener("resize", maybeMarqueeSigs);

  // Accounts-page one-screen fit (mobile plan): the own card and the
  // register button stay fixed; the subordinate list and the contacts list
  // scroll within the remaining viewport height — the contacts box bottom
  // is pinned just above the viewport bottom so the height alignment is
  // visible. PC keeps the table.
  // 键盘态视口高（0.2.5 高优 v2）：pan 模式键盘下 innerHeight 不缩、只有
  // visualViewport 缩——量测取两者较小值，两种键盘模式都成立。
  // Bottom floating bar (mobile nav) occupies the viewport bottom when
  // fixed - every one-screen fit must budget for it (boss rc2 feedback 1).
  window.__fixedNavInset = function () {
    // Below the breakpoint the bar overlays the viewport bottom by design;
    // measure it when rendered, fall back to its 56px design height when
    // an early fit runs before first paint settles.
    if (window.innerWidth > 800) return 0;
    var n = document.querySelector("nav");
    if (!n) return 56;
    var h = Math.round(n.getBoundingClientRect().height) || 56;
    return Math.max(h, 56);
  };

  function acKbVh() {
    var vh = window.visualViewport ? Math.min(window.innerHeight, Math.round(window.visualViewport.height)) : window.innerHeight;
    return vh - (window.__fixedNavInset ? window.__fixedNavInset() : 0);
  }
  function fitAccountsOneScreen() {
    var page = $("#tab-accounts");
    if (!page || page.classList.contains("hidden")) return;
    if (window.innerWidth > 800) { page.style.removeProperty("--acc-1s"); return; }
    // Document lock (superior 0.2.2 feedback point 2): size the tab itself
    // like #tab-inbox so the page can never scroll, whatever the inner
    // measurement timing does.
    var tabTop = page.getBoundingClientRect().top;
    if (tabTop > 0) {
      var accH = acKbVh() - tabTop;
      if (accH < 300) accH = 300;
      page.style.setProperty("--acc-1s", accH + "px");
    }
    var subList = document.querySelector(".sub-list");
    var ctBox = $("#acc-m-contacts");
    if (!ctBox) return;
    // 1048e（boss rc2c 实测：滑动中被拽回顶部）：列表滚动中严禁重钉——真机滑动时

    // 地址栏伸缩触发 resize，重钉按页顶几何重算会把滚动清零（maxHeight=none

    // 未钳位即回落 0）。回到顶部后的下次 fit 自然恢复。

    if (ctBox.scrollTop > 2) return;

    ctBox.style.maxHeight = "none";
    if (subList) subList.style.maxHeight = "none";
    var ctTop = ctBox.getBoundingClientRect().top;
    if (ctTop <= 0) return; // not laid out (hidden tab)
    var reserve = 120; // minimum visible slice of the contacts list
    var card = document.querySelector(".agentreg-card");
    if (subList) {
      // Superior 09-02 (01M1FWBSM6): "subordinate may take slightly more"
      // means the WHOLE subordinate card (button + note + sliding list) —
      // so budget the card at ~52% of the space below the own card and cap
      // the sliding list at whatever the button/note chrome leaves.
      var cardTop = card ? card.getBoundingClientRect().top : subList.getBoundingClientRect().top;
      var avail = acKbVh() - 24 - cardTop;
      var fixed = card ? card.offsetHeight - subList.offsetHeight : 0;
      var cardTarget = Math.round(avail * 0.52);
      var subH = Math.min(subList.scrollHeight, Math.max(96, cardTarget - fixed));
      subList.style.maxHeight = subH + "px";
    }
    var ctTop2 = ctBox.getBoundingClientRect().top;
    var ctH = acKbVh() - ctTop2;
    if (ctH < reserve && subList) {
      // Shrink the subordinate list by the deficit, then re-pin exactly.
      var deficit = reserve - ctH;
      var cur = parseInt(subList.style.maxHeight, 10) || 0;
      subList.style.maxHeight = Math.max(96, cur - deficit) + "px";
      ctTop2 = ctBox.getBoundingClientRect().top;
      ctH = acKbVh() - ctTop2;
    }
    ctBox.style.maxHeight = Math.max(96, ctH) + "px";
    // Correction pass: if anything still pushes the document past the
    // viewport (tab padding/margins included), give the difference back
    // out of the locked tab height itself — the same closure the inbox
    // and compose fits use.
    var over = document.documentElement.scrollHeight - window.innerHeight;
    if (over > 0) {
      var curAcc = parseInt(page.style.getPropertyValue("--acc-1s"), 10) || 0;
      if (curAcc > 240) page.style.setProperty("--acc-1s", Math.max(240, curAcc - over) + "px");
      // the contacts box is flush by design - it must give back the same
      // overflow or the page keeps a scrollable gray tail
      var cmBox = document.querySelector("#acc-m-contacts");
      if (cmBox) {
        var cm = parseInt(cmBox.style.maxHeight, 10) || 0;
        if (cm > 96) cmBox.style.maxHeight = Math.max(96, cm - over) + "px";
      }
    }
    // Converging self-refit: the first pass can run before the mobile

    // layout (fixed nav / fonts) settles - re-run until two passes agree,

    // so the list lands flush on the bar on every entry path.

    var sig2 = Math.round(acKbVh()) + "/" + Math.round(tabTop) + "/" + Math.round(ctBox.getBoundingClientRect().top) + "/" + (ctBox.style.maxHeight || "");

    if (fitAccountsOneScreen._sig !== sig2) {

      fitAccountsOneScreen._sig = sig2;

      clearTimeout(fitAccountsOneScreen._rt);

      fitAccountsOneScreen._rt = setTimeout(fitAccountsOneScreen, 300);

    }
  }
  window.addEventListener("resize", fitAccountsOneScreen);
  // Header growth sentinel (boss rc6: gray band of shifting heights): the
  // header can grow AFTER the first fit (whoami fill / marquee mount) -
  // re-dispatch resize so every one-screen fit recomputes with the
  // settled geometry.
  (function headerGrowthSentinel() {
    var hdr = document.getElementById("app-header");
    if (!hdr || !window.MutationObserver) return;
    var t = null;
    new MutationObserver(function () {
      clearTimeout(t);
      t = setTimeout(function () { window.dispatchEvent(new Event("resize")); }, 120);
    }).observe(hdr, { childList: true, subtree: true, characterData: true });
  })();

  // 输入法高优修（上级 0.2.5）：软键盘视口变化时账户页重算（与 manage.js
  // 的 vv 重算同口径，各模块挂自家 fit，防抖 120ms）。
  if (window.visualViewport) {
    var accVvT = null;
    window.visualViewport.addEventListener("resize", function () {
      clearTimeout(accVvT);
      accVvT = setTimeout(function () {
        fitAccountsOneScreen();
        var ae = document.activeElement;
        if (ae && (ae.tagName === "TEXTAREA" || ae.tagName === "INPUT")) {
          try { ae.scrollIntoView({ block: "start" }); } catch (_) {}
        }
      }, 120);
    });
  }
  document.addEventListener("accounts:refresh", function () {
    setTimeout(fitAccountsOneScreen, 50);
    setTimeout(fitAccountsOneScreen, 300); // second pass: late fonts/layout
  });

  // showApp reveals the panel and applies role-based tab visibility.
  function showApp() {
    hideAllScreens();
    $("#app-header").classList.remove("hidden");
    document.querySelector("main").classList.remove("hidden");
    const s = getSession();
    if (s) {
      $("#whoami").innerHTML = '<span class="wm-txt">' +
        esc(s.address + (s.is_admin ? " (admin)" : "")) + "</span>";
      maybeMarqueeWhoami();
      applyRole(!!s.is_admin);
      refreshInboxBadge();
      // Per-user caches must not leak across logins (logout keeps the DOM).
      document.dispatchEvent(new CustomEvent("manage:reset"));
      // Refresh preferences from the account record (silent; localStorage
      // seed already applied). keepSession: a failure here must not log
      // anyone out.
      api("/api/profile/self", { keepSession: true }).then(function (p) {
        mergePrefs(p && p.prefs);
      }).catch(function () {});
    }
  }

  $("#btn-logout").addEventListener("click", async function () {
    // v0.6.27: revoke server-side token before clearing local auth.
    try { await api("/api/auth/token", { method: "DELETE" }); } catch (_) {}
    setSession(null);
    localStorage.removeItem("agentmail_token");
    showLogin();
  });

  // ---- login ----

  // v0.1.9 (Felix): select-on-focus — the remembered address is prefilled;
  // typing over it without select used to create franken addresses that could
  // not log in (found while reproducing the v0.1.3 P1 locally).
  $("#login-address").addEventListener("focus", function () { this.select(); });
  $("#btn-login").addEventListener("click", async function () {
    const address = $("#login-address").value.trim();
    const password = $("#login-password").value;
    const remember = $("#login-remember").checked;
    const status = $("#login-status");
    if (!address || !password) { status.textContent = "Address and password are required."; return; }
    status.textContent = "Signing in…";
    // Cache creds tentatively so api() sends them, then verify via account/info.
    setSession({ address: address, password: password, is_admin: false });
    try {
      const me = await api("/api/account/info?query=self");
      const s = getSession(); s.is_admin = !!me.is_admin; setSession(s);
      maybeMarqueeWhoami(); // role suffix changes text width (01M1836CAK)
      // v0.6.27 token: acquire after login, store per "remember me" pref.
      // Password is NEVER stored in localStorage (alice red line).
      try {
        const tok = await api("/api/auth/token", { method: "POST" });
        if (tok && tok.token) {
          if (remember) setToken(address, tok.token, s.is_admin);
          // else: password stays in sessionStorage (session-only mode).
        }
      } catch (_) { /* token endpoint optional; basic auth still works */ }
      status.textContent = "";
      showApp();
      // 0.3.3-B (boss directive): on phones the app opens on the Accounts
      // page (the IM-style list); PC keeps Overview as the landing tab.
      activateTab(window.matchMedia && window.matchMedia("(max-width: 800px)").matches ? "accounts" : "overview");
    } catch (e) {
      setSession(null);
      // The tentative session (set above) makes core's 401 path report
      // "session expired" — but during login that means the credentials
      // are wrong (drill A2).
      status.textContent = /session expired/i.test(e.message)
        ? t("login.badCreds")
        : "Login failed: " + e.message;
    }
  });

  // ---- register (on the login page) ----

  $("#link-show-register").addEventListener("click", function (e) { e.preventDefault(); showRegisterForm(); });
  $("#btn-register-cancel").addEventListener("click", showLoginForm);

  // Portal entry points: login goes to the classic form; register opens the
  // register form directly (inside the login page, which hosts it — the
  // one-click button lives on that form).
  $("#btn-portal-login").addEventListener("click", showLogin);
  $("#btn-portal-register").addEventListener("click", function () { showLogin(); showRegisterForm(); });
  // Team register (v0.5.14): third portal entry — same auth-card surface,
  // dedicated form; visibility follows registration_enabled in loadPortal.
  $("#btn-portal-team").addEventListener("click", function () { showLogin(); showTeamForm(); });
  (function wireTeamReg() {
    const submit = $("#btn-team-submit");
    if (!submit) return;
    $("#team-name").addEventListener("input", updateTeamPreview);
    $("#btn-team-cancel").addEventListener("click", function () { showPortal(); });
    $("#btn-team-done").addEventListener("click", function () { showPortal(); });
    // Stepper (the number input is gone; +/- clamp 1..10).
    $("#btn-team-less").addEventListener("click", function () {
      var n = $$("#team-member-rows .team-mrow").length;
      if (n > 1) renderTeamMemberRows(n - 1);
    });
    $("#btn-team-more").addEventListener("click", function () {
      var n = $$("#team-member-rows .team-mrow").length;
      if (n < 10) renderTeamMemberRows(n + 1);
    });
    // Reroll: the single dice next to a row, or reroll-all.
    $("#team-member-rows").addEventListener("click", function (ev) {
      var dice = ev.target.closest(".dice");
      if (!dice) return;
      var row = dice.closest(".team-mrow");
      var input = row && row.querySelector("input");
      if (!input) return;
      var used = {};
      $$("#team-member-rows .team-mrow input").forEach(function (inp) {
        if (inp !== input && inp.value) used[inp.value] = 1;
      });
      input.value = randomTeamName(used);
    });
    $("#btn-team-reroll-all").addEventListener("click", function () {
      var inputs = $$("#team-member-rows .team-mrow input");
      var used = {};
      inputs.forEach(function (inp) { inp.value = randomTeamName(used); });
    });
    submit.addEventListener("click", async function () {
      const name = ($("#team-name").value || "").trim();
      const pw = $("#team-password").value || "";
      const status = $("#team-status");
      const inputs = $$("#team-member-rows .team-mrow input");
      const members = inputs.map(function (inp) { return (inp.value || "").trim(); });
      if (!name) { status.textContent = t("reg.needName"); return; }
      if (!/^[A-Za-z0-9_-]+$/.test(name)) { status.textContent = t("reg.nameRule"); return; }
      if (pw.length < 8) { status.textContent = t("reg.pwTooShort"); return; }
      if (!members.length || members.length > 10) { status.textContent = t("team.sizeRange"); return; }
      for (var i = 0; i < members.length; i++) {
        if (!members[i]) { status.textContent = t("team.needMemberName"); return; }
        if (!/^[A-Za-z0-9_-]+$/.test(members[i])) { status.textContent = t("reg.nameRule"); return; }
      }
      submit.disabled = true;
      status.textContent = t("common.loading");
      try {
        // `members` is the v2 contract (server creates exactly these);
        // team_size rides along so older servers still accept the request.
        const res = await api("/api/register-team", {
          method: "POST",
          // api() passes the body through to fetch verbatim — a raw object
          // would stringify to "[object Object]" and the server would reject
          // it ("invalid character 'o'"). Every other call site stringifies.
          body: JSON.stringify({ username: name, password: pw, team_size: members.length, members: members }),
        });
        if (renderTeamSuccess(res, pw) !== false) {
          $("#team-copy-status").textContent = "";
          $("#team-form-block").classList.add("hidden");
          $("#team-success-block").classList.remove("hidden");
        } else {
          status.textContent = t("common.error", { msg: "empty team response" });
        }
      } catch (e) {
        status.textContent = t("common.error", { msg: e.message });
      }
      submit.disabled = false;
    });
    // Success page: per-card copy (creds / agent prompt) + copy-all.
    $("#team-cred-cards").addEventListener("click", function (ev) {
      var btn = ev.target.closest("button.cp");
      if (!btn) return;
      var text;
      if (btn.dataset.cpPrompt) {
        text = buildAgentPrompt(btn.dataset.cpPrompt, btn.dataset.cpPromptPw);
      } else {
        text = btn.dataset.cpAddr + "\n" + btn.dataset.cpPw;
      }
      copyText(text).then(function (ok) {
        var st = $("#team-copy-status");
        st.textContent = ok ? t("common.copied") : t("common.copyManual");
        setTimeout(function () { st.textContent = ""; }, 2000);
      });
    });
    $("#btn-team-copy").addEventListener("click", function () {
      var box = $("#team-cred-cards");
      // Rebuild the download text from the rendered cards (source of
      // truth), scoped to the box: an unscoped selector once collected
      // zero rows and produced an empty txt (hotfix v0.1.12.1).
      var lines = [];
      if (box) $$(".cred-card", box).forEach(function (card) {
        var who = card.querySelector(".who").textContent;
        // Real passwords ride in the button dataset (owner shows the
        // keep-it reminder instead of echoing the password).
        var btn = card.querySelector("button.cp");
        if (btn) lines.push(who + ": " + btn.dataset.cpAddr + "  " + btn.dataset.cpPw);
      });
      if (!lines.length) {
        var stE = $("#team-copy-status");
        stE.textContent = t("common.error", { msg: "no credentials rendered" });
        setTimeout(function () { stE.textContent = ""; }, 2500);
        return;
      }
      // Superior 08-31: one-click DOWNLOAD instead of clipboard — easier
      // to persist safely. Real passwords ride in the button datasets.
      var blob = new Blob([lines.join("\r\n") + "\r\n"], { type: "text/plain;charset=utf-8" });
      var dl = document.createElement("a");
      dl.href = URL.createObjectURL(blob);
      dl.download = "team-credentials.txt";
      document.body.appendChild(dl);
      dl.click();
      dl.remove();
      setTimeout(function () { URL.revokeObjectURL(dl.href); }, 1000);
      var st = $("#team-copy-status");
      st.textContent = t("team.downloadDone");
      setTimeout(function () { st.textContent = ""; }, 2000);
    });
  })();
  $("#link-back-portal").addEventListener("click", function (e) { e.preventDefault(); showPortal(); });
  $("#register-name").addEventListener("input", updateRegisterPreview);

  $("#btn-register-submit").addEventListener("click", async function () {
    const name = ($("#register-name").value || "").trim();
    const pw = $("#register-password").value || "";
    const status = $("#register-status");
    if (!name) { status.textContent = t("reg.needName"); return; }
    if (!/^[A-Za-z0-9_-]+$/.test(name)) {
      status.textContent = t("reg.nameRule");
      return;
    }
    // Human registrations choose their own password (required, min 8 —
    // mirrors the setup rule). Agents use the one-click flow instead.
    if (pw.length < 8) { status.textContent = t("reg.pwTooShort"); return; }
    status.textContent = t("reg.registering");
    try {
      const res = await api("/api/register", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name, password: pw }),
      });
      status.textContent = "";
      $("#register-password").value = "";
      showRegisterSuccess(res.address, res.password);
    } catch (e) {
      status.textContent = "Registration failed: " + e.message;
    }
  });

  // Success-screen buttons.
  $("#btn-register-login").addEventListener("click", function () {
    const addr = $("#register-success-address").textContent;
    const pwEl = $("#register-success-password");
    $("#login-address").value = addr;
    showLoginForm();
    if (pwEl.dataset.selfSet) {
      // Self-set password: nothing to prefill — focus the field and let
      // the user type it (auto-submitting the reminder text guaranteed
      // a 401; drill A1).
      $("#login-password").focus();
      return;
    }
    $("#login-password").value = pwEl.textContent;
    $("#btn-login").click();
  });
  $("#btn-register-another").addEventListener("click", showRegisterForm);

  $("#btn-setup").addEventListener("click", async function () {
    const domain = $("#setup-domain").value.trim();
    const pw = $("#setup-admin-password").value;
    const status = $("#setup-status");
    if (!domain) { status.textContent = "Domain is required."; return; }
    if (pw.length < 8) { status.textContent = "Password must be at least 8 characters."; return; }
    status.textContent = "Initializing…";
    try {
      const res = await api("/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ admin_password: pw, domain: domain }),
      });
      status.textContent = "Done. Reloading…";
      toast("System initialized", "success");
      // System is now initialized; reload so init() routes to the login page,
      // where the admin can sign in with the password just chosen.
      setTimeout(function () { window.location.reload(); }, 1500);
    } catch (e) {
      status.textContent = t("common.error", { msg: e.message });
    }
  });

  init();

  // ---- compose ----

  // Populate the Compose To-field dropdown with known recipients (admins get
  // every account; regular accounts get their contacts). Builds a custom
  // dropdown (not a native datalist) so clicking a recipient clears the input
  // and fills it — the behavior admin requested.

  // ---- 0.3.2 系统更新弹窗（0.3.1 方案放行；契约定稿=Devi 0922：推送表
  // {id,version,title,body_md,published_at,published}+last_read_push_id+四端点。
  // 触发=进系统总览页后取最新已发布推送，未读则弹；关闭即上报已读、不阻塞；
  // latest 失败/未上线=静默不弹零打扰；文案经数据面下发、前端一字不改） ----
  (function updatesModalInit() {
    function mdLite(s) {
      if (window.marked && window.DOMPurify) {
        try {
          return DOMPurify.sanitize(window.marked.parse(s || "", { breaks: true }), {
            FORBID_TAGS: ["style", "img", "audio", "video", "iframe"], FORBID_ATTR: ["style"]
          });
        } catch (_) { /* fall through to plain */ }
      }
      return "<pre>" + esc(s || "") + "</pre>";
    }
    // 落位定稿（0922 呈审图）：title=内容标题块；body 逐行渲染，间隔号/符点开头
    // 行为悬挂缩进条目（续行对齐首字），其余行走 markdown
    function renderPush(title, body) {
      var out = title ? '<div class="updates-headline">' + esc(title) + "</div>" : "";
      var lines = String(body || "").split("\n");
      var parts = [];
      for (var i = 0; i < lines.length; i++) {
        var ln = lines[i];
        var c = ln.charAt(0);
        if ((c === "\u00b7" || c === "\u2022" || c === "\u30fb") && ln.charAt(1) === " ")
          parts.push('<p class="updates-item">' + esc(ln) + "</p>");
        else if (ln.replace(/\s/g, "") !== "") parts.push(mdLite(ln));
      }
      return out + parts.join("");
    }
    var shownId = null;
    function maybeShowUpdates() {
      if (!getSession() || shownId !== null) return;
      api("/api/updates/latest", { keepSession: true }).then(function (d) {
        if (!d || !d.unread || !d.push || shownId !== null) return;
        shownId = d.push.id;
        $("#updates-modal-title").textContent = t("updates.title") + " \u00b7 " + (d.push.version || "");
        $("#updates-body").innerHTML = renderPush(d.push.title, d.push.body_md) +
          (d.unread_more > 0 ? '<p class="updates-more muted">' + esc(t("updates.more", { n: d.unread_more })) + "</p>" : "");
        $("#btn-updates-ok").textContent = t("updates.ok");
        $("#updates-modal").classList.remove("hidden");
      }).catch(function () { }); // 数据面未上线/失败=零打扰
    }
    function markUpdatesRead() {
      var m = $("#updates-modal");
      if (m && !m.classList.contains("hidden")) m.classList.add("hidden");
      if (shownId !== null) {
        var pid = shownId; shownId = null;
        api("/api/updates/read", { method: "POST", body: JSON.stringify({ id: pid }), keepSession: true }).catch(function () {});
      }
    }
    document.addEventListener("click", function (ev) {
      var id = ev.target && ev.target.id;
      if (id === "btn-updates-ok" || id === "btn-updates-close") markUpdatesRead();
      else if (ev.target && ev.target.id === "updates-modal") markUpdatesRead(); // 同限额弹窗：点遮罩关
    });
    // 触发：进系统总览页（brand 标识 / 概览 tab；仅进入时查一次，不跨页追弹）
    document.addEventListener("click", function (ev) {
      var b = ev.target && ev.target.closest && ev.target.closest("#brand-home, .tab[data-tab='overview']");
      if (b) setTimeout(maybeShowUpdates, 400);
    }, true);
  })();

})();
