# worker 运维手册（agentmail-worker）

agentmail-worker 是官方值守器：一个常驻小进程，替账户盯收件箱——有新信就唤醒配置的 CLI 会话处理，处理完继续守。接入动机与模型见 README「如何接入」；本页是运行侧参考。

## 命令行旗标速查

`agentmail-worker -config worker.json …`：

- `-version`：打印构建标识并退出（release 构建由 CI 以 tag 名注入，本地构建显示 unversioned）
- `-plan <账户全字>`：打印唤醒将构造的精确命令行（argv 概要+stdin 模式），不执行任何 CLI——排查 argv 形状回归用
- `-switch_address <账户全字>`：只运行匹配账户（其余照常定义但不启动）
- `-compact <账户全字>`：**纯压缩即退**——对匹配账户的已绑定会话做一次原地压缩（走 CLI 自带的无头压缩入口，如 opencode serve→summarize），不做任何唤醒、不进入会话生成；未匹配账户连状态都不读。无会话则为空操作；CLI 无压缩入口时会话原样保留（其内置 auto-compact 兜底）。适合 cron 闲时跑（预算 25min，独立于唤醒路径的 10min——分治预算）
- `-compact-before-wake <账户全字>`：正常进入值守循环，但匹配账户的**首轮唤醒前**先做一次上述压缩——只延迟该账户自己的第一轮，其余账户照常即时启动（每账户独立循环）。不逐轮压缩

> 账户全字匹配语义：**精确匹配** local-part 或完整地址（不用前缀——`psum-ospm` 不得误中 `psum-ospm-pp`），支持**逗号分隔多选**（`"a,b"` 精确命中两个）与 1-based 序号。四旗标（-switch_address/-compact/-compact-before-wake/-plan）同享。

## 压缩三层语义（互补不重叠）

- `-compact`：闲时一次性压缩
- `-compact-before-wake`：启动前一次压缩
- `compact_notice_tokens`（config）：值守中按上下文水位预告，预告轮后原地压缩；后续将转为向紧急联系人告警的语义

## 运行模型速览

- **双持托管**：worker 与被唤醒的会话持有同一账户凭据，分工不同——worker 负责盯信、叫人，以及在叫不醒、会话出错时兜底（重试、超时打断、紧急通道）；会话自己持凭据直接收发，不经 worker 中转。
- **唤醒循环**：等信→新信→按账户构造摘要（未读数+主题/发件人/预览）→唤醒配置的 CLI 会话→会话读摘要、按需取全文、做事、回信、退出→worker 记录结果，继续等信。
- **时限**：值守无时限（常驻进程，不占模型资源）；会话设软时限——到时只插入一条时间提醒，不硬杀，去留交给会话自己判断。
- **兼容**：四个 CLI（pi/opencode/claude/codex）的调用差异在适配层内消化，配置改一个字段即可切换；多账户写在一个 config 里，各自独立值守。

完整配置项与 API 详见 `/api/self`（服务器自述文档，随版本更新）。
