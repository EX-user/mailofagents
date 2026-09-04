// agentmail threads domain — the Topics view (third mgmt capsule segment).
// Superior directive (v0.6.24 car): mail is strung into topics by in_reply_to
// over the CALLER-VISIBLE set (self + declared subordinates, inbox ∪ sent —
// the same rule as /api/mgmt/subs-overview), so a human sees one topic's
// complete shape across all subordinate accounts.
// HARD CONSTRAINT (audit_frontend_imports.sh): imports ONLY ./core.js;
// cross-domain interactions go through DOM CustomEvents:
//   listens:  threads:entered {}  threads:refresh {}  threads:reset {}
//             i18n:change
//   emits:    mgmt:browse-account {address, folder?}  (full text in manage)
//             mgmt:fallback-browse {}  (remembered-entry empty-list bounce,
//             superior ruling 01M11ND4; manage switches back to browse)
// The i18n dictionary stays a classic global (window.I18N).
import { $, $$, esc, api, getSession, fmtTime } from "./core.js";

(function () {
  "use strict";

  function t(key, vars) {
    return window.I18N ? window.I18N.t(key, vars) : key;
  }

  // List paging. min_count=1 keeps lone messages visible (superior ruling:
  // 孤立信情形 — a singleton is its own topic row).
  var PAGE = 10;
  var listOffset = 0;
  var listTotal = -1;
  var listMinCount = 1; // min topic length filter (1=all, 2=exclude lone, etc.)
  var subsCache = null; // [address] of declared subordinates (owner resolve)

  function shortAddr(a) { return String(a || "").split("@")[0]; }

  function ensureSubs() {
    if (subsCache) return Promise.resolve(subsCache);
    return api("/api/subs", { keepSession: true }).then(function (d) {
      subsCache = ((d && d.subordinates) || []).map(function (s) { return s.address; });
      return subsCache;
    }, function () { subsCache = []; return subsCache; });
  }

  // resolveOwner maps a message to the mailbox that holds it: the sender
  // when a subordinate wrote it, the first subordinate recipient otherwise,
  // self last. Used to pick /api/subs/{A}/message?id= for the body fetch.
  function resolveOwner(m, subs, me) {
    var sender = String(m.from || "").toLowerCase();
    if (subs.indexOf(sender) >= 0) return m.from;
    var rcpts = (m.to || []).concat(m.cc || []);
    for (var i = 0; i < rcpts.length; i++) {
      if (subs.indexOf(String(rcpts[i]).toLowerCase()) >= 0) return rcpts[i];
    }
    return me;
  }

  function fetchBody(m) {
    var me = ((getSession() || {}).address || "").toLowerCase();
    return ensureSubs().then(function (subs) {
      var subsLower = subs.map(function(a){ return String(a).toLowerCase(); });
      var owner = resolveOwner(m, subsLower, me);
      var path = owner === me
        ? "/api/message?id=" + encodeURIComponent(m.id)
        : "/api/subs/" + encodeURIComponent(owner) + "/message?id=" + encodeURIComponent(m.id);
      return api(path, { keepSession: true }).then(function (d) {
        var msg = d && (d.message || d);
        return (msg && msg.body) || m.preview || "";
      });
    });
  }

  // ---- list rendering (root + latest leaf per row, per superior ruling) ----

  // 行卡片异步落地会插到先建好的分页条前面——每次落地把分页条挪回末尾
  function keepPagerLast(box) {
    var pg = box.querySelector(".th-pager");
    if (pg) box.appendChild(pg);
  }
  function renderTopicRow(tp, comp) {
    var msgs = (comp && comp.messages) || [];
    var rootMsg = msgs.length ? msgs[0] : null;
    var leafMsg = msgs.length > 1 ? msgs[msgs.length - 1] : null;
    var box = '<div class="th-topic" data-th-root="' + esc(tp.root_id) + '">';
    if (rootMsg) {
      box += '<div class="th-row"><span class="th-who">' + esc(shortAddr(rootMsg.from)) + '</span>' +
        '<span class="th-subj">' + esc(rootMsg.subject || tp.subject || "—") + '</span>' +
        '<span class="th-when">' + fmtTime(rootMsg.received_at) + '</span></div>' +
        '<div class="th-pv">' + esc(rootMsg.preview || "") + '</div>';
    }
    if (leafMsg) {
      box += '<div class="th-arrow">↓ ' + t("threads.latest") + '</div>' +
        '<div class="th-leaf"><div class="th-row"><span class="th-who">' + esc(shortAddr(leafMsg.from)) + '</span>' +
        '<span class="th-when">' + fmtTime(leafMsg.received_at) + '</span></div>' +
        '<div class="th-pv">' + esc(leafMsg.preview || "") + '</div></div>';
    } else {
      box += '<span class="th-lone">' + t("threads.lone") + '</span>';
    }
    box += '<div class="th-meta mono">' + t("threads.count", { n: tp.count }) +
      ' · ' + (tp.participants || []).map(shortAddr).join(" · ") + '</div>';
    box += "</div>";
    return box;
  }


  // PC 分栏开关（上级 09-04）：话题-列表态恒为左列表右详情，
  // 未点话题时右侧给占位提示；森林态/移动端不分栏
  function syncThreadSplit() {
    var detail = $("#th-detail");
    var wrap = $("#mgmt-threads");
    var list = $("#th-list");
    if (!detail || !wrap || !list) return;
    var on = window.innerWidth > 800 &&
      !wrap.classList.contains("hidden") && !list.classList.contains("hidden");
    detail.classList.toggle("hidden", !on);
    wrap.classList.toggle("th-split", on);
    if (on && !detail.querySelector(".th-rail")) {
      detail.innerHTML = '<p class="muted" style="padding:12px 4px;">' +
        esc(t("threads.pickHint")) + "</p>";
    }
  }
  function loadThreadsList() {
    var box = $("#th-list");
    if (!box) return;
    // 列表重载后旧详情失效：分栏保持，详情重置为占位
    syncThreadSplit();
    var thd = $("#th-detail");
    if (thd && !thd.classList.contains("hidden")) {
      thd.innerHTML = '<p class="muted" style="padding:12px 4px;">' +
        esc(t("threads.pickHint")) + "</p>";
    }
    box.textContent = t("common.loading");
    api("/api/threads?limit=" + PAGE + "&offset=" + listOffset + "&min_count=" + listMinCount, { keepSession: true })
      .then(function (d) {
        var topics = (d && d.threads) || [];
        listTotal = d && typeof d.total === "number" ? d.total : topics.length;
        // Min-count filter control (superior: set lower bound for topic length)
        var ctrl = document.createElement("div");
        ctrl.className = "th-filter";
        var filterOpts = [{ v: 1, l: t("threads.filterAll") }, { v: 2, l: t("threads.filter2") },
                    { v: 3, l: t("threads.filter3") }, { v: 5, l: t("threads.filter5") }];
        filterOpts.forEach(function (o) {
          var btn = document.createElement("button");
          btn.type = "button";
          btn.className = "th-filter-btn" + (listMinCount === o.v ? " on" : "");
          btn.textContent = o.l;
          btn.addEventListener("click", function () {
            if (listMinCount !== o.v) { listMinCount = o.v; listOffset = 0; loadThreadsList(); }
          });
          ctrl.appendChild(btn);
        });
        box.textContent = "";
        box.appendChild(ctrl);
        if (!topics.length) {
          box.innerHTML = '<p class="muted">' + esc(t("threads.empty")) + "</p>";
          // Remembered entry must not strand the user on an empty view —
          // fall back to browse SILENTLY (superior 01M11P89: no prompt, the
          // pane just switches itself back). User-picked filters never
          // trigger this; the flag is one-shot.
          if (document.__mgmtRestoreThreads) {
            document.__mgmtRestoreThreads = false;
            document.dispatchEvent(new CustomEvent("mgmt:fallback-browse"));
          }
          return;
        }
        box.textContent = "";
        // Root + latest leaf need each component; fetch in parallel
        // (page is capped at 10). Server-side enrichment of the index row
        // (root/leaf previews on ThreadTopic) would drop these fetches —
        // proposed to Devi as a follow-up, not blocking.
        topics.forEach(function (tp) {
          api("/api/thread?root=" + encodeURIComponent(tp.root_id), { keepSession: true })
            .then(function (comp) {
              var el = document.createElement("div");
              el.innerHTML = renderTopicRow(tp, comp);
              box.appendChild(el.firstChild);
              keepPagerLast(box);
            }, function () {
              var el = document.createElement("div");
              el.innerHTML = renderTopicRow(tp, null);
              box.appendChild(el.firstChild);
              keepPagerLast(box);
            });
        });
        // Pagination: prev / page indicator / next
        if (listTotal > PAGE) {
          var curPage = Math.floor(listOffset / PAGE) + 1;
          var totalPages = Math.ceil(listTotal / PAGE);
          var pag = document.createElement("div");
          // 收发件同款翻页控件组（上级 09-04）：胶囊 ◀ 页码 ▶，
           // PC 悬浮列表右下、移动端整行
          pag.className = "inbox-pager th-pager";
          // 两钮恒在（与收发件一致）：首页/末页用 disabled 表达，
          // 条件追加会让第一页少一颗钮、两页形态不对称
          var prevBtn = document.createElement("button");
          prevBtn.type = "button";
          prevBtn.className = "row-action";
          prevBtn.setAttribute("data-i18n", "pager.prev");
          prevBtn.textContent = t("pager.prev");
          prevBtn.disabled = curPage <= 1;
          prevBtn.addEventListener("click", function () {
            listOffset = Math.max(0, listOffset - PAGE);
            loadThreadsList();
          });
          pag.appendChild(prevBtn);
          // 收发件同款：胶囊内为页码输入（回车/步进跳页），总页数仅移动端显示
          var pageInput = document.createElement("input");
          pageInput.type = "number";
          pageInput.className = "th-page-input";
          pageInput.min = 1; pageInput.max = totalPages; pageInput.value = curPage;
          pageInput.addEventListener("change", function () {
            var v = parseInt(pageInput.value, 10);
            if (isNaN(v)) return;
            v = Math.max(1, Math.min(totalPages, v));
            if ((v - 1) * PAGE !== listOffset) { listOffset = (v - 1) * PAGE; loadThreadsList(); }
          });
          pag.appendChild(pageInput);
          var indicator = document.createElement("span");
          indicator.className = "th-page-info muted";
          indicator.textContent = t("threads.page", { a: curPage, b: totalPages });
          pag.appendChild(indicator);
          var nextBtn = document.createElement("button");
          nextBtn.type = "button";
          nextBtn.className = "row-action";
          nextBtn.setAttribute("data-i18n", "pager.next");
          nextBtn.textContent = t("pager.next");
          nextBtn.disabled = curPage >= totalPages;
          nextBtn.addEventListener("click", function () {
            listOffset += PAGE;
            loadThreadsList();
          });
          pag.appendChild(nextBtn);
          box.appendChild(pag);
        }
      }, function (e) {
        box.innerHTML = '<p class="muted">' + esc(t("common.error", { msg: e.message })) + "</p>";
      });
  }

  // ---- detail rendering: irt tree that degrades to a clean linear rail ----
  // Chains (every parent has one child) render flat, one message under the
  // next; only true forks indent (superior ruling: tree is fine if it falls
  // back to a good linear display). Bodies are inlined (胶囊展开) — the
  // message content MUST be visible here.

  function renderThreadDetail(rootId, msgs) {
    var byId = {};
    msgs.forEach(function (m) { byId[m.id] = m; });
    // Stable pass: bucket each message under its parent when the parent is
    // in the visible set, else under the root marker "". Chronological.
    var children = {};
    msgs.slice().sort(function (a, b) { return a.received_at - b.received_at; }).forEach(function (m) {
      var p = m.in_reply_to || "";
      if (p && byId[p]) (children[p] = children[p] || []).push(m);
      else (children[""] = children[""] || []).push(m);
    });
    // Dangling refs (irt outside the visible set) get one placeholder each.
    var dangled = {};
    msgs.forEach(function (m) {
      var p = m.in_reply_to || "";
      if (p && !byId[p]) dangled[m.id] = true;
    });

    var html = '<span class="th-back" id="th-back">‹ ' + t("threads.back") + "</span>";
    html += '<div class="th-rail">';
    function walk(id, depth) {
      (children[id] || []).forEach(function (m) {
        if (dangled[m.id]) {
          html += '<div class="th-gap">··· ' + t("threads.gap") + " ···</div>";
          delete dangled[m.id]; // one placeholder per dangling message
        }
        var parentMsg = m.in_reply_to && byId[m.in_reply_to] ? byId[m.in_reply_to] : null;
        var reply = "";
        if (parentMsg) {
          var replyVars = {}; replyVars.who = esc(shortAddr(parentMsg.from));
          reply = ' <span class="th-reply">↩ ' + t("threads.replyTo", replyVars) + "</span>";
        }
        html += '<div class="th-msg" data-th-msg="' + esc(m.id) + '" data-th-from="' + esc(m.from) + '" data-th-to="' + esc((m.to || []).join(",")) + '" style="' +
          (depth > 0 ? "margin-left:" + (18 * depth) + "px;" : "") + '">' +
          '<div class="th-hd"><span class="th-toggle">▶</span><span class="th-who">' + esc(shortAddr(m.from)) + '</span>' +
          '<span class="th-arr">→</span><span class="mono">' + esc(shortAddr((m.to || [])[0] || "")) + '</span>' +
          "<span>· " + fmtTime(m.received_at) + "</span>" + reply +
          (m.unread ? ' <span class="th-badge">' + t("threads.unread") + "</span>" : "") +
          "</div>" +
          // Superior 01M15DKW1: two-line peek before opening (was one).
          '<div class="th-peek">' + esc(m.preview || "") + "</div>" +
          '<div class="th-full hidden" data-th-body="' + esc(m.id) + '"></div>' +
          "</div>";
        var kids = children[m.id] || [];
        // Forks indent one level; chains stay flat (linear fallback).
        walk(m.id, kids.length > 1 ? depth + 1 : depth);
      });
    }
    walk("", 0);
    html += "</div>";
    return html;
  }

  function openThread(rootId) {
    // PC 分栏（上级 09-04）：宽屏点话题不切屏——详情渲染进右侧
    // #th-detail，与左侧列表并排；移动端保持原切屏行为。
    var detail = $("#th-detail");
    var split = window.innerWidth > 800 && !!detail;
    var wrap = $("#mgmt-threads");
    var box = split ? detail : $("#th-list");
    if (!box) return;
    if (split) {
      detail.classList.remove("hidden");
      if (wrap) wrap.classList.add("th-split");
      detail.scrollTop = 0;
    } else if (detail) {
      detail.classList.add("hidden");
      if (wrap) wrap.classList.remove("th-split");
    }
    // （移动端/森林路径不经过 sync——恒分栏由 setTView/loadThreadsList 管）
    box.textContent = t("common.loading");
    api("/api/thread?root=" + encodeURIComponent(rootId), { keepSession: true })
      .then(function (comp) {
        var msgs = (comp && comp.messages) || [];
        box.innerHTML = renderThreadDetail(comp && comp.root || rootId, msgs);
        var back = $("#th-back");
        if (back) back.addEventListener("click", function () {
          if (split && detail) {
            detail.classList.add("hidden");
            if (wrap) wrap.classList.remove("th-split");
          } else loadThreadsList();
        });
        // Toggle fold/expand on each message — body fetched lazily on
        // first expand, then cached (compose-thread pattern per superior).
        $$(".th-msg", box).forEach(function (el) {
          var toggle = $(".th-toggle", el);
          var full = $(".th-full", el);
          if (!toggle || !full) return;
          toggle.addEventListener("click", async function (ev) {
            ev.stopPropagation();
            const peek = $(".th-peek", el);
            if (full.classList.contains("hidden")) {
              // Expand: full body replaces the one-line peek.
              if (peek) peek.classList.add("hidden");
              if (!full.dataset.loaded) {
                full.textContent = t("common.loading");
                full.classList.remove("hidden");
                toggle.textContent = "▼";
                try {
                  var body = await fetchBody({
                    id: el.getAttribute("data-th-msg"),
                    from: el.getAttribute("data-th-from"),
                    to: (el.getAttribute("data-th-to") || "").split(",").filter(Boolean)
                  });
                  full.textContent = body || "";
                } catch (e) {
                  full.textContent = t("common.error", { msg: e.message });
                }
                full.dataset.loaded = "1";
                // Unread dot removal on expand
                var badge = $(".th-badge", el);
                if (badge) badge.remove();
              } else {
                full.classList.remove("hidden");
                toggle.textContent = "▼";
              }
            } else {
              full.classList.add("hidden");
              if (peek) peek.classList.remove("hidden"); // peek returns on collapse
              toggle.textContent = "▶";
            }
          });
        });
      }, function (e) {
        box.innerHTML = '<p class="muted">' + esc(t("common.error", { msg: e.message })) + "</p>";
      });
  }

  function wireThreadsPane() {
    var box = $("#th-list");
    if (!box || box.dataset.wired) return;
    box.dataset.wired = "1";
    box.addEventListener("click", function (ev) {
      var topic = ev.target.closest("[data-th-root]");
      if (topic) openThread(topic.getAttribute("data-th-root"));
    });
  }

  // ---- event surface ----
  document.addEventListener("threads:entered", function () {
    if (!getSession()) return;
    wireThreadsPane();
    if (!$("#th-list .th-topic") && !$("#th-list .th-rail")) loadThreadsList();
  });
  document.addEventListener("threads:refresh", function () {
    if (getSession()) loadThreadsList();
  });
  document.addEventListener("threads:reset", function () {
    listOffset = 0;
    listTotal = -1;
    subsCache = null;
    var box = $("#th-list");
    if (box) { box.textContent = ""; delete box.dataset.wired; }
  });

  // ---- 森林/列表 视图切换（superior-approved v7 mock）----
  // 胶囊在话题页头部；森林卡片点击经 threads:open 回列表视图打开详情。
  function setTView(v) {
    var forest = v === "forest";
    // 森林视图不分栏；回列表恢复常驻分栏（上级 09-04）
    var thd = $("#th-detail");
    if (thd) thd.classList.add("hidden");
    var wrap2 = $("#mgmt-threads");
    if (wrap2) wrap2.classList.remove("th-split");
    var segBtns = $("#tf-ctl");
    if (segBtns) segBtns.classList.toggle("hidden", !forest);
    var list = $("#th-list");
    if (list) list.classList.toggle("hidden", forest);
    var fs = $("#tf-scroll");
    if (fs) fs.classList.toggle("hidden", !forest);
    $$("#th-viewseg [data-tview]").forEach(function (b) {
      b.classList.toggle("on", b.dataset.tview === v);
    });
    document.dispatchEvent(new CustomEvent(forest ? "tf:on" : "tf:off"));
    if (!forest) syncThreadSplit();
  }
  $$("#th-viewseg [data-tview]").forEach(function (b) {
    b.addEventListener("click", function () { setTView(b.dataset.tview); });
  });
  document.addEventListener("threads:open", function (ev) {
    setTView("list");
    if (ev && ev.detail && ev.detail.root) openThread(ev.detail.root);
  });
  document.addEventListener("i18n:change", function () {
    if (getSession() && ($("#th-list .th-topic") || $("#th-list .th-rail"))) {
      // Re-render in the new language from the list start.
      listOffset = 0;
      loadThreadsList();
    }
  });
})();
