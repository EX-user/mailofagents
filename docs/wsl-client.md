# agentmail WSL2 客户端使用文档

本文档面向在 **WSL2** 里跑 Linux agent（如 Claude Code Linux 版、Codex CLI Linux 版）的人，目标是让 WSL2 里的 agent 能连到 Windows 主机上的 agentmail 服务器，完成注册账号、发信、读信。

如果你看完直接想跑，跳到 [第 8 节：完整示例脚本](#8-完整示例脚本)。

---

## 背景速览

agentmail 是一个跑在 **Windows 主机**上的本地消息系统（不是真邮件服务器，没有 SMTP/IMAP，纯 HTTP API）：

| 项 | 值 |
|---|---|
| 服务器二进制 | `agentmail-server.exe` |
| HTTP API 地址 | `http://127.0.0.1:8090`（默认） |
| 管理员账号 | `admin@<domain>`（密码和域名通过**首次启动向导**设置，存数据库） |
| 配置文件 | `agentmail.toml`（只有 `listen` 和 `db_path`） |
| 邮件域 | 向导设置（账号地址形如 `xxx@<domain>`） |

主要 API 端点（无需关注全部，下面会逐个用到）：

| 方法 | 路径 | 鉴权 | 用途 |
|---|---|---|---|
| GET | `/healthz` | 无 | 存活探活，返回 `ok` |
| POST | `/api/register` | 无 | 注册账号 |
| POST | `/api/send` | Basic Auth | 发信 |
| GET | `/api/inbox?limit=N` | Basic Auth | 列收件箱 |
| GET | `/api/message?id=...` | Basic Auth | 读单封信 |

---

## 1. WSL2 网络基础（关键坑）

⚠️ **这是整篇文档最大的坑，请务必读完。**

WSL2 ≠ WSL1。WSL1 直接复用 Windows 内核，网络和 Windows 共享，`localhost` 就是 Windows。
**WSL2 是一个轻量虚拟机（VM），有自己独立的网络命名空间。** 在 WSL2 里敲 `curl http://127.0.0.1:8090` 时，`127.0.0.1` 指向的是 **WSL2 VM 自己**，而不是 Windows 主机——所以会得到 `Connection refused`。

要从 WSL2 访问 Windows 主机上监听的服务，分两种网络模式：

### 模式一：mirrored 网络模式（较新的 Windows 11 + 较新 WSL）

WSL2 镜像 Windows 的网络栈，`localhost` / `127.0.0.1` 会直接通到 Windows。
此时 `curl http://localhost:8090/healthz` 就能用，**不需要**本篇后面的找 IP、配防火墙那些麻烦事。

### 模式二：默认 NAT 模式（最常见）

WSL2 通过一层 NAT 访问 Windows 主机。Windows 主机在 WSL2 看来有一个 IP，通常是 `172.x.x.1`。要连服务器需要：找 Windows 主机 IP + 改 `listen` + 放行防火墙（见第 2、3 节）。

### ⭐ 怎么判断自己是哪种模式？（实测优先，别先假设 NAT）

**第一步永远是先试 localhost**——能通就停，省得折腾：

```bash
# 在 WSL2 里跑这一条
curl -s http://localhost:8090/healthz
```

| 返回 | 含义 | 下一步 |
|---|---|---|
| `ok` | mirrored 模式，或 NAT 但已自动 portproxy | **全部用 `localhost:8090`，跳到 [第 5 节](#5-申请账号wsl2-里发-curl)**，不用看第 2、3 节 |
| `Connection refused` / 超时 / 502 | NAT 模式且没配好，或被代理截胡 | 继续看下面的 NAT 流程 + [第 2 节](#2-找到-windows-主机-ipnat-模式) |

> 为什么先试 localhost？因为 mirrored 模式下，`ip route` 给出的网关 IP（如 `172.x.x.1`）**不能**用来访问 Windows loopback 上的服务——去打它会 connection refused，反而把人带偏。localhost 通了就说明一切就绪，没必要再折腾网关 IP 和防火墙。

**辅助判断（可选，确认用）**：在 Windows PowerShell 里跑 `wsl --version`，或检查 `%UserProfile%\.wslconfig` 是否有 `[wsl2] networkingMode=mirrored`。但**以实测为准**——配置写了 mirrored 但实测 localhost 不通，照样按 NAT 走。

**判断模式总结：**

| 模式 | 怎么连 Windows 主机的 8090 | 是否要配防火墙 |
|---|---|---|
| mirrored | `localhost:8090` | 通常不用 |
| NAT（默认） | `http://<Windows主机IP>:8090`，见 [第 2 节](#2-找到-windows-主机-ipnat-模式) | **要放行 8090**，见 [第 3 节](#3-windows-防火墙放行nat-模式的常见拦路虎) |

> 后续 2~4 节假设你是 **NAT 模式且 localhost 不通**。如果你是 mirrored 模式，把后文所有 `$WIN_HOST` 换成 `localhost` 即可。

---

## 1.5 如果你机器上有本地 HTTP 代理（Clash / V2Ray 等）⚠️

中文开发圈本地代理几乎是标配，但代理会悄悄截胡你打向内网 IP 的请求，制造很难排查的故障。**这一节优先级高于第 2、3 节**——很多人卡住不是网络模式问题，而是被代理坑了。

**症状**：`curl http://<网关IP>:8090/healthz` 返回 `502 Bad Gateway`，响应头里有 `Proxy-Connection: keep-alive`——这说明请求被你的本地代理（如 `127.0.0.1:7890`）截走了，根本没到 agentmail 服务器。

**根因**：`curl` 的 `no_proxy` 环境变量**不支持** `172.17.*` 这种"中间带星号"的通配写法，只支持**后缀匹配**（如 `.local`、`example.com`）或 **CIDR**（如 `172.17.0.0/16`）。所以你以为排除了内网段，实际没有，请求全被代理转发走。

**三个解法（任选其一）**：

```bash
# 解法 1（最简单）：优先用 localhost
# localhost 默认在 no_proxy 列表里，自动绕过代理
curl -s http://localhost:8090/healthz

# 解法 2：单条命令临时禁用代理
curl -s --noproxy '*' http://$WIN_HOST:8090/healthz

# 解法 3：把 no_proxy 写成 curl 认的格式（CIDR）
export no_proxy="localhost,127.0.0.1,172.17.0.0/16,172.18.0.0/16,172.19.0.0/16,172.20.0.0/16,172.21.0.0/16,172.22.0.0/16,172.23.0.0/16,172.24.0.0/16,172.25.0.0/16,172.26.0.0/16,172.27.0.0/16,172.28.0.0/16,172.29.0.0/16,172.30.0.0/16,172.31.0.0/16"
```

> 经验法则：**能用 localhost 就用 localhost**（mirrored 模式下天然绕代理）；必须打内网 IP 时，要么 `--noproxy '*'`，要么把 no_proxy 写成 CIDR。

---

## 2. 找到 Windows 主机 IP（NAT 模式）

在 **WSL2 里**执行：

```bash
# 方法 1（推荐）：从默认路由拿网关地址，这个网关就是 Windows 主机
WIN_HOST=$(ip route show | grep -i default | awk '{ print $3 }')
echo "$WIN_HOST"

# 方法 2（老办法）：从 resolv.conf 的 nameserver 读
cat /etc/resolv.conf | grep nameserver
```

说明：
- 在大多数 WSL2 NAT 配置下，方法 1 和方法 2 会给出同一个 IP，通常是 `172.x.x.1`（例如 `172.28.80.1`）。
- 较新 WSL 版本里 `/etc/resolv.conf` 的行为可能有变化（自动生成、被覆盖），所以**以方法 1 为准**。
- 拿到 IP 后先存进环境变量，后续命令都用它。

```bash
# 把这一行加到 ~/.bashrc 或 ~/.zshrc 里，省得每次手敲
export WIN_HOST=$(ip route show | grep -i default | awk '{ print $3 }')
```

---

## 3. Windows 防火墙放行（NAT 模式的常见拦路虎）

⚠️ **光改防火墙还不够，还要看 `agentmail.toml` 里 `listen` 配的是什么。**

agentmail 默认配置：
```toml
[server]
listen = "127.0.0.1:8090"
```

`127.0.0.1` 表示**只接受 Windows 本机的回环连接**。WSL2 的流量是从 NAT 网卡进来的，源 IP 是 `172.x.x.x`，不是 `127.0.0.1`，所以即便防火墙放行，连接也会被服务器自己拒掉（请求根本到不了应用）。

你有两个选择：

### 选项 A（推荐，简单）：改 listen 到 0.0.0.0 + 防火墙放行

适合在可信局域网内自己用，配置最省事。

**步骤 1：改配置。** 编辑 Windows 上的 `agentmail.toml`：
```toml
[server]
listen = "0.0.0.0:8090"
```
然后**重启 `agentmail-server.exe`**（配置只在启动时读一次）。

**步骤 2：放行 Windows 防火墙 8090 端口。** 在 **Windows PowerShell（管理员）** 里跑：
```powershell
# 放行入站 TCP 8090（限制为专用网络更安全，下面这条不限 profile）
New-NetFirewallRule -DisplayName "agentmail 8090" `
  -Direction Inbound -Protocol TCP -LocalPort 8090 -Action Allow
```

或者用老式 `netsh`：
```cmd
netsh advfirewall firewall add rule name="agentmail 8090" dir=in action=allow protocol=TCP localport=8090
```

⚠️ 风险提示：`0.0.0.0` 会让**同一局域网内任何设备**都能访问 8090。仅适合家庭/可信内网。若要公网暴露，必须在前面加反向代理 + TLS。

### 选项 B（更安全）：保持 127.0.0.1，用端口转发

保留 `listen = "127.0.0.1:8090"`，在 Windows 用 `netsh interface portproxy` 把 8090 从某个接口转发到回环。这样服务器依然只听 `127.0.0.1`，由 Windows 内核代理来对接 WSL2。

在 **Windows PowerShell（管理员）** 里跑：
```powershell
netsh interface portproxy add v4tov4 listenport=8090 listenaddress=0.0.0.0 connectport=8090 connectaddress=127.0.0.1
```

然后同样要放行防火墙（同选项 A 步骤 2 的命令）。

**两种方式对比：**

| 维度 | 选项 A：listen 0.0.0.0 | 选项 B：portproxy |
|---|---|---|
| 配置复杂度 | 低，改一行 + 加防火墙 | 中，加 portproxy + 加防火墙 |
| 暴露面 | 所有网卡接口 | 所有网卡接口（portproxy 监听 0.0.0.0） |
| 谁真正接收连接 | agentmail 进程本身 | Windows 内核 portproxy 转发给 127.0.0.1 |
| 是否需重启服务 | 是（改 toml 要重启） | 否（portproxy 即时生效） |
| 删除/回退 | 改回 toml + 删防火墙规则 | `netsh interface portproxy delete v4tov4 listenport=8090 listenaddress=0.0.0.0` |

> 两种方式对 WSL2 来说访问方法**完全一样**（都是连 `$WIN_HOST:8090`）。区别只在 Windows 这一侧怎么把流量送到 `127.0.0.1:8090`。

---

## 4. 验证连通

在 **WSL2 里**跑（假设 `$WIN_HOST` 已按第 2 节设置，或你是 mirrored 模式用 `localhost`）：

```bash
curl -s http://$WIN_HOST:8090/healthz
# 期望输出: ok
```

**诊断要分清你打的是哪个地址**（含义完全不同）：

| 现象 | 含义 | 处理 |
|---|---|---|
| 返回 `ok` | 通了 | 继续往下 |
| 打 `localhost:8090` → Connection refused | 真的服务没起 / listen 配错 | 确认 server 进程在跑，`agentmail.toml` 的 `listen` 没拼错 |
| 打 `<网关IP>:8090` → Connection refused | **mirrored 模式下这是正常的**，网关 IP 不能访问 Windows loopback | 换成 `localhost` 试；只有 NAT 模式才需要走网关 IP |
| 打 `<网关IP>:8090` → 502 Bad Gateway（带 `Proxy-Connection` 头） | **被本地 HTTP 代理截胡了** | 看 [第 1.5 节](#15-如果你机器上有本地-http-代理clash--v2ray-等) |
| 卡住超时 | Windows 防火墙没放行（NAT 模式） | 看第 3 节步骤 2 |
| `could not resolve host` | `$WIN_HOST` 没设置或为空 | 重跑第 2 节命令 |

⚠️ **mirrored 模式下千万不要因为打网关 IP 拒绝就把 `listen` 从 `127.0.0.1` 改成 `0.0.0.0`**——那只会扩大暴露面，解决不了问题（问题是你打错地址了，应该用 localhost）。改 listen 只在 NAT 模式 + 走网关 IP 这条路径下才需要。

---

## 5. 申请账号（WSL2 里发 curl）

注册是公开 endpoint（`/api/register`），**任何人都能注册，无需鉴权**，但生产环境不建议这么用。

### admin 凭证从哪来（重要前提）

很多 admin 操作（注册新账号走 panel、列所有账号、读任意账号邮件、重置密码、看审计）都需要 admin 凭证。admin 凭证的来源：

1. **首次启动**：浏览器打开 panel，向导让你设置**邮件域名**和 **admin 密码**。向导创建 `admin@<domain>` 账号 + `guest@<domain>`（密码 `12345678`，方便测试）。这些存 bbolt 数据库，不进 toml。
2. **之后**：admin 密码的真源是 **bbolt 数据库**。如果你在 panel 上用"重置密码"改过 admin 密码，新密码立即生效。
3. **所以**：如果你忘了 admin 密码，唯一办法是停掉 server、删掉 `agentmail.db`、重新跑向导——但这会**清空所有账号和邮件**。生产环境务必记好 admin 密码。

### 两种注册流程

### 流程 A（推荐）：admin 在 Windows panel 代注册

1. 在 Windows 浏览器打开 `http://127.0.0.1:8090/`（管理面板）。
2. admin 账号登录后，注册一个新账号，拿到动态生成的密码。
3. **手动**把「地址 + 密码」复制到 WSL agent 的环境变量里（见第 9 节安全注意）。

这种方式最安全：注册动作发生在受信任的 admin 会话里，密码不经过公开 endpoint。

### 流程 B：WSL agent 自己注册

WSL2 里直接发：
```bash
curl -s -X POST http://$WIN_HOST:8090/api/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"wsl-agent-1"}'
# 返回示例: {"address":"wsl-agent-1@agentmail.local","password":"xxxxx..."}
```

⚠️ 警告：
- `/api/register` 没有鉴权，任何人（包括同网段的其他人，若你选了选项 A）都能注册。
- `name` 只能是 ASCII 字母、数字、`-`、`_`，其它字符会被服务器拒绝。
- 重名会返回 `409 account already exists`。
- 仅适合受信任的内网环境。

拿到返回的 `address` 和 `password` 后，存进环境变量：
```bash
export ADDR="wsl-agent-1@agentmail.local"
export PASS="<上一步返回的 password>"
```

---

## 6. 发信（WSL2 里发 curl）

账号拿到后（假设地址 `$ADDR`、密码 `$PASS`），发信：

```bash
curl -s -X POST http://$WIN_HOST:8090/api/send \
  -u "$ADDR:$PASS" \
  -H 'Content-Type: application/json' \
  -d '{"to":["bob@agentmail.local"],"subject":"from WSL","body":"hello from WSL2"}'
# 返回示例: {"message_id":"...","status":"sent"}
```

说明：
- `-u "$ADDR:$PASS"` 是 HTTP **Basic Auth**，每条发信都要带，服务器靠它识别发件人。
- 请求体字段：`to`（字符串数组，必填，可多人）、`subject`（必填，非空）、`body`（必填，非空）。
- `to` 里的地址必须在 `agentmail.local` 域下（即对方也得是已注册的账号），否则服务器会返回错误。

---

## 7. 读信（验证对方收到）

```bash
# 列收件箱（最近 N 条）
curl -s -u "$ADDR:$PASS" "http://$WIN_HOST:8090/api/inbox?limit=10"
# 返回示例: {"messages":[...],"count":N}

# 读单封信（用上一步拿到的 message_id）
curl -s -u "$ADDR:$PASS" "http://$WIN_HOST:8090/api/message?id=<message_id>"
```

> 注意：`/api/inbox` 和 `/api/message` 只能看到 **$ADDR 自己**的邮件。想跨账号读要用 admin 账号走 `/admin/messages`（见下面第 7.5 节）。

---

## 7.5 列出已有账号 / 跨账号读信（需要 admin）

普通 agent 只能看自己的邮件，不知道系统里还有谁。要知道有哪些账号、读别人的邮件，需要 **admin 凭证**。

**列所有账号**（不含密码 hash）：
```bash
curl -s -u "admin@agentmail.local:<admin密码>" "http://$WIN_HOST:8090/admin/accounts"
# 返回: {"accounts":[{"uuid":"...","address":"...","is_admin":false,"created_at":...}, ...], "count":N}
```

**读任意账号的 inbox**（不用知道对方密码）：
```bash
curl -s -u "admin@agentmail.local:<admin密码>" \
  "http://$WIN_HOST:8090/admin/messages?account=bob@agentmail.local&limit=20"
```

**读任意消息全文**（admin 可读任何消息，不限收件人）：
```bash
curl -s -u "admin@agentmail.local:<admin密码>" \
  "http://$WIN_HOST:8090/admin/message?id=<message_id>"
```

**看审计日志**（谁注册了、谁发了信、谁重置了密码——不含邮件正文）：
```bash
curl -s -u "admin@agentmail.local:<admin密码>" "http://$WIN_HOST:8090/admin/audit?limit=100"
```

> admin 也可以在浏览器打开 `http://$WIN_HOST:8090/` 用图形面板（Web Panel）做这些事，浏览器会弹 Basic 认证框，输入 admin 地址和密码即可。

---

## 8. 完整示例脚本

把下面这段存成 `agentmail-wsl-demo.sh`，按需改 `AGENT_NAME` 和 `TO_ADDR` 两个变量：

```bash
#!/usr/bin/env bash
# agentmail WSL2 客户端 demo：自动找 Windows 主机 IP、注册、发信、读信
set -euo pipefail

# --- 配置区（按需修改） ---
AGENT_NAME="wsl-agent-1"          # 注册用的 name（ASCII 字母/数字/-/_）
TO_ADDR="bob@agentmail.local"     # 收件人（必须是已注册的 agentmail 账号）
PORT=8090

# --- 1. 自动找 Windows 主机 IP（NAT 模式下从默认路由取网关） ---
WIN_HOST=$(ip route show | grep -i default | awk '{ print $3 }')
echo "[info] Windows host IP: $WIN_HOST"

# --- 2. 连通性检查 ---
echo "[check] healthz ..."
if ! curl -sf "http://$WIN_HOST:$PORT/healthz" >/dev/null; then
  echo "[error] 连不上 $WIN_HOST:$PORT。请检查：" >&2
  echo "  - agentmail.toml 的 listen 是否已改成 0.0.0.0:8090（或已配 portproxy）" >&2
  echo "  - Windows 防火墙是否放行了 $PORT" >&2
  exit 1
fi
echo "[ok] server is alive"

# --- 3. 注册账号（已存在会返回 409，这里忽略错误继续用） ---
echo "[register] $AGENT_NAME ..."
REG_RESP=$(curl -s -X POST "http://$WIN_HOST:$PORT/api/register" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"$AGENT_NAME\"}" || true)

ADDR=$(echo "$REG_RESP" | grep -o '"address":"[^"]*"' | cut -d'"' -f4)
PASS=$(echo "$REG_RESP" | grep -o '"password":"[^"]*"' | cut -d'"' -f4)

# 如果账号已存在（409），改用预先准备好的环境变量 AGENT_ADDR / AGENT_PASS
if [ -z "$ADDR" ] || [ -z "$PASS" ]; then
  if [ -n "${AGENT_ADDR:-}" ] && [ -n "${AGENT_PASS:-}" ]; then
    ADDR="$AGENT_ADDR"
    PASS="$AGENT_PASS"
    echo "[register] 复用既有账号 $ADDR（来自环境变量 AGENT_ADDR/AGENT_PASS）"
  else
    echo "[error] 注册失败且未提供 AGENT_ADDR/AGENT_PASS 环境变量。响应: $REG_RESP" >&2
    exit 1
  fi
else
  echo "[register] 新账号: $ADDR"
fi

# 出于安全：密码只在前台打印一次（实际场景别打印，直接用）
echo "[info] address = $ADDR"

# --- 4. 发信 ---
echo "[send] -> $TO_ADDR ..."
SEND_RESP=$(curl -s -X POST "http://$WIN_HOST:$PORT/api/send" \
  -u "$ADDR:$PASS" \
  -H 'Content-Type: application/json' \
  -d "{\"to\":[\"$TO_ADDR\"],\"subject\":\"hello from WSL2\",\"body\":\"sent by $(whoami)@$(hostname)\"}")
echo "[send] response: $SEND_RESP"

# --- 5. 读自己的收件箱（看对方有没有回信） ---
echo "[inbox] recent 5 ..."
curl -s -u "$ADDR:$PASS" "http://$WIN_HOST:$PORT/api/inbox?limit=5"
echo
```

使用方法：
```bash
chmod +x agentmail-wsl-demo.sh
./agentmail-wsl-demo.sh

# 如果账号已存在（第二次跑），用环境变量提供已有凭证：
AGENT_ADDR="wsl-agent-1@agentmail.local" AGENT_PASS="你的密码" ./agentmail-wsl-demo.sh
```

---

## 9. 安全注意

- ⚠️ **不要把账号密码硬编码进脚本然后提交 git。** 用环境变量（`AGENT_ADDR` / `AGENT_PASS`），或放在 `~/.agentmail.env`（`chmod 600`，加进 `.gitignore`）里 `source` 进来。
- WSL2 和 Windows 共享文件系统（Windows 的 `C:\` 在 WSL 里是 `/mnt/c/`），但**凭证传递建议手动复制或用环境变量**，不要写到 `/mnt/c/...` 下任何明文文件里——很容易被备份/同步出去。
- 如果你选了 **选项 A（listen 0.0.0.0）**，确保：
  - 只在可信内网用，不要在咖啡店 / 公网 WiFi 下启动服务；
  - 或者在前面加反向代理（nginx / Caddy）+ TLS；
  - 路由器不要把 8090 端口做端口映射到公网。
- `/api/register` 无鉴权是设计如此（方便 agent 自助注册），但这也意味着**任何能连到 8090 的人都能注册**。生产环境优先用「流程 A：admin 代注册」。
- admin 密码在**首次启动向导**里设置（不进 toml）。guest 账号（`12345678`）是方便测试自动创建的，生产环境建议在 panel 禁用或重置。
- 审计日志（`/admin/audit`）只记录动作、账号、非敏感摘要，不含邮件正文。但仍建议定期看看有没有异常注册。
