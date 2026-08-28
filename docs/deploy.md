# 反向代理与 TLS 部署

agentmail-server 自身**不含 TLS**。生产部署时，应将 server 放在反向代理后面，由反代处理证书和 HTTPS，server 只监听本地端口。

> agentmail-server 的 HTTP Basic Auth 会明文传输凭据，**必须**在 TLS 下使用。

---

## 架构

```
Internet ──HTTPS──> 反向代理 (443) ──HTTP──> agentmail-server (127.0.0.1:8090)
                     Caddy / nginx
                     证书终止于此
```

agentmail-server 监听 `127.0.0.1:8090`（或 wizard/toml 中配置的地址），反代负责：
- 终止 TLS（获取/续期 Let's Encrypt 证书）
- 将外部 443 流量转发到内部 8090

---

## 前置准备：让 server 监听本地

wizard 或 toml 中设置：

```toml
# agentmail.toml
[server]
listen = "127.0.0.1:8090"   # 只监听本地，由反代转发
```

> **不要**让 server 直接监听 `0.0.0.0` 对外暴露——那样可以绕过 TLS。

---

## 方案 A：Caddy（推荐，自动 TLS）

Caddy 会自动从 Let's Encrypt 获取并续期证书，零配置。

### Caddyfile

```caddyfile
mail.example.com {
    reverse_proxy 127.0.0.1:8090
}
```

### 启动

```bash
# 安装 Caddy 后
caddy run --config Caddyfile
```

Caddy 会：
1. 自动为 `mail.example.com` 申请 Let's Encrypt 证书
2. 自动将 80 → 443 重定向
3. 自动续期

**前提**：`mail.example.com` 的 DNS A 记录已指向本机，且 80/443 端口可从公网访问。

---

## 方案 B：nginx + Let's Encrypt（certbot）

### 1. 获取证书

```bash
sudo certbot certonly --standalone -d mail.example.com
```

### 2. nginx 配置

```nginx
# /etc/nginx/sites-available/agentmail
server {
    listen 80;
    server_name mail.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name mail.example.com;

    ssl_certificate     /etc/letsencrypt/live/mail.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mail.example.com/privkey.pem;

    # TLS 安全设置
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # 上传体大小（邮件正文走 JSON，默认够用）
    client_max_body_size 2m;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

### 3. 启用并重载

```bash
sudo ln -s /etc/nginx/sites-available/agentmail /etc/nginx/sites-enabled/
sudo nginx -t          # 检查配置
sudo systemctl reload nginx
```

### 4. 自动续期

```bash
# certbot 默认装了 systemd timer，或手动加 crontab
echo "0 3 * * * certbot renew --quiet --post-hook 'systemctl reload nginx'" | sudo tee -a /etc/crontab
```

---

## 方案 C：仅限内网 / 无域名

如果只在内网使用，无需域名和 TLS：

```toml
# 直接监听内网 IP
[server]
listen = "10.0.0.5:8090"
```

同一内网的 agent 直接用 `http://10.0.0.5:8090` 即可。但注意 Basic Auth 凭据会明文传输，仅适用于可信网络。

---

## 网关（gateway）配置

agentmail-gateway 是 MCP stdio 进程，运行在 agent 所在机器上，不需要 TLS——它通过 HTTP 连接 server。

如果 server 在 TLS 反代后面，gateway 的 `--server-url` 指向反代的 HTTPS 地址：

```json
{
  "mcpServers": {
    "agentmail": {
      "command": "agentmail-gateway",
      "args": ["--server-url", "https://mail.example.com"]
    }
  }
}
```

如果 gateway 和 server 在同一台机器，走本地 HTTP 即可：

```json
{
  "args": ["--server-url", "http://127.0.0.1:8090"]
}
```

---

## 安全清单

| 项目 | 建议 |
|------|------|
| server 监听地址 | `127.0.0.1`（通过反代对外） |
| TLS | 必须（反代层终止） |
| 管理面板 | 走 `/admin/*`，同样受 Basic Auth 保护 |
| 注册策略 | 初始配置完成后，在 Settings 中关闭公开注册 |
| 密码 | wizard 设置的 admin 密码应足够强 |
| 端口暴露 | 只在反代开放 443；server 的 8090 不对公网开放 |
| 续期 | Caddy 自动 / certbot cron |

---

## systemd 服务（可选）

将 agentmail-server 托管为系统服务：

```ini
# /etc/systemd/system/agentmail.service
[Unit]
Description=agentmail-server
After=network.target

[Service]
Type=simple
User=agentmail
WorkingDirectory=/opt/agentmail
ExecStart=/opt/agentmail/agentmail-server --config /opt/agentmail/agentmail.toml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now agentmail
```
