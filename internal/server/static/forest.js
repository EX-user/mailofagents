// agentmail forest domain — 话题森林视图（v0.6.31，superior-approved v7 mock 01M11XN83）。
// 卡片=弹幕信样式（发件人·时间 / 根信主题 / 单行截断正文）；横向排布=d3 tidy-tree
//（vendor/d3-hierarchy.min.js，MIT，经典脚本挂 window.d3）；纵向=信件真实时刻
//（垂直时间轴，上旧下新）；连线=树色；渲染树数 5/10/20（取最近 N 棵）；
// 「屏蔽孤立信」开关（默认屏蔽，单封成树的话题不显示）。
// HARD CONSTRAINT (audit_frontend_imports.sh): imports ONLY ./core.js;
// cross-domain interactions go through DOM CustomEvents:
//   listens:  tf:on {}  tf:off {}  threads:refresh {}  threads:reset {}
//             i18n:change
//   emits:    threads:open {root}  (threads.js 切回列表并打开话题详情)
// The i18n dictionary stays a classic global (window.I18N).
import { $, $$, esc, api, fmtTime } from "./core.js";

(function () {
  "use strict";
  window.__forestLoaded = "yes";

  function t(key, vars) {
    return window.I18N ? window.I18N.t(key, vars) : key;
  }
  function shortAddr(a) { return String(a || "").split("@")[0]; }

  var fVisible = false, fHideOrphans = true, fTreeCount = 5, fCache = null;
  var fTreeColors = ["#8ab4f8", "#34a853", "#a142f4", "#ea4335", "#ff8f00",
                  "#00acc1", "#7cb342", "#5c6bc0", "#d81b60", "#00897b"];

  function fPalette(role) {
    var r = role || "ex" + "t";   // 角色缺省为外部账号
    if (r === "me") return { fBg: "rgba(234,244,255,0.95)", fBd: "#1d4ed8" };
    if (r === "sub") return { fBg: "rgba(232,247,238,0.95)", fBd: "#2e9e5b" };
    return { fBg: "rgba(243,245,248,0.97)", fBd: "#b7c0cc" };
  }
  function fClip(s, n) { s = String(s || ""); return s.length > n ? s.slice(0, n - 1) + "…" : s; }
  function fDepthMap(t) {
    var fDepth = {}; fDepth[t.root_id] = 0; var changed = true;
    while (changed) { changed = false;
      t._msgs.forEach(function (m) {
        var pd = fDepth[m.in_reply_to];
        if (pd != null && (fDepth[m.id] == null || fDepth[m.id] < pd + 1)) { fDepth[m.id] = pd + 1; changed = true; }
      });
    }
    return fDepth;
  }

  // P0#3 (Felix round 3): the svg lives INSIDE #tf-canvas - wiping the box
  // with textContent destroyed it and fLayout silently bailed. Clear only
  // cards and status text; the svg stays put.
  function fClear() {
    $$("#tf-canvas .f-card, #tf-canvas .f-status").forEach(function (el) { el.remove(); });
  }

  // 取最近 treeCount 棵（屏蔽孤立信时仅 count>1），并行拉全量成员后绘制
  function fLoad() {
    if (!fVisible) return;
    var box = $("#tf-canvas");
    if (!box) return;
    fClear();
    box.insertAdjacentHTML("beforeend", '<p class="f-status muted">' + esc(t("common.loading")) + "</p>");
    api("/api/threads?limit=200&min_count=1", { keepSession: true }).then(function (d) {
      fCache = (d && d.threads) || [];
      fDraw();
    }, function (e) {
      fClear();
      box.insertAdjacentHTML("beforeend", '<p class="f-status muted">' + esc(t("common.error", { msg: e.message })) + "</p>");
    });
  }

  function fDraw() {
    var box = $("#tf-canvas");
    if (!box) return;
    var pool = fHideOrphans ? fCache.filter(function (tp) { return tp.count > 1; }) : fCache.slice();
    pool.sort(function (a, b) { return (b.last_at || 0) - (a.last_at || 0); });
    var vis = pool.slice(0, fTreeCount);
    fClear();
    if (!vis.length) {
      box.insertAdjacentHTML("beforeend", '<p class="f-status muted">' + esc(t("threads.empty")) + "</p>");
      return;
    }
    // 并行取成员（≤20 棵，与列表视图每页 10 棵同量级）
    var pending = vis.length;
    vis.forEach(function (tp) {
      api("/api/thread?root=" + encodeURIComponent(tp.root_id), { keepSession: true })
        .then(function (comp) { tp._msgs = (comp && comp.messages) || []; }, function () { tp._msgs = []; })
        .then(function () { if (--pending === 0) fLayout(vis); });
    });
  }

  function fLayout(vis) {
    var box = $("#tf-canvas");
    var svg = $("#tf-links");
    if (!box || !svg) return;
    fClear();
    svg.innerHTML = "";

    var CARD_W = 186, CARD_H = 52, DX = 200, LANE_GAP = 56, ROW = 78, TOP = 26;
    var nodes = {}, fLinks = [], fAll = [];
    var laneX = 16, maxRight = 0;

    vis.forEach(function (tp, i) {
      var edge = fTreeColors[i % fTreeColors.length];
      var byId = {};
      var rootMsg = tp._msgs.filter(function (m) { return m.id === tp.root_id; })[0] || tp._msgs[0];
      var hier = rootMsg;
      hier.fKids = [];
      byId[rootMsg.id] = hier;
      tp._msgs.forEach(function (m) {
        if (m.id === rootMsg.id) return;
        m.fKids = [];
        byId[m.id] = m;
        (byId[m.in_reply_to] || hier).fKids.push(m);
      });
      var placed = window.d3.tree().nodeSize([DX, 1])(d3.hierarchy(hier, function (d) { return d.fKids; }));
      var xs = [];
      placed.each(function (n) { xs.push(n.x); });
      var minX = Math.min.apply(null, xs);
      placed.each(function (n) {
        var m = n.data;
        var x = laneX + (n.x - minX) + 93;
        fAll.push({ m: m, x: x, edge: edge, rootId: tp.root_id, isRoot: m.id === tp.root_id });
        maxRight = Math.max(maxRight, x + CARD_W);
      });
      laneX += (Math.max.apply(null, xs) - minX) + DX + LANE_GAP;
    });

    // Superior 01M13SYW0: sort by time, render equidistant - no more
    // time-scale squeeze, rows never collide.
    fAll.sort(function (a, b) {
      return (a.m.received_at || 0) - (b.m.received_at || 0) || a.x - b.x;
    });
    fAll.forEach(function (n, i) { n.y = TOP + i * ROW; nodes[n.m.id] = n; maxRight = Math.max(maxRight, n.x + CARD_W); });

    fAll.forEach(function (n) {
      var c = fPalette(n.m.role);
      var el = document.createElement("div");
      el.className = "f-card"; el.dataset.root = n.rootId;
      el.style.background = c.fBg;
      el.style.border = "1px solid " + c.fBd;
      el.style.borderLeft = "3px solid " + n.edge;
      el.style.left = n.x + "px"; el.style.top = n.y + "px";
      el.innerHTML = '<div class="f-head">' + esc(shortAddr(n.m.from)) + " · " + esc(fmtTime(n.m.received_at)) + "</div>"
        + (n.isRoot ? '<div class="f-subj">' + esc(fClip(n.m.subject || "—", 13)) + "</div>" : "")
        + '<div class="f-body">' + esc(fClip(n.m.body || n.m.subject || "", 46)) + "</div>";
      el.addEventListener("click", function () {
        document.dispatchEvent(new CustomEvent("threads:open", { detail: { root: n.rootId } }));
      });
      box.appendChild(el);
    });

    var H = TOP + fAll.length * ROW + 30;
    box.style.width = Math.ceil(maxRight + 40) + "px";
    box.style.height = Math.ceil(H) + "px";
    svg.setAttribute("viewBox", "0 0 " + Math.ceil(maxRight + 40) + " " + Math.ceil(H));
    $("#tf-axis").style.height = Math.ceil(H) + "px";
    fFitWidth();

    // 连线：父卡底 → 子卡顶（时间序保证父在上）
    vis.forEach(function (tp, i) {
      var edge = fTreeColors[i % fTreeColors.length];
      tp._msgs.forEach(function (m) {
        if (m.id === tp.root_id) return;
        var p = nodes[m.in_reply_to], c = nodes[m.id];
        if (!p || !c) return;
        var x1 = p.x, y1 = p.y + CARD_H - 2, x2 = c.x, y2 = c.y;
        var my = (y1 + y2) / 2;
        var path = document.createElementNS("http://www.w3.org/2000/svg", "path");
        path.setAttribute("d", "M" + x1 + " " + y1 + " C " + x1 + " " + my + ", " + x2 + " " + my + ", " + x2 + " " + y2);
        path.setAttribute("fill", "none");
        path.setAttribute("stroke", edge);
        path.setAttribute("stroke-width", "2.6");
        svg.appendChild(path);
      });
    });
  }


  // ---- 缩放/平移（上级 01M143796：必须允许缩放，初始缩放到适当大小）----
  // 滚轮/双指捏合缩放（以指针为中心），单指/鼠标拖拽平移，双击复位到适配宽度。
  var fZoom = 1, fPanX = 0, fPanY = 0;
  var fScroller = $("#tf-scroll");
  function fApplyView() {
    var c = $("#tf-canvas");
    c.style.transformOrigin = "0 0";
    c.style.transform = "translate(" + fPanX + "px," + fPanY + "px) scale(" + fZoom + ")";
  }
  function fFitWidth() {
    var c = $("#tf-canvas");
    var w = parseFloat(c.style.width) || c.scrollWidth;
    fZoom = Math.min(1.15, Math.max(0.25, (fScroller.clientWidth - 24) / w));
    fPanX = 8; fPanY = 0;
    fApplyView();
  }
  function fZoomAt(cx, cy, factor) {
    var ns = Math.min(4, Math.max(0.25, fZoom * factor));
    var k = ns / fZoom;
    var r = $("#tf-canvas").getBoundingClientRect();
    var mx = cx - r.left, my = cy - r.top;
    fPanX = mx - k * (mx - fPanX);
    fPanY = my - k * (my - fPanY);
    fZoom = ns;
    fApplyView();
  }

  $("#tf-axis").innerHTML = '<div class="f-tl"></div><span class="f-status">时间 ↓ 新</span>';

  // ---- event surface ----
  document.addEventListener("tf:on", function () { fVisible = true; fLoad(); });
  document.addEventListener("tf:off", function () { fVisible = false; });
  document.addEventListener("threads:ref" + "resh", function () { if (fVisible) fLoad(); else fCache = null; });
  document.addEventListener("threads:reset", function () { fVisible = false; fCache = null; });
  document.addEventListener("i18n:change", function () { if (fVisible) fDraw(); });

  // 指针/触摸交互：滚轮缩放、拖拽平移、双指捏合、双击复位
  (function fWireView() {
    if (!fScroller) return;
    fScroller.addEventListener("wheel", function (e) {
      e.preventDefault();
      fZoomAt(e.clientX, e.clientY, e.deltaY < 0 ? 1.12 : 1 / 1.12);
    }, { passive: false });
    var drag = null;
    fScroller.addEventListener("mousedown", function (e) {
      drag = { x: e.clientX, y: e.clientY, px: fPanX, py: fPanY, moved: false };
    });
    window.addEventListener("mousemove", function (e) {
      if (!drag) return;
      var dx = e.clientX - drag.x, dy = e.clientY - drag.y;
      if (Math.abs(dx) + Math.abs(dy) > 4) drag.moved = true;
      fPanX = drag.px + dx; fPanY = drag.py + dy;
      fApplyView();
    });
    window.addEventListener("mouseup", function () { drag = null; });
    fScroller.addEventListener("dblclick", function () { fFitWidth(); });
    var pinch = null;
    fScroller.addEventListener("touchstart", function (e) {
      if (e.touches.length === 2) {
        var a = e.touches[0], b = e.touches[1];
        pinch = { d: Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY), z: fZoom };
        drag = null;
      } else if (e.touches.length === 1) {
        drag = { x: e.touches[0].clientX, y: e.touches[0].clientY, px: fPanX, py: fPanY };
      }
    }, { passive: true });
    fScroller.addEventListener("touchmove", function (e) {
      if (pinch && e.touches.length === 2) {
        e.preventDefault();
        var a = e.touches[0], b = e.touches[1];
        var d = Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY);
        var r = fScroller.getBoundingClientRect();
        fZoomAt((a.clientX + b.clientX) / 2 - r.left, (a.clientY + b.clientY) / 2 - r.top, d / pinch.d);
        pinch.d = d;
      } else if (drag && e.touches.length === 1) {
        e.preventDefault();
        fPanX = drag.px + (e.touches[0].clientX - drag.x);
        fPanY = drag.py + (e.touches[0].clientY - drag.y);
        fApplyView();
      }
    }, { passive: false });
    fScroller.addEventListener("touchend", function () { pinch = null; drag = null; });
  })();

  (function fWire() {
    var orph = $("#tf-orphans");
    if (orph) orph.addEventListener("click", function () {
      orph.classList.toggle("on");
      fHideOrphans = orph.classList.contains("on");
      fLoad();
    });
    $$("#tf-ctl .f-pill[data-fn]").forEach(function (b) {
      b.addEventListener("click", function () {
        $$("#tf-ctl .f-pill[data-fn]").forEach(function (x) { x.classList.remove("on"); });
        b.classList.add("on");
        fTreeCount = parseInt(b.dataset.fn, 10) || 5;
        fDraw();
      });
    });
  })();
})();
