// agentmail overview domain — S2 car 3 of the zero-build ESM split
// (superior directive: the Overview / connections-graph view moves out of
// manage.js so a new colleague can own the data-visualization surface).
// HARD CONSTRAINT (audit_frontend_imports.sh): imports ONLY ./core.js;
// every cross-domain interaction goes through DOM CustomEvents:
//   listens:  overview:entered {}  overview:refresh {}  overview:reset {}
//             i18n:change
//   emits:    nav:activate {tab:"accounts"}
//             mgmt:browse-account {address, folder?}
// The i18n dictionary stays a classic global (window.I18N).
import { $, $$, esc, api, getSession, toast, fmtTime } from "./core.js";

(function () {
  "use strict";

  function t(key, vars) {
    return window.I18N ? window.I18N.t(key, vars) : key;
  }

  // ---- v0.6 mail management views ----
  // Segment capsule switches between the Messages view (the existing dual
  // pane, preserved verbatim) and the Subs Overview aggregate. The choice
  // persists; overview lazy-loads on first entry and re-renders on language
  // change. Rows deep-link into Messages with the account preselected.

  // Liveness window prefs (strong/weak hours) live on the account profile —
  // fetched once per login, same source of truth the manage module uses.
  var overviewPrefs = null;
  function ensureOverviewPrefs() {
    if (overviewPrefs) return Promise.resolve(overviewPrefs);
    return api("/api/profile/self", { keepSession: true }).then(function (p) {
      overviewPrefs = (p && p.prefs) || p || {};
      return overviewPrefs;
    }, function () { return {}; });
  }
  function mgmtIsActive(s) {
    // Three-tier liveness (superior's rule, v0.6): GREEN = mail traffic
    // (send or receive) within the strong window; YELLOW = inbox read
    // activity within the weak window but no traffic (script-like);
    // GRAY = idle / never. Windows are user prefs (hours), defaults
    // strong=24, weak=48.
    var now = Date.now() / 1000;
    var strongH = (overviewPrefs && overviewPrefs.livenessStrongHours) || 24;
    var weakH = (overviewPrefs && overviewPrefs.livenessWeakHours) || 48;
    var traffic = Math.max(s.last_in_at || 0, s.last_out_at || 0);
    var read = s.last_read_at || 0;
    if (traffic && now - traffic <= strongH * 3600) return "strong";
    if (read && now - read <= weakH * 3600) return "weak";
    if (!traffic && !read) return "never";
    return "idle";
  }

  function mgmtOverviewHtml(d) {
    var subs = (d && d.subs) || [];
    var box = "";
    if (!subs.length) {
      return '<div class="mgmt-empty"><div>' + t("mgmt.emptyTitle") + '</div><div class="sub">' +
        t("mgmt.emptySub") + '</div><button class="row-action" data-mgmt-go="accounts">' +
        t("mgmt.goAccounts") + "</button></div>";
    }
    var live = 0, in7 = 0, out7 = 0;
    subs.forEach(function (s) {
      var stt = mgmtIsActive(s);
      if (stt === "strong" || stt === "weak") live++;
      in7 += s.count_in_7d || 0; out7 += s.count_out_7d || 0;
    });
    box += '<div class="mgmt-sum">' + t("mgmt.sum", { n: subs.length, a: live, i: in7, o: out7 }) +
      ' <button class="row-action mgmt-refresh" data-mgmt-go="refresh">' + t("mgmt.refresh") + "</button></div>";
    box += '<table class="mgmt-ovw"><thead><tr>' +
      "<th>" + t("mgmt.colAccount") + "</th><th>" + t("mgmt.colCounts") + "</th><th>" + t("mgmt.colAvg") + "</th><th>" + t("mgmt.colTop") + "</th></tr></thead><tbody>";
    subs.forEach(function (s) {
      var st = mgmtIsActive(s);
      var dotCls = st === "strong" ? "green" : (st === "weak" ? "yellow" : "idle");
      var liveTxt = st === "strong" ? t("mgmt.liveStrong")
        : (st === "weak" ? t("mgmt.liveWeak")
        : (st === "never" ? t("mgmt.never") : t("mgmt.idle")));
      var top = (s.top_contacts || []).map(function (c) {
        return shortAddr(c.address) + "×" + c.count;
      }).join(" · ") || "—";
      function fmtAvg(v) { return v > 0 ? (v >= 1000 ? (v / 1000).toFixed(1) + "K" : String(v)) : "—"; }
      // Liveness moved into the account cell (feedback: the standalone
      // column wrapped at five characters); the dot keeps its meaning via
      // the hover title.
      box += '<tr data-mgmt-acct="' + esc(s.address) + '">' +
        '<td data-label="' + esc(t("mgmt.colAccount")) + '"><span class="dot ' + dotCls + '" title="' + esc(liveTxt) + '"></span><span class="mono">' + esc(s.address) + '</span>' +
        (s.signature ? '<br><small class="muted">' + esc(s.signature) + "</small>" : "") + "</td>" +
        '<td data-label="' + esc(t("mgmt.colCounts")) + '" class="mono">' + (s.count_in_7d || 0) + " / " + (s.count_out_7d || 0) + "</td>" +
        '<td data-label="' + esc(t("mgmt.colAvg")) + '" class="mono">' + fmtAvg(s.avg_len_in) + " / " + fmtAvg(s.avg_len_out) + "</td>" +
        '<td data-label="' + esc(t("mgmt.colTop")) + '" class="mono">' + esc(top) + "</td></tr>";
    });
    box += "</tbody></table>";
    // Connections graph (superior: force-directed, N2 label blocks + A4
    // volume-scaled wedges, shown on BOTH desktop and mobile, inside the
    // Overview view). The container is rendered by renderMgmtGraph after
    // this HTML lands in the DOM.
    // Floating circular controls (superior request): the buttons sit ON the
    // canvas, overlaid above the graph, outside the pan/zoom transform —
    // a DOM layer on top of the vis-network canvas.
    box += '<h4 class="mgmt-graph-title">' + t("mgmt.graphTitle") + "</h4>" +
      '<div id="mgmt-graph-wrap" class="mgmt-graph-wrap">' +
      '<div class="mgmt-graph-controls overlay">' +
      '<button type="button" class="gg-btn" id="gg-map" title="' + esc(t("mgmt.gMap")) + '"></button>' +
      '<button type="button" class="gg-btn" id="gg-nums" title="' + esc(t("mgmt.gNums")) + '"></button>' +
      '<button type="button" class="gg-btn gg-btn-days" id="gg-days"></button>' +
      "</div>" +
      '<div id="mgmt-graph" class="mgmt-graph"></div>' +
      "</div>";
    return box;
  }

  // Graph display preferences (superior request): persisted so a reload
  // keeps the chosen mapping / numbers / range.
  var graphPrefs = { map: "linear", nums: true, days: 7 };
  try {
    var gp = JSON.parse(localStorage.getItem("mgmtGraphPrefs") || "{}");
    if (gp.map === "log" || gp.map === "linear") graphPrefs.map = gp.map;
    if (typeof gp.nums === "boolean") graphPrefs.nums = gp.nums;
    // 上级 01M186FW6Y 续：时间档改 1d/7d/30d，不留全部历史档
    if (gp.days === 1 || gp.days === 7 || gp.days === 30) graphPrefs.days = gp.days;
  } catch (_) {}
  function saveGraphPrefs() {
    try { localStorage.setItem("mgmtGraphPrefs", JSON.stringify(graphPrefs)); } catch (_) {}
  }
  function windowLabel(days) { return days + "d"; }
  function syncGraphControlLabels() {
    // Circular on-canvas buttons stay terse (superior request): map shows
    // the active mode, numbers a filled/hollow dot, range the window.
    var b1 = $("#gg-map"), b2 = $("#gg-nums"), b3 = $("#gg-days");
    if (b1) b1.textContent = graphPrefs.map === "log" ? t("mgmt.mapLog") : t("mgmt.mapLinear");
    if (b2) { b2.textContent = t("mgmt.gNumsShort"); b2.classList.toggle("off", !graphPrefs.nums); }
    if (b3) b3.textContent = graphPrefs.days + "d";
  }

  function shortAddr(a) {
    return String(a || "").split("@")[0];
  }

  // Lazy-load the vendored vis-network (688KB) the first time the graph
  // renders — the login page and other tabs never pay for it.
  var visNetworkReady = null;
  function loadVisNetwork() {
    if (window.vis && window.vis.Network) return Promise.resolve();
    if (visNetworkReady) return visNetworkReady;
    visNetworkReady = new Promise(function (resolve, reject) {
      var s = document.createElement("script");
      s.src = "/static/vis-network.min.js";
      s.onload = function () { resolve(); };
      s.onerror = function () { visNetworkReady = null; reject(new Error("vis-network load failed")); };
      document.head.appendChild(s);
    });
    return visNetworkReady;
  }

  // Live graph handle (v0.6.8): kept for re-fit on tab re-entry so the
  // viewport stays centered on the nodes between visits.
  var mgmtNetwork = null;
  // Superior 01M186FW6Y: keep the edge DataSet + per-edge metadata so the
  // linear/log (and numbers) toggles restyle IN PLACE — a full re-render
  // re-ran physics and visibly jiggled the layout.
  var mgmtEdgeSet = null, mgmtEdgeMeta = [], mgmtMaxCount = 1;
  function renderMgmtGraph(graph, subs) {
    var el = $("#mgmt-graph");
    if (!el) return;
    var nodes = (graph && graph.nodes) || [];
    var edges = (graph && graph.edges) || [];
    mgmtNetwork = null; // fresh render invalidates the old instance
    mgmtEdgeSet = null;
    if (!nodes.length) { el.textContent = ""; return; }
    var livenessByAddr = {};
    (subs || []).forEach(function (s) { livenessByAddr[String(s.address).toLowerCase()] = mgmtIsActive(s); });
    el.innerHTML = '<div class="muted">' + t("common.loading") + "</div>";
    loadVisNetwork().then(function () {
      var myAddr = ((getSession() || {}).address || "").toLowerCase();
      var vn = new vis.DataSet(nodes.map(function (n) {
        var kind = n.kind || "external";
        var isMe = kind === "self";
        var st = livenessByAddr[String(n.address || "").toLowerCase()];
        var border = isMe ? "#1d4ed8"
          : kind === "sub" ? (st === "strong" ? "#2e9e5b" : st === "weak" ? "#d9a419" : "#9aa4b2")
          : "#a7b1bd";
        var bg = isMe ? "#dbeafe"
          : kind === "sub" ? (st === "strong" ? "#dcf2e4" : st === "weak" ? "#f7edd4" : "#eceff3")
          : "#f2f4f7";
        var wl = windowLabel(graphPrefs.days);
        return {
          id: n.address, label: (isMe ? t("mgmt.meLabel") : shortAddr(n.address)) +
            (kind !== "external" ? "\n" + wl + " " + (n.volume || 0) : ""),
          shape: "box", borderWidth: isMe ? 2 : 1,
          color: { background: bg, border: border },
          font: { face: "ui-monospace, Consolas, monospace", size: 11, color: "#23303f" },
          value: Math.max(1, n.volume || 1), scaling: { min: 8, max: 26, label: { enabled: false } },
          title: shortAddr(n.address) + (kind !== "external" ? " · " + wl + " " + (n.volume || 0) : ""),
          _kind: kind
        };
      }));
      var ve = [];
      // Data-adaptive normalization: every scale is RELATIVE to the largest
      // count in the current graph. Mapping mode is the floating button's
      // choice: LINEAR (default) maps strength straight to width/alpha;
      // LOG compresses contrast so a wide range stays readable.
      var maxCount = 1;
      mgmtEdgeMeta = [];
      edges.forEach(function (e) {
        maxCount = Math.max(maxCount, e.a_to_b || 0, e.b_to_a || 0);
      });
      mgmtMaxCount = maxCount;
      var logDen = Math.log(1 + maxCount);
      function graphScale(count) {
        var c = count || 0;
        if (graphPrefs.map === "log") return Math.log(1 + c) / logDen;
        return c / maxCount;
      }
      edges.forEach(function (e, ei) {
        var ab = e.a_to_b || 0, ba = e.b_to_a || 0;
        var last = e.last_at ? "\n" + fmtTime(e.last_at) : "";
        if (!ab && !ba) {
          var eid0 = "ge" + ei + "z";
          mgmtEdgeMeta.push({ id: eid0, count: -1 });
          ve.push({ id: eid0, from: e.a, to: e.b, label: (graphPrefs.nums ? "—" : "") + last, dashes: true,
            color: { color: "#c4ccd6" }, width: 0.8, font: { size: 9, face: "Consolas" },
            smooth: { type: "curvedCW", roundness: 0.16 }, _sub: pickGraphSub(e, myAddr) });
          return;
        }
        // Two directed arcs per pair (superior: arrows in both directions,
        // each with its own count) — A4 wedges scale with volume.
        if (ab) ve.push(mgmtGraphEdge(e.a, e.b, ab, last, e, "ge" + ei + "a"));
        if (ba) ve.push(mgmtGraphEdge(e.b, e.a, ba, last, e, "ge" + ei + "b"));
      });
      function mgmtGraphEdge(from, to, count, last, orig, eid) {
        var k = graphScale(count);
        mgmtEdgeMeta.push({ id: eid, count: count });
        // v0.6.6: alpha rides the same normalization as width — light
        // traffic reads thin AND faint. Label = count only; the
        // last-activity time moved to the hover tooltip (it occluded the
        // graph at real volumes). Number color/opacity rides the SAME k
        // as the arrow (superior request); the numbers button hides them.
        var alpha = 0.15 + 0.85 * k;
        return {
          id: eid,
          from: from, to: to, label: graphPrefs.nums ? String(count) : undefined,
          title: count + " · " + last,
          arrows: { to: { enabled: true, scaleFactor: 0.3 + 0.7 * k } },
          width: 0.3 + 2.5 * k,
          color: { color: "rgba(91,107,125," + alpha.toFixed(2) + ")", highlight: "#3b82f6" },
          font: { size: 9, face: "Consolas", color: "rgba(35,48,63," + Math.min(1, 0.35 + 0.65 * k).toFixed(2) + ")" },
          smooth: { type: "curvedCW", roundness: 0.2 },
          _sub: pickGraphSub(orig, myAddr)
        };
      }
      function pickGraphSub(e, me) {
        // Edge click → Messages preselected on the subordinate endpoint.
        var a = String(e.a || "").toLowerCase(), bAddr = String(e.b || "").toLowerCase();
        if (a !== me && (livenessByAddr[a] || bAddr === me)) return e.a;
        if (bAddr !== me && (livenessByAddr[bAddr] || a === me)) return e.b;
        return null;
      }
      var data = { nodes: vn, edges: new vis.DataSet(ve) };
      mgmtEdgeSet = data.edges;
      // Keep the instance: re-entering the tab re-fits the viewport so the
      // graph never drifts off-center between visits (superior feedback).
      mgmtNetwork = new vis.Network(el, data, {
        // Roomier layout (feedback: nodes sat too close at real volumes):
        // stronger repulsion + longer springs spread the pairs so the
        // (now capped) arrows never bury the labels. Iterations trimmed
        // 400 -> 260: visibly faster first paint, layout quality held.
        physics: {
          enabled: true, solver: "barnesHut",
          barnesHut: { gravitationalConstant: -6000, springLength: 160, springConstant: 0.04, damping: 0.12 },
          stabilization: { iterations: 260, fit: true }
        },
        interaction: { hover: true, dragView: true, zoomView: true },
        edges: { selectionWidth: 2 }
      });
      mgmtNetwork.once("stabilizationIterationsDone", function () {
        try { mgmtNetwork.fit({ animation: false }); } catch (_) {}
      });
      mgmtNetwork.on("click", function (params) {
        // Node click: jump to Messages preselected on that sub; edge click:
        // preselect the sub endpoint of the pair. The browse pane belongs
        // to the manage module — ask it via the event bus.
        var jump = null;
        if (params.nodes && params.nodes.length) {
          var id = String(params.nodes[0]).toLowerCase();
          if (id !== myAddr && livenessByAddr[id]) jump = params.nodes[0];
        } else if (params.edges && params.edges.length) {
          var ed = ve.filter(function (x) { return x.id === params.edges[0]; })[0];
          if (ed && ed._sub) jump = ed._sub;
        }
        if (jump) document.dispatchEvent(new CustomEvent("mgmt:browse-account", { detail: { address: jump } }));
      });
    }).catch(function () {
      el.innerHTML = '<p class="muted">' + esc(t("mgmt.graphLoadFail")) + "</p>";
    });
  }

  var mgmtOverviewLoaded = false;
  var mgmtOverviewData = null; // last payload — lets the map/nums buttons
  // re-render without a server round-trip (only the range button refetches).
  async function loadMgmtOverview() {
    var box = $("#mgmt-overview");
    if (!box) return;
    box.textContent = t("common.loading");
    await ensureOverviewPrefs();
    try {
      var d = await api("/api/mgmt/subs-overview?days=" + graphPrefs.days, { keepSession: true });
      mgmtOverviewData = d;
      box.innerHTML = mgmtOverviewHtml(d);
      mgmtOverviewLoaded = true;
      syncGraphControlLabels();
      wireGraphControls();
      renderMgmtGraph(d && d.graph, (d && d.subs) || []);
    } catch (e) {
      box.innerHTML = '<p class="muted">' + esc(t("common.error", { msg: e.message })) + "</p>";
    }
  }

  // Floating graph controls: one click cycles the value, persists, and
  // re-renders. map/nums reuse the cached payload; days refetches.
  // Superior 01M186FW6Y: map/nums restyle edges IN PLACE (DataSet.update of
  // visual props never re-runs physics — nodes stay exactly where they are).
  function restyleGraphEdges() {
    if (!mgmtNetwork || !mgmtEdgeSet) {
      if (mgmtOverviewData) renderMgmtGraph(mgmtOverviewData.graph, mgmtOverviewData.subs || []);
      return;
    }
    var logDen = Math.log(1 + mgmtMaxCount);
    var updates = mgmtEdgeMeta.map(function (meta) {
      if (meta.count < 0) return { id: meta.id, label: graphPrefs.nums ? "—" : "" };
      var c = meta.count || 0;
      var k = graphPrefs.map === "log" ? Math.log(1 + c) / logDen : c / mgmtMaxCount;
      var alpha = 0.15 + 0.85 * k;
      return {
        id: meta.id,
        // vis DataSet.update 忽略 undefined 字段——必须用空串才能真正清掉标签
        label: graphPrefs.nums ? String(meta.count) : " ",
        arrows: { to: { enabled: true, scaleFactor: 0.3 + 0.7 * k } },
        width: 0.3 + 2.5 * k,
        color: { color: "rgba(91,107,125," + alpha.toFixed(2) + ")", highlight: "#3b82f6" },
        font: { size: 9, face: "Consolas", color: "rgba(35,48,63," + Math.min(1, 0.35 + 0.65 * k).toFixed(2) + ")" }
      };
    });
    mgmtEdgeSet.update(updates);
  }
  function wireGraphControls() {
    var b1 = $("#gg-map"), b2 = $("#gg-nums"), b3 = $("#gg-days");
    if (b1) b1.addEventListener("click", function () {
      graphPrefs.map = graphPrefs.map === "linear" ? "log" : "linear";
      saveGraphPrefs(); syncGraphControlLabels();
      restyleGraphEdges();
    });
    if (b2) b2.addEventListener("click", function () {
      graphPrefs.nums = !graphPrefs.nums;
      saveGraphPrefs(); syncGraphControlLabels();
      restyleGraphEdges();
    });
    if (b3) b3.addEventListener("click", function () {
      graphPrefs.days = graphPrefs.days === 1 ? 7 : (graphPrefs.days === 7 ? 30 : 1);
      saveGraphPrefs();
      loadMgmtOverview();
    });
  }


  // In-view actions: refresh, or deep-link to Accounts / a subordinate's
  // Messages view (the browse pane is the manage module's — bus again).
  (function wireOverviewActions() {
    var box = $("#mgmt-overview");
    if (!box) return;
    box.addEventListener("click", function (ev) {
      var btn = ev.target.closest("[data-mgmt-go]");
      if (btn) {
        if (btn.dataset.mgmtGo === "accounts") { document.dispatchEvent(new CustomEvent("nav:activate", { detail: { tab: "accounts" } })); return; }
        if (btn.dataset.mgmtGo === "refresh") { loadMgmtOverview(); return; }
      }
      var row = ev.target.closest("tr[data-mgmt-acct]");
      if (row) document.dispatchEvent(new CustomEvent("mgmt:browse-account", { detail: { address: row.dataset.mgmtAcct, folder: "inbox" } }));
    });
  })();

  // ---- event surface (the manage module owns the capsule + browse pane) ----
  document.addEventListener("overview:entered", function () {
    if (!mgmtOverviewLoaded && getSession()) loadMgmtOverview();
    else if (mgmtNetwork) setTimeout(function () {
      try { mgmtNetwork.fit({ animation: false }); } catch (_) {}
    }, 0);
  });
  document.addEventListener("overview:refresh", function () {
    if (mgmtOverviewLoaded) loadMgmtOverview();
  });
  document.addEventListener("overview:reset", function () {
    mgmtOverviewLoaded = false;
    overviewPrefs = null;
    var b2 = document.getElementById("mgmt-overview");
    if (b2) b2.textContent = "";
  });
  document.addEventListener("i18n:change", function () {
    if (mgmtOverviewLoaded) loadMgmtOverview();
  });
})();
