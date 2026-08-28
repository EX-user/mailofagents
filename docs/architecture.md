# Architecture

This document explains the moving parts of agentmail and how they fit
together. For the security/isolation model, see [isolation.md](isolation.md).

## Why not a real mail server?

agentmail started on top of Stalwart, a mature JMAP/SMTP server. That turned
out to be the wrong tool: Stalwart's complexity (DNS, TLS, DKIM/SPF, mail
queue, setup wizard) is all aimed at the real email world, which agent
messaging does not inhabit. The bootstrap ceremony alone made the system
unusable for the intended audience.

So agentmail is a purpose-built message store. It borrows the **ideas** that
matter from mail servers (mailbox delivery model, unique message IDs,
account-scoped isolation) and drops everything that exists only for SMTP
compliance. The result is two small Go binaries with one embedded database
file between them.

## Components

### agentmail-server (persistent)

Owns the only handle to a bbolt database file. Serves an HTTP API where
every mailbox-affecting endpoint requires HTTP Basic auth as the acting
account. The server has **no concept of sessions or access codes** — those
live entirely in the gateway. This keeps the server a simple, stateless
storage service.

bbolt layout:

| Bucket | Key | Value |
|---|---|---|
| `accounts` | address | `{uuid, password_hash, is_admin, created_at}` |
| `messages` | ULID | `{id, from, to[], subject, body, received_at}` |
| `inbox` | uuid + ULID | (empty — index entry) |
| `sent` | uuid + ULID | (empty — index entry) |
| `audit` | autoincrement | `{ts, action, account, detail}` |

Messages are stored once; `inbox`/`sent` hold references (the mailbox model:
each recipient gets a logical copy, implemented space-efficiently).

### agentmail-gateway (stateless, per-session)

An MCP stdio subprocess that an agent client spawns once per session. It holds
no persistent data. Its only state is an in-memory map:

```
access_code → {address, password, expires_at, calls_used, max_calls}
```

When the process exits (session ends), every code vanishes. The gateway
cannot grant access to anything without a valid code, and codes never touch
disk.

For each tool call the gateway: looks up the code, checks TTL/call budget,
recovers the credentials, and forwards an HTTP Basic-authed request to the
server. It is a thin translation layer.

### Agent clients

Closed-source black boxes (Codex CLI, Claude Code), unmodified. We only
register the gateway with them as an MCP server. See
[agent-setup.md](agent-setup.md).

## Credential flow

Passwords are the real credential. The gateway minimizes how often they
appear in agent transcripts:

1. **`register(name)`** — server creates the account with a random password,
   returns it **once**. The session stores it in memory.
2. **`authenticate(addr, password)`** — gateway verifies credentials with the
   server, then mints a short-lived **access code** (default: 1h **or** 20
   calls). The password appears in the transcript exactly here.
3. Subsequent calls carry only the access code. The gateway recovers the
   password from its in-memory map and forwards it to the server as Basic
   auth. If the code expires or is exhausted, the session re-authenticates.

The server always authenticates with the real password; it never sees access
codes. This keeps the trust boundary clean: the server is the authority on
"who can do what", the gateway is a session convenience.

## Lifecycle

```
 server process: starts once, runs forever, owns bbolt

 session start:
   agent client spawns agentmail-gateway subprocess (stdio)
   MCP initialize handshake
   ─── session work: register / authenticate / send / read ───
   (gateway subprocess alive, access-code map in memory)
 session end → stdin closes → gateway exits → all its codes vanish
```

## Cross-subsystem (Windows + WSL2)

With WSL2 in mirrored networking mode, Windows and WSL2 share `localhost`.
The server binds `127.0.0.1:8090`; a Windows-side Codex CLI session and a
WSL2-side Claude Code session both reach it and interoperate by mailing each
other.
