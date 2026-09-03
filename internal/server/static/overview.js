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
        (s.signature ? '<div class="ovw-sig mq"><span class="sig-track"><span class="sig-txt">' + esc(s.signature) + '</span><span class="sig-dup" aria-hidden="true">' + esc(s.signature) + "</span></span></div>" : "") + "</td>" +
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
      '<button type="button" class="gg-btn" id="gg-play" title="' + esc(t("mgmt.gPlay")) + '">▶</button>' +
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
var mgmtNodeSet = null;
  function renderMgmtGraph(graph, subs) {
    var el = $("#mgmt-graph");
    if (!el) return;
    var nodes = (graph && graph.nodes) || [];
    var edges = (graph && graph.edges) || [];
    mgmtNetwork = null; // fresh render invalidates the old instance
    mgmtEdgeSet = null;
    mgmtNodeSet = null;
    playSeqCache = null; // 图数据重载→播放序列缓存失效
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
        // 播放特性一（上级 0.2.5）：「我」节点大一号更醒目——scaling 上限
        // 抬高 + 字号加大，其余节点照旧。
        var nodeScaling = isMe
          ? { min: 20, max: 38, label: { enabled: false } }
          : { min: 8, max: 26, label: { enabled: false } };
        var nodeFont = { face: "ui-monospace, Consolas, monospace", size: isMe ? 12 : 11, color: "#23303f" };
        return {
          id: n.address, label: (isMe ? t("mgmt.meLabel") : shortAddr(n.address)) +
            (kind !== "external" ? "\n" + wl + " " + (n.volume || 0) : ""),
          shape: "box", borderWidth: isMe ? 2 : 1,
          color: { background: bg, border: border },
          font: nodeFont,
          value: Math.max(1, n.volume || 1), scaling: nodeScaling,
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
        mgmtEdgeMeta.push({ id: eid, count: count, from: from, to: to });
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
      mgmtNodeSet = data.nodes;
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
        // 播放/暂停期间：canvas 点按=暂停↔继续（上级 0.2.5），不触发跳转
        if (playState) {
          if (playState.paused) {
            playState.paused = false;
            playState.timer = setInterval(playTick, playBeatMs());
            if (playBadge) playBadge.textContent = playBadgeLabel("");
          } else {
            clearInterval(playState.timer);
            playState.timer = null;
            playState.paused = true;
            if (playBadge) playBadge.textContent = playBadgeLabel("⏸ ");
          }
          return;
        }
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
      wireOvwSplit();
      renderMgmtGraph(d && d.graph, (d && d.subs) || []);
      // S2: the marquee measurer lives in app.js — let it size the new
      // .mq signature lines (accounts one-screen plan shares the grammar).
      document.dispatchEvent(new CustomEvent("ovw:rendered"));
    } catch (e) {
      box.innerHTML = '<p class="muted">' + esc(t("common.error", { msg: e.message })) + "</p>";
    }
  }

  // Split drag handle (superior 09-02): on phones the Overview stacks the
  // scrollable subs table above the connections graph; the handle between
  // them drags the split vertically. Height persists in localStorage.
  function wireOvwSplit() {
    var wrap = document.getElementById("mgmt-graph-wrap");
    var box = document.getElementById("mgmt-overview");
    if (!wrap || !box || wrap.dataset.splitWired === "1") return;
    var handle = document.createElement("button");
    handle.type = "button";
    handle.className = "ovw-split";
    handle.setAttribute("aria-label", "drag to resize");
    wrap.parentNode.insertBefore(handle, wrap);
    var saved = 0;
    try { saved = parseInt(localStorage.getItem("ovw_graph_h") || "0", 10); } catch (_) {}
    if (saved > 100) wrap.style.height = saved + "px";
    handle.addEventListener("pointerdown", function (e) {
      if (window.innerWidth > 800) return;
      e.preventDefault();
      handle.setPointerCapture(e.pointerId);
      var startY = e.clientY;
      var startH = wrap.getBoundingClientRect().height;
      function move(ev) {
        var h = startH + (startY - ev.clientY); // dragging up grows the graph
        var max = box.getBoundingClientRect().height - 160; // keep ≥160px for the list
        wrap.style.height = Math.max(120, Math.min(max, h)) + "px";
      }
      function up() {
        handle.removeEventListener("pointermove", move);
        handle.removeEventListener("pointerup", up);
        try { localStorage.setItem("ovw_graph_h", String(Math.round(wrap.getBoundingClientRect().height))); } catch (_) {}
      }
      handle.addEventListener("pointermove", move);
      handle.addEventListener("pointerup", up);
    });
    wrap.dataset.splitWired = "1";
  }

  // Floating graph controls: one click cycles the value, persists, and
  // re-renders. map/nums reuse the cached payload; days refetches.  // Superior 01M186FW6Y: map/nums restyle edges IN PLACE (DataSet.update of
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

  // ---- 播放模式（上级 0.2.5）----
  // 范围内（如 7d）全部发信活动按时间排序后逐拍高亮：当拍的发件节点与
  // 连线加亮（高亮=更醒目，非变浅），非当拍元素降显；播放/暂停期间禁
  // 拖拽/选中、节点位置冻结、数字隐藏；canvas 点按=暂停/继续，暂停时左
  // 下角浮层显示当拍信件（上级复核口径：信件 id+时间，不显示 from/to）；
  // 停止完整还原（经 restyleGraphEdges+节点原档回填）。
  // 数据两档：真实档=自+从属各收件/发件箱分页拉取逐信 {id,t,from,to}
  // （cc 经收件方邮箱覆盖）；拉取失败/为空回退合成档（按各边 count 合
  // 成均匀时刻）。缓存按 days 键控，换档/重载失效。
  var playState = null;      // null | {steps, idx, timer, paused, seqNo, total, real}
  var playNodeOrig = null;   // 节点原样式快照（id → {color, font, borderWidth}）
  var playBadge = null;      // 左下角当前信浮层
  var playSeqCache = null;   // {days, steps, real}

  function playGroupSteps(evs) {
    // 上级 09-03 复核：不合拍——每信一拍，浮层逐信显示 id
    return evs.map(function (ev) { return [ev]; });
  }
  function buildPlaySteps() { // 合成档（退化回退）
    var evs = [];
    mgmtEdgeMeta.forEach(function (meta) {
      if (!(meta.count > 0) || !meta.from) return;
      for (var i = 0; i < meta.count; i++) {
        // 合成时刻：每条边内部均匀、边间按比例交错（无真实时间戳时）
        evs.push({ t: (i + 0.5) / meta.count, id: meta.id, from: meta.from, to: meta.to, n: i + 1 });
      }
    });
    evs.sort(function (a, b) { return a.t - b.t; });
    return { steps: playGroupSteps(evs), total: evs.length, real: false };
  }
  // 真实档：逐信拉取自+从属的收件/发件箱（分页），窗口内按时间排序。
  // 事件→边映射：发件箱信 × 每个 to = (owner→to) 边事件；收件箱信 =
  // (from→owner) 边事件（cc 收件方经其收件箱覆盖）。(letterId, 边) 去重。
  async function fetchPlaySteps() {
    if (playSeqCache && playSeqCache.days === graphPrefs.days) return playSeqCache;
    var myAddr = ((getSession() || {}).address || "").toLowerCase();
    var subs = (mgmtOverviewData && mgmtOverviewData.subs) || [];
    var owners = [myAddr];
    subs.forEach(function (s) {
      var a = String(s.address || "").toLowerCase();
      if (a && owners.indexOf(a) < 0) owners.push(a);
    });
    var cutoff = Date.now() - graphPrefs.days * 86400000;
    var edgeByPair = {};
    mgmtEdgeMeta.forEach(function (meta) {
      if (meta.count > 0 && meta.from) {
        edgeByPair[String(meta.from).toLowerCase() + ">" + String(meta.to).toLowerCase()] = meta;
      }
    });
    var seenEv = {};
    var evs = [];
    function absorb(owner, sent, msgs) {
      (msgs || []).forEach(function (m) {
        var t = m.received_at || 0;
        if (t > 0 && t < 1e12) t = t * 1000; // 秒级时间戳→毫秒归一
        if (t && t < cutoff) return;
        var pairs = [];
        if (sent) {
          (m.to || []).forEach(function (rcpt) { pairs.push([owner, String(rcpt).toLowerCase()]); });
        } else {
          pairs.push([String(m.from || "").toLowerCase(), owner]);
        }
        pairs.forEach(function (pr) {
          var meta = edgeByPair[pr[0] + ">" + pr[1]];
          if (!meta) return;
          var key = m.id + "|" + meta.id;
          if (seenEv[key]) return;
          seenEv[key] = 1;
          evs.push({ t: t, id: meta.id, lid: m.id, from: pr[0], to: pr[1] });
        });
      });
    }
    async function pullOwn(sent) {
      var path = sent ? "/api/sent" : "/api/inbox";
      for (var offset = 0; offset < 3000; offset += 50) {
        var d = await api(path + "?limit=50&offset=" + offset, { keepSession: true });
        var msgs = (d && d.messages) || [];
        absorb(myAddr, sent, msgs);
        if (msgs.length < 50) break;
      }
    }
    async function pullSub(owner) {
      // 从属箱：/api/subs/{addr}/messages?folder=inbox|sent（单大页，无 offset）
      for (var fi = 0; fi < 2; fi++) {
        var folder = fi === 0 ? "inbox" : "sent";
        try {
          var d = await api("/api/subs/" + encodeURIComponent(owner) +
            "/messages?folder=" + folder + "&limit=1000", { keepSession: true });
          absorb(owner, folder === "sent", (d && d.messages) || []);
        } catch (_) { /* 单箱失败降级跳过 */ }
      }
    }
    for (var i = 0; i < owners.length; i++) {
      if (owners[i] === myAddr) {
        await pullOwn(false);
        await pullOwn(true);
      } else {
        await pullSub(owners[i]);
      }
    }
    evs.sort(function (a, b) { return a.t - b.t; });
    var built = { days: graphPrefs.days, steps: playGroupSteps(evs), total: evs.length, real: true };
    if (!built.steps.length) return null; // 真实档空→调用方回退合成档
    playSeqCache = built;
    return built;
  }
  function playBadgeEnsure() {
    var wrap = document.querySelector("#mgmt-graph-wrap");
    if (!wrap) return null;
    if (!playBadge) {
      playBadge = document.createElement("div");
      playBadge.className = "play-badge";
      wrap.appendChild(playBadge);
    }
    return playBadge;
  }
  // 四态拖尾（上级 09-03）：当拍全亮，此后每过一拍降一档——
  // tier0 全亮 → tier1 .78 → tier2 .5 → tier3 淡化 .25；节点同步（opacity
  // 不影响 vis label，文字色每档并行下调）。
  var playTiers = [
    { edge: { color: "#2563eb", width: 3, font: "#23303f", arrow: 0.5, esize: 10 },
      node: { opacity: 1, font: "rgba(35,48,63,1)" } },
    { edge: { color: "rgba(37,99,235,0.72)", width: 2.4, font: "rgba(35,48,63,0.72)", arrow: 0.45, esize: 9 },
      node: { opacity: 0.78, font: "rgba(35,48,63,0.75)" } },
    { edge: { color: "rgba(37,99,235,0.42)", width: 1.5, font: "rgba(35,48,63,0.42)", arrow: 0.3, esize: 9 },
      node: { opacity: 0.5, font: "rgba(35,48,63,0.48)" } },
    { edge: { color: "rgba(91,107,125,0.10)", width: 0.5, font: "rgba(35,48,63,0.10)", arrow: 0.15, esize: 9 },
      node: { opacity: 0.25, font: "rgba(35,48,63,0.25)" } }
  ];
  function playTierStep(step, tier) {
    var T0 = playTiers[Math.max(0, Math.min(3, tier))];
    mgmtEdgeSet.update(step.map(function (ev) {
      return { id: ev.id, color: { color: T0.edge.color }, width: T0.edge.width,
        font: { size: T0.edge.esize, face: "Consolas", color: T0.edge.font },
        arrows: { to: { enabled: true, scaleFactor: T0.edge.arrow } } };
    }));
    var seen = {};
    var nUp = [];
    step.forEach(function (ev) {
      [ev.from, ev.to].forEach(function (id) {
        if (seen[id]) return;
        seen[id] = 1;
        var o = (playNodeOrig || {})[id] || {};
        var f = o.font || {};
        if (tier === 0) { // 全亮档回填原色原字
          nUp.push({ id: id, opacity: 1, color: o.color, font: o.font, borderWidth: o.borderWidth || 1 });
        } else {
          nUp.push({ id: id, opacity: T0.node.opacity,
            font: { face: f.face || "ui-monospace, Consolas, monospace", size: f.size || 11, color: T0.node.font } });
        }
      });
    });
    mgmtNodeSet.update(nUp);
  }
  // 浮层文本（上级口径：信件 id+时间，不显示 from/to）；prefix=暂停符号等
  function playBadgeLabel(prefix) {
    var step = playState.steps[playState.idx - 1];
    if (!step) return prefix + "…";
    var e0 = step[0];
    // fmtTime 的数字参数按秒计（core.js），内部 t 已归一毫秒——显示前折回
    var label = e0.lid ? ("✉ " + e0.lid + " · " + fmtTime(Math.round(e0.t / 1000))) : ("✉ 合成序列 #" + playState.idx);
    return prefix + label + (step.length > 1 ? " ×" + step.length : "") +
      "  [" + playState.idx + "/" + playState.steps.length + "]";
  }
  function playTick() {
    if (!playState) return;
    if (playState.idx >= playState.steps.length) { stopPlay(); return; }
    // 四态拖尾（上级 09-03）：当拍全亮，越旧的拍逐档下沉，隔帧下降。
    // 刷档顺序=从最旧到最新：同一连线在相邻拍重复出现时，最新一拍的
    // 档位最后落笔获胜（否则旧拍的淡化会盖掉当拍高亮——上级实测 bug）。
    for (var back = 3; back >= 1; back--) {
      if (playState.idx - back >= 0) playTierStep(playState.steps[playState.idx - back], back);
    }
    playTierStep(playState.steps[playState.idx], 0);
    playState.idx++;
    if (playBadge) playBadge.textContent = playBadgeLabel("");
  }
  function stopPlay() {
    if (playState && playState.timer) clearInterval(playState.timer);
    playState = null;
    restyleGraphEdges(); // 连线按偏好完整还原（含数字）
    if (mgmtNodeSet && playNodeOrig) {
      var nUp = Object.keys(playNodeOrig).map(function (id) {
        var o = playNodeOrig[id];
        return { id: id, opacity: 1, color: o.color, font: o.font, borderWidth: o.borderWidth || 1 };
      });
      mgmtNodeSet.update(nUp);
    }
    playNodeOrig = null;
    if (playBadge) { playBadge.remove(); playBadge = null; }
    if (mgmtNetwork) mgmtNetwork.setOptions({ interaction: { dragNodes: true, dragView: true, selectable: true, hover: true } });
    var bp = $("#gg-play");
    if (bp) { bp.textContent = "▶"; bp.classList.remove("is-stop"); }
  }
  var playLoading = false;
  async function startPlay() {
    if (!mgmtNetwork || !mgmtEdgeSet || playState || playLoading) return;
    playLoading = true;
    playBadgeEnsure();
    if (playBadge) playBadge.textContent = "载入往来…";
    var built = null;
    try { built = await fetchPlaySteps(); } catch (_) {}
    if (!built || !built.steps.length) built = buildPlaySteps(); // 真实档空/失败→合成档
    if (!built.steps.length) {
      if (playBadge) playBadge.textContent = "范围内无流量";
      setTimeout(function () { if (!playState && playBadge) { playBadge.remove(); playBadge = null; } }, 1500);
      playLoading = false;
      return;
    }
    playState = { steps: built.steps, idx: 0, timer: null, paused: false, total: built.total, real: !!built.real, speed: 1 };
    // 冻结+禁拖+禁选（暂停态下边不再可选中，上级 0.2.5 复核）+隐藏数字
    mgmtNetwork.setOptions({ interaction: { dragNodes: false, dragView: false, selectable: false, hover: false } });
    playNodeOrig = {};
    var nUp = [];
    mgmtNodeSet.forEach(function (n) {
      playNodeOrig[n.id] = { color: n.color, font: n.font, borderWidth: n.borderWidth };
      nUp.push({ id: n.id, opacity: 0.25, font: { face: "ui-monospace, Consolas, monospace", size: 11, color: "rgba(35,48,63,0.25)" } });
    });
    mgmtNodeSet.update(nUp);
    mgmtEdgeSet.update(mgmtEdgeMeta.map(function (meta) {
      return { id: meta.id, label: " ", width: 0.5,
        color: { color: "rgba(91,107,125,0.10)" },
        font: { size: 9, face: "Consolas", color: "rgba(35,48,63,0.10)" },
        arrows: { to: { enabled: true, scaleFactor: 0.15 } } };
    }));
    playBadgeEnsure();
    var bp = $("#gg-play");
    // 三态循环（上级 09-03）：▶ 待播 → ▶▶ 一倍速播放中（点进二倍速）→ ■(大) 二倍速播放中（点停）
    if (bp) { bp.textContent = "▶▶"; bp.classList.remove("is-stop"); }
    playLoading = false;
    playState.timer = setInterval(playTick, playBeatMs());
  }
  function playBeatMs() { // 一倍速=原先一半（上级定义），2×=原速
    return playState && playState.speed === 2 ? 30 : 60;
  }
  function togglePlay() {
    if (playLoading) return;
    if (!playState) { startPlay(); return; }
    if (playState.speed === 1) { // 1×→2×
      playState.speed = 2;
      if (playState.timer) { clearInterval(playState.timer); playState.timer = setInterval(playTick, playBeatMs()); }
      var b2 = $("#gg-play");
      if (b2) {
        b2.textContent = "■"; b2.classList.add("is-stop");
        // 内联同款字号（上级 09-03：■ 字形天然小于 ▶，放大一档；内联防 CSS 缓存吞变更）
        b2.style.fontSize = "16px"; b2.style.lineHeight = "1";
      }
      return;
    }
    stopPlay(); // 2×→停止
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
    var bp = $("#gg-play");
    if (bp) bp.addEventListener("click", togglePlay);
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
