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
    const tab = $(".tab[data-tab=inbox]");
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
  var prevInboxUnread = 0;
  async function refreshInboxBadge() {
    if (!getSession()) { setInboxBadge(0); return; }
    // Background tabs skip the tick — the badge refreshes on visibility
    // return, so 5s polling stays cheap in aggregate (admin: 2-5s wanted).
    if (document.visibilityState === "hidden") return;
    const seq = ++badgeSeq;
    try {
      const d = await api("/api/inbox?limit=1");
      if (seq !== badgeSeq) return; // a newer refresh superseded this one
      var cur = d.unread_count || 0;
      if (cur > prevInboxUnread) {
        // New mail detected — notify manage.js incremental merger (v0.2.1).
        document.dispatchEvent(new CustomEvent("inbox:newmail"));
      }
      prevInboxUnread = cur;
      setInboxBadge(cur);
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
  document.addEventListener("accounts:refresh", function () { loadAccounts(); });
  document.addEventListener("nav:activate", function (ev) {
    var tab = (ev.detail || {}).tab;
    if (tab) activateTab(tab);
  });
})();

  function activateTab(name) {
    // Leaving a message view by any route (tab switch included) must stop
    // all audio (feedback: sound kept playing after leaving the content).
    document.dispatchEvent(new CustomEvent("audio:stop-all"));    $$(".tab").forEach(function (b) {
      b.classList.toggle("active", b.dataset.tab === name);
    });
    $$(".tab-panel").forEach(function (p) { p.classList.add("hidden"); });
    $("#tab-" + name).classList.remove("hidden");
    if (name === "overview") loadOverview();
    if (name === "accounts") loadAccounts();
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
        api("/api/profile/self", { keepSession: true }).catch(function () { return null; }),
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
        tbody.innerHTML = '<tr><td colspan="5">' + t("acc.noAccounts") + '</td></tr>';
        return;
      }
      tbody.innerHTML = data.accounts.map(function (a) {
        const rowCls = a.disabled ? " class=\"row-disabled\"" : "";
        // Build tag badges: admin, listed (visible), disabled.
        var tags = "";
        if (a.is_admin) tags += ' <span class="badge-admin">admin</span>';
        if (a.visible) tags += ' <span class="badge-listed">listed</span>';
        if (a.disabled) tags += ' <span class="badge-disabled">disabled</span>';
        const toggleBtn = a.is_admin
          ? "" // admin cannot be disabled (lockout guard), so no toggle button
          : a.disabled
            ? '<button class="row-action" data-enable="' + esc(a.address) + '">' + t("act.enable") + '</button>'
            : '<button class="row-action" data-disable="' + esc(a.address) + '">' + t("act.disable") + '</button>';
        return "<tr" + rowCls + ">" +
          '<td class="addr-cell" data-label="' + t("col.address") + '">' + esc(a.address) + "</td>" +
          '<td data-label="' + t("col.tags") + '">' + tags.trim() + "</td>" +
          '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(a.signature || "") + '</span><span class="sig-dup" aria-hidden="true">' + esc(a.signature || "") + "</span></span></td>" +
          '<td data-label="' + t("col.created") + '">' + fmtTime(a.created_at) + "</td>" +
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
      tbody.innerHTML = '<tr><td colspan="5">Error: ' + esc(e.message) + "</td></tr>";
    }
  }

  // loadAccountsRegular renders the regular-user Accounts view: themselves
  // (with a change-password button) plus the people they've exchanged mail with
  // (from /api/contacts). No admin/disabled/uuid columns — those are sensitive
  // and not relevant to a personal view.
  async function loadAccountsRegular(selfAddr) {
    // The "+ Register new account" button is admin-only.
    const regBtn = $("#btn-register");
    if (regBtn) regBtn.classList.add("hidden");
    const invBtnRegular = $("#btn-invalid");
    if (invBtnRegular) invBtnRegular.classList.add("hidden");
    // PC register-subordinate block above the table (mobile keeps the
    // in-container button; admin sessions never see it).
    const subregPc = $("#subreg-pc");
    if (subregPc) subregPc.classList.remove("hidden");
    const tbody = $("#accounts-table tbody");
    tbody.textContent = "";
    // Subordinate management UI lives in Preferences since v0.6; Accounts
    // still needs fresh edges for the sub badges (and read-only rows).
    var subs = await requestSubs(true).catch(function () { return null; });
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
      });
    } catch (e) { /* non-fatal — badges degrade to sub-only */ }
    var rows = [];
    rows.push(
      "<tr class=\"own-row\">" +
      '<td class="addr-cell mq" data-label="' + t("col.address") + '"><span class="sig-track"><span class="sig-txt"><strong>' + esc(selfAddr) + '</strong></span><span class="sig-dup" aria-hidden="true"><strong>' + esc(selfAddr) + "</strong></span></span></td>" +
      '<td data-label="' + t("col.tags") + '"><span class="badge-listed">you</span>' + (ownVisible ? ' <span class="badge-listed">listed</span>' : "") + "</td>" +
      '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(ownSig) + '</span><span class="sig-dup" aria-hidden="true">' + esc(ownSig) + "</span></span></td>" +
      "<td data-label=\"Created\"></td>" +
      '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action" id="btn-change-pw">' + t("act.changePw") + '</button></td>' +
      "</tr>"
    );
    // Mobile one-screen plan: the own account renders as a compact card
    // (same grammar as the contact cards) above the subordinate zone; the
    // table row above hides on phones via CSS (.own-row).
    var ownMobile =
      '<div class="ct-card">' +
      '<div class="ct-line">' + (ownVisible ? '<span class="badge-listed">listed</span>' : "") +
      '<div class="ct-addr mq"><span class="sig-track"><span class="sig-txt"><strong>' + esc(selfAddr) + '</strong></span><span class="sig-dup" aria-hidden="true"><strong>' + esc(selfAddr) + "</strong></span></span></div>" +
      '<span class="badge-listed">you</span></div>' +
      (ownSig ? '<div class="ct-sig mq"><span class="sig-track"><span class="sig-txt">' + esc(ownSig) + '</span><span class="sig-dup" aria-hidden="true">' + esc(ownSig) + "</span></span></div>" : "") +
      '<div class="ct-foot"><button class="row-action pill-btn" id="btn-change-pw-m">' + t("act.changePw") + '</button></div>' +
      "</div>";
    // Subordinates render TWICE from one pass (superior feedback round 3):
    // PC = leading table rows right after the own row (no container; the
    // register button lives above the table — #subreg-pc in index.html);
    // phones keep the approved container card (agentreg-row below) and hide
    // the PC rows via CSS.
    var subZone = "", pcSubRows = "", ctZone = "";
    subsList.forEach(function (e) {
      var sig = e.signature || listedSig[e.address] || "";
      var badge = '<span class="badge-sub">' + t("subs.badge") + "</span>" +
        (listedSet[e.address] ? ' <span class="badge-listed">listed</span>' : "");
      pcSubRows +=
        '<tr class="subrow-pc">' +
        '<td class="addr-cell" data-label="' + t("col.address") + '">' + esc(e.address) + "</td>" +
        '<td data-label="' + t("col.tags") + '">' + badge + "</td>" +
        '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(sig) + '</span><span class="sig-dup" aria-hidden="true">' + esc(sig) + "</span></span></td>" +
        "<td data-label=\"Created\"></td>" +
        '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action" data-compose="' + esc(e.address) + '">' + t("act.compose") + '</button><button class="row-action" data-remove-sub="' + esc(e.address) + '">' + t("subs.removeBtn") + "</button></td>" +
        "</tr>";
      // Mobile container card (one-screen plan): badges + address share one
      // line (address marquees on overflow), signature max one line (same),
      // pill buttons bottom-right — all inside the scrollable .sub-list.
      subZone +=
        '<div class="sub-card">' +
        '<div class="sub-meta">' + badge + "</div>" +
        '<div class="sub-addr mq"><span class="sig-track"><span class="sig-txt">' + esc(e.address) + '</span><span class="sig-dup" aria-hidden="true">' + esc(e.address) + "</span></span></div>" +
        '<div class="sub-sig mq">' + (sig ? '<span class="sig-track"><span class="sig-txt">' + esc(sig) + '</span><span class="sig-dup" aria-hidden="true">' + esc(sig) + "</span></span>" : "") + "</div>" +
        '<div class="sub-foot"><button class="row-action pill-btn" data-compose="' + esc(e.address) + '">' + "✉ " + t("act.compose") + '</button><button class="row-action pill-btn" data-remove-sub="' + esc(e.address) + '">' + "✕ " + t("subs.removeBtn") + "</button></div>" +
        "</div>";
    });
    rows.push(pcSubRows);
    rows.push(
      '<tr class="agentreg-row">' +
      '<td colspan="5" class="agentreg-cell">' +
      '<div class="agentreg-card">' +
      '<button id="btn-subreg" class="primary">' + t("subs.registerBtn") + "</button>" +
      '<div class="muted" style="font-size:12px; margin-top:5px;">' + t("subs.registerNote") + "</div>" +
      '<div class="sep-line" style="margin:8px 0;"></div>' +
      (subZone ? '<div class="sub-list">' + subZone + "</div>" : '<div class="muted" style="font-size:12px;">' + t("subs.noneVisible") + "</div>") +
      "</div></td>" +
      "</tr>"
    );
    var seenAddrs = {};
    try {
      const data = await api("/api/contacts", { keepSession: true });
      (data.contacts || []).forEach(function (c) {
        if (subAddrs[c]) return; // already shown (PC leading rows / mobile container)
        seenAddrs[c] = 1;
        // Subordinate addresses carry a badge (admin feedback: same style
        // family as the admin/listed badges on the admin view).
        var badge = (listedSet[c] ? ' <span class="badge-listed">listed</span>' : "") +
          (subAddrs[c] ? ' <span class="badge-sub">' + t("subs.badge") + "</span>" : "");
        // Every address row gets the same shape (feedback: subordinate
        // rows with and without mail history must look identical):
        // badge column, Compose action; Created only where known.
        // The address carries the marquee track for the mobile one-screen
        // plan (phones merge badges+address into one line); the twin card
        // below feeds #acc-m-contacts (the phone-only scrollable list).
        rows.push(
          "<tr class=\"ct-row\">" +
          '<td class="addr-cell mq" data-label="' + t("col.address") + '"><span class="sig-track"><span class="sig-txt">' + esc(c) + '</span><span class="sig-dup" aria-hidden="true">' + esc(c) + "</span></span></td>" +
          '<td data-label="' + t("col.tags") + '">' + badge.trim() + "</td>" +
          '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(listedSig[c] || "") + '</span><span class="sig-dup" aria-hidden="true">' + esc(listedSig[c] || "") + "</span></span></td>" +
          "<td data-label=\"Created\"></td>" +
          '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action" data-compose="' + esc(c) + '">' + t("act.compose") + "</button></td>" +
          "</tr>"
        );
        ctZone +=
          '<div class="ct-card">' +
          '<div class="ct-line">' + badge.trim() +
          '<div class="ct-addr mq"><span class="sig-track"><span class="sig-txt">' + esc(c) + '</span><span class="sig-dup" aria-hidden="true">' + esc(c) + "</span></span></div></div>" +
          '<div class="ct-sig mq">' + (listedSig[c] ? '<span class="sig-track"><span class="sig-txt">' + esc(listedSig[c]) + '</span><span class="sig-dup" aria-hidden="true">' + esc(listedSig[c]) + "</span></span>" : "") + "</div>" +
          '<div class="ct-foot"><button class="row-action pill-btn" data-compose="' + esc(c) + '">✉ ' + t("act.compose") + "</button></div>" +
          "</div>";
      });
    } catch (e) {
      // contacts failure is non-fatal; just show self.
    }
    // Subordinate accounts render ONLY inside the register card's zone
    // (approved two-zone layout) — nothing about them joins the main list.
    tbody.innerHTML = rows.join("");
    const btn = $("#btn-change-pw");
    if (btn) btn.addEventListener("click", openChangePassword);
    $$("[data-compose]", tbody).forEach(function (b) {
      b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: b.dataset.compose } })); });
    });
    // v0.6.5: remove-subordinate buttons (PC rows + mobile cards) — the
    // destructive twin of compose, guarded by a consequence-aware confirm.
    $$("[data-remove-sub]", tbody).forEach(function (b) {
      b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("subs:remove", { detail: { address: b.dataset.removeSub, role: "superior" } })); });
    });
    // Mobile one-screen plan: the phone-only contacts list mirrors the
    // contact rows (which hide via CSS); compose wiring included.
    var ctBox = $("#acc-m-contacts");
    if (ctBox) {
      ctBox.innerHTML = ctZone;
      $$("#acc-m-contacts [data-compose]").forEach(function (b) {
        b.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:to", { detail: { address: b.dataset.compose } })); });
      });
    }
    var ownBox = $("#acc-m-own");
    if (ownBox) {
      ownBox.innerHTML = ownMobile;
      var pwM = $("#btn-change-pw-m");
      if (pwM) pwM.addEventListener("click", openChangePassword);
    }
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
    api("/api/admin/invalid").then(function (d) {
      renderInvalidList((d && d.messages) || []);
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
      var res = await api("/api/admin/invalid", { method: "DELETE", body: JSON.stringify(body) });
      $("#invalid-confirm").classList.add("hidden");
      invalidPending = null;
      var doneMsg = t("inv.deleted", { n: (res && res.deleted) || 0 });
      // Refresh the list in place; the status line survives the reload.
      api("/api/admin/invalid").then(function (d) {
        renderInvalidList((d && d.messages) || []);
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
  const PREFS_DEFAULTS = { audio_autoplay: false, image_preview: true, livenessWeakHours: 48, livenessStrongHours: 24 };
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
      livenessStrongHours: numHours(src["liveness.strongHours"],
        numHours(local["liveness.strongHours"], PREFS_DEFAULTS.livenessStrongHours)),
      livenessWeakHours: numHours(src["liveness.weakHours"],
        numHours(local["liveness.weakHours"], PREFS_DEFAULTS.livenessWeakHours)),
    };
    return userPrefs;
  }

  async function savePrefs() {
    const strongEl = $("#pref-liveness-strong"), weakEl = $("#pref-liveness-weak");
    const strong = strongEl ? parseInt(strongEl.value, 10) : NaN;
    const weak = weakEl ? parseInt(weakEl.value, 10) : NaN;
    const status = $("#prefs-status");
    if (strongEl && (!isFinite(strong) || strong < 1 || strong > 8760) ||
        weakEl && (!isFinite(weak) || weak < 1 || weak > 8760)) {
      status.textContent = t("prefs.livenessBad");
      return;
    }
    const prefs = {
      audio_autoplay: !!$("#pref-audio-autoplay").checked,
      image_preview: !!$("#pref-image-preview").checked,
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
  // v0.6.34 display address (design 01M13ZZ5A §4, superior ruling 01M14HSA:
  // READ-ONLY — value comes only from registration input): the settings page
  // is the ONLY slot allowed to show the mixed-case display form; the mail
  // face (lists, headers, threads, forest) stays lowercase key everywhere.
  (function wireDispAddr() {
    const dVal = $("#dispaddr-value");
    if (!dVal) return;
    function baseAddr() {
      const sess = getSession();
      return sess ? String(sess.address || "").toLowerCase() : "";
    }
    function render(local) {
      const base = baseAddr();
      dVal.textContent = local ? base.replace(/^[^@]+/, local) : base;
    }
    function fRefresh() {
      api("/api/account/display-local", { keepSession: true }).then(function (d) {
        render((d && d.display_local) || "");
      }, function () { /* endpoint absent on older servers: keep key form */ });
    }
    const prefsBtn = $("#btn-prefs");
    if (prefsBtn) prefsBtn.addEventListener("click", function () { setTimeout(fRefresh, 250); });
    document.addEventListener("profile:entered", function () { setTimeout(fRefresh, 250); });
    if (getSession()) fRefresh();
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
    const s = getSession();
    if (!s || !s.is_admin) return; // admin-only card
    scCard.hidden = false;
    const scKeys = [
      ["sc-tagline-zh", "portal_tagline_zh"], ["sc-tagline-en", "portal_tagline_en"],
      ["sc-ptitle-zh", "portal_title_zh"], ["sc-ptitle-en", "portal_title_en"],
      ["sc-ntitle-zh", "panel_title_zh"], ["sc-ntitle-en", "panel_title_en"],
    ];
    function scPrefill() {
      api("/api/site-copy", { keepSession: true }).then(function (d) {
        scKeys.forEach(function (kv) {
          const el = $("#" + kv[0]);
          if (el && d && d[kv[1]]) el.value = d[kv[1]];
        });
      }, function () { /* older server: keep placeholders */ });
    }
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
    scPrefill();
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
      status.textContent = "";
      // Preferences toggles (v0.6): server prefs win, local fallback.
      mergePrefs(p.prefs);
      $("#pref-audio-autoplay").checked = userPrefs.audio_autoplay;
      $("#pref-image-preview").checked = userPrefs.image_preview;
      const lvS = $("#pref-liveness-strong"), lvW = $("#pref-liveness-weak");
      if (lvS) lvS.value = userPrefs.livenessStrongHours;
      if (lvW) lvW.value = userPrefs.livenessWeakHours;
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
          activateTab("overview");
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
        return '<div class="dir-card">' + portalAvatar(e.address) +
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
  function portalAvatar(addr) {
    let h = 0;
    for (let i = 0; i < addr.length; i++) h = (h * 31 + addr.charCodeAt(i)) % 360;
    const ini = (addr.split("@")[0] || "?").slice(0, 2).toUpperCase();
    return '<div class="avatar" style="background:linear-gradient(135deg,hsl(' + h + ',65%,50%),hsl(' +
      ((h + 40) % 360) + ',65%,38%))">' + esc(ini) + "</div>";
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
        if (sum) sum.textContent = parts.join(" · ") || "—";
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
    $$(".tab").forEach(function (b) {
      const tab = b.dataset.tab;
      const adminOnly = ADMIN_ONLY_TABS.indexOf(tab) !== -1;
      b.classList.toggle("hidden", adminOnly && !isAdmin);
    });
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

  // maybeMarqueeSigs runs over-wide signature cells (Accounts + Directory)
  // as a seamless one-way loop (superior feedback: ping-pong never reveals
  // the whole text). The track carries the text twice; each copy has the
  // same trailing gap, so translateX(-50%) is exactly one period.
  // Reduced-motion users keep the ellipsis.
  function maybeMarqueeSigs() {
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    $$(".sig-cell, .mq").forEach(function (cell) {
      cell.classList.remove("marquee"); // dup hidden again -> measure raw overflow
      cell.style.removeProperty("--wm-dur");
      const diff = cell.scrollWidth - cell.clientWidth;
      if (diff > 8) {
        // Linear period over (overflow + one gap), ~28px/s.
        cell.style.setProperty("--wm-dur", Math.max(8, (diff + 48) / 28) + "s");
        cell.classList.add("marquee");
      }
    });
  }
  window.addEventListener("resize", maybeMarqueeSigs);

  // Accounts-page one-screen fit (mobile plan): the own card and the
  // register button stay fixed; the subordinate list and the contacts list
  // scroll within the remaining viewport height — the contacts box bottom
  // is pinned just above the viewport bottom so the height alignment is
  // visible. PC keeps the table.
  function fitAccountsOneScreen() {
    var page = $("#tab-accounts");
    if (!page || page.classList.contains("hidden")) return;
    if (window.innerWidth > 800) return;
    var subList = document.querySelector(".sub-list");
    var ctBox = $("#acc-m-contacts");
    if (!ctBox) return;
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
      var avail = window.innerHeight - 10 - cardTop;
      var fixed = card ? card.offsetHeight - subList.offsetHeight : 0;
      var cardTarget = Math.round(avail * 0.52);
      var subH = Math.min(subList.scrollHeight, Math.max(96, cardTarget - fixed));
      subList.style.maxHeight = subH + "px";
    }
    var ctTop2 = ctBox.getBoundingClientRect().top;
    var ctH = window.innerHeight - 10 - ctTop2;
    if (ctH < reserve && subList) {
      // Shrink the subordinate list by the deficit, then re-pin exactly.
      var deficit = reserve - ctH;
      var cur = parseInt(subList.style.maxHeight, 10) || 0;
      subList.style.maxHeight = Math.max(96, cur - deficit) + "px";
      ctTop2 = ctBox.getBoundingClientRect().top;
      ctH = window.innerHeight - 10 - ctTop2;
    }
    ctBox.style.maxHeight = Math.max(96, ctH) + "px";
  }
  window.addEventListener("resize", fitAccountsOneScreen);
  document.addEventListener("accounts:refresh", function () { setTimeout(fitAccountsOneScreen, 50); });

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
      activateTab("overview");
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
})();
