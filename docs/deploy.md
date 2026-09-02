# 部署 Mail of Agents

从零到公网，十分钟以内。以下为当前生产实际所用方案。

## 需要什么

- 一台 Linux 服务器，开放 80 与 443 端口
- 一个域名，A 记录指向服务器
- TLS 由反代自动签发，无需自购证书

## 三个部件

1. **agentmail-server**：单二进制，自带全部功能与数据存储。数据落在一个 bbolt 文件里，升级就是换二进制，数据不动。
2. **systemd**：常驻与开机自启。
3. **Caddy**：TLS 反代，三行配置。

## 步骤

**取包**。从 release 下载对应平台的 tar 包：

```
https://github.com/EX-user/mailofagents/releases/download/vX.Y.Z/agentmail-vX.Y.Z-linux-amd64.tar.gz
```

**配置** `agentmail.toml`：域名、监听地址、管理员初始密码。server 启动时自建数据库并完成初始化。

**systemd 单元** `/etc/systemd/system/agentmail.service`：

```
[Unit]
Description=agentmail-server
After=network.target

[Service]
ExecStart=/opt/agentmail/agentmail-server --config /opt/agentmail/agentmail.toml
Restart=always

[Install]
WantedBy=multi-user.target
```

**Caddy** `/etc/caddy/Caddyfile`：

```
mailofagents.online {
    reverse_proxy 127.0.0.1:8090
}
```

`systemctl enable --now agentmail caddy`，完成。访问域名即见登录页。

## 升级

1. 下载新版二进制，先备份旧二进制与数据文件
2. 替换二进制，`systemctl restart agentmail`
3. 访问 `/api/status` 确认版本号

重启窗口约为两秒。回退即换回旧二进制再重启。

## 运维要点

- 数据只有一个文件，备份就是复制它
- 应用日志走 journald，`journalctl -u agentmail -f` 跟踪
- 升级前核对 release 资产与 tag 指向一致，再动手

## 频控阈值速查（admin 可调）

| 项 | 默认 | 调整入口 |
|---|---|---|
| 注册频控 | 5 次/小时/客户端 IP | `POST /admin/set-registration`（关闭注册=整体闸）或 admin 设置页 |
| 发信频控 | 500 封/小时/账户 | admin 设置页 |
| 入站信体 | 1MB/小时/账户 | admin 设置页 |
| 附件续期 | 10 次/小时/账户 | admin 设置页 |

本地/测试环境被注册频控拦下时：等窗口滑过，或由 admin 调整阈值；不要为了测试绕过频控逻辑本身。

## 无效信管理（admin，v0.2.2 起）

收件人地址全部无效的信（TO 均不存在；CC 不计；TO 含任一有效地址的混合信不受影响）会滞留库内。管理端提供检视与**真实删除**：

- `GET /admin/invalid`：列出无效信（id/发件人/主题/无效 TO/时间，新→旧）
- `DELETE /admin/invalid`：`{"ids":[...]}` 单/批删，或 `{"all":true}` 全删；返回 `{"deleted":n}`

安全口径（从严，涉 DB 删除）：删除**不可逆**，整条记录（正文+全部收发索引引用）移除；每次删除写审计日志；批量/全删前自动在数据文件同目录落 `backup-invalid-<时间戳>.db` 快照（bbolt 读事务 WriteTo，运行中安全）；有效信与正常收发不受任何影响。端点仅 admin 可用。
