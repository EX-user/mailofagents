# App 端消息通知 · 设计定稿（v0.6.30 目标）

状态：待 alice 审 → 呈上级。作者：Devi。裁定输入：alice 01M11J45M（路径 C 批准+两点决策+安全补两条）、Felix 01M11J05C（三路径初评）、上级令 01M11HYJ。

## 0. 目标与非目标

目标：用户「不开 app 也能知道有信」——通知只做信号，不做阅读替代。
非目标（首版）：正文/主题下发、已读回执联动推送、iOS/APNs、厂商通道实装。

## 1. 路径：C 混合（已裁定）

| 层 | 技术 | 覆盖面 | 首版定位 |
|---|---|---|---|
| 主 | Web Push（VAPID）经 TWA Chrome 内核转系统通知 | 标准机型 | 全量打通 |
| 兜底 | 应用内轮询→横幅+角标 | WebView fallback / 国产 ROM | 最小实现（Felix 侧） |
| 观察 | 厂商通道（小米/华为/OPPO） | 国内 ROM 保底 | 不投入，按上级真机反馈再议 |

选型理由：A 单挑 Web Push 把成败押在最弱一环（上级手机即 WebView fallback 类 ROM）；B 原生通道工程量与厂商碎片化不配体量；C 分层降级风险。

## 2. 后端设计（我侧）

### 2.1 存储（bbolt）

- 新 bucket `pushSubs`，key=`account \0 subHash`（subHash=sha256(endpoint)），value=Subscription JSON：
  `{endpoint, keys{p256dh,auth}, created_at, last_used}`。
- 同账户多设备各自独立条目；endpoint 失效（410/404 回推）时删除该条目。
- 删除账户级联清空其订阅。

### 2.2 VAPID

- 密钥对生成工具 `cmd/pushkeygen`（一次性运维动作，私钥进部署配置不入库）；公钥下发的 `/api/push/vapid-key` 匿名可取（SW 订阅前必需）。
- `.gateway-min-version` 机制不受影响；floor 维持 v0.6.15。

### 2.3 API

- `POST /api/push/subscribe`：body=PushSubscription JSON。**必须持本账户凭证**（Bearer session token 或 Basic），订阅所有权校验=凭证账户==订阅归属账户（安全补②，防替他人订阅骚扰）。同 endpoint 幂等覆盖。
- `DELETE /api/push/subscribe`：撤当前设备订阅。
- 登出**保留**订阅（记住登录场景多设备友好）——与 session token 语义一致；改密杀全部时不涉推送（订阅凭证在端侧 SW 持有，服务端仅存 endpoint）。

### 2.4 触发与节流

- 钩子位置：本地投递成功路径（server 写信后收件人落箱成功才推，中转失败不推）。
- 节流聚合：同账户 60s 聚合窗内合并为单条「有新信」推送（防轰炸）。
- **免打扰时段裁决在服务端**（决策①）：账户设置静默时段内只入队不发，时段结束补发一条汇总信号；客户端时钟不可信。

### 2.5 Payload 红线

只含：`{unread_count, from_name}`——零正文、零主题、零附件元数据、**零收件账户地址**（裁定 01M11J9FY：推送中介不可信，地址=泄露「谁收到信」；SW 本地自知账户）。from_name 仅显示名非地址。

### 2.6 安全面（补两条均落地）

1. 推送端点限速：订阅接口 per-account+per-IP 双限速，防滥用我方服务器当轰炸器。
2. 所有权校验：见 2.3。
3. 发送侧：web push 标准 TTL（如 1h）+ 仅 HTTPS endpoint + 订阅响应 410 自动清理。

## 3. 前端（Felix/Iris 侧，摘要待他们细化）

- SW 文件 + 权限请求 UX + 偏好页开关（Iris 可随车）；通知点击深链落到具体信件。
- fallback 轮询兜底最小横幅+角标。

## 4. 与既有体系整合点

- token 体系（241156a）：订阅走 Bearer 优先、Basic 回退，无效 Bearer 不落 Basic 的现有中间件直接复用。
- 审计：`ActionPushSubscribe`/`ActionPushRevoke` 两枚举顺手兑现审计枚举小件（此前 alice 建议 ActionSessionToken 时同批提出风格）。
- 推送触发点在本侧 server 写信路径，gateway 分支不改。

## 5. 排期（同意 Felix）

v0.6.29 已有主题撕裂+附件覆写在途，本件排 **v0.6.30**。里程碑：
M1 订阅存储+VAPID 端点+ownership/限速 → M2 投递钩子+聚合+免打扰 → M3 前端 SW/UX（Felix/Iris） → M4 真机验证（上级 ROM 属关键验收机）。

## 6. 审定记录（alice 01M11J9FY：整体放行）

- [x] 方案整体放行
- [x] payload 字段集=**{unread_count, from_name}**（去 account，防推送中介侧地址泄露）
- [x] 免打扰**默认关闭**（上级要「能收到」为先；偏好页开关自选启用）
- [x] DND 补发=静默结束补发**单条汇总**「静默期间收到 N 封新信」，不逐封重放
