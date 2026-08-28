// agentmail setup wizard — vanilla JS, no build step.
(function () {
  "use strict";

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
      hint.textContent = "Database file: " + p;
    } else {
      hint.textContent = "Relative path — resolves next to the server executable as: ./" + p;
    }
  }

  // Live-update admin domain hint when domain field changes.
  $("#wz-domain").addEventListener("input", updateAdminDomain);
  $("#wz-db-path").addEventListener("input", updateDbHint);

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
      toast(status, "All fields are required.", false);
      return;
    }
    if (body.admin_password.length < 8) {
      toast(status, "Password must be at least 8 characters.", false);
      return;
    }
    status.textContent = "Initializing…";
    try {
      const res = await api("/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      $("#wz-admin-addr").textContent = res.admin_address || ("admin@" + body.domain);
      $("#wizard-step-config").classList.add("hidden");
      $("#wizard-step-mcp").classList.remove("hidden");
      buildCapsules(body.listen);
    } catch (e) {
      toast(status, "Error: " + e.message, false);
    }
  });

  // --- step 2: MCP capsules ---
  function buildCapsules(listen) {
    const serverURL = "http://" + listen;
    const container = $("#mcp-capsules");
    const clients = [
      { id: "codex", label: "I use Codex CLI", desc: "~/.codex/config.toml" },
      { id: "zcode", label: "I use zcode", desc: "~/.zcode/cli/config.json" },
      { id: "opencode", label: "I use opencode", desc: "opencode.json (project-level)" },
      { id: "claude", label: "I use Claude Code", desc: "claude mcp add command" },
    ];
    container.innerHTML = clients.map(function (c) {
      return '<div class="capsule">' +
        '<button class="capsule-header" data-capsule="' + c.id + '">' + esc(c.label) +
        ' <span class="muted capsule-desc">' + esc(c.desc) + '</span>' +
        ' <span class="capsule-toggle">▾</span></button>' +
        '<div class="capsule-body hidden" id="capsule-body-' + c.id + '"></div>' +
        '</div>';
    }).join("");
    document.querySelectorAll("[data-capsule]").forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const id = btn.dataset.capsule;
        const body = $("#capsule-body-" + id);
        if (!body.classList.contains("hidden")) {
          body.classList.add("hidden");
          return;
        }
        body.classList.remove("hidden");
        if (body.dataset.loaded !== "1") {
          try {
            const info = await api("/api/bootstrap-info");
            body.innerHTML = renderSnippet(id, info);
            wireActions(body, id);
            body.dataset.loaded = "1";
          } catch (e) {
            body.innerHTML = '<span class="error-text">Error: ' + esc(e.message) + '</span>';
          }
        }
      });
    });
  }

  function renderSnippet(id, info) {
    const gw = info.gateway_path || "agentmail-gateway";
    const url = info.server_url || "http://127.0.0.1:8090";
    const gwEsc = esc(gw);
    const gwJson = esc(gw.replace(/\\/g, "\\\\"));
    let snippet = "", where = "";
    switch (id) {
      case "codex":
        snippet = '[mcp_servers.agentmail]\ncommand = "' + gwJson + '"\nargs = ["--server-url", "' + url + '"]';
        where = "Save to: <code>~/.codex/config.toml</code>";
        break;
      case "zcode":
        snippet = '{\n  "mcp": {\n    "servers": {\n      "agentmail": {\n        "type": "stdio",\n        "command": "' + gwJson + '",\n        "args": ["--server-url", "' + url + '"],\n        "enabled": true\n      }\n    }\n  }\n}';
        where = "Save to: <code>~/.zcode/cli/config.json</code> (global) or <code>&lt;project&gt;/.zcode/config.json</code> (workspace)";
        break;
      case "opencode":
        snippet = '{\n  "mcp": {\n    "agentmail": {\n      "type": "local",\n      "command": ["' + gwEsc + '", "--server-url", "' + url + '"],\n      "enabled": true\n    }\n  }\n}';
        where = "Save to: <code>opencode.json</code> in your project root";
        break;
      case "claude":
        snippet = 'claude mcp add agentmail -- ' + gwEsc + ' --server-url ' + url;
        where = "Run this command in your terminal";
        break;
    }
    return '<div class="muted snippet-where">' + where + '</div>' +
      '<pre class="snippet">' + esc(snippet) + '</pre>' +
      '<div class="row"><button class="row-action copy-btn">Copy config</button> <span class="write-status muted"></span></div>';
  }

  function wireActions(body, id) {
    const status = body.querySelector(".write-status");
    const copyBtn = body.querySelector(".copy-btn");
    if (copyBtn) {
      copyBtn.addEventListener("click", function (e) {
        e.stopPropagation();
        const text = body.querySelector(".snippet").textContent;
        navigator.clipboard.writeText(text).then(function () {
          status.textContent = "✓ Copied";
        });
      });
    }
  }

  // --- launch ---
  $("#wz-launch").addEventListener("click", async function () {
    const status = $("#wz-launch-status");
    status.textContent = "Starting server…";
    try {
      await api("/launch", { method: "POST" });
      const panelURL = "http://" + $("#wz-listen").value.trim() + "/";
      setTimeout(function () {
        document.body.innerHTML = '<div class="setup-card" style="text-align:center;">' +
          '<h1>Server starting…</h1>' +
          '<p class="muted">The server is now running on your configured address.</p>' +
          '<button class="primary" id="open-panel-btn">Open panel</button>' +
          '</div>';
        document.getElementById("open-panel-btn").addEventListener("click", function () {
          window.location.href = panelURL;
        });
      }, 2000);
    } catch (e) {
      status.textContent = "Error: " + e.message;
    }
  });

  function esc(s) {
    return String(s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  loadDefaults();
})();
