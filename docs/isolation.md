# Isolation model

This document states plainly what agentmail guarantees, what it delegates,
and what it does **not** guarantee. Isolation is real but layered.

## The problem

Multiple AI agent sessions share one operating-system user account. We want
each session to have its own mailbox and to **not** be able to read another
session's mail. We also want one human admin to read *all* mail for ops and
audit. There is deliberately **no end-to-end encryption** — the "admin reads
all" requirement is incompatible with it.

The hard constraint: **the OS cannot distinguish one agent client session
from another.** To the OS the agent client is one process as one user; its
sessions are an internal abstraction the OS never sees.

## Three layers

### Layer 1 — Enforced (by the server)

A hard guarantee, holding even against a malicious session, because it is
enforced by agentmail-server which the session cannot influence.

- **Per-account authentication.** Every mailbox operation is authenticated to
  the server as a specific account via HTTP Basic auth. The server refuses to
  serve account A's mail to a request authenticated as account B. This is
  server-side code, not convention.
- **Short-lived access codes.** After `authenticate()`, sessions carry an
  access code instead of a password. Codes are bounded by a wall-clock TTL
  (default 1h). A code leaked into a transcript has a small window of
  usefulness.
- **Tiered call budget.** Write-side operations (`register`,
  `authenticate`, `send_email`) are capped at 20 calls per code; read-side
  operations (`read_inbox`, `get_message`, `wait_for_new_mail`) are **uncapped**
  so a session can poll its inbox without burning the budget. This is safe
  because per-account isolation (Layer 1) already constrains reads to the code
  owner's own mail — an over-used code on reads can only re-read the owner's
  inbox, never anyone else's. The TTL remains the time-bound protection for
  both tiers.
- **Codes never touch disk.** They live only in the gateway process's memory.
  When the gateway exits (session end), every code it issued vanishes. There
  is no credential file to point at.

If you trust the server process is not subverted, then **no session can read
another session's mail through the intended interface**, regardless of what
code the session runs.

### Layer 2 — Convention (by the agent client)

Not a hard guarantee. Cooperative isolation provided by the agent client
(Codex CLI, Claude Code) keeping sessions' memories separate.

- Sessions under the same agent client are separate subjects. They do not
  normally read each other's memory. This is the agent client's
  responsibility.

### Layer 3 — What we do **not** guarantee

**Within one OS user account, a malicious session can in principle read
another session's in-memory credentials.**

- Two sessions Alice and Bob run under the same agent client, same OS user.
- If Alice runs arbitrary code and the user tells it to, Alice could read
  Bob's in-memory history, extract Bob's password, and authenticate as Bob.
- We cannot prevent this: the OS gives Alice and Bob identical privileges.
  The only wall between them is the agent client's own session isolation
  (Layer 2).

This is why there is **no credential-file feature**: a credential file would
let Alice impersonate Bob by merely *pointing* at the file, with no password
knowledge — lowering the bar to the floor. Requiring the actual password
means Alice at least has to *obtain* it first.

**Cross-OS-user or cross-OS (Windows vs WSL2) sessions** get real process/OS
boundaries for free, so isolation between them is stronger.

## Summary

| Threat | Blocked by | Strength |
|---|---|---|
| Session B calls the API hoping to see A's mail | Server per-account auth | **Enforced** |
| Leaked access code reused later | TTL + call budget + in-memory only | **Enforced** |
| Session reads another's mail by guessing the password | Server auth | **Enforced** |
| Admin reads all mail | Admin endpoints (by design) | Intended |
| Session A reads session B's password from B's memory | Agent client isolation | **Convention only** |
| Session A points at B's credential file | N/A — **no credential files exist** | By design |

Read the last two rows twice.
