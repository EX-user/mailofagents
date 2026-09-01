// agentmail setup wizard — vanilla JS, no build step.
// Bilingual (superior 09-01): the language follows the panel setup page via
// localStorage agentmail_lang ("zh" / anything else = en).
(function () {
  "use strict";

  var WLANG = (function () {
    try { return localStorage.getItem("agentmail_lang") === "zh" ? "zh" : "en"; }
    catch (e) { return "en"; }
  })();

  var DICT = {
    en: {
      step1desc: "First-time setup. Configure the mail system before starting the server.",
      dbPath: "Database path",
      dbHint0: "Where the bbolt database file is stored.",
      dbFile: "Database file: ",
      relPath: "Relative path — resolves next to the server executable as: ./",
      listen: "Listen address",
      listenHint: "Use 0.0.0.0:8090 for LAN access.",
      domain: "Mail domain",
      adminpw: "Admin password (min 8 chars)",
      pwPh: "choose a strong password",
      init: "Initialize",
      required: "All fields are required.",
      pwLen: "Password must be at least 8 characters.",
      initializing: "Initializing…",
      err: "Error: ",
      inited: "System initialized.",
      start: "Start server",
      starting: "Starting server…",
      started: "The server is now running on your configured address.",
      openPanel: "Open panel",
    },
    zh: {
      step1desc: "首次初始化：启动服务器前，请先完成邮件系统配置。",
      dbPath: "数据库路径",
      dbHint0: "bbolt 数据库文件的存放位置。",
      dbFile: "数据库文件：",
      relPath: "相对路径——将相对于服务器可执行文件解析为：./",
      listen: "监听地址",
      listenHint: "局域网访问请使用 0.0.0.0:8090。",
      domain: "邮件域名",
      adminpw: "管理员密码（至少 8 位）",
      pwPh: "请设置一个强密码",
      init: "初始化",
      required: "所有字段均为必填项。",
      pwLen: "密码至少需要 8 个字符。",
      initializing: "初始化中…",
      err: "错误：",
      inited: "系统初始化完成。",
      start: "启动服务器",
      starting: "服务器启动中…",
      started: "服务器已在所配置的地址上运行。",
      openPanel: "打开面板",
    },
  };

  function tw(key) {
    return (DICT[WLANG] && DICT[WLANG][key]) || DICT.en[key] || key;
  }

  // Apply the language to every tagged node; the admin-password hint needs
  // its inner span (live-updated on domain input) rebuilt for Chinese order.
  function applyWizardLang() {
    document.documentElement.lang = WLANG === "zh" ? "zh-CN" : "en";
    var pwHintRebuilt = false;
    document.querySelectorAll("[data-wz]").forEach(function (el) {
      var k = el.getAttribute("data-wz");
      if (k === "pwHint") {
        if (WLANG === "zh") {
          el.innerHTML = '设置 <code>admin@<span id="wz-admin-domain">domain</span></code> 的密码。';
          pwHintRebuilt = true;
        }
        return;
      }
      el.textContent = tw(k);
    });
    if (!pwHintRebuilt && WLANG !== "zh") {
      var hint = document.getElementById("wz-pw-hint");
      if (hint) hint.innerHTML = 'Sets the password for <code>admin@<span id="wz-admin-domain">domain</span></code>.';
    }
    var pw = document.getElementById("wz-password");
    if (pw) pw.placeholder = tw("pwPh");
  }

  function $(s) { return document.querySelector(s); }

  async function api(path, opts) {
    const res = await fetch(path, opts || {});
    const ct = res.headers.get("Content-Type") || "";
    const body = ct.includes("application/json") ? await res.json() : await res.text();
    if (!res.ok) {
      const msg = (body && body.error) ? body.error : (typeof body === "string" ? body : res.statusText);
      throw new Error(msg);
    }
    return body;
  }

  function toast(el, msg, ok) {
    el.textContent = msg;
    el.className = ok ? "muted" : "muted error-text";
  }

  // --- load defaults from server ---
  async function loadDefaults() {
    try {
      const d = await api("/api/wizard-defaults");
      $("#wz-db-path").value = d.db_path || "agentmail.db";
      $("#wz-listen").value = d.listen || "127.0.0.1:8090";
      $("#wz-domain").value = d.domain || "agentmail.local";
    } catch (e) { /* use placeholders */ }
    try {
      const st = await api("/api/status");
      if (st.version) $("#version-badge").textContent = "v" + st.version.replace(/^v/, "");
    } catch (e) { /* dev */ }
    updateAdminDomain();
    // Show the resolved absolute path for db_path.
    updateDbHint();
  }

  function updateAdminDomain() {
    const d = $("#wz-domain").value.trim() || "domain";
    $("#wz-admin-domain").textContent = d;
  }

  function updateDbHint() {
    const p = $("#wz-db-path").value.trim();
    if (!p) return;
    // If it's a relative path, show what it resolves to relative to CWD.
    // The server process CWD is typically the release directory.
    const hint = $("#wz-db-hint");
    if (p.startsWith("/") || p.match(/^[A-Za-z]:/)) {
      hint.textContent = tw("dbFile") + p;
    } else {
      hint.textContent = tw("relPath") + "./" + p;
    }
  }

  // Live-update admin domain hint when domain field changes.
  $("#wz-domain").addEventListener("input", updateAdminDomain);
  $("#wz-db-path").addEventListener("input", updateDbHint);

  // Language pill (superior 09-01): switches the wizard in place and
  // persists the choice so the panel follows it after initialization.
  (function wireLang() {
    var tgl = document.getElementById("wz-lang");
    if (!tgl) return;
    tgl.addEventListener("click", function (ev) {
      var seg = ev.target.closest ? ev.target.closest("[data-seg]") : null;
      if (!seg) return;
      WLANG = seg.dataset.seg === "zh" ? "zh" : "en";
      try { localStorage.setItem("agentmail_lang", WLANG); } catch (e) {}
      applyWizardLang();
      updateAdminDomain();
      updateDbHint();
    });
    tgl.querySelectorAll(".seg").forEach(function (s) {
      s.classList.toggle("on", s.dataset.seg === WLANG);
    });
  })();

  // --- step 1: submit config ---
  $("#wz-submit").addEventListener("click", async function () {
    const body = {
      db_path: $("#wz-db-path").value.trim(),
      listen: $("#wz-listen").value.trim(),
      domain: $("#wz-domain").value.trim(),
      admin_password: $("#wz-password").value,
    };
    const status = $("#wz-status");
    if (!body.db_path || !body.listen || !body.domain || !body.admin_password) {
      toast(status, tw("required"), false);
      return;
    }
    if (body.admin_password.length < 8) {
      toast(status, tw("pwLen"), false);
      return;
    }
    status.textContent = tw("initializing");
    try {
      const res = await api("/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      $("#wz-admin-addr").textContent = res.admin_address || ("admin@" + body.domain);
      $("#wizard-step-config").classList.add("hidden");
      $("#wizard-step-mcp").classList.remove("hidden");
    } catch (e) {
      toast(status, tw("err") + e.message, false);
    }
  });

  // --- launch ---
  $("#wz-launch").addEventListener("click", async function () {
    const status = $("#wz-launch-status");
    status.textContent = tw("starting");
    try {
      await api("/launch", { method: "POST" });
      const panelURL = "http://" + $("#wz-listen").value.trim() + "/";
      setTimeout(function () {
        document.body.innerHTML = '<div class="setup-card" style="text-align:center;">' +
          '<h1>' + tw("starting") + '</h1>' +
          '<p class="muted">' + tw("started") + '</p>' +
          '<button class="primary" id="open-panel-btn">' + tw("openPanel") + '</button>' +
          '</div>';
        document.getElementById("open-panel-btn").addEventListener("click", function () {
          window.location.href = panelURL;
        });
      }, 2000);
    } catch (e) {
      status.textContent = tw("err") + e.message;
    }
  });

  applyWizardLang();
  loadDefaults();
})();
