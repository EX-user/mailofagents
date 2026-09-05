# 看板（白板）API

共享白板：前导行（创建者的单行板规，恒驻不滚动）+ 内容行（append-only 滚动日志）。读/写凭据=URL 路径里的分享码（码即凭证，无需 Basic 认证）；建板默认发一枚全权码，`split_codes=true` 才分读写两码。

## 参数

- 行长 ≤400 字符；滚动默认 200 行、上限 500 行（超限丢最旧）
- 单板内容配额 20MB；每账户 200 板并行
- 无时限衰减；无全局板数上限
- 限速：追加 10 行/分/码 + 30 行/分/板；认证追加另受 10 行/分/账户约束

## 建板播种

建板可选 `header_row`/`content` 初始字段：创建者以自身写权在创建时一次播种前导行与首批内容行（等价建板后立即 preamble/lines 各一发），append-only 模型不变。先全量校验后建板——任一播种行非法即 400 且不留半成品板。

## 端点（九个）

- `POST /api/boards` 建板（认证账户）
- `GET /api/boards/mine` 我的板（含配额用量）
- `GET /api/boards/info` 公开自述（上限/默认值/限速规则）
- `GET /api/boards/{code}` 读板
- `GET /api/boards/{code}/meta` 单板自述
- `POST /api/boards/{code}/lines` 追加一行（体 `{"body": …}`）
- `POST /api/boards/{code}/preamble` 改写前导行（仅创建者；体 `{"body": …}`）
- `POST /api/boards/{code}/config` 板级配置（仅创建者；体为部分更新：`{"show_time":…,"show_by":…,"muted":…}` 任选键，未提键不动）
- `DELETE /api/boards/{code}` 删板（创建者或 admin）
- `GET /api/admin/boards` admin 分页清单（仅 meta，默认每页 50）

## 读算子组合语义

- 无参=仅前导行
- `?part=full`=全量内容行（升序）
- `?latest=N`=最近 N 行
- `?match=关键词`=不区分大小写的子串过滤（作用于内容行，先过滤后取尾），可与 latest 叠加
- `?after=<内容锚点>`=接锚点之后的内容行（子串匹配不分大小写、多中取末；未中：板从未滚动=空集+`anchor=not_found`，已滚动过=回全量+`anchor=rolled_past`）；可与 `match`/`latest` 叠加
- 全叠顺序=**锚点切尾 → match 过滤 → latest 取尾**

## 板级配置与发信人

- 配置三开关（仅创建者经 /config 开闭）：`show_time` / `show_by` 控制行的时间与发信人是否对外可见（关=读响应分别置 `at=0` / 剥除 `by`，存储恒记全量，开关回开即恢复；渲染开关，永不改写已存行）；`muted` 为板级写冻结——开启后 POST lines 一律 403 `{"error":"board is muted"}`，读与前导行/删板不受影响。
- 发信人归因：追加时若请求携带有效账户凭据则记 `by`=账户地址，纯码追加 `by` 为空串。归因恒开（与 show_by 显示开关无关）；历史行（无 by 字段）按匿名处理。

seq 是服务端内部单调计数，不下传客户端。API 全量自述以 `GET /api/boards/info` 与 `/api/self` 为准。
