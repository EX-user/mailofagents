# Overview 模块交接文档（car3，v0.6.21）

> 面向接手「管理视图（数据可视化）」的新同事。概览页（从属总表+连接图）已从
> manage.js 拆为独立模块 `internal/server/static/overview.js`（~356 行）。

## 边界（硬约束，机器门禁把关）

- 只许 `import ... from "./core.js"`（`scripts/audit_frontend_imports.sh`）。
- 跨模块交互只走 DOM CustomEvent（`scripts/audit_free_identifiers.py` 拦闭包泄漏）。
- 零构建：无打包器/无 TS；直接 ES module，`index.html` 一模块一 `<script type="module">`。

## 数据来源（全部现成，无需后端改动）

| 用途 | 端点 | 说明 |
|---|---|---|
| 总表+图数据 | `GET /api/mgmt/subs-overview?days=7\|30\|0` | `{window_days, subs[], graph:{nodes,edges}}`；0=全部时间 |
| 活跃度偏好 | `GET /api/profile/self` | `prefs.livenessStrongHours/WeakHours`（模块自取） |
| 图渲染库 | `/static/vis-network.min.js` | 惰性加载（`loadVisNetwork()`），勿在首屏引入 |

`subs[]` 字段：address/signature/last_in_at/last_out_at/count_in_7d/count_out_7d/
avg_len_in/avg_len_out/top_contacts[]/last_read_at。
`graph.nodes`：{address, kind: self|sub|external, volume}；`graph.edges`：
{a, b, a_to_b, b_to_a, last_at}（last_at 为全时段）。

## 事件面（本模块的对外契约）

- 监听：`overview:entered`（切入/自动加载+refit）、`overview:refresh`、`overview:reset`、`i18n:change`
- 发出：`nav:activate {tab:"accounts"}`、`mgmt:browse-account {address, folder?}`（manage.js 负责选账号+加载）

## 现有可视化语义（改动前先读）

- **liveness 三档**（mgmtIsActive）：绿=强窗内有收发；黄=弱窗内仅读；灰=闲置。窗口小时数来自用户偏好。
- **边映射**（graphScale）：`k = c/max`（线性）或 `log(1+c)/log(1+max)`（对数）；宽 `0.3+2.5k`、
  透明度 `0.15+0.85k`、箭头 `0.3+0.7k`、**数字颜色 `0.35+0.65k`**（随强度，与边同映射）。
- **三悬浮钮**（graphPrefs，localStorage `mgmtGraphPrefs`）：映射线性/对数、数字开/关、范围 7d/30d/all。
- 节点标签：`短地址\n{窗口} {volume}`；边点击跳查信（经 `mgmt:browse-account`）。

## 验证流程（提交前必过）

1. `node --check overview.js`（WSL 无 node，用 Windows 侧或中转副本）
2. `bash scripts/audit_frontend_imports.sh` && `python3 scripts/audit_free_identifiers.py`
3. 演示服亲验（本机 8848）+ 预览图送上级裁定后才可上线

## 模块地图（S2 拆分后）

| 模块 | 行数 | 职责 |
|---|---|---|
| app.js | ~2200 | 入口壳：路由/登录/事件中枢/游客页组 |
| compose.js | ~740 | 写信域（含 irt 锚点、cc/收件人 autocomplete） |
| manage.js | ~1285 | 查信+从属+审计域（browse 列表/详情） |
| overview.js | ~356 | 概览+连接图（**新同事接手面**） |
| core.js | — | 唯一共享层：DOM/api/session/toast（无状态） |
