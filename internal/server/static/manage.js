// agentmail manage cluster — S2 car 2 (mail + subs + mgmt views, one tab).
// HARD CONSTRAINT (audit_frontend_imports.sh): imports ONLY ./core.js;
// cross-domain interaction goes through DOM CustomEvents:
//   listens:  manage:entered (tab activation: account options + mgmt re-kick)
//             manage:refresh (prefs thresholds changed -> recolor dots)
//             manage:reset   (login switch: caches must not leak)
//             subs:request {force, resolve} (other domains need sub edges)
//             audio:stop-all (leaving a message view must stop audio)
//   emits:    badge:refresh, accounts:refresh, nav:activate,
//             compose:reply / compose:reply-self (reply buttons here)
// The i18n dictionary stays a classic global (window.I18N).
import { $, $$, esc, api, getSession, basicAuth, toast, fmtTime, fmtBytes, copyText } from "./core.js";

(function () {
  "use strict";

  // i18n shortcut, same semantics as the entry's copy.
  function t(key, vars) {
    return window.I18N ? window.I18N.t(key, vars) : key;
  }

  // ---- mail ----

  async function ensureAccountOptions() {
    const sel = $("#mail-account");
    if (sel.dataset.loaded === "1") return;
    const s = getSession();
    // Regular users only ever see their own mail: lock the selector to self
    // and disable it (no global account picker).
    if (s && !s.is_admin) {
      sel.innerHTML = "";
      // "All visible accounts" aggregated view + per-account picks
      // (mrf2000 feedback): the pseudo-option merges own mail with every
      // subordinate's in one list; individual addresses still selectable.
      const subs = await loadSubs().catch(function () { return null; });
      const subsList = (subs && subs.subordinates) || [];
      const add = function (addr) {
        // v0.6.33: normalize display-case — subs/contact entries inherit the
        // sender's letter-header case while accounts are stored lowercase.
        addr = String(addr || "").toLowerCase();
        const o = document.createElement("option");
        o.value = addr; o.textContent = addr;
        sel.appendChild(o);
      };
      if (subsList.length) {
        const all = document.createElement("option");
        all.value = "__vis__"; all.textContent = t("mail.allVisible");
        sel.appendChild(all);
      }
      // v0.6.34: lowercase-normalized entries can collide (pop + PoP) —
      // dedupe by the normalized form so the selector shows one option.
      const addedAddrs = {};
      const addUnique = function (addr) {
        addr = String(addr || "").toLowerCase();
        if (addedAddrs[addr]) return;
        addedAddrs[addr] = 1;
        add(addr);
      };
      addUnique(s.address);
      subsList.forEach(function (e) {
        addUnique(e.address);
      });
      // More than the own account visible: the selector switches between
      // them; single-account users get it locked to self.
      sel.disabled = sel.options.length <= 1;
      sel.dataset.loaded = "1";
      return;
    }
    try {
      const data = await api("/admin/accounts");
      sel.innerHTML = "";
      // "All accounts" pseudo-option: iterate every account on Load.
      const all = document.createElement("option");
      all.value = "__all__"; all.textContent = t("mail.allAccounts");
      sel.appendChild(all);
      (data.accounts || []).forEach(function (a) {
        const o = document.createElement("option");
        o.value = a.address; o.textContent = a.address;
        sel.appendChild(o);
      });
      if (sel.options.length <= 1) {
        const o = document.createElement("option");
        o.value = ""; o.textContent = "(no accounts)";
        sel.appendChild(o);
      }
      sel.dataset.loaded = "1";
    } catch (e) {
      toast("Load accounts failed: " + e.message, "error");
    }
  }

  $("#btn-load-mail").addEventListener("click", loadMailList);

  async function loadMailList() {
    resetAudioPlayers();
    const account = $("#mail-account").value;
    const folder = $("#mail-folder").value;
    const limit = parseInt($("#mail-limit").value, 10) || 50;
    const list = $("#mail-list");
    const detail = $("#mail-detail");
    detail.innerHTML = t("mail.selectHint");
    if (!account) { list.textContent = t("mail.noAccount"); return; }
    list.textContent = t("common.loading");
    try {
      const s = getSession();
      const isRegular = s && !s.is_admin;
      // Subordinate view (v0.5.7): a regular account browsing one of its
      // declared subordinates — summaries only (no body fetch endpoint in
      // v1), read-only, attachments as metadata.
      const isSubView = isRegular && account !== s.address;

      var msgs = [];
      // Aggregated "all visible accounts" view (mrf2000 feedback): own
      // inbox+sent merged with every subordinate's messages. Owner is
      // stamped on each message so the detail pane routes correctly.
      if (isRegular && account === "__vis__") {
        const f = folder === "all" ? "both" : folder;
        const jobs = [];
        // Own mail (regular account: own endpoints).
        jobs.push(api("/api/inbox?limit=" + limit, { keepSession: true }).then(function (d) {
          return (d.messages || []).map(function (m) { m.__owner = s.address; return m; });
        }).catch(function () { return []; }));
        jobs.push(api("/api/sent?limit=" + limit, { keepSession: true }).then(function (d) {
          return (d.messages || []).map(function (m) { m.__owner = s.address; return m; });
        }).catch(function () { return []; }));
        // Each declared subordinate (summaries).
        const subsList = (subsCache && subsCache.subordinates) || [];
        subsList.forEach(function (e) {
          jobs.push(api("/api/subs/" + encodeURIComponent(e.address) +
            "/messages?folder=" + f + "&limit=" + limit, { keepSession: true }).then(function (d) {
            return (d.messages || []).map(function (m) { m.__owner = e.address; return m; });
          }).catch(function () { return []; }));
        });
        const results = await Promise.all(jobs);
        results.forEach(function (arr) { msgs = msgs.concat(arr); });
        msgs.sort(function (a, b) { return (b.received_at || 0) - (a.received_at || 0); });
        // Dedup by id — but a letter the viewer SENT also lives in the
        // recipient's (subordinate's) inbox. The own-sent copy is always
        // read; letting it shadow the recipient's unread copy made fresh
        // mail show as read here (superior report 2026-08-27). When both
        // copies exist, keep the unread one (its __owner also routes the
        // detail pane to the right mailbox).
        var seenA = {}, dedupA = [];
        msgs.forEach(function (m) {
          if (!seenA[m.id]) { seenA[m.id] = 1; dedupA.push(m); }
          else if (m.unread) {
            for (var i = 0; i < dedupA.length; i++) {
              if (dedupA[i].id === m.id) { dedupA[i] = m; break; }
            }
          }
        });
        msgs = dedupA;
      } else if (isSubView) {
        const f = folder === "all" ? "both" : folder;
        const d = await api("/api/subs/" + encodeURIComponent(account) +
          "/messages?folder=" + f + "&limit=" + limit, { keepSession: true });
        msgs = d.messages || [];
      } else if (account === "__all__" && !isRegular) {
        // Aggregated endpoint (v0.5.4): one server-side merged scan replaces
        // the old fan-out of 2 requests per account (50+ accounts = 100+
        // concurrent bbolt scans saturating the server).
        const d = await api("/admin/messages-all?limit=" + limit +
          (folder === "all" ? "" : "&folder=" + encodeURIComponent(folder)));
        msgs = d.messages || [];
      } else {
        // Single account: one or two direct queries, merged by time.
        const folders = folder === "all" ? ["inbox", "sent"] : [folder];
        const results = await Promise.all(folders.map(function (f) {
          const path = isRegular
            ? (f === "sent" ? "/api/sent?limit=" + limit : "/api/inbox?limit=" + limit)
            : (f === "sent"
                ? "/admin/sent?account=" + encodeURIComponent(account) + "&limit=" + limit
                : "/admin/messages?account=" + encodeURIComponent(account) + "&limit=" + limit);
          return api(path).then(function (d) { return d.messages || []; })
            .catch(function () { return []; });
        }));
        results.forEach(function (arr) { msgs = msgs.concat(arr); });
        msgs.sort(function (a, b) { return (b.received_at || 0) - (a.received_at || 0); });
        // De-duplicate by id (a message can appear in both inbox and sent views).
        // Same shadowing hazard as the __vis__ merge: keep the unread copy.
        var seen = {}, dedup = [];
        msgs.forEach(function (m) {
          if (!seen[m.id]) { seen[m.id] = 1; dedup.push(m); }
          else if (m.unread) {
            for (var i = 0; i < dedup.length; i++) {
              if (dedup[i].id === m.id) { dedup[i].unread = true; break; }
            }
          }
        });
        msgs = dedup;
      }

      if (!msgs.length) { list.textContent = t("mail.noMessages"); fitMailPcOneScreen(); return; }
      list.innerHTML = "";
      // Aggregated view banner (feedback): a proper title instead of the
      // internal pseudo-account; each message still shows its owner in the
      // detail pane.
      if (account === "__vis__") {
        const note = document.createElement("div");
        note.className = "muted";
        note.style.cssText = "padding:4px 2px 10px;";
        note.textContent = t("mail.visBanner", { n: msgs.length });
        list.appendChild(note);
      }
      msgs.forEach(function (m) {
        const item = document.createElement("div");
        item.className = "mail-item" + (m.unread ? " unread" : "");
        item.innerHTML =
          (m.unread ? '<span class="unread-dot" title="unread">●</span>' : "") +
          '<div class="subj">' + esc(m.subject || "(no subject)") + "</div>" +
          '<div class="meta"><b>' + t("mail.from") + "</b> '" + esc(m.from) +
          ' · <b>to:</b> ' + esc((m.to || []).join(", ")) +
          " · <small>" + fmtTime(m.received_at) + "</small></div>" +
          '<div class="prev">' + esc(m.preview || "") + "</div>";
        item.addEventListener("click", function () {
          // "__vis__" must be tested FIRST: it also satisfies isSubView
          // (≠ self), and routing through showSubDetail("__vis__") leaked
          // the internal pseudo-account into the header and 404'd the
          // detail fetch (body fell back to the preview — reported as
          // "clipped"). Owner-stamped routing decides self vs subordinate.
          if (account === "__vis__") {
            if (m.__owner && m.__owner !== s.address) showSubDetail(m.__owner, m, item);
            else showDetail(m.id, item);
          } else if (isSubView) showSubDetail(account, m, item);
          else showDetail(m.id, item);
        });
        list.appendChild(item);
      });
      // List-first (superior feedback 2026-08-27): entering the browse view
      // lands on the LIST, not inside a message — the old auto-open of the
      // newest item hid the overview. The detail pane idles with a hint
      // until the user clicks a row.
      {
        const detail = $("#mail-detail");
        if (detail && msgs.length) {
          detail.innerHTML = '<p class="muted" style="padding:12px 4px;">' +
            esc(t("mail.pickHint")) + "</p>";
        }
      }
      mailSearchSrv = false;
      applyMailSearch();
      fitMailPcOneScreen();
    } catch (e) {
      list.textContent = t("common.error", { msg: e.message });
    }
  }

  // Messages search box (superior's request). Since the /api/search contract
  // (superior 2026-08-31): queries of >=2 chars on a CONCRETE account go
  // server-side for full-text (subject/from/to/body, box=both, result limit
  // follows the list selector); short queries and pseudo-account aggregate
  // views (__vis__/__all__) keep the instant client-side page filter, which
  // is also the fallback for backends without /api/search (404).
  let mailSearchTimer = null;
  let mailSearchSrv = false; // last list render came from the server search
  function applyMailSearch() {
    var input = $("#mail-search");
    var list = $("#mail-list");
    if (!input || !list) return;
    var q = (input.value || "").trim();
    var accEl = $("#mail-account");
    var account = accEl ? accEl.value : "";
    var concrete = account && account !== "__vis__" && account !== "__all__";
    if (q.length >= 2 && concrete) {
      if (mailSearchTimer) clearTimeout(mailSearchTimer);
      mailSearchTimer = setTimeout(function () { runMailServerSearch(q, account); }, 300);
      return;
    }
    if (mailSearchSrv && !q) { mailSearchSrv = false; loadMailList(); return; }
    if (!q) mailSearchSrv = false;
    applyMailClientFilter(q);
  }
  function applyMailClientFilter(q) {
    q = (q || "").toLowerCase();
    var list = $("#mail-list");
    var items = $$(".mail-item", list);
    var shown = 0;
    items.forEach(function (el) {
      var hit = !q || (el.textContent || "").toLowerCase().indexOf(q) !== -1;
      el.classList.toggle("hidden", !hit);
      if (hit) shown++;
    });
    var status = $("#mail-search-status");
    if (status && !mailSearchSrv) {
      status.textContent = q ? t("mail.searchCount", { n: shown, m: items.length }) : "";
    }
  }
  async function runMailServerSearch(q, account) {
    if (ftSearchUnsupported) { mailSearchSrv = false; applyMailClientFilter(q); return; }
    const list = $("#mail-list");
    const detail = $("#mail-detail");
    const status = $("#mail-search-status");
    const s = getSession();
    const isRegular = s && !s.is_admin;
    try {
      const limit = parseInt($("#mail-limit").value, 10) || 50;
      const d = await api("/api/search?q=" + encodeURIComponent(q) +
        "&box=both&account=" + encodeURIComponent(account) + "&limit=" + limit);
      mailSearchSrv = true;
      const msgs = d.messages || [];
      const total = d.total_count || msgs.length;
      list.innerHTML = "";
      detail.innerHTML = t("mail.selectHint");
      if (!msgs.length) {
        list.textContent = t("mail.noMessages");
        if (status) status.textContent = t("mail.ftHits", { n: 0 });
        return;
      }
      msgs.forEach(function (m) {
        const item = document.createElement("div");
        item.className = "mail-item" + (m.unread ? " unread" : "");
        item.innerHTML =
          (m.unread ? '<span class="unread-dot" title="unread">●</span>' : "") +
          '<div class="subj">' + esc(m.subject || "(no subject)") + "</div>" +
          '<div class="meta"><b>' + t("mail.from") + "</b> '" + esc(m.from) +
          ' · <b>to:</b> ' + esc((m.to || []).join(", ")) +
          " · <small>" + fmtTime(m.received_at) + "</small></div>" +
          '<div class="prev">' + esc(m.preview || "") + "</div>";
        item.addEventListener("click", function () {
          // Same routing as the normal list: regular + other account =
          // subordinate detail; everything else self detail.
          if (isRegular && account !== s.address) showSubDetail(account, m, item);
          else showDetail(m.id, item);
        });
        list.appendChild(item);
      });
      if (status) {
        status.textContent = total > msgs.length
          ? t("mail.ftHits", { n: total }) + " · " + t("mail.ftTrunc", { n: msgs.length })
          : t("mail.ftHits", { n: total });
      }
    } catch (e) {
      // Endpoint missing or failing (legacy 404 bodies are plain text):
      // degrade silently to the client filter so the box never appears dead.
      mailSearchSrv = false;
      ftSearchUnsupported = true;
      applyMailClientFilter(q);
    }
  }
  (function wireMailSearch() {
    var input = $("#mail-search");
    if (!input) return;
    input.addEventListener("input", applyMailSearch);
  })();

  // ---- inbox/mail mobile List|Message tabs (<=800px) ----
  // One pane visible at a time; opening a message flips to Message. The
  // buttons are hidden on desktop and the classes are inert there, so the
  // side-by-side grid is untouched.
  function mailShowPane(gridId, pane) {
    const grid = document.getElementById(gridId);
    if (!grid) return;
    grid.classList.toggle("mshow-detail", pane === "detail");
    const tabs = document.querySelector('.mail-tabs[data-grid="' + gridId.replace("-grid", "") + '"]');
    if (!tabs) return;
    $$(".mtab", tabs).forEach(function (b) {
      b.classList.toggle("on", b.dataset.pane === pane);
    });
  }
  $$(".mail-tabs .mtab").forEach(function (b) {
    b.addEventListener("click", function () {
      const tabs = b.closest(".mail-tabs");
      mailShowPane(tabs.dataset.grid + "-grid", b.dataset.pane);
    });
  });

  // revealDetailOnMobile: on narrow screens the detail pane is a tab away —
  // flip to it when a message is opened so users see it happened. No-op on
  // desktop (PC layout unaffected).
  function revealDetailOnMobile(gridId, detailEl) {
    if (window.innerWidth > 800) return;
    mailShowPane(gridId, "detail");
  }

  // Module-local (P0 fix, superior report 01M0ZJQH): this used to borrow
  // compose.js's ATTACH_IMAGE_RE closure — invisible across ES modules, so
  // any detail render with attachments threw ReferenceError and killed the
  // whole pane (attachments AND body vanished).
  const ATTACH_IMAGE_RE = /\.(png|jpe?g|gif|webp)$/i;
  function attachIsImage(a) {
    return !!(a && a.filename && ATTACH_IMAGE_RE.test(a.filename));
  }

  // Audio attachments (v0.5.12): inline <audio controls> preview, same
  // authenticated-blob + MIME-rebuild pattern as images.
  const ATTACH_AUDIO_RE = /\.(mp3|wav|ogg|m4a|webm)$/i;
  function attachIsAudio(a) {
    return !!(a && a.filename && ATTACH_AUDIO_RE.test(a.filename));
  }

  // PDF attachments (superior, point-to-point): a "Read PDF" button on the
  // card opens a dimmed fullscreen reader window (one size smaller than the
  // viewport, mirroring the image lightbox). The blob is fetched lazily on
  // the click — PDFs can be large, so nothing is prefetched with the render.
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

  // mdSanitizedHtml: shared sanitizer config — img/style/audio/video are
  // stripped everywhere markdown renders (attachments AND letter bodies).
  function mdSanitizedHtml(text) {
    return DOMPurify.sanitize(marked.parse(text), {
      FORBID_TAGS: ["img", "style", "audio", "video"],
      FORBID_ATTR: ["style"],
    });
  }

  // renderMd: markdown text -> sanitized .md-body element (marked +
  // DOMPurify). Plain-<pre> fallback when the vendor libs are missing.
  // Shared by the inline preview and the fullscreen lightbox.
  function renderMd(text) {
    if (window.marked && window.DOMPurify) {
      try {
        const box = document.createElement("div");
        box.className = "md-body";
        box.innerHTML = mdSanitizedHtml(text);
        return box;
      } catch (_) { /* fall through to raw */ }
    }
    const pre = document.createElement("pre");
    pre.className = "md-body md-body-raw";
    pre.textContent = text;
    return pre;
  }

  // letterBodyHtml (superior): the message body renders as markdown ONLY
  // when the body_markdown preference is on (default off — letters stay
  // plain text until the user opts in). Same sanitizer rules as md
  // attachments (img/style stripped). compose.js is untouched by design.
  function letterBodyHtml(text) {
    // Prefs not loaded yet (fresh session race — superior repro 09-04):
    // render raw but flag the <pre>; upgradeLetterBody swaps it once the
    // prefs fetch resolves. Known-off renders plain with no flag.
    if (mgmtPrefs == null) {
      return '<pre class="body" data-pref-pending="1">' + esc(text || "") + "</pre>";
    }
    if (mgmtPrefs.body_markdown === true && window.marked && window.DOMPurify) {
      try {
        return '<div class="md-body md-body-letter">' + mdSanitizedHtml(text || "") + "</div>";
      } catch (_) { /* fall through to raw */ }
    }
    return '<pre class="body">' + esc(text || "") + "</pre>";
  }

  // upgradeLetterBody: called right after a detail render — once the prefs
  // fetch resolves, swap a pending raw body for rendered markdown. Entering
  // the inbox directly no longer depends on the fetch winning the race.
  function upgradeLetterBody(detail, m) {
    ensureMgmtPrefs().then(function (p) {
      if (!detail.isConnected) return;
      const pre = detail.querySelector('pre.body[data-pref-pending]');
      if (!pre) return;
      pre.removeAttribute("data-pref-pending");
      if (!(p && p.body_markdown === true)) return;
      try {
        const div = document.createElement("div");
        div.className = "md-body md-body-letter";
        div.innerHTML = mdSanitizedHtml(m.body || "");
        pre.replaceWith(div);
      } catch (_) { /* keep raw on any failure */ }
    });
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
    // 上级 01M1B6J5W：预览可放大——滚轮/双击/双指捏合（1–6×，指针锚点式），
    // 放大后拖拽平移；拖拽过的收尾点按不当作关闭手势（关闭仍走背景/Esc）。
    let lbScale = 1, lbTx = 0, lbTy = 0;
    const lbApply = function () {
      im.style.transform = "translate(" + lbTx + "px," + lbTy + "px) scale(" + lbScale + ")";
      im.style.cursor = lbScale > 1 ? "grab" : "zoom-in";
    };
    const lbZoomAt = function (cx, cy, factor) {
      const ns = Math.min(6, Math.max(1, lbScale * factor));
      if (ns === lbScale) return;
      const r = im.getBoundingClientRect();
      const lbpX = cx - r.left, lbpY = cy - r.top;
      lbTx += lbpX * (1 - ns / lbScale);
      lbTy += lbpY * (1 - ns / lbScale);
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
    let lbLastTap = 0;
    lb.addEventListener("touchstart", function (ev) {
      if (ev.touches.length === 2) {
        lbPinch = {
          d: Math.hypot(ev.touches[0].clientX - ev.touches[1].clientX,
                        ev.touches[0].clientY - ev.touches[1].clientY),
        };
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
    lb.addEventListener("touchend", function (ev) {
      if (lbPinch && ev.touches.length < 2) lbPinch = null;
      if (lbScale > 1 && lbScale < 1.02) { lbScale = 1; lbTx = 0; lbTy = 0; lbApply(); }
      // 双击（双点）切换放大/还原——自带计时，不依赖合成 dblclick
      if (!lbDrag || !lbDrag.moved) {
        const now = Date.now();
        const t0 = ev.changedTouches[0];
        if (t0 && now - lbLastTap < 350) {
          lbLastTap = 0;
          lbZoomAt(t0.clientX, t0.clientY, lbScale > 1 ? 1 / lbScale : 2.5);
        } else {
          lbLastTap = now;
        }
      }
      if (lbDrag) lbDrag = null;
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

  // openPdfLightbox (superior, point-to-point): dimmed fullscreen overlay
  // with a reader window one size smaller than the viewport (same sizing
  // tier as the image lightbox's 92vw/88vh). The PDF renders in an <iframe>
  // fed the authenticated blob URL — the download endpoint's octet-stream
  // MIME is rebuilt to application/pdf or browsers refuse to render it.
  // Closes via ×, backdrop, or Esc; the blob URL dies with the overlay.
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
    if (!(mgmtPrefs && mgmtPrefs.audio_autoplay === true)) return;
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
      if (!(mgmtPrefs && mgmtPrefs.audio_autoplay === true)) return;
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
    // 全屏展开: fetch the md text and open it in the lightbox.
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
      if (attachIsImage(a) && mgmtPrefs && mgmtPrefs.image_preview === false) { holder.remove(); return; }
      try {
        const res = await fetch("/api/files/" + encodeURIComponent(a.id) + "/download?code=" + encodeURIComponent(a.access_code), {
          headers: { Authorization: basicAuth() },
        });
        if (!res.ok) throw new Error(res.status);
        // Markdown branch (superior): render inline as sanitized HTML —
        // images/styles/audio/video are stripped by the sanitizer, so an
        // .md attachment cannot pull remote assets or inject markup.
        if (attachIsMd(a)) {
          // Inline preview everywhere here (the mobile skip is compose's
          // thread-capsule-only — see compose.js); 25vh cap keeps it small.
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
  function mailStepNav(item, dir) {
    const items = $$("#mail-list .mail-item");
    const idx = items.indexOf(item);
    const target = items[idx + dir];
    if (target) target.click();
    else toast(t("inbox.noMore"), "error");
  }

  function wireMailNav(detail, item) {
    const p = $('[data-nav="-1"]', detail), n = $('[data-nav="1"]', detail);
    if (p) p.addEventListener("click", function () { mailStepNav(item, -1); });
    if (n) n.addEventListener("click", function () { mailStepNav(item, 1); });
  }

  // Thread rendering (v0.6.16 ①): "in reply to ‹parent id›" row in the
  // field zone; click reopens the parent (same loader, visibility rules
  // identical). Superior ruling (v0.6.25): the row is a header field —
  // same visual treatment as From/To/Subject — placed BEFORE the
  // reply/forward action row, so the header zone reads:
  //   From / To / Subject / Date / ↩ in-reply-to / [Reply] [Forward]
  //   <hr>  body  attachments
  function wireReplyRef(detail, msg, reopen) {
    if (!msg || !msg.in_reply_to) return;
    var row = document.createElement("div");
    row.className = "detail-row reply-ref";
    row.innerHTML = "<b>↩ " + esc(t("act.repliedFrom")) + ":</b> ";
    var link = document.createElement("a");
    link.href = "javascript:void(0)";
    link.textContent = "‹" + msg.in_reply_to + "›";
    row.appendChild(link);
    // Insert before the .row action container that holds the reply/forward
    // buttons (identified by containing btn-inbox-reply or btn-mail-reply).
    var btnRow = detail.querySelector("#btn-inbox-reply, #btn-mail-reply");
    if (btnRow) {
      var actionBox = btnRow.closest(".row");
      if (actionBox && actionBox.parentNode) actionBox.parentNode.insertBefore(row, actionBox);
      else btnRow.parentNode.insertBefore(row, btnRow);
    } else {
      var hr = detail.querySelector("hr");
      if (hr && hr.parentNode) hr.parentNode.insertBefore(row, hr);
    }
    link.addEventListener("click", function () { reopen(msg.in_reply_to); });
  }

  async function showDetail(id, item) {
    resetAudioPlayers();
    $$(".mail-item", $("#mail-list")).forEach(function (el) { el.classList.remove("selected"); });
    if (item) item.classList.add("selected");
    const detail = $("#mail-detail");
    detail.innerHTML = inboxDetailFrame('<div class="inbox-loading">' + t("common.loading") + "</div>");
    wireMailNav(detail, item);
    revealDetailOnMobile("mail-grid", detail);
    try {
      // Regular accounts read their own mail via /api/message (the admin
      // endpoint would 401 and reset the session — live bug in the regular
      // Mail-tab self-view since v0.5.7).
      const viewer = getSession();
      const detailPath = (viewer && !viewer.is_admin)
        ? "/api/message?id=" + encodeURIComponent(id)
        : "/admin/message?id=" + encodeURIComponent(id);
      const m = await api(detailPath);
      // Drop the unread mark only when this fetch actually cleared it on the
      // server: /api/message marks read for the viewer, /admin/message is
      // read-only — viewing a letter you are not a recipient of must keep
      // its unread mark (superior 01M1DNS3W).
      if (item && viewer && !viewer.is_admin) {
        item.classList.remove("unread");
        const dot = $(".unread-dot", item);
        if (dot) dot.remove();
      }
      detail.innerHTML = inboxDetailFrame(
        '<div class="detail-row"><b>From:</b> ' + esc(m.from) + "</div>" +
        '<div class="detail-row"><b>To:</b> ' + esc((m.to || []).join(", ")) + "</div>" +
        (m.cc && m.cc.length ? '<div class="detail-row"><b>Cc:</b> ' + esc(m.cc.join(", ")) + "</div>" : "") +
        '<div class="detail-row"><b>Subject:</b> ' + esc(m.subject || "") + "</div>" +
        '<div class="detail-row"><b>Date:</b> ' + fmtTime(m.received_at) + "</div>" +
        '<div class="detail-row"><b>ID:</b> <code>' + esc(m.id || m.message_id) + "</code></div>" +
        '<div class="row" style="margin:8px 0;">' +
        '<button class="row-action" id="btn-mail-reply" data-reply-to="' + esc(m.from) + '" data-reply-subject="' + esc(m.subject || "") + '" data-reply-id="' + esc(m.id || m.message_id || "") + '">' + t("act.reply") + "</button>" +
        '<button class="row-action" id="btn-mail-forward" style="margin-left:8px;">' + t("act.forward") + "</button></div>" +
        "<hr>" + letterBodyHtml(m.body) + attachmentCards(m));
      upgradeLetterBody(detail, m);
      wireMailNav(detail, item);
      wireReplyRef(detail, m, function (pid) { showDetail(pid, item); });
      wireAttachmentDownloads(detail, m);
      hydrateAttachmentPreviews(detail, m);
      const replyBtn = $("#btn-mail-reply");
      if (replyBtn) replyBtn.addEventListener("click", function () {
        document.dispatchEvent(new CustomEvent("compose:reply", { detail: { to: replyBtn.dataset.replyTo, subject: replyBtn.dataset.replySubject, parentId: replyBtn.dataset.replyId } }));
      });
      const fwdBtn = $("#btn-mail-forward");
      if (fwdBtn) fwdBtn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:forward", { detail: { m: m } })); });
    } catch (e) {
      detail.innerHTML = inboxDetailFrame('<p class="muted">' + esc(t("common.error", { msg: e.message })) + "</p>");
      wireMailNav(detail, item);
    }
  }

  // ---- subordinates (v0.5.7) ----
  // Self-declared directed edges: A declares itself a subordinate of B, so B
  // can browse A's mail (read-only, attachments metadata only). This module
  // backs both the Accounts-tab relationship manager and the Mail-tab
  // optgroup + read-only detail view.
  // GET /api/subs → {subordinates: [edges under me], superiors: [edges I declared]}

  let subsCache = null; // {subordinates: [], superiors: []} or null

  async function loadSubs(force) {
    if (subsCache && !force) return subsCache;
    const d = await api("/api/subs", { keepSession: true });
    subsCache = { subordinates: d.subordinates || [], superiors: d.superiors || [] };
    renderSubsUI();
    return subsCache;
  }

  // renderSubsUI fills the Accounts-tab "Subordinate relationships" block:
  // my superiors (dissolvable from either side, v0.6.5) below the declare
  // input.
  function renderSubsUI() {
    const section = $("#subs-section");
    if (!section || !subsCache) return;
    const mine = $("#subs-mine");
    if (!subsCache.superiors.length) {
      mine.innerHTML = '<p class="muted">' + t("subs.noneMine") + "</p>";
    } else {
      mine.innerHTML = "<h4>" + t("subs.mineTitle") + "</h4>" + subsCache.superiors.map(function (e) {
        return '<div class="row" style="justify-content:space-between;">' +
          "<span>" + esc(e.address) + ' <small class="muted">' + t("subs.since") + " " + fmtTime(e.created_at) + "</small></span>" +
          '<button class="row-action" data-unsub="' + esc(e.address) + '">' + t("subs.removeBtn") + "</button>" +
          "</div>";
      }).join("");
      $$("[data-unsub]", mine).forEach(function (btn) {
        btn.addEventListener("click", function () { removeSub(btn.dataset.unsub, "subordinate"); });
      });
    }
  }

  async function declareSub(address) {
    const status = $("#subs-status");
    status.textContent = t("common.loading");
    try {
      await api("/api/subs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ superior: address, scope: "both" }),
      });
      status.textContent = t("subs.declared");
      $("#subs-declare-input").value = "";
      await loadSubs(true);
      document.dispatchEvent(new CustomEvent("accounts:refresh")); // refresh badges
      invalidateMailAccountOptions();
    } catch (e) {
      status.textContent = "";
      toast(e.message, "error"); // 429/404 surface the server text verbatim
    }
  }

  // removeSub dissolves a subordination edge from either side (v0.6.5):
  // role picks the confirm wording — "superior" (removing one of my
  // subordinates) or "subordinate" (leaving my superior). One endpoint,
  // POST /api/subs/remove, deletes the single record; the server notifies
  // the other side. Views (accounts table, subs list, mail selector, mgmt
  // overview/graph) are all scan-derived — reload them and they are clean.
  async function removeSub(address, role) {
    var key = role === "subordinate" ? "subs.removeConfirmSub" : "subs.removeConfirmSuperior";
    if (!confirm(t(key, { addr: address }))) return;
    try {
      await api("/api/subs/remove", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ address: address }),
      });
      toast(t("subs.removed"));
      await loadSubs(true);
      document.dispatchEvent(new CustomEvent("accounts:refresh"));
      invalidateMailAccountOptions();
      document.dispatchEvent(new CustomEvent("overview:refresh"));
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // invalidateMailAccountOptions forces the Mail-tab selector rebuild on the
  // next visit (edges changed).
  function invalidateMailAccountOptions() {
    const sel = $("#mail-account");
    if (sel) delete sel.dataset.loaded;
  }

  $("#btn-subs-declare").addEventListener("click", function () {
    const v = ($("#subs-declare-input").value || "").trim();
    if (!v) return;
    declareSub(v);
  });
  $("#subs-declare-input").addEventListener("keydown", function (ev) {
    if (ev.key === "Enter") $("#btn-subs-declare").click();
  });

  // showSubDetail renders the read-only detail pane for a subordinate's
  // message. Fetches the full body via GET /api/subs/{A}/message?id= (v0.5.7.1
  // server); on failure falls back to the summary (preview). Attachments stay
  // metadata-only either way (Q2: no download).
  async function showSubDetail(subAddr, m, item) {
    resetAudioPlayers();
    $$(".mail-item", $("#mail-list")).forEach(function (el) { el.classList.remove("selected"); });
    if (item) item.classList.add("selected");
    const detail = $("#mail-detail");
    detail.innerHTML = inboxDetailFrame('<div class="inbox-loading">' + t("common.loading") + "</div>");
    wireMailNav(detail, item);
    revealDetailOnMobile("mail-grid", detail);
    let full = null;
    try {
      const d = await api("/api/subs/" + encodeURIComponent(subAddr) +
        "/message?id=" + encodeURIComponent(m.id), { keepSession: true });
      full = d.message || null;
    } catch (_) { /* fall back to summary-level rendering */ }
    const msg = full || m; // full has body/cc/attachments; summary has preview/files
    // Received mail (sender ≠ the subordinate): offer "reply as myself".
    // Mail the subordinate sent TO the viewer: offer plain "Reply" with the
    // same semantics as the Inbox reply (To + Re: subject, no quote).
    const canReply = msg.from && msg.from !== subAddr;
    const viewer = getSession();
    const myAddr = ((viewer && viewer.address) || "").toLowerCase();
    // Drop the unread mark only on a self-read: the uniform path clears the
    // caller's own unread server-side (me==A routes to GetMessage), while a
    // read of someone else's letter deliberately never touches their read
    // state — keep their mark then (superior 01M1DNS3W).
    if (item && myAddr && myAddr === String(subAddr).toLowerCase()) {
      item.classList.remove("unread");
      const dot = $(".unread-dot", item);
      if (dot) dot.remove();
    }
    const sentToMe = myAddr && String(msg.from || "").toLowerCase() === String(subAddr).toLowerCase() &&
      (msg.to || []).some(function (a) { return String(a).toLowerCase() === myAddr; });
    const atts = (msg.attachments || []);
    detail.innerHTML = inboxDetailFrame(
      '<div class="detail-row"><span class="badge-sub">' + t("subs.badge") + "</span> " +
      esc(subAddr) + ' · <i class="muted">' + t("subs.readonly") + "</i></div>" +
      '<div class="detail-row"><b>From:</b> ' + esc(msg.from) + "</div>" +
      '<div class="detail-row"><b>To:</b> ' + esc((msg.to || []).join(", ")) + "</div>" +
      (msg.cc && msg.cc.length ? '<div class="detail-row"><b>Cc:</b> ' + esc(msg.cc.join(", ")) + "</div>" : "") +
      '<div class="detail-row"><b>Subject:</b> ' + esc(msg.subject || "") + "</div>" +
      '<div class="detail-row"><b>Date:</b> ' + fmtTime(msg.received_at) + "</div>" +
      '<div class="detail-row"><b>ID:</b> <code>' + esc(msg.id || msg.message_id) + "</code></div>" +
      (atts.length
        ? '<div class="attach-list">' + atts.map(function (a) {
            return '<div class="attach-card attach-card-file">' +
              '<span class="attach-clip">📎</span>' +
              '<span class="attach-name">' + esc(a.filename) + "</span>" +
              '<span class="attach-size">' + esc(fmtBytes(a.size)) + "</span>" +
              '<span class="muted">' + t("subs.attachNoDl") + "</span></div>";
          }).join("") + "</div>"
        : (m.files ? '<div class="detail-row">📎 ' + m.files + t("subs.attachMeta") + "</div>" : "")) +
      "<hr>" + letterBodyHtml(msg.body != null ? msg.body : (msg.preview || "")) +
      (canReply || sentToMe
        ? '<div class="row" style="margin-top:12px;">' +
          (canReply ? '<button class="primary" id="btn-reply-as-self">' + t("subs.replyAsSelf") + "</button>" : "") +
          (sentToMe ? '<button class="primary" id="btn-reply-to-sub"' + (canReply ? ' style="margin-left:8px;"' : "") + '>' + t("act.reply") + "</button>" : "") +
          "</div>"
        : ""));
    upgradeLetterBody(detail, msg);
    wireMailNav(detail, item);
    wireReplyRef(detail, msg, function (pid) { showSubDetail(subAddr, { id: pid }, item); });
    const rbtn = $("#btn-reply-as-self");
    if (rbtn) rbtn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:reply-self", { detail: { m: msg } })); });
    const rbtn2 = $("#btn-reply-to-sub");
    if (rbtn2) rbtn2.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:reply", { detail: { to: subAddr, subject: msg.subject || "", parentId: (msg.id || msg.message_id) } })); });
  }

  // composeReplyAsSelf: the superior replies in their own name to the
  // subordinate's correspondent, quoting the original (full body when the
  // detail endpoint answered; preview text as fallback).
  function closeSubregModal() {
    $("#subreg-modal").classList.add("hidden");
  }
  function closeSubregAsk() {
    $("#subreg-ask-modal").classList.add("hidden");
  }
  (function wireSubreg() {
    const tbody = document.querySelector("#accounts-table tbody");
    if (!tbody) return;
    // Shared register-subordinate routine: the mobile in-container button
    // (#btn-subreg, via tbody delegation) and the PC above-table button
    // (#btn-subreg-pc, outside the table so it needs its own listener)
    // both run this.
    const NAME_RE = /^[A-Za-z0-9_-]+$/;
    function subregAskState() {
      var named = $("#subreg-ask-modal input[value='named']").checked;
      var name = ($("#subreg-name").value || "").trim();
      return { named: named, name: name, ok: !named || (NAME_RE.test(name) && name.length <= 32) };
    }
    function updateSubregAsk() {
      var st = subregAskState();
      var errEl = $("#subreg-ask-err");
      var prev = $("#subreg-addr-prev");
      // Preview domain = the owner's own address domain (that's what the
      // backend mints subordinates under). location.hostname is only a
      // fallback — on self-hosted setups host ≠ mail domain.
      var sess = getSession();
      var myAddr = (sess && sess.address) || "";
      var domain = myAddr.split("@")[1] || window.location.hostname || "agentmail.local";
      if (!st.named) {
        prev.textContent = "";
        errEl.hidden = true;
        return;
      }
      prev.textContent = t("subs.regAddrWill", { addr: (st.name || "…") + "@" + domain });
      if (!st.name) { errEl.hidden = true; return; }
      if (!st.ok) { errEl.textContent = t("subs.regBad"); errEl.hidden = false; }
      else { errEl.hidden = true; }
    }
    // Typing in the name field implies "Specify a name" — auto-select
    // that radio so the typed name is actually used (fedfh drill: typing
    // without clicking the radio silently minted a random name).
    $("#subreg-name").addEventListener("focus", function () {
      $("#subreg-ask-modal input[value='named']").checked = true;
      updateSubregAsk();
    });
    $("#subreg-name").addEventListener("input", function () {
      var radio = $("#subreg-ask-modal input[value='named']");
      if (!radio.checked) radio.checked = true;
      updateSubregAsk();
    });
    async function doSubreg(btn) {
      var ask = $("#subreg-ask-modal");
      var status = $("#subs-status");
      // Hotfix v0.1.12.1 (Devi obs): pre-login the register call can only
      // 401 — never walk the user into the ask modal (fake-success path).
      if (!getSession()) { status.textContent = t("common.error", { msg: "401 Unauthorized" }); return; }
      if (!ask) {
        // Fallback (ask modal missing): legacy confirm + random.
        if (!confirm(t("subs.regConfirm"))) return;
        await subregPost(null, btn, status);
        return;
      }
      ask.classList.remove("hidden");
      $("#subreg-name").value = "";
      $("#subreg-ask-modal input[value='random']").checked = true;
      updateSubregAsk();
      $("#btn-subreg-ask-ok").focus();
    }
    async function subregPost(username, btn, status) {
      btn.disabled = true;
      status.textContent = t("common.loading");
      var errEl = $("#subreg-ask-err");
      try {
        const res = await api("/api/register-subordinate", {
          method: "POST",
          body: JSON.stringify(username ? { username: username } : {}),
        });
        $("#subreg-address").textContent = res.address || "";
        $("#subreg-password").textContent = res.password || "";
        $("#subreg-copy-status").textContent = "";
        // Prompt copy lives in app.js (buildAgentPrompt) — hand the fresh
        // credentials over before the modal is shown (superior 09-02).
        document.dispatchEvent(new CustomEvent("subreg:success", { detail: { address: res.address || "", password: res.password || "" } }));
        $("#subreg-modal").classList.remove("hidden");
        $("#btn-subreg-close").focus();
        status.textContent = t("subs.regDone");
        closeSubregAsk();
      } catch (e) {
        // A taken name stays in the ask modal with an inline error; other
        // errors surface in the ask modal too (the ask replaces confirm()).
        var msg = String(e.message || "");
        if (errEl && /409|taken|exists|occupied|conflict/i.test(msg)) {
          errEl.textContent = t("subs.regTaken");
          errEl.hidden = false;
          status.textContent = "";
        } else if (errEl) {
          errEl.textContent = msg;
          errEl.hidden = false;
          status.textContent = "";
        } else {
          status.textContent = t("common.error", { msg: msg });
        }
      }
      btn.disabled = false;
    }
    // Ask modal wiring.
    var askModal = $("#subreg-ask-modal");
    if (askModal) {
      $("#btn-subreg-ask-close").addEventListener("click", closeSubregAsk);
      $("#btn-subreg-ask-cancel").addEventListener("click", closeSubregAsk);
      askModal.addEventListener("click", function (e) { if (e.target === this) closeSubregAsk(); });
      $$("#subreg-ask-modal input[name='subreg-mode']").forEach(function (r) {
        r.addEventListener("change", updateSubregAsk);
      });
      $("#subreg-name").addEventListener("input", updateSubregAsk);
      $("#btn-subreg-ask-ok").addEventListener("click", function () {
        var st = subregAskState();
        if (!st.ok) { updateSubregAsk(); return; }
        subregPost(st.named ? st.name : null, this, $("#subs-status"));
      });
    }
    tbody.addEventListener("click", function (ev) {
      const btn = ev.target.closest("#btn-subreg");
      if (btn) doSubreg(btn);
    });
    const pcBtn = $("#btn-subreg-pc");
    if (pcBtn) pcBtn.addEventListener("click", function () { doSubreg(pcBtn); });
    // The accounts-page admin button (#btn-register) is the same subordinate
    // flow (superior 09-02: accounts-page registration is subordinate-only);
    // it reaches this modal via the S2 event dispatched by app.js.
    document.addEventListener("subs:register", function () {
      doSubreg($("#btn-register") || pcBtn);
    });
    $("#btn-subreg-close").addEventListener("click", function () {
      closeSubregModal();
      // Refresh edges + badges + Mail selector after dismissing.
      loadSubs(true).then(function () {
        document.dispatchEvent(new CustomEvent("accounts:refresh"));
        invalidateMailAccountOptions();
      }).catch(function () {});
    });
    $("#subreg-modal").addEventListener("click", function (e) {
      if (e.target === this) $("#btn-subreg-close").click();
    });
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      const a = $("#subreg-ask-modal");
      if (a && !a.classList.contains("hidden")) { closeSubregAsk(); return; }
      const m = $("#subreg-modal");
      if (m && !m.classList.contains("hidden")) $("#btn-subreg-close").click();
    });
    $("#btn-subreg-copy").addEventListener("click", function () {
      const text = $("#subreg-address").textContent + "\n" + $("#subreg-password").textContent;
      copyText(text).then(function (ok) {
        const st = $("#subreg-copy-status");
        st.textContent = ok ? t("common.copied") : t("common.copyManual");
        setTimeout(function () { st.textContent = ""; }, 2000);
      });
    });
    // Copy-prompt twin (superior 09-02): the prompt text is filled by
    // app.js on subreg:success — this only ships it to the clipboard.
    $("#btn-subreg-copy-prompt").addEventListener("click", function () {
      copyText($("#subreg-prompt").textContent).then(function (ok) {
        const st = $("#subreg-copy-status");
        st.textContent = ok ? t("common.copied") : t("common.copyManual");
        setTimeout(function () { st.textContent = ""; }, 2000);
      });
    });
  })();

  // ---- inbox (personal, all users) ----

  // loadInbox fills the left pane with the caller's own inbox. Regular accounts
  // read /api/inbox; admins read their own inbox via /admin/messages (self).
  // Inbox paging state. The personal Inbox tab reads the caller's own inbox
  // via /api/inbox (admins satisfy account auth too), which supports offset.
  const INBOX_PAGE_SIZE = 20;
  let inboxPage = 0;
  // v0.2.1 incremental pull: anchor tracks the newest inbox item loaded into the
  // list; only advanced after a successful merge (no-miss iron rule).
  var inboxAnchorId = "";
  var lastSilentFull = 0;
  var INBOX_SILENT_FALLBACK_MS = 20 * 60 * 1000;
  // Superior 01M18D521: inbox card becomes the mail hub — mode pill
  // (receive / sent / both) + client-side search over the loaded page.
  let inboxMode = "in"; // in | sent | both
  // Full-text search state (superior 2026-08-31, /api/search contract):
  // >=2 chars switches the card from client-side page filtering to
  // server-side full-text search; box follows the current mode pill.
  let inboxSearchQ = "";
  let inboxSearchTimer = null;
  // /api/search absent or failing → session-wide fallback to the legacy
  // client-side page filter (404 bodies are plain text: not detectable
  // by status string, so any failure degrades silently once).
  let ftSearchUnsupported = false;

  async function loadInbox(page) {
    resetAudioPlayers();
    if (typeof page === "number") inboxPage = page;
    if (inboxPage < 0) inboxPage = 0;
    const offset = inboxPage * INBOX_PAGE_SIZE;
    const list = $("#inbox-list");
    const detail = $("#inbox-detail");
    const status = $("#inbox-status");
    detail.innerHTML = t("mail.selectHint");
    status.textContent = t("common.loading");
    list.textContent = "";
    // Both admins and regular accounts read their own inbox via /api/inbox
    // (the admin credential satisfies account auth). showInboxDetail uses the
    // same account path to fetch a message body.
    try {
      // Mode-aware source: in → /api/inbox, sent → /api/sent, both →
      // merge the two pages newest-first.
      const data = await api("/api/inbox?limit=" + INBOX_PAGE_SIZE + "&offset=" + offset);
      let msgs = data.messages || [];
      // v0.2.1: update the incremental anchor to the newest inbox item id
      if (msgs.length && msgs[0].id) inboxAnchorId = msgs[0].id;
      let sentTotal = 0;
      if (inboxMode !== "in") {
        try {
          // named sentRes (not `sent`): app.js's overview also has a `sent`
          // local, and the free-identifier audit flags cross-module
          // same-name collisions.
          const sentRes = await api("/api/sent?limit=" + INBOX_PAGE_SIZE + "&offset=" + offset);
          sentTotal = sentRes.total_count || (sentRes.messages || []).length;
          if (inboxMode === "both" && sentRes.messages && sentRes.messages.length) {
            msgs = msgs.concat(sentRes.messages).sort(function (x, y) {
              return (y.received_at || 0) - (x.received_at || 0);
            }).slice(0, INBOX_PAGE_SIZE);
          } else if (inboxMode === "sent") {
            msgs = sentRes.messages || [];
          }
        } catch (_) { /* sent endpoint unavailable: stay with inbox */ }
      }
      const unreadCount = data.unread_count || 0;
      // 模式感知总数（上级 09-04 bug 报告）：发件模式此前错加收件总数，
       // 「发件」与「全部」两个视图的「共 X 封」显示成同一个数——
       // 发件只取发件总数，收件只取收件总数，全部才相加。
      const total = inboxMode === "sent"
        ? sentTotal
        : (data.total_count || 0) + sentTotal;
      const totalPages = Math.max(1, Math.ceil(total / INBOX_PAGE_SIZE));
      if (!msgs.length && inboxPage === 0) {
        list.textContent = t("mail.noMessages");
        updateInboxPager(1, 1);
        status.textContent = unreadCount ? (unreadCount + " unread") : "";
        return;
      }
      if (!msgs.length) {
        // Past the end (e.g. mail was deleted): clamp to last page.
        const last = totalPages - 1;
        if (inboxPage > last) { inboxPage = last; loadInbox(inboxPage); return; }
        list.textContent = t("mail.noMore");
        updateInboxPager(totalPages, inboxPage + 1);
        status.textContent = "";
        return;
      }
      list.innerHTML = "";
      msgs.forEach(function (m) {
        const item = document.createElement("div");
        item.className = "mail-item" + (m.unread ? " unread" : "");
        item.innerHTML =
          (m.unread ? '<span class="unread-dot" title="unread">●</span>' : "") +
          '<div class="subj">' + esc(m.subject || "(no subject)") + "</div>" +
          '<div class="meta"><b>' + t("mail.from") + "</b> '" + esc(m.from) +
          " · <small>" + fmtTime(m.received_at) + "</small></div>" +
          (inboxMode !== "in" && m.to && m.to.length ? '<div class="meta"><b>To:</b> ' + esc(m.to.join(", ")) + "</div>" : "") +
          '<div class="prev">' + esc(m.preview || "") + "</div>";
        item.addEventListener("click", function () { showInboxDetail(m.id, item, false); });
        list.appendChild(item);
      });
      updateInboxPager(totalPages, inboxPage + 1);
      fitInboxOneScreen();
      status.textContent = t("inbox.pageStat", { n: msgs.length, m: total }) +
        (unreadCount ? " · " + t("inbox.unreadCnt", { u: unreadCount }) : "");
      // Direct write with sequencing: supersede any in-flight poll so a
      // stale "unread" response cannot re-light the dot after this point.
      document.dispatchEvent(new CustomEvent("badge:refresh"));
      // Avoid the empty detail pane (feedback): preload the newest message.
      // Desktop shows it right away; mobile stays on the List tab (the
      // detail is preloaded behind it and opens on tap as usual).
      {
        const first = list.querySelector(".mail-item");
        const newest = msgs[0];
        if (first && newest) {
          showInboxDetail(newest.id, first, true).then(function () {
            // The preload read the newest message server-side — pull the
            // badge right away instead of waiting for the 5s tick
            // (admin: opening the inbox should clear the dot immediately).
            document.dispatchEvent(new CustomEvent("badge:refresh"));
          });
        }
      }
    } catch (e) {
      list.textContent = t("common.error", { msg: e.message });
      status.textContent = "";
    }
  }

  // ---- mark all read (approved): server endpoint first, loop fallback ----
  $("#btn-mark-all").addEventListener("click", async function () {
    if (!confirm(t("inbox.markAllConfirm"))) return;
    const status = $("#inbox-status");
    const btn = $("#btn-mark-all");
    btn.disabled = true;
    try {
      try {
        await api("/api/inbox/mark-all-read", { method: "POST" });
      } catch (e) {
        // Endpoint absent (older server): page through the inbox and read
        // each unread message via /api/message — slower but same effect.
        if (String(e.message).indexOf("404") === -1) throw e;
        let offset = 0, marked = 0;
        for (;;) {
          const d = await api("/api/inbox?limit=50&offset=" + offset);
          const unread = (d.messages || []).filter(function (m) { return m.unread; });
          for (const m of unread) {
            await api("/api/message?id=" + encodeURIComponent(m.id));
            marked++;
            status.textContent = t("inbox.markAllProgress", { n: marked });
          }
          offset += 50;
          if (offset >= (d.total_count || 0)) break;
        }
      }
      status.textContent = t("inbox.markAllDone");
      toast(t("inbox.markAllDone"));
      $("#inbox-search").value = "";
      inboxSearchQ = "";
      await loadInbox(0);
      document.dispatchEvent(new CustomEvent("badge:refresh"));
    } catch (e) {
      status.textContent = t("common.error", { msg: e.message });
    }
    btn.disabled = false;
  });

  // updateInboxPager enables/disables prev/next and sets the page input +
  // "of N" label. totalPages is computed from total_count; currentPage is 1-based.
  function updateInboxPager(totalPages, currentPage) {
    if (totalPages < 1) totalPages = 1;
    if (currentPage < 1) currentPage = 1;
    if (currentPage > totalPages) currentPage = totalPages;
    const prev = $("#btn-inbox-prev");
    const next = $("#btn-inbox-next");
    const input = $("#inbox-page-input");
    const totalLabel = $("#inbox-page-total");
    prev.disabled = currentPage <= 1;
    next.disabled = currentPage >= totalPages;
    input.max = String(totalPages);
    input.value = String(currentPage);
    totalLabel.textContent = t("pager.of", { n: totalPages });
  }

  // Server-side full-text search (contract v1: GET /api/search, response
  // {messages,total_count,account,box}; box maps the mode pill; 404 =
  // legacy backend → fall back to the client-side page filter). Rendering
  // mirrors loadInbox's item shape; newest-first comes from the server, and
  // the newest-item preload is skipped (searches must not auto-read bodies).
  async function loadInboxSearch(page) {
    if (ftSearchUnsupported) {
      inboxSearchQ = "";
      loadInbox(inboxPage).then(applyInboxClientFilter);
      return;
    }
    resetAudioPlayers();
    if (typeof page === "number") inboxPage = page;
    if (inboxPage < 0) inboxPage = 0;
    const list = $("#inbox-list");
    const detail = $("#inbox-detail");
    const status = $("#inbox-status");
    detail.innerHTML = t("mail.selectHint");
    status.textContent = t("common.loading");
    list.textContent = "";
    const box = inboxMode === "in" ? "in" : inboxMode === "sent" ? "out" : "both";
    try {
      const d = await api("/api/search?q=" + encodeURIComponent(inboxSearchQ) +
        "&box=" + box + "&limit=" + INBOX_PAGE_SIZE + "&offset=" + (inboxPage * INBOX_PAGE_SIZE));
      const msgs = d.messages || [];
      const total = d.total_count || msgs.length;
      const totalPages = Math.max(1, Math.ceil(total / INBOX_PAGE_SIZE));
      list.innerHTML = "";
      if (!msgs.length) {
        list.textContent = t("mail.noMessages");
        updateInboxPager(1, 1);
        status.textContent = t("mail.ftHits", { n: 0 });
        return;
      }
      msgs.forEach(function (m) {
        const item = document.createElement("div");
        item.className = "mail-item" + (m.unread ? " unread" : "");
        item.innerHTML =
          (m.unread ? '<span class="unread-dot" title="unread">●</span>' : "") +
          '<div class="subj">' + esc(m.subject || "(no subject)") + "</div>" +
          '<div class="meta"><b>' + t("mail.from") + "</b> '" + esc(m.from) +
          " · <small>" + fmtTime(m.received_at) + "</small></div>" +
          (box !== "in" && m.to && m.to.length ? '<div class="meta"><b>To:</b> ' + esc(m.to.join(", ")) + "</div>" : "") +
          '<div class="prev">' + esc(m.preview || "") + "</div>";
        item.addEventListener("click", function () { showInboxDetail(m.id, item, false); });
        list.appendChild(item);
      });
      updateInboxPager(totalPages, inboxPage + 1);
      fitInboxOneScreen();
      status.textContent = t("mail.ftHits", { n: total });
    } catch (e) {
      // Endpoint missing or failing (legacy backends answer 404 with a
      // plain-text body — not detectable by message string): degrade
      // silently to the client-side page filter for the whole session.
      ftSearchUnsupported = true;
      inboxSearchQ = "";
      loadInbox(inboxPage).then(applyInboxClientFilter);
    }
  }
  // Client-side filter over the rendered page (pre-search behavior, kept as
  // the fallback and for sub-2-char queries).
  function applyInboxClientFilter() {
    const q = ($("#inbox-search").value || "").trim().toLowerCase();
    $$(".mail-item", $("#inbox-list")).forEach(function (el) {
      el.style.display = !q || el.textContent.toLowerCase().indexOf(q) >= 0 ? "" : "none";
    });
  }
  // Dispatcher: search mode routes to the server loader, else the normal one.
  function refreshInbox(page) {
    if (inboxSearchQ) return loadInboxSearch(page);
    return loadInbox(page);
  }
  $("#btn-load-inbox").addEventListener("click", function () { refreshInbox(0); });
  $("#btn-inbox-prev").addEventListener("click", function () { if (inboxPage > 0) refreshInbox(inboxPage - 1); });
  $("#btn-inbox-next").addEventListener("click", function () { refreshInbox(inboxPage + 1); });
  // Jump-to-page: on Enter or blur, clamp and load the typed page (1-based).
  // Superior 01M18D521: search filter (like the Mail tab) — client-side
  // filter over the rendered page items (subject / address / preview).
  $("#inbox-search").addEventListener("input", function () {
    const raw = (this.value || "").trim();
    if (inboxSearchTimer) clearTimeout(inboxSearchTimer);
    if (raw.length < 2) {
      const had = inboxSearchQ;
      inboxSearchQ = "";
      if (had) loadInbox(0);          // leaving search: restore the normal list
      else applyInboxClientFilter();  // short query: instant client filter
      return;
    }
    inboxSearchTimer = setTimeout(function () {
      inboxSearchQ = raw.toLowerCase();
      loadInboxSearch(0);
    }, 300);
  });
  // Mode pill: switching re-fetches page 0 from the chosen source.
  $$("#inbox-mode-pill [data-imode]").forEach(function (b) {
    b.addEventListener("click", function () {
      if (b.classList.contains("on")) return;
      $$("#inbox-mode-pill [data-imode]").forEach(function (x) { x.classList.remove("on"); });
      b.classList.add("on");
      inboxMode = b.dataset.imode;
      refreshInbox(0);
    });
  });

  $("#inbox-page-input").addEventListener("change", function () {
    const input = $("#inbox-page-input");
    let p = parseInt(input.value, 10);
    const max = parseInt(input.max, 10) || 1;
    if (isNaN(p) || p < 1) p = 1;
    if (p > max) p = max;
    input.value = String(p);
    refreshInbox(p - 1); // dispatcher routes search/normal; 0-based
  });

  // inboxStepNav (v0.5.4): 上一封/下一封 along the current list order; at a
  // page edge it flips the pager and opens the boundary message. The nav row
  // lives atop the detail pane (CSS shows it only <=800px on phones).
  function inboxStepNav(item, dir) {
    const items = $$(".mail-item", $("#inbox-list"));
    const idx = items.indexOf(item);
    const nextIdx = idx + dir;
    if (nextIdx >= 0 && nextIdx < items.length) {
      items[nextIdx].click();
      return;
    }
    const page = dir < 0 ? inboxPage - 1 : inboxPage + 1;
    if (page < 0) { toast(t("inbox.noNewer"), "error"); return; }
    refreshInbox(page).then(function () {
      const fresh = $$(".mail-item", $("#inbox-list"));
      const target = dir < 0 ? fresh[fresh.length - 1] : fresh[0];
      if (target) target.click();
      else toast(t("inbox.noMore"), "error");
    });
  }

  function inboxNavRow() {
    // Compact pill (superior-approved) in its own band above the letter —
    // the letter body scrolls in a separate region below it, so the buttons
    // are always reachable and never overlap the content.
    return '<div class="inbox-nav">' +
      '<button class="row-action" data-nav="-1">↑ ' + t("inbox.prev") + "</button>" +
      '<button class="row-action" data-nav="1">' + t("inbox.next") + " ↓</button>" +
      "</div>";
  }

  // Two-zone detail frame: nav band + independently scrolling letter region.
  function inboxDetailFrame(inner) {
    return inboxNavRow() + '<div class="detail-scroll">' + inner + "</div>";
  }

  async function showInboxDetail(id, item, auto) {
    resetAudioPlayers();
    $$(".mail-item", $("#inbox-list")).forEach(function (el) { el.classList.remove("selected"); });
    if (item) item.classList.add("selected");
    const detail = $("#inbox-detail");
    detail.innerHTML = inboxDetailFrame('<div class="inbox-loading">' + t("common.loading") + "</div>");
    const navPrev = $('[data-nav="-1"]', detail), navNext = $('[data-nav="1"]', detail);
    if (navPrev) navPrev.addEventListener("click", function () { inboxStepNav(item, -1); });
    if (navNext) navNext.addEventListener("click", function () { inboxStepNav(item, 1); });
    // Auto-preload (newest message on inbox load) stays on the List tab on
    // mobile — only a user tap flips to Message.
    if (!auto) revealDetailOnMobile("inbox-grid", detail);
    if (item) {
      item.classList.remove("unread");
      const dot = $(".unread-dot", item);
      if (dot) dot.remove();
    }
    try {
      // The Inbox tab is the viewer's own mail, so /api/message works for both
      // roles (admin satisfies account auth).
      const m = await api("/api/message?id=" + encodeURIComponent(id));
      // Final render includes the nav row (earlier only the loading frame
      // had it — data arrival wiped it; feedback root cause); the letter
      // itself lives in the scroll region below the nav band.
      detail.innerHTML = inboxDetailFrame(
        '<div class="detail-row"><b>From:</b> ' + esc(m.from) + "</div>" +
        '<div class="detail-row"><b>To:</b> ' + esc((m.to || []).join(", ")) + "</div>" +
        (m.cc && m.cc.length ? '<div class="detail-row"><b>Cc:</b> ' + esc(m.cc.join(", ")) + "</div>" : "") +
        '<div class="detail-row"><b>Subject:</b> ' + esc(m.subject || "") + "</div>" +
        '<div class="detail-row"><b>Date:</b> ' + fmtTime(m.received_at) + "</div>" +
        '<div class="detail-row"><b>ID:</b> <code>' + esc(m.id || m.message_id) + "</code></div>" +
        '<div class="row" style="margin:8px 0;">' +
        (String(m.from || "").toLowerCase() === String(getSession().address || "").toLowerCase()
          ? // Superior 01M1AWXF: own sent mail gets Follow-up (recipients kept,
            // irt wired, subject prefixed) instead of Reply.
            '<button class="row-action" id="btn-inbox-followup" data-follow-to="' + esc((m.to || []).join(", ")) + '" data-follow-subject="' + esc(m.subject || "") + '" data-follow-id="' + esc(m.id || m.message_id || "") + '">' + t("act.followUp") + "</button>"
          : '<button class="row-action" id="btn-inbox-reply" data-reply-to="' + esc(m.from) + '" data-reply-subject="' + esc(m.subject || "") + '" data-reply-id="' + esc(m.id || m.message_id || "") + '">' + t("act.reply") + "</button>") +
        '<button class="row-action" id="btn-inbox-forward" style="margin-left:8px;">' + t("act.forward") + "</button></div>" +
        "<hr>" + letterBodyHtml(m.body) + attachmentCards(m));
      upgradeLetterBody(detail, m);
      wireAttachmentDownloads(detail, m);
      hydrateAttachmentPreviews(detail, m);
      wireReplyRef(detail, m, function (pid) {
        // irt cross-view fallback (superior 01M1AVGJY): the parent letter may
        // live in a subordinate mailbox the Inbox tab cannot read
        // (/api/message answers not found). Probe the own path first; on a
        // miss hand the jump to Manage > Browse, which reads subordinate
        // mailboxes and — when nothing can see it either — shows its own
        // not-found error.
        api("/api/message?id=" + encodeURIComponent(pid)).then(function () {
          showInboxDetail(pid, null, false);
        }, function () {
          mgmtRefJumpToBrowse(pid);
        });
      });
      document.dispatchEvent(new CustomEvent("badge:refresh"));
      {
        const p1 = $('[data-nav="-1"]', detail), n1 = $('[data-nav="1"]', detail);
        if (p1) p1.addEventListener("click", function () { inboxStepNav(item, -1); });
        if (n1) n1.addEventListener("click", function () { inboxStepNav(item, 1); });
      }
      const replyBtn = $("#btn-inbox-reply");
      if (replyBtn) replyBtn.addEventListener("click", function () {
        document.dispatchEvent(new CustomEvent("compose:reply", { detail: { to: replyBtn.dataset.replyTo, subject: replyBtn.dataset.replySubject, parentId: replyBtn.dataset.replyId } }));
      });
      const followBtn = $("#btn-inbox-followup");
      if (followBtn) followBtn.addEventListener("click", function () {
        document.dispatchEvent(new CustomEvent("compose:followUp", { detail: { to: followBtn.dataset.followTo, subject: followBtn.dataset.followSubject, parentId: followBtn.dataset.followId } }));
      });
      const fwdBtn = $("#btn-inbox-forward");
      if (fwdBtn) fwdBtn.addEventListener("click", function () { document.dispatchEvent(new CustomEvent("compose:forward", { detail: { m: m } })); });
    } catch (e) {
      // Keep the nav row on errors too — the reader can still step away.
      detail.innerHTML = inboxDetailFrame('<p class="muted">' + esc(t("common.error", { msg: e.message })) + "</p>");
      const p1 = $('[data-nav="-1"]', detail), n1 = $('[data-nav="1"]', detail);
      if (p1) p1.addEventListener("click", function () { inboxStepNav(item, -1); });
      if (n1) n1.addEventListener("click", function () { inboxStepNav(item, 1); });
    }
  }

  // mgmtRefJumpToBrowse (superior 01M1AVGJY): open an in_reply_to target the
  // Inbox tab cannot see. Switches to Manage > Browse, probes the read-only
  // subordinate detail endpoints (first hit wins — the 从属 badge renders as
  // usual), and when nothing can see the letter falls back to the plain self
  // detail, whose catch shows the not-found error in Browse.
  async function mgmtRefJumpToBrowse(pid) {
    try {
      const subs = await loadSubs(false);
      const edges = (subs && subs.subordinates) || [];
      for (let i = 0; i < edges.length; i++) {
        const addr = edges[i] && edges[i].address;
        if (!addr) continue;
        try {
          await api("/api/subs/" + encodeURIComponent(addr) +
            "/message?id=" + encodeURIComponent(pid), { keepSession: true });
          mgmtRefRenderBrowse(function () { showSubDetail(addr, { id: pid }, null); });
          return;
        } catch (_) { /* not in this mailbox — try the next */ }
      }
    } catch (_) { /* subs unavailable — drop to the self path */ }
    mgmtRefRenderBrowse(function () { showDetail(pid, null); });
  }

  // mgmtRefRenderBrowse switches to Manage > Browse and runs render once the
  // entry auto-load (manage:entered → loadMailList) has done its own detail
  // reset — that reset would otherwise wipe the letter rendered before it.
  function mgmtRefRenderBrowse(renderFn) {
    const navBtn = document.querySelector('.tab[data-tab="mail"]');
    if (navBtn) navBtn.click();
    const segBtn = document.querySelector('#mgmt-seg button[data-mview="browse"]');
    if (segBtn) segBtn.click();
    const sel = document.querySelector("#mail-account");
    let n = 0;
    (function tick() {
      n++;
      const ready = !sel || sel.dataset.loaded === "1" || n > 40;
      if (!ready) { setTimeout(tick, 25); return; }
      setTimeout(renderFn, 120);
    })();
  }

  (function wireMgmt() {
    var seg = $("#mgmt-seg");
    if (!seg) return;
    function setView(v) {
      var browse = $("#mgmt-browse"), overview = $("#mgmt-overview"), threads = $("#mgmt-threads");
      if (v !== "overview" && v !== "threads") v = "browse";
      if (browse) browse.classList.toggle("hidden", v !== "browse");
      if (overview) overview.classList.toggle("hidden", v !== "overview");
      if (threads) threads.classList.toggle("hidden", v !== "threads");
      $$("#mgmt-seg button").forEach(function (b) {
        b.classList.toggle("on", b.dataset.mview === v);
      });
      try { localStorage.setItem("mgmt-view", v); } catch (_) {}
      // The overview/threads panes are their own modules'; entering (or
      // re-entering) goes through the event bus.
      if (v === "overview") document.dispatchEvent(new CustomEvent("overview:entered"));
      if (v === "threads") document.dispatchEvent(new CustomEvent("threads:entered"));
      // Feedback 09-04: overview can overflow the page and leave a scroll
      // offset behind; returning to the one-screen browse grid then looks
      // like it no longer fills the viewport height. Reset to top on switch.
      if (v === "browse") window.scrollTo(0, 0);
    }
    seg.addEventListener("click", function (ev) {
      var b = ev.target.closest("button[data-mview]");
      if (b) setView(b.dataset.mview);
    });
    // Superior ruling refined (01M11ND4): remembering the last view is fine,
    // but a data-dependent view must never come up empty. A remembered
    // "threads" restores with a one-shot flag; the threads module dispatches
    // mgmt:fallback-browse when its list is empty and we drop back to the
    // browse view (and to browse as the remembered choice). overview renders
    // its stats layout for any account, so it restores without a guard.
    document.addEventListener("mgmt:fallback-browse", function () {
      document.__mgmtRestoreThreads = false;
      setView("browse");
    });
    var start = "browse";
    try {
      var sv = localStorage.getItem("mgmt-view");
      if (sv === "threads" || sv === "overview") start = sv;
    } catch (_) {}
    // The flag only arms for a live (token) session: without it the restore
    // is a no-op and a later manual click into threads must never bounce.
    if (start === "threads" && getSession()) document.__mgmtRestoreThreads = true;
    setView(start);
  })();

  // ---- audit ----

  // S2 surface (audit small-ticket): the tab lives in app.js, the loader
  // lives here — cross-domain goes through a DOM event, never a direct call
  // (a direct call from app.js threw ReferenceError and the audit list
  // silently never loaded).
  document.addEventListener("audit:entered", function () { loadAudit(); fitAuditOneScreen(); });
  // Audit mobile one-screen (superior 0.2.4 pool item): same measured
  // column + correction closure as the other one-screen fits.
  // 键盘态视口高（0.2.5 高优 v2）：pan 模式键盘下 innerHeight 不缩、只有
  // visualViewport 缩——量测一律取两者较小值，两种键盘模式都成立。
  function mgKbVh() {
    return window.visualViewport ? Math.min(window.innerHeight, Math.round(window.visualViewport.height)) : window.innerHeight;
  }
  function fitAuditOneScreen() {
    var tab = document.getElementById("tab-audit");
    if (!tab || tab.classList.contains("hidden")) return;
    if (window.innerWidth > 800) { tab.style.removeProperty("--audit-1s"); return; }
    var top = tab.getBoundingClientRect().top;
    if (top <= 0) return;
    var h = mgKbVh() - top - 10;
    if (h < 300) h = 300;
    tab.style.setProperty("--audit-1s", h + "px");
    var over = document.documentElement.scrollHeight - window.innerHeight;
    if (over > 0) tab.style.setProperty("--audit-1s", Math.max(240, h - over) + "px");
  }
  window.addEventListener("resize", fitAuditOneScreen);
  async function loadAudit() {
    const tbody = $("#audit-table tbody");
    tbody.textContent = "";
    try {
      const data = await api("/admin/audit?limit=100");
      const entries = data.entries || [];
      if (!entries.length) {
        tbody.innerHTML = '<tr><td colspan="4">No entries.</td></tr>';
        return;
      }
      tbody.innerHTML = entries.map(function (e) {
        return "<tr>" +
          "<td>" + fmtTime(e.timestamp) + "</td>" +
          "<td><code>" + esc(e.action) + "</code></td>" +
          "<td>" + esc(e.account || "—") + "</td>" +
          "<td>" + esc(e.detail || "") + "</td>" +
          "</tr>";
      }).join("");
    } catch (e) {
      tbody.innerHTML = '<tr><td colspan="4">Error: ' + esc(e.message) + "</td></tr>";
    }
    fitAuditOneScreen(); // re-measure once rows land
  }


  // Module-local prefs (v0.6.17 P0): car2 left reads of the entry's
  // userPrefs closure — modules cannot see it. Self-fetch once per login
  // (same source of truth: the account profile), reset with the session.
  let mgmtPrefs = null;
  // app.js savePrefs dispatches this after a successful profile save —
  // drop the cache so the next detail render sees the new toggles
  // (body_markdown takes effect without a re-login).
  document.addEventListener("manage:refresh", function () { mgmtPrefs = null; });
  function ensureMgmtPrefs() {
    if (mgmtPrefs) return Promise.resolve(mgmtPrefs);
    return api("/api/profile/self", { keepSession: true }).then(function (p) {
      mgmtPrefs = (p && p.prefs) || p || {};
      return mgmtPrefs;
    }, function () { return null; });
  }

  // ---- cross-domain event wiring (protocol surface of this module) ----
  // ---- View re-homing (v0.2.1 item one, superior order via alice
  // 01M1DCN7Y): away from Inbox/Mail longer than HOME_REHOME_MS re-lands
  // the page on its home sub-view (Inbox -> 收件+List, Mail -> Browse+List)
  // instead of restoring the last sub-view; quick in-out within the
  // threshold restores as before. Leave stamps are written on page hide
  // and on switching away to another tab. PC/mobile alike (it is state,
  // not layout). ----
  var HOME_REHOME_MS = 60 * 60 * 1000; // configurable constant, default 1h
  function stampPageLeave(key) {
    try { localStorage.setItem("view-leave-" + key, String(Date.now())); } catch (_) {}
  }
  function leftLongAgo(key) {
    try {
      var v = parseInt(localStorage.getItem("view-leave-" + key), 10);
      return !!v && (Date.now() - v) > HOME_REHOME_MS;
    } catch (_) { return false; }
  }
  (function wireReHomeStamps() {
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden) return;
      var ib = document.getElementById("tab-inbox");
      var ml = document.getElementById("tab-mail");
      if (ib && !ib.classList.contains("hidden")) stampPageLeave("inbox");
      if (ml && !ml.classList.contains("hidden")) stampPageLeave("mail");
    });
    document.addEventListener("click", function (ev) {
      var t = ev.target;
      var b = t && t.closest ? t.closest("button.tab[data-tab]") : null;
      if (!b) return;
      ["inbox", "mail"].forEach(function (k) {
        if (b.dataset.tab === k) return;
        var el = document.getElementById("tab-" + k);
        if (el && !el.classList.contains("hidden")) stampPageLeave(k);
      });
    }, true);
  })();
  document.addEventListener("inbox:entered", function () {
  $("#inbox-search").value = "";
  inboxSearchQ = "";
  // Re-home (v0.2.1 item one): away too long -> 收件+List.
  if (leftLongAgo("inbox")) {
    $$("#inbox-mode-pill [data-imode]").forEach(function (x) {
      x.classList.toggle("on", x.dataset.imode === "in");
    });
    inboxMode = "in";
    mailShowPane("inbox-grid", "list");
  }
  fitInboxOneScreen();
  loadInbox(0);
});

// Superior 08-31: one-screen inbox on phones. The tab height = viewport
// minus whatever sits above it (header + nav wrap per language), so it
// is measured, not hardcoded; recomputed on resize/rotation. Desktop is
// untouched (>800px clears the inline height).
// (Gate fix 01M1DWBJ1-train: hoisted to module top level — the definition
// previously sat inside the manage:entered callback, so the inbox:entered
// call threw ReferenceError, loadInbox(0) after it never ran (empty inbox
// on entry, since_id anchor never initialized), and the resize listeners
// re-registered on every manage entry.)
function fitInboxOneScreen() {
  var tab = document.getElementById("tab-inbox");
  if (!tab) return;
  if (window.innerWidth > 800) {
    // Superior 09-02: PC one-screen too — same measured flex column, the
    // grid grows to the viewport and the panes scroll internally (all
    // three modes share one grid, so mode switches need no re-fit).
    tab.style.height = "";
    fitInboxPcOneScreen();
    return;
  }
  var top = tab.getBoundingClientRect().top;
  var h = mgKbVh() - Math.max(top, 0) - 10;
  if (h < 300) h = 300;
  tab.style.setProperty("--inbox-1s", h + "px");
  // 精调：容器尾部内边距等造成的页面溢出量直接扣除（一屏=零页面滚动）
  var over = document.documentElement.scrollHeight - window.innerHeight;
  if (over > 0) {
    h = Math.max(h - over, 240);
    tab.style.setProperty("--inbox-1s", h + "px");
  }
}

// PC twin of the mobile inbox fit: tab height = viewport minus whatever
// sits above it; grid flexes, panes scroll, pager stays visible.
function fitInboxPcOneScreen() {
  var tab = document.getElementById("tab-inbox");
  if (!tab || tab.classList.contains("hidden")) return;
  var top = tab.getBoundingClientRect().top;
  var h = mgKbVh() - Math.max(top, 0) - 10;
  if (h < 360) h = 360;
  tab.style.setProperty("--inbox-pc-1s", h + "px");
  var over = document.documentElement.scrollHeight - window.innerHeight;
  if (over > 0) tab.style.setProperty("--inbox-pc-1s", Math.max(h - over, 360) + "px");
}
window.addEventListener("resize", fitInboxOneScreen);
// 查信页 PC 一屏（上级 09-04）：fitInboxPcOneScreen 的量高孪生——容器
// 高度=视口减头部；CSS 侧 grid 撑满、双栏内部滚动，pager 留在视口内。
function fitMailPcOneScreen() {
  var tab = document.getElementById("tab-mail");
  if (!tab || tab.classList.contains("hidden")) return;
  if (window.innerWidth <= 800) return;
  var top = tab.getBoundingClientRect().top;
  var h = mgKbVh() - Math.max(top, 0) - 10;
  if (h < 360) h = 360;
  tab.style.setProperty("--mail-pc-1s", h + "px");
  var over = document.documentElement.scrollHeight - window.innerHeight;
  if (over > 0) tab.style.setProperty("--mail-pc-1s", Math.max(h - over, 360) + "px");
}
window.addEventListener("resize", fitMailPcOneScreen);

// 输入法弹出/收起高优修（上级 0.2.5，01M1JG5D）：软键盘改变视口时一屏
// 量测高度还是键盘前的旧值——底部控件停在旧位盖住输入栏。visualViewport
// 的 resize 在两种键盘模式（窗口重排/仅视觉视口变化）下都会触发，统一
// 在此重算本模块全部一屏量测；防抖 120ms 防连发。
(function fWireVvRefit() {
  var vv = window.visualViewport;
  if (!vv) return;
  var t = null;
  vv.addEventListener("resize", function () {
    clearTimeout(t);
    t = setTimeout(function () {
      fitInboxOneScreen();
      fitInboxPcOneScreen();
      fitMgmtOneScreen();
      fitAuditOneScreen();
      // 重算改变了布局，浏览器原生的焦点滚动会被作废——重算后再把
      // 焦点输入框滚回可视区顶部（0.2.5 v4：与写邮件页 v3 同口径 start，
      // 输入框以下的内容让位，不被键盘遮）
      var ae = document.activeElement;
      if (ae && (ae.tagName === "TEXTAREA" || ae.tagName === "INPUT")) {
        try { ae.scrollIntoView({ block: "start" }); } catch (_) {}
      }
    }, 120);
  });
})();

// Superior 09-01 (01M1D7QBV): one-screen Mail-manage on phones — threads
// list scrolls inside a measured column, forest viewport aligns to it,
// overview stacks subs rows (scrolling) above the connections graph.
// Measured like fitInboxOneScreen; PC (>800px) untouched.
function fitMgmtOneScreen() {
  var th = document.getElementById("mgmt-threads");
  var ov = document.getElementById("mgmt-overview");
  var br = document.getElementById("mgmt-browse");
  var dir = document.getElementById("ovw-directory");
  if (window.innerWidth > 800) {
    if (th) th.style.removeProperty("--th-1s");
    if (ov) ov.style.removeProperty("--ovw-m-1s");
    if (br) br.style.removeProperty("--mgmt-b-1s");
    if (dir) dir.style.removeProperty("--ovw-dir-1s");
    return;
  }
  function fitBox(el, prop, min) {
    var top = el.getBoundingClientRect().top;
    var h = mgKbVh() - Math.max(top, 0) - 10;
    if (h < min) h = min;
    el.style.setProperty(prop, h + "px");
    var over = document.documentElement.scrollHeight - window.innerHeight;
    // 修正下限 240（<min）：键盘缩视口时要能真正抵消溢出，否则页面仍可
    // 微拖、底部控件盖输入栏（上级 0.2.5 高优 01M1JG5D）
    if (over > 0) el.style.setProperty(prop, Math.max(h - over, 240) + "px");
  }
  // Browse (superior 09-01): 查信 sub-page joins the one-screen family —
  // the mail grid scrolls inside the measured column.
  if (br && !br.classList.contains("hidden")) fitBox(br, "--mgmt-b-1s", 280);
  if (th && !th.classList.contains("hidden")) fitBox(th, "--th-1s", 280);
  if (ov && !ov.classList.contains("hidden")) fitBox(ov, "--ovw-m-1s", 280);
  // Directory (总览-通讯录, superior 0.2.2 feedback point 1): the table
  // scrolls inside its own measured column.
  if (dir && !dir.classList.contains("hidden")) fitBox(dir, "--ovw-dir-1s", 280);
}
window.addEventListener("resize", fitMgmtOneScreen);
document.addEventListener("threads:entered", function () { setTimeout(fitMgmtOneScreen, 250); });
(function () {
  var seg = document.getElementById("mgmt-seg");
  if (seg) seg.addEventListener("click", function () { setTimeout(fitMgmtOneScreen, 250); });
  // Overview capsule (系统/我的/通讯录): switching views must re-fit — the
  // directory column only exists once its sub-view is shown.
  var oseg = document.getElementById("ovw-seg");
  if (oseg) oseg.addEventListener("click", function () { setTimeout(fitMgmtOneScreen, 250); });
  if (!window.MutationObserver) return;
  var mo = new MutationObserver(function () { setTimeout(fitMgmtOneScreen, 80); });
  var th = document.getElementById("th-list");
  var ov = document.getElementById("mgmt-overview");
  var ml = document.getElementById("mail-list");
  var odir = document.getElementById("ovw-directory");
  if (th) mo.observe(th, { childList: true, subtree: true });
  if (ov) mo.observe(ov, { childList: true, subtree: true });
  if (ml) mo.observe(ml, { childList: true, subtree: true });
  if (odir) mo.observe(odir, { childList: true, subtree: true });
})();

document.addEventListener("manage:entered", function () {
    // Re-home (v0.2.1 item one): away too long -> Browse+List. The segment
    // button click routes through the same path as a manual switch, so the
    // view storage and threads flags stay consistent.
    if (leftLongAgo("mail")) {
      var homeBtn = document.querySelector('#mgmt-seg button[data-mview="browse"]');
      if (homeBtn) homeBtn.click();
      mailShowPane("mail-grid", "list");
    }
      ensureMgmtPrefs();
    // Auto-load on tab entry (superior request): default filters = all
    // visible accounts / inbox / 100. The options fetch is async — load
    // AFTER it resolves, otherwise the select is still empty and the list
    // bails with "no account selected" (superior report 01M0ZMAC).
    ensureAccountOptions().catch(function () {}).then(loadMailList);
    // Re-kick: the mgmt segment may have restored "overview" at boot before
    // a session existed (original activateTab("mail") branch semantics).
    var seg = $("#mgmt-seg");
    if (seg && seg.querySelector('button[data-mview="overview"].on')) {
      document.dispatchEvent(new CustomEvent("overview:entered"));
    }
  });
  document.addEventListener("manage:refresh", function () {
    document.dispatchEvent(new CustomEvent("overview:refresh"));
    document.dispatchEvent(new CustomEvent("threads:refresh"));
  });
  document.addEventListener("manage:reset", function () {
    mgmtPrefs = null;
    subsCache = null;
    document.dispatchEvent(new CustomEvent("overview:reset"));
    document.dispatchEvent(new CustomEvent("threads:reset"));
    if (typeof invalidateMailAccountOptions === "function") invalidateMailAccountOptions();
  });
  document.addEventListener("subs:request", function (ev) {
    var d = ev.detail || {};
    loadSubs(!!d.force).then(d.resolve, function () { d.resolve && d.resolve(null); });
  });
  document.addEventListener("audio:stop-all", function () {
    resetAudioPlayers();
  });
  // The overview module (graph/rows) asks to open a mailbox in the browse
  // pane — selection + load live here.
  document.addEventListener("mgmt:browse-account", function (ev) {
    var d = ev.detail || {};
    var seg = $("#mgmt-seg");
    if (seg) {
      var btn = seg.querySelector('button[data-mview="browse"]');
      if (btn) btn.click();
    }
    var sel = $("#mail-account");
    if (sel && Array.prototype.some.call(sel.options, function (o) { return o.value === d.address; })) {
      sel.value = d.address;
      if (d.folder) {
        var folder = $("#mail-folder");
        if (folder) folder.value = d.folder;
      }
      var loadBtn = $("#btn-load-mail");
      if (loadBtn) loadBtn.click();
    } else {
      toast(t("mgmt.acctNotInList"), "error");
    }
  });

  document.addEventListener("subs:remove", function (ev) {
    var d = ev.detail || {};
    removeSub(d.address, d.role);
  });

  // ---- v0.2.1 incremental pull: since_id pre-pend + no-miss iron rule ----
  (function inboxIncremental() {
    // Rebuild the inbox list HTML from an array of MessageSummary objects.
    function renderItem(m) {
      const item = document.createElement("div");
      item.className = "mail-item" + (m.unread ? " unread" : "");
      item.innerHTML =
        '<div class="mail-sender">' + esc(m.from || "") + "</div>" +
        '<div class="mail-subject">' + esc(m.subject || "") + "</div>" +
        '<div class="mail-preview">' + esc(m.preview || "") + "</div>" +
        '<span class="mail-time">' + fmtTime(m.received_at) + "</span>";
      item.addEventListener("click", function () { showInboxDetail(m.id, item, false); });
      return item;
    }

    document.addEventListener("inbox:newmail", async function () {
      if (!inboxAnchorId) return; // no anchor yet — manual load is the source
      if (inboxMode !== "in" && inboxMode !== "both") return; // only inbox modes
      try {
        const d = await api("/api/inbox?since_id=" + encodeURIComponent(inboxAnchorId) + "&limit=20", { keepSession: true });
        const fresh = (d.messages || []).filter(function (m) { return m.id > inboxAnchorId; });
        if (!fresh.length) return;
        // No-miss iron rule: if the first returned id <= anchor, the server sent
        // out-of-order data — discard auto-merge and fall back to a full load.
        if (fresh[0].id <= inboxAnchorId) {
          console.error("inbox:incremental out-of-order, falling back", fresh[0].id, "<=", inboxAnchorId);
          silentFullLoad();
          return;
        }
        const list = $("#inbox-list");
        if (!list) return;
        // Pre-pend newest-first (fresh is already newest-first from server).
        var frag = document.createDocumentFragment();
        fresh.forEach(function (m) { frag.appendChild(renderItem(m)); });
        list.insertBefore(frag, list.firstChild);
        // Advance anchor after successful merge.
        inboxAnchorId = fresh[0].id;
        // Update the inbox-status line with the new total.
        var st = $("#inbox-status");
        if (st) st.textContent = "+" + fresh.length + " new";
      } catch (e) {
        console.error("inbox:incremental error:", e.message);
        silentFullLoad();
      }
    });

    function silentFullLoad() {
      var now = Date.now();
      if (now - lastSilentFull < INBOX_SILENT_FALLBACK_MS) return; // throttled
      lastSilentFull = now;
      console.warn("inbox:silent full-load fallback (merge anomaly)");
      loadInbox(0);
    }
  })();

})();
