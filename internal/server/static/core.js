// core.js — shared foundation of the agentmail panel (S1 of the zero-build
// ESM split, governance plan v1 / architecture ruling 01M0W7HQ).
//
// HARD CONSTRAINT (architecture ruling): domain modules import ONLY core;
// cross-domain interactions go through DOM events, never sibling imports.
//
// Scope: DOM helpers, session/auth cache, the fetch wrapper, toast, escaping.
// i18n stays a separate classic script (window.t) — untouched by S1.

// The 401 path needs to surface the login screen, which lives in app.js.
// Dependency inversion: the entry registers its handler here.
let unauthorizedHandler = null;
export function setUnauthorizedHandler(fn) { unauthorizedHandler = fn; }

// ---- DOM helpers ----

export function copyText(text) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    return navigator.clipboard.writeText(text).then(function () { return true; }, function () { return false; });
  }
  // Legacy fallback: select a temporary node and execCommand("copy").
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed"; ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch (_) { ok = false; }
  ta.remove();
  return Promise.resolve(ok);
}

export function fmtTime(unixOrIso) {
  if (!unixOrIso) return "\u2014";
  let d;
  if (typeof unixOrIso === "number") d = new Date(unixOrIso * 1000);
  else d = new Date(unixOrIso);
  if (isNaN(d.getTime())) return String(unixOrIso);
  return d.toLocaleString();
}

export function fmtBytes(n) {
  if (typeof n !== "number" || isNaN(n) || n < 0) return null;
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let v = n;
  for (let i = 0; i < units.length; i++) {
    v = v / 1024;
    if (v < 1024 || i === units.length - 1) return (Math.round(v * 10) / 10) + " " + units[i];
  }
}

export function $(sel, root) { return (root || document).querySelector(sel); }
export function $$(sel, root) { return Array.from((root || document).querySelectorAll(sel)); }

// ---- session / auth (v0.6.27: session-token support) ----

const SESSION_KEY = "agentmail_creds";   // sessionStorage: {address, password, is_admin}
const TOKEN_KEY    = "agentmail_token";  // localStorage: {address, token}

export function getSession() {
  // Token path (remember-me): localStorage has higher priority when token is valid.
  try {
    const t = JSON.parse(localStorage.getItem(TOKEN_KEY) || "null");
    if (t && t.address && t.token) return { address: t.address, token: t.token, is_admin: undefined };
  } catch (_) {}
  // Password path (session-only or fallback).
  try { return JSON.parse(sessionStorage.getItem(SESSION_KEY) || "null"); }
  catch (_) { return null; }
}
export function setSession(s) {
  if (s && s.address && s.password) sessionStorage.setItem(SESSION_KEY, JSON.stringify(s));
  else sessionStorage.removeItem(SESSION_KEY);
}
// setToken stores/retrieves a session token in localStorage ("remember me").
export function setToken(address, token) {
  if (address && token) localStorage.setItem(TOKEN_KEY, JSON.stringify({ address, token }));
  else localStorage.removeItem(TOKEN_KEY);
}
export function clearAuth() {
  sessionStorage.removeItem(SESSION_KEY);
  localStorage.removeItem(TOKEN_KEY);
}

// Returns the Authorization header value for the cached creds, or "".
// Token path: Bearer <token> ; password path: Basic base64.
export function basicAuth() {
  const s = getSession();
  if (!s || !s.address) return "";
  if (s.token) return "Bearer " + s.token;
  return "Basic " + btoa(unescape(encodeURIComponent(s.address + ":" + s.password)));
}

// ---- fetch wrapper ----

// api wraps fetch with the cached auth header. If a call comes back 401,
// the cached creds are stale/wrong: clear them and surface the login page.
export async function api(path, opts) {
  opts = opts || {};
  const headers = Object.assign({}, opts.headers || {});
  const auth = basicAuth();
  if (auth && !headers.Authorization) headers.Authorization = auth;
  if (opts.body && !headers["Content-Type"]) headers["Content-Type"] = "application/json";
  const res = await fetch(path, Object.assign({}, opts, { headers: headers }));
  if (res.status === 401 && getSession()) {
    // opts.keepSession marks non-critical subrequests (fan-out reads):
    // an endpoint-level 401 surfaces as a row-level error instead of
    // tearing down the whole session (defense per the v0.5.10.2 review —
    // one failed subrequest must not log the user out).
    if (opts.keepSession) throw new Error("401 Unauthorized");
    clearAuth(); // token invalid — force re-login
    if (unauthorizedHandler) { try { unauthorizedHandler(); } catch (_) {} }
    throw new Error("session expired — please log in again");
  }
  if (!res.ok) {
    let msg = res.status + " " + res.statusText;
    try { const t = await res.text(); if (t) msg = t; } catch (_) {}
    throw new Error(msg);
  }
  const ct = res.headers.get("Content-Type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

// ---- toast ----

export function toast(msg, kind) {
  const el = $("#toast");
  el.textContent = msg;
  el.className = "toast" + (kind ? " " + kind : "");
  el.classList.remove("hidden");
  clearTimeout(toast._t);
  toast._t = setTimeout(function () { el.classList.add("hidden"); }, 4000);
}

// ---- escaping ----

export function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
    return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
  });
}
