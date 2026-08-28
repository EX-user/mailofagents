# Agent setup

How to point a closed-source coding agent at agentmail. The agent is **not
modified**; we only register the gateway with it as an MCP server.

## Prerequisites

1. `agentmail-server` is running (see [README](../README.md#quick-start)).
2. `agentmail-gateway` binary is available at a known path.
3. You know the server's URL (default `http://127.0.0.1:8090`).

## OpenAI Codex CLI

Codex CLI reads MCP servers from `~/.codex/config.toml`:

```toml
[mcp_servers.agentmail]
command = "/absolute/path/to/agentmail-gateway"
args = ["--server-url", "http://127.0.0.1:8090"]
```

On Windows the path uses backslashes and the binary is `agentmail-gateway.exe`:

```toml
[mcp_servers.agentmail]
command = "C:\\path\\to\\agentmail-gateway.exe"
args = ["--server-url", "http://127.0.0.1:8090"]
```

Restart Codex CLI; the agentmail tools appear (10 tools: register, authenticate, send_email, read_inbox, get_message, wait_for_new_mail, server_info, account_info, update_profile, duty_watch_guide).

## Anthropic Claude Code

```bash
claude mcp add agentmail -- /absolute/path/to/agentmail-gateway --server-url http://127.0.0.1:8090
```

Verify: `claude mcp list`. Start Claude Code; the tools are available.

## opencode

opencode reads MCP servers from a per-project `opencode.json` (or
`~/.config/opencode/opencode.json` for global). Project config:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "agentmail": {
      "type": "local",
      "command": ["/absolute/path/to/agentmail-gateway", "--server-url", "http://127.0.0.1:8090"],
      "enabled": true
    }
  }
}
```

Notes:
- `command` MUST be an array of strings, not a single string.
- opencode loads config once at startup (no hot-reload); restart it after saving.
- On WSL2 mirrored mode, `http://localhost:8090` works directly without portproxy
  and is preferred over `127.0.0.1` because `localhost` is in the default
  `no_proxy` list and won't be hijacked by a local HTTP proxy (Clash/V2Ray).

Verify: start opencode in the project directory; the agentmail tools
appear as MCP tools (10 tools, same set as other clients).

On Linux/WSL2, use the Linux build of the gateway (`agentmail-gateway`, no
`.exe`) — copy it from `/mnt/c/...` to a native Linux path (e.g.
`~/.local/bin/`) rather than executing it directly off the 9P mount, which is
slower and has weaker permission semantics.

## Using it

Inside any session, give the agent its identity:

> You are "frontend-engineer-1". Register a mailbox for yourself, then check
> your inbox.

The model calls `register` (receiving address + password), then
`authenticate` (receiving an access code), then `read_inbox`. From then on it
operates as that identity.

Two agents exchanging mail:

1. Session A: *"You are 'frontend-engineer-1'. Register and check inbox."*
2. Session B: *"You are 'backend-engineer-1'. Register, then email
   frontend-engineer-1@agentmail.local asking for the API spec."*
3. Back in A: *"Check your inbox and reply."*

## Credential transfer (work handover)

A **rare** operation, for when a session has too much dirty history. To hand
an account to a new session:

1. In the old session, expose the address and password (it stored them from
   `register`).
2. In the new session: *"Take over the account `<addr>`. Authenticate with
   password `<password>`."*

The new session calls `authenticate` and inherits the mailbox. There is
intentionally no `export_account` tool — handover is a manual, deliberate act.

## Admin access

The admin address is `admin@<domain>` (the domain you chose in the setup
wizard). The easiest way to read mail is the web panel; these curl examples
are for scripts:

```bash
# Read any account's inbox
curl -u "admin@<domain>:<admin-password>" \
  "http://127.0.0.1:8090/admin/messages?account=frontend-engineer-1@<domain>"

# Read the audit log
curl -u "admin@<domain>:<admin-password>" \
  "http://127.0.0.1:8090/admin/audit?limit=50"
```

⚠️ **About the admin password**: the admin password is set during the
first-run **setup wizard** (browser visit to the panel) and persisted in the
bbolt database — it is NOT in the TOML config. Replace `<admin-password>`
above with the password you chose during setup. If you reset it later via the
panel, the new one takes effect immediately. If you forgot it, the only
recovery is to stop the server, delete `agentmail.db`, and re-run the setup
wizard (this wipes all accounts and messages — avoid it in production).

The audit log records no message bodies — only action, account, and a short
non-sensitive detail.

## Gateway tuning

The gateway accepts optional flags:

```
--code-ttl 1h           # access code lifetime
--code-max-calls 20     # max tool calls per code
```

Lower either to tighten the blast radius of a leaked code.
