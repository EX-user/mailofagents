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
