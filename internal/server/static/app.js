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
  async function refreshInboxBadge() {
    if (!getSession()) { setInboxBadge(0); return; }
    // Background tabs skip the tick — the badge refreshes on visibility
    // return, so 5s polling stays cheap in aggregate (admin: 2-5s wanted).
    if (document.visibilityState === "hidden") return;
    const seq = ++badgeSeq;
    try {
      const d = await api("/api/inbox?limit=1");
      if (seq !== badgeSeq) return; // a newer refresh superseded this one
      setInboxBadge(d.unread_count || 0);
    } catch (_) { /* badge is best-effort */ }
  }
  setInterval(refreshInboxBadge, 5000);
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") refreshInboxBadge();
  });

  // ---- Overview | Directory segment (v0.6.9 layout, superior-approved) ----
  // Directory is the second in-page view of Overview; same pill pattern as
  // the Manage tab's Messages|Overview. The prefs pill in the header opens
  // the (now nav-less) profile panel.
  function setOvwView(v) {
    var main = $("#ovw-main"), dir = $("#ovw-directory");
    if (!main || !dir) return;
    if (v !== "directory") v = "main";
    main.classList.toggle("hidden", v !== "main");
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
    if (name === "audit") loadAudit();
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
    recent.textContent = t("common.loading");
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
        recent.innerHTML = '<p class="muted">Sign in to an admin account to see system activity.</p>';
      } catch (e) {
        $("#stats-system").textContent = t("common.error", { msg: e.message });
        recent.textContent = "";
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
        recent.textContent = t("ovw.noActivity");
        return;
      }
      recent.innerHTML = "<ul>" + a.entries.map(function (e) {
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
      return;
    }
    // Admin view has global tools; the subordinate manager is regular-only.
    const subsSectionAdmin = $("#subs-section");
    if (subsSectionAdmin) subsSectionAdmin.classList.add("hidden");
    const subregPcAdmin = $("#subreg-pc");
    if (subregPcAdmin) subregPcAdmin.classList.add("hidden");
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
      "<tr>" +
      '<td class="addr-cell" data-label="' + t("col.address") + '"><strong>' + esc(selfAddr) + "</strong> <small class=\"muted\">(you)</small></td>" +
      '<td data-label="' + t("col.tags") + '"><span class="badge-listed">you</span>' + (ownVisible ? ' <span class="badge-listed">listed</span>' : "") + "</td>" +
      '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(ownSig) + '</span><span class="sig-dup" aria-hidden="true">' + esc(ownSig) + "</span></span></td>" +
      "<td data-label=\"Created\"></td>" +
      '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action" id="btn-change-pw">' + t("act.changePw") + '</button></td>' +
      "</tr>"
    );
    // Subordinates render TWICE from one pass (superior feedback round 3):
    // PC = leading table rows right after the own row (no container; the
    // register button lives above the table — #subreg-pc in index.html);
    // phones keep the approved container card (agentreg-row below) and hide
    // the PC rows via CSS.
    var subZone = "", pcSubRows = "";
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
      // Mobile container card (compact round, superior-approved): address
      // on its own line, badges + signature share one meta line, a small
      // pill compose button sits bottom-right - no micro-labels.
      subZone +=
        '<div class="sub-card">' +
        '<div class="sub-addr">' + esc(e.address) + "</div>" +
        '<div class="sub-meta">' + badge + (sig ? '<span class="sub-sig">' + esc(sig) + "</span>" : "") + "</div>" +
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
      (subZone || '<div class="muted" style="font-size:12px;">' + t("subs.noneVisible") + "</div>") +
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
        rows.push(
          "<tr>" +
          '<td class="addr-cell" data-label="' + t("col.address") + '">' + esc(c) + "</td>" +
          '<td data-label="' + t("col.tags") + '">' + badge.trim() + "</td>" +
          '<td class="sig-cell" data-label="' + t("col.signature") + '"><span class="sig-track"><span class="sig-txt">' + esc(listedSig[c] || "") + '</span><span class="sig-dup" aria-hidden="true">' + esc(listedSig[c] || "") + "</span></span></td>" +
          "<td data-label=\"Created\"></td>" +
          '<td class="actions-cell" data-label="' + t("col.actions") + '"><button class="row-action" data-compose="' + esc(c) + '">' + t("act.compose") + "</button></td>" +
          "</tr>"
        );
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

  // The Accounts-tab "+ Register new account" button opens the login-page
  // register flow (the single source of truth for registration since v0.2.12).
  // An older in-tab prompt()-based register handler used to bind this same
  // button; it was removed because it double-fired alongside the login-page
  // handler and caused "account already exists" + a confusing "local-part"
  // prompt. Registration now lives only on the login page.
  $("#btn-register").addEventListener("click", function () {
    setSession(null); // signing out to reach the login page
    showLogin();
    showRegisterForm();
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
  // (partial update: empty input keeps the server value; ≤200 chars/key).
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
        if (!isNaN(dt.getTime())) lbl = dt.toLocaleDateString(undefined, { weekday: "short" });
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
        '<button class="am-mini" data-am="extend">' + t("am.extend") + "</button>" +
        '<button class="am-mini del" data-am="release">' + t("am.release") + "</button>" +
        "</span></div>";
    }

    async function loadRows() {
      const box = $("#am-rows");
      if (!box) return;
      box.textContent = t("common.loading");
      try {
        const res = await api("/api/files/list", { keepSession: true });
        const files = (res && res.files) || [];
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
  function renderTeamSuccess(res) {
    var box = $("#team-cred-cards");
    if (!box) return;
    var html = "";
    function card(cls, who, addr, pw, extraBtn) {
      return '<div class="cred-card ' + cls + '">' +
        '<div><div class="who">' + who + "</div>" +
        "<code>" + esc(addr) + '</code> <span class="pw">' + esc(pw) + "</span></div>" +
        '<div class="btns"><button class="cp" data-cp-addr="' + esc(addr) + '" data-cp-pw="' + esc(pw) + '">' + t("team.copyCreds") + "</button>" +
        (extraBtn || "") + "</div></div>";
    }
    if (res && res.owner) {
      html += card("owner", t("team.owner") + " - " + t("team.ownerCan"),
        res.owner.address, res.owner.password);
    }
    (res && res.members || []).forEach(function (m, i) {
      html += card("", t("team.member") + " " + (i + 1), m.address, m.password,
        '<button class="cp" data-cp-prompt="' + esc(m.address) + '" data-cp-prompt-pw="' + esc(m.password) + '">' + t("team.copyPrompt") + "</button>");
    });
    box.innerHTML = html;
  }

  // buildAgentPrompt returns the ready-to-paste agent setup prompt for an
  // account: intro, gateway download, inline MCP config, credentials, and
  // curl examples (no MCP needed). Shared by the manual register flow and
  // the one-click register.
  function buildAgentPrompt(address, password) {
    const serverURL = location.origin;
    return "I've registered an account for you on mailofagents.online — a deployment\n" +
      "of the open-source agentmail project (https://github.com/EX-user/mailofagents).\n" +
      "It's a mail system for AI agents: you can use it to reach other agents or\n" +
      "humans via MCP tools or the web panel.\n\n" +
      "Setup:\n" +
      "1. First check whether the agentmail MCP tools are already available to\n" +
      "   you (e.g. send_email / read_inbox). If they are, skip steps 2-3 —\n" +
      "   you're already set up.\n" +
      "2. Download the agentmail-gateway binary for your platform from the latest\n" +
      "   release at https://github.com/EX-user/mailofagents/releases.\n" +
      "3. Register the agentmail MCP server with this config (set 'command' to\n" +
      "   the path where you run the gateway):\n\n" +
      '{\n' +
      '  "mcpServers": {\n' +
      '    "agentmail": {\n' +
      '      "command": "<your-gateway-path>/agentmail-gateway",\n' +
      '      "args": [],\n' +
      '      "env": {\n' +
      '        "AGENTMAIL_SERVER_URL": "' + serverURL + '"\n' +
      '      }\n' +
      '    }\n' +
      '  }\n' +
      '}\n\n' +
      "4. Once you've downloaded the gateway and added the config, tell me to\n" +
      "   restart my agent software — that may be needed to activate the MCP\n" +
      "   configuration.\n\n" +
      "Your credentials:\n" +
      "  address: " + address + "\n" +
      "  password: " + password + "\n" +
      "  server URL: " + serverURL + "\n\n" +
      "No MCP setup? The same API works over plain HTTP with Basic auth:\n" +
      "  curl -u " + address + ":" + password + " " + serverURL + "/api/inbox\n" +
      "  curl -u " + address + ":" + password + " \"" + serverURL + "/api/message?id=MSG_ID\"\n" +
      "  curl -u " + address + ":" + password + " -X POST " + serverURL + "/api/send \\\n" +
      "    -H 'Content-Type: application/json' \\\n" +
      "    -d '{\"to\":[\"someone@" + systemDomain + "\"],\"subject\":\"hi\",\"body\":\"hello\"}'\n\n" +
      "Then call authenticate(address, password) to get an access code, and use\n" +
      "send_email / read_inbox / get_message / wait_for_new_mail. When you're set\n" +
      "up, ask me whether you should enter duty (watch) mode — and if so, when you\n" +
      "have no other task, wait for replies using a script.";
  }

  function showRegisterSuccess(address, password) {
    $("#login-form-block").classList.add("hidden");
    $("#register-form-block").classList.add("hidden");
    const sb = $("#register-success-block");
    $("#register-success-address").textContent = address;
    // Human-set passwords never echo back (server returns none) — the page
    // just reminds the user to keep it; server-generated ones (one-click)
    // still show once.
    $("#register-success-password").textContent = password || t("reg.pwUserSet");
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
    $$(".sig-cell").forEach(function (cell) {
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
      status.textContent = "Login failed: " + e.message;
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
        renderTeamSuccess(res);
        $("#team-copy-status").textContent = "";
        $("#team-form-block").classList.add("hidden");
        $("#team-success-block").classList.remove("hidden");
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
      var res = null;
      // Rebuild the copy-all text from the rendered cards (source of truth).
      var lines = [];
      $$("#team-cred-cards .cred-card").forEach(function (card) {
        var who = card.querySelector(".who").textContent;
        var addr = card.querySelector("code").textContent;
        var pw = card.querySelector(".pw").textContent;
        lines.push(who + ": " + addr + "  " + pw);
      });
      copyText(lines.join("\n")).then(function (ok) {
        var st = $("#team-copy-status");
        st.textContent = ok ? t("common.copied") : t("common.copyManual");
        setTimeout(function () { st.textContent = ""; }, 2000);
      });
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
    const pw = $("#register-success-password").textContent;
    $("#login-address").value = addr;
    $("#login-password").value = pw;
    showLoginForm();
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
