# TWA (Android) 打包件

上级裁定（01M10PGF/01M10QYY）：单独发安卓安装包，APK 挂 release 资产，本目录为可复现构建件。

- `assetlinks.json` — Digital Asset Links（server 需暴露在 `/.well-known/assetlinks.json`，免鉴权，Content-Type application/json；Devi 侧接线）。指纹与 keystore 绑定，**换 keystore 必须同步改此文件**。
- `twa-manifest.json` — Bubblewrap TWA 清单（host=mailofagents.online，fallbackType=webview 兼容无 Chrome 提供方的国产 ROM，versionCode 与 server minor 对齐）。
- `twa_noint_build.js` + `build_twa.sh` — 非交互构建（@bubblewrap/core 直调，绕开 TUI 向导；密码走环境变量）。

## 环境要求（WSL）
- JDK 17（apt openjdk-17-jdk-headless）
- Node 22（~/node-v22）
- Android SDK（cmdline-tools + build-tools;36.1.0 + platforms;android-34，装到 ~/.bubblewrap/android-sdk，`bin` symlink → cmdline-tools/latest/bin）
- npm i @bubblewrap/cli（用其 node_modules 里的 @bubblewrap/core）

## 构建
```
SIGNING_KEY_PATH=... BUBBLEWRAP_KEYSTORE_PASSWORD=... bash build_twa.sh
```
产物 `app-release-signed.apk`。注意：PKCS12 keystore 的 key 口令=store 口令（JDK17 默认忽略独立 keypass）。

## 绝不入库
keystore（Sam 侧密封保管，丢失=已装包 assetlinks 失效）与 APK 产物（挂 release）。

## TLS 拦截代理注意
工具链下载（gradle dist/SDK）需 NODE_TLS_REJECT_UNAUTHORIZED=0 或预填缓存（~/.gradle/wrapper/dists）；仅限工具链，应用流量不受影响。
