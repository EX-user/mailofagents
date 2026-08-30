#!/usr/bin/env python3
"""Self-bootstrap acceptance for GET /api/self (v0.1.4 implementation order).

The ONLY input is the server base URL. Everything else — how to register,
how to authenticate, how to send and read — is learned from the
self-description document itself, proving the doc is sufficient for a cold
agent to bootstrap.

Usage: self_bootstrap_e2e.py <base-url>
"""
import json
import sys
import urllib.request
import uuid

BASE = sys.argv[1].rstrip("/")


def call(path, method="GET", body=None, headers=None, raw=False):
    data = None
    hdrs = dict(headers or {})
    if body is not None:
        data = json.dumps(body).encode()
        hdrs.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(BASE + path, data=data, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            payload = r.read()
            return r.status, payload if raw else json.loads(payload or b"{}")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")


def main():
    # Step 0 — the only thing we "know": read the self-description.
    status, doc = call("/api/self")
    assert status == 200, f"/api/self = {status}"
    domain = doc["domain"]
    print(f"[self] service={doc['service']} version={doc['version']} domain={domain}")
    assert doc.get("bootstrap_recipe") and doc.get("http_api"), "recipe/http_api missing"

    # Step 1 — register (recipe step 1: name + password, min 8 chars).
    name = "boot-" + uuid.uuid4().hex[:8]
    password = "pw-" + uuid.uuid4().hex
    status, res = call("/api/register", "POST", {"name": name, "password": password})
    assert status == 200, f"register = {status}: {res}"
    address = f"{name}@{domain}"
    print(f"[register] {address}")

    # Step 2 — Basic auth header per http_api.auth.basic.
    import base64
    basic = "Basic " + base64.b64encode(f"{address}:{password}".encode()).decode()
    auth = {"Authorization": basic}

    # Step 3 — send a letter to ourselves (recipe step 3, http_api.send).
    status, res = call("/api/send", "POST", {
        "to": [address],
        "subject": "bootstrap hello",
        "body": "letter one, written by an agent that only knew the URL",
    }, headers=auth)
    assert status == 200, f"send = {status}: {res}"

    # Step 4 — read the inbox (recipe step 4).
    status, inbox = call("/api/inbox?limit=5", headers=auth)
    assert status == 200, f"inbox = {status}"
    letters = inbox.get("messages", inbox if isinstance(inbox, list) else [])
    assert letters, "inbox empty — letter did not arrive"

    # Step 5 — open one letter (recipe step 4, http_api.read).
    mid = letters[0].get("id") or letters[0].get("message_id")
    status, msg = call(f"/api/message?id={mid}", headers=auth)
    assert status == 200, f"message = {status}"
    print(f"[read] id={mid} subject={msg.get('subject')!r}")

    print("SELF_BOOTSTRAP_PASS")


if __name__ == "__main__":
    main()
