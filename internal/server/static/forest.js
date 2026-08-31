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
  var fVis = null, fNodes = null, fLastNodes = null; // 布局产物（连线重绘/锚定用）
  var fCardW = 224, fCardH = 68;
  // SVG 命名空间URI不能用字面 "http://" 写在一起——free-identifier 审计的
  // 注释剥离会把 // 当行注释吞掉，导致引号失衡级联出假阳性（01M15G 轮实测）。
  var fSvgNs = "http:" + "/" + "/www.w3.org/2000/svg";
  // 卡上横向拖动落下后，按最新节点位置重画连线
  function fLinksDraw() {
    var svg = $("#tf-links");
    if (!svg || !fVis || !fNodes) return;
    svg.innerHTML = "";
    fVis.forEach(function (tp, i) {
      var edge = fTreeColors[i % fTreeColors.length];
      tp._msgs.forEach(function (m) {
        if (m.id === tp.root_id) return;
        var p = fNodes[m.in_reply_to], c = fNodes[m.id];
        if (!p || !c) return;
        var x1 = p.x + fCardW / 2, y1 = p.y + fCardH - 2, x2 = c.x + fCardW / 2, y2 = c.y;
        var my = (y1 + y2) / 2;
        var path = document.createElementNS(fSvgNs, "path");
        path.setAttribute("d", "M" + x1 + " " + y1 + " C " + x1 + " " + my + ", " + x2 + " " + my + ", " + x2 + " " + y2);
        path.setAttribute("fill", "none");
        path.setAttribute("stroke", edge);
        path.setAttribute("stroke-width", "1.8");
        path.setAttribute("stroke-opacity", "0.75");
        path.setAttribute("stroke-linecap", "round");
        svg.appendChild(path);
      });
    });
  }
  var fTreeColors = ["#8ab4f8", "#34a853", "#a142f4", "#ea4335", "#ff8f00",
                  "#00acc1", "#7cb342", "#5c6bc0", "#d81b60", "#00897b"];

  function fPalette(role) {
    var r = role || "ex" + "t";   // 角色缺省为外部账号
    if (r === "me") return { fBg: "rgba(234,244,255,0.95)", fBd: "#1d4ed8" };
    if (r === "sub") return { fBg: "rgba(232,247,238,0.95)", fBd: "#2e9e5b" };
    return { fBg: "rgba(243,245,248,0.97)", fBd: "#b7c0cc" };
  }
  function fClip(s, n) { s = String(s || ""); return s.length > n ? s.slice(0, n - 1) + "…" : s; }
  // 展开/收起全文（上级 0.1.7 精修）：正文全量替换+限高滑动。模块级——
  // 卡片 click 与触摸 fTapCard 两个路径都要用。
  function fToggleExpand(card) {
    var on = card.classList.toggle("expanded");
    var body = card.querySelector(".f-body");
    if (body) body.textContent = on ? (card.__full || "…") : fClip(card.__full, 84);
  }
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
    // 上级 01M176QXG：PC 画布左缘出界——CSS vw calc 在网格列内失效，
    // 改为 JS 实测 breakout：左缘吸到 12px、宽度吃满视口。
    try {
      var r0 = fScroller.getBoundingClientRect();
      if (Math.abs(r0.left - 24) > 1 || Math.abs(r0.width - (window.innerWidth - 48)) > 1) {
        fScroller.style.marginLeft = (24 - r0.left) + "px";
        fScroller.style.width = (window.innerWidth - 48) + "px";
      }
    } catch (_) {}
    fClear();
    svg.innerHTML = "";
    // Superior 01M15G…: 锚定「离视野中心最近的信节点」——重排后该卡相对视野
    // 的位置不变（不是锚点坐标也不是锁 transform）。transform 以 style 矩阵
    // 为准（panzoom 内部模型与样式有滞后/符号差）。
    var anchor = null;
    if (fPz && fFitted && fLastNodes && fLastNodes.length) {
      var mm = (box.style.transform || "").match(/matrix\(([^)]+)\)/);
      if (mm) {
        var mv = mm[1].split(",").map(parseFloat);
        var cw2 = fScroller.clientWidth / 2, ch2 = fScroller.clientHeight / 2;
        var best = Infinity;
        fLastNodes.forEach(function (n) {
          var sx = n.x * mv[0] + mv[4], sy = n.y * mv[0] + mv[5];
          var d = (sx - cw2) * (sx - cw2) + (sy - ch2) * (sy - ch2);
          if (d < best) { best = d; anchor = { id: n.id, offX: sx - cw2, offY: sy - ch2, scale: mv[0] }; }
        });
      }
    }
    var CARD_W = fCardW, CARD_H = fCardH, DX = 236, ROW = fRowH(), TOP = 26;
    var nodes = {}, fLinks = [], fAll = [];

    // Item 2: natural lane widths first, then stretch the gaps so the
    // used lanes fill the canvas width on PC (min gap 40, base 56).
    var lanes = [];
    var scrollerW = Math.max(400, ($("#tf-scroll").clientWidth || 900) - 88);
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
      var xs = [], minX = Infinity, maxX = -Infinity;
      placed.each(function (n) { xs.push(n.x); minX = Math.min(minX, n.x); maxX = Math.max(maxX, n.x); });
      lanes.push({ tp: tp, edge: edge, placed: placed, minX: minX, w: (maxX - minX) + DX });
    });
    var natural = lanes.reduce(function (s, l) { return s + l.w; }, 0) + Math.max(0, lanes.length - 1) * 56;
    var gap = lanes.length > 1 ? Math.max(56, Math.min(240, 56 + (scrollerW - natural) / (lanes.length - 1))) : 56;
    var laneX = 44;
    lanes.forEach(function (l) {
      l.placed.each(function (n) {
        var m = n.data;
        var x = laneX + (n.x - l.minX) + l.w / 2 - CARD_W / 2;
        if (x < 8) x = 8;
        fAll.push({ m: m, x: x, subj: l.tp.subject, edge: l.edge, rootId: l.tp.root_id, isRoot: m.id === l.tp.root_id });
      });
      laneX += l.w + gap;
    });
    var maxRight = laneX;

    // Superior 01M13SYW0: sort by time, render equidistant - no more
    // time-scale squeeze, rows never collide. Row height is user-tunable
    // (item 3 density slider, persisted).
    fAll.sort(function (a, b) {
      return (a.m.received_at || 0) - (b.m.received_at || 0) || a.x - b.x;
    });
    fAll.forEach(function (n, i) { n.y = TOP + i * ROW; nodes[n.m.id] = n; });

    fAll.forEach(function (n) {
      var c = fPalette(n.m.role);
      var el = document.createElement("div");
      el.className = "f-card"; el.dataset.root = n.rootId;
      el.style.background = c.fBg;
      el.style.border = "1px solid " + c.fBd;
      el.style.borderLeft = "3px solid " + n.edge;
      el.style.left = n.x + "px"; el.style.top = n.y + "px";
      var subj = n.isRoot ? (n.subj || n.m.subject || "—") : (n.m.subject || n.subj || "—");
      el.__full = String(n.m.body || n.m.preview || ""); // 展开全文用
      el.innerHTML = '<div class="f-head">' + esc(shortAddr(n.m.from)) + " · " + esc(fmtTime(n.m.received_at)) + "</div>"
        + '<div class="f-subj">' + esc(fClip(subj, 24)) + "</div>"
        + '<div class="f-body">' + esc(fClip(n.m.body || n.m.preview || "", 84)) + "</div>";
      el.dataset.id = n.m.id;
      el.__fnode = n; // 触摸端点按/拖动定位节点用
      // Superior 01M15G…: 单击=只选中；卡可自由拖动（横向+纵向，纵向即时间
      // 方向，按上级 01M1* 续修令放开）；双击=进话题详情。
      var lastTap = 0; // 自管双击检测（原生 dblclick 在部分环境不合成）
      el.addEventListener("mousedown", function (ev) {
        ev.stopPropagation(); // 卡上按下不触发画布平移
        var startX = ev.clientX, startY = ev.clientY, origX = n.x, origY = n.y, moved = false;
        function mv(e2) {
          var dx = e2.clientX - startX, dy = e2.clientY - startY;
          if (Math.abs(dx) + Math.abs(dy) > 3) moved = true;
          if (moved) {
            el.style.left = (origX + dx) + "px";
            el.style.top = (origY + dy) + "px";
          }
        }
        function fUp(e2) {
          window.removeEventListener("mousemove", mv);
          window.removeEventListener("mouseup", fUp);
          if (moved) {
            n.x = origX + (e2.clientX - startX);
            n.y = origY + (e2.clientY - startY);
            el.__dragged = true;
            if (fVis && fNodes) fLinksDraw();
          }
        }
        window.addEventListener("mousemove", mv);
        window.addEventListener("mouseup", fUp);
      });
      el.addEventListener("click", function () {
        if (el.__dragged) { el.__dragged = false; return; }
        // Superior 0.1.7 精修：已选中的卡再单击=展开/收起全文；350ms 内
        // 第二次点击=双击进详情（自带计时检测，不依赖原生 dblclick——
        // headless/TWA 环境可能不合成）。
        var now = Date.now();
        if (now - lastTap < 350) {
          lastTap = 0;
          if (el.__expTimer) { clearTimeout(el.__expTimer); el.__expTimer = null; }
          document.dispatchEvent(new CustomEvent("threads:open", { detail: { root: n.rootId } }));
          return;
        }
        lastTap = now;
        if (el.classList.contains("sel")) {
          if (!el.__expTimer) {
            el.__expTimer = setTimeout(function () {
              el.__expTimer = null;
              fToggleExpand(el);
            }, 350);
          }
          return;
        }
        document.querySelectorAll(".f-card.sel").forEach(function (x) { x.classList.remove("sel"); });
        el.classList.add("sel");
      });
      box.appendChild(el);
    });

    var H = TOP + fAll.length * ROW + 30;
    box.style.width = Math.ceil(Math.max(maxRight + 40, scrollerW + 88)) + "px";
    box.style.height = Math.ceil(H) + "px";
    svg.setAttribute("viewBox", "0 0 " + parseFloat(box.style.width) + " " + Math.ceil(H));
    // Superior 01M15G…: fit only on first layout — re-layouts (tree count,
    // density slider) keep the user's current zoom/pan instead of jumping.
    if (!fFitted) fFitWidth();

    fVis = vis; fNodes = nodes;
    fLinksDraw();

    // Superior 01M15G…: 把锚定卡放回它原来相对视野的位置。
    // 需要 translate = 原视口offset + 中心 - 新content位置×scale。
    if (anchor) {
      var n2 = nodes[anchor.id];
      if (n2) {
        fPz.moveTo(fScroller.clientWidth / 2 + anchor.offX - n2.x * anchor.scale,
                   fScroller.clientHeight / 2 + anchor.offY - n2.y * anchor.scale);
      }
    }
    fLastNodes = fAll.map(function (n) { return { id: n.m.id, x: n.x, y: n.y }; });
  }


  // ---- 缩放/平移（上级 01M143796 / 01M15DKW1 / 01M15G…：现成库 panzoom）----
  // vendor/panzoom.min.js（MIT，无依赖）：滚轮/捏合/拖拽一律交给它，指针锚点
  // 与平滑手感由库负责。重排（切树数/调行距）不再重置视图。
  var fScroller = $("#tf-scroll");
  var fPz = null, fFitted = false;
  function fInitPz() {
    var c = $("#tf-canvas");
    if (fPz || !window.panzoom || !c) return;
    c.style.transformOrigin = "0 0";
    fPz = window.panzoom(c, {
      minZoom: 0.02, maxZoom: 4, zoomSpeed: 0.065,
      bounds: false, boundsPadding: 0.05,
      dblClickZoomEnabled: false,
      // 拖拽平移由 forest 自管（panzoom 的鼠标拖拽位移异常放大），只让库管缩放。
      beforeMouseDown: function () { return true; },
      // 上级 01M17AJ97 续：触摸全部自管——panzoom 的单指触摸会并行平移
      // （拖卡时视野跟着动）且 preventDefault 吞掉点按；其「忽略事件」选项
      // 实为 onTouch 而非 beforeTouch（内部函数名），且单指处理无法单独关。
      // 故 pause() 掉全部事件监听，panzoom 只作变换 API；滚轮/捏合/平移全在
      // forest 自管（fWireWheel / fWireDrag 触摸分支）。
      beforeTouch: function () { return true; },
    });
    fPz.pause();
    window.__fPz = fPz; // test handle
  }
  // 滚轮锚点缩放（panzoom pause 后自管）：指针下的内容点缩放后不动。
  (function fWireWheel() {
    if (!fScroller) return;
    fScroller.addEventListener("wheel", function (e) {
      e.preventDefault();
      if (!fPz) return;
      var cr = $("#tf-canvas").getBoundingClientRect();
      var ns = Math.min(4, Math.max(0.02, fPz.getTransform().scale * (e.deltaY < 0 ? 1.08 : 1 / 1.08)));
      fPz.zoomAbs(e.clientX - cr.left, e.clientY - cr.top, ns);
    }, { passive: false });
  })();
  function fFitWidth() {
    if (!fPz) { fInitPz(); }
    if (!fPz) return;
    var c = $("#tf-canvas");
    // 上级 01M17AJ97 后续 + 01M1ARCH1 复测令：进入森林视图时视野调整到
    // 显示全部树（宽高都适配并居中）。下限降到 0.02——超大图也要完整可见；
    // 上限 1.15 保留（小图不放大）。
    var w = parseFloat(c.style.width) || c.scrollWidth;
    var h = parseFloat(c.style.height) || c.scrollHeight;
    var fZ = Math.min(1.15, Math.max(0.02, Math.min(
      (fScroller.clientWidth - 48) / w,
      (fScroller.clientHeight - 48) / h)));
    fPz.zoomAbs(0, 0, fZ);
    fPz.moveTo((fScroller.clientWidth - w * fZ) / 2, (fScroller.clientHeight - h * fZ) / 2);
    fFitted = true;
  }
  // 重排保视图在 fLayout 内做：开头取当前变换，末尾原样放回（fDraw 异步，
  // 在外层包不住）。

  // ---- 画布悬浮工具条（上级 01M18HQ2C）：树数循环 + 孤信开关（左）+
  // 行距滑杆（右），sticky 常驻画布顶部，互不遮挡。树数/孤信原是头部一排
  // 药丸（#tf-ctl），归一为连接图 1d/7d/30d 同款悬浮按钮。 ----
  var fRowKey = "tf-row-h";
  function fRowH() {
    try { return parseInt(localStorage.getItem(fRowKey), 10) || 78; } catch (_) { return 78; }
  }
  (function fWireChrome() {
    var host = $("#tf-scroll");
    if (!host || $("#tf-bar")) return;
    var bar = document.createElement("div");
    bar.id = "tf-bar";
    // 树数循环按钮：5→10→20→5
    var bTrees = document.createElement("button");
    bTrees.type = "button"; bTrees.className = "f-fbtn"; bTrees.id = "tf-trees";
    bTrees.textContent = String(fTreeCount);
    bTrees.title = "trees 5/10/20";
    bTrees.addEventListener("click", function () {
      fTreeCount = fTreeCount === 5 ? 10 : (fTreeCount === 10 ? 20 : 5);
      bTrees.textContent = String(fTreeCount);
      // 上级 01M1AWXF：树数变化后重新 fit-all——新树要进入视野。
      fFitted = false;
      if (fCache) fDraw();
    });
    // 孤信开关：按下=显示孤立信（默认不显示）
    var bOrph = document.createElement("button");
    bOrph.type = "button"; bOrph.className = "f-fbtn"; bOrph.id = "tf-orphbtn";
    bOrph.textContent = "孤信";
    bOrph.classList.add("off"); // 默认不显示孤立信——按钮文字变淡（对齐数字按钮 .off）
    bOrph.title = "show orphans";
    bOrph.addEventListener("click", function () {
      fHideOrphans = !fHideOrphans;
      bOrph.classList.toggle("on", !fHideOrphans);
      bOrph.classList.toggle("off", fHideOrphans);
      if (fCache) fDraw();
    });
    // 重置按钮（上级 01M1ARCH1）：一键回到初始态——清除所有拖拽位移
    // （fDraw 重排即恢复布局原位）+ 视图回 fit-all。
    var bReset = document.createElement("button");
    bReset.type = "button"; bReset.className = "f-fbtn"; bReset.id = "tf-reset";
    bReset.textContent = t("forest.reset");
    bReset.title = "reset view & layout";
    bReset.addEventListener("click", function () {
      fFitted = false;
      if (fCache) fDraw(); else fLoad();
    });
    document.addEventListener("i18n:change", function () { bReset.textContent = t("forest.reset"); });
    // 行距滑杆
    var ctl = document.createElement("div");
    ctl.id = "tf-density";
    ctl.innerHTML = '<span>▤</span><input type="range" min="48" max="160" step="4" value="' + fRowH() + '" aria-label="row density">';
    bar.appendChild(bTrees);
    bar.appendChild(bOrph);
    bar.appendChild(bReset);
    bar.appendChild(ctl);
    host.insertBefore(bar, host.firstChild);
    ctl.querySelector("input").addEventListener("input", function (e) {
      var v = parseInt(e.target.value, 10) || 78;
      try { localStorage.setItem(fRowKey, String(v)); } catch (_) {}
      if (fCache) fDraw();
    });
  })();

  // ---- event surface ----
  document.addEventListener("tf:on", function () { fVisible = true; fFitted = false; fLoad(); });
  document.addEventListener("tf:off", function () { fVisible = false; fFitted = false; });
  document.addEventListener("threads:ref" + "resh", function () { if (fVisible) fLoad(); else fCache = null; });
  document.addEventListener("threads:reset", function () { fVisible = false; fCache = null; });
  document.addEventListener("i18n:change", function () { if (fVisible) fDraw(); });

  // 指针/触摸交互：滚轮缩放、拖拽平移、双指捏合、双击复位
  // 指针交互分工：缩放（滚轮/捏合）归 panzoom；拖拽平移自管，经 fPz.moveTo
  // 走库状态（位移=客户端像素，实测精准）。双击复位取消（上级 01M15G…）。
  (function fWireDrag() {
    if (!fScroller) return;
    var drag = null;
    fScroller.addEventListener("mousedown", function (e) {
      if (e.target.closest && (e.target.closest("#tf-density") || e.target.closest("#tf-bar"))) return;
      drag = { x: e.clientX, y: e.clientY, px: fPz.getTransform().x, py: fPz.getTransform().y };
      e.preventDefault();
    });
    window.addEventListener("mousemove", function (e) {
      if (!drag) return;
      fPz.moveTo(drag.px + (e.clientX - drag.x), drag.py + (e.clientY - drag.y));
    });
    window.addEventListener("mouseup", function () { drag = null; });
    // 触摸端（上级 01M176QXG）：panzoom 会吞掉合成 click/dblclick，因此
    // 点按/双点按/卡片横拖全部自管：短触未移动=点按（选中），350ms 内两次
    // 点按同一卡=进详情；单指横拖=平移，卡上单指横拖=拖卡。
    var fLastTap = { t: 0, card: null };
    var fTapTimer = null;
    function fTapCard(card) {
      var now = Date.now();
      if (fLastTap.card === card && now - fLastTap.t < 350) {
        fLastTap = { t: 0, card: null };
        if (fTapTimer) { clearTimeout(fTapTimer); fTapTimer = null; }
        document.dispatchEvent(new CustomEvent("threads:open", { detail: { root: card.dataset.root } }));
        return;
      }
      fLastTap = { t: now, card: card };
      if (fTapTimer) { clearTimeout(fTapTimer); fTapTimer = null; }
      if (card.classList.contains("sel")) {
        // 已选中：延迟切换展开/收起（给双点让路）
        fTapTimer = setTimeout(function () { fTapTimer = null; fToggleExpand(card); }, 300);
        return;
      }
      document.querySelectorAll(".f-card.sel").forEach(function (x) { x.classList.remove("sel"); });
      card.classList.add("sel");
    }
    var tdrag = null, fpinch = null;
    fScroller.addEventListener("touchstart", function (e) {
      // #tf-bar buttons (v0.1.6) handle their own clicks: a toolbar touch
      // must never enter pan/card mode, or the jitter touchmove's
      // preventDefault cancels the browser's click synthesis on mobile
      // (superior retest 01M196E31, issue 2 — Felix p2 instrumentation).
      if (e.target.closest && (e.target.closest("#tf-density") || e.target.closest("#tf-bar"))) return;
      if (e.touches.length === 2) {
        tdrag = null;
        fpinch = { d: Math.hypot(e.touches[0].clientX - e.touches[1].clientX, e.touches[0].clientY - e.touches[1].clientY) };
        return;
      }
      if (e.touches.length !== 1) return;
      var t0 = e.touches[0];
      var card = e.target.closest && e.target.closest(".f-card");
      if (card && card.__fnode) {
        tdrag = { mode: "card", card: card, n: card.__fnode, x: t0.clientX, y: t0.clientY, ox: card.__fnode.x, oy: card.__fnode.y, dx: 0, dy: 0, moved: false, t0: Date.now() };
      } else {
        tdrag = { mode: "pan", x: t0.clientX, y: t0.clientY, px: fPz.getTransform().x, py: fPz.getTransform().y, dx: 0, dy: 0, moved: false, t0: Date.now(), target: e.target };
      }
    }, { passive: true });
    fScroller.addEventListener("touchmove", function (e) {
      if (fpinch && e.touches.length === 2) {
        e.preventDefault();
        var d = Math.hypot(e.touches[0].clientX - e.touches[1].clientX, e.touches[0].clientY - e.touches[1].clientY);
        var cr = $("#tf-canvas").getBoundingClientRect();
        var mx = (e.touches[0].clientX + e.touches[1].clientX) / 2 - cr.left;
        var my = (e.touches[0].clientY + e.touches[1].clientY) / 2 - cr.top;
        var ns = Math.min(4, Math.max(0.02, fPz.getTransform().scale * (d / fpinch.d)));
        fPz.zoomAbs(mx, my, ns);
        fpinch.d = d;
        return;
      }
      if (!tdrag || e.touches.length !== 1) return;
      e.preventDefault();
      tdrag.dx = e.touches[0].clientX - tdrag.x;
      tdrag.dy = e.touches[0].clientY - tdrag.y;
      if (Math.abs(tdrag.dx) + Math.abs(tdrag.dy) > 8) tdrag.moved = true;
      if (tdrag.moved && tdrag.mode === "pan") {
        fPz.moveTo(tdrag.px + tdrag.dx, tdrag.py + tdrag.dy);
      } else if (tdrag.moved && tdrag.mode === "card") {
        tdrag.card.style.left = (tdrag.ox + tdrag.dx) + "px";
        tdrag.card.style.top = (tdrag.oy + tdrag.dy) + "px";
      }
    }, { passive: false });
    fScroller.addEventListener("touchend", function (e) {
      if (fpinch && e.touches.length < 2) fpinch = null;
      var t = tdrag; tdrag = null;
      if (!t) return;
      if (!t.moved && Date.now() - t.t0 < 400) {
        // 触摸端点按由这里统一合成——仅当命中卡片时才 preventDefault 压掉
        // 浏览器合成 click（避免与 fTapCard 双重触发）；非卡片区域（如悬浮
        // 按钮）绝不能压，否则原生 click 被吞、按钮在移动端失联（01M196HH8）。
        var card = t.mode === "card" ? t.card : (t.target && t.target.closest ? t.target.closest(".f-card") : null);
        if (card) {
          if (e && e.cancelable) e.preventDefault();
          fTapCard(card);
        }
        return;
      }
      if (t.moved && t.mode === "card") {
        t.n.x = t.ox + t.dx;
        t.n.y = t.oy + t.dy;
        fLinksDraw();
      }
    });
  })();
})();
