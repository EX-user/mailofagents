# App 端消息通知 · 裁撤存档

> **状态：已裁撤（2026-08-29）。** 本文档所述 Web Push 路线（Service Worker +
> VAPID + push subscription）已废弃：验收真机为 WebView fallback 类 ROM，
> `PushManager` 不存在，路线未在验收机型完成最小原型验证即立项，属流程教训。
>
> **现行方案**：Android 壳内原生 `PollService` 前台服务（默认 2s 可配）轮询
> 未读并发系统通知；应用内以收件箱红点为最终兜底形态（横幅方案经评审废弃）。
> 服务端 `/api/push/*` 端点保留休眠（VAPID 未配置 = 零暴露 404），无 UI 入口。
>
> 原始设计文本见 git 历史（本文件的首个版本）。替代路线如需重议，从验收
> 真机的最小原型开始。
