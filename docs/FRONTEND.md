# 前端方案

这份文档记录**当前决定**，不是迁移计划。Rust 版 `argent` 的 Tauri 桌面壳已随该版本冻结；
Go 版另起炉灶。

## 决定

一个终端 UI + 一个内嵌 Web GUI，共用同一套 agent：

| 端 | 方案 | 状态 |
|---|---|---|
| 终端 | CLI 一次性运行 + `internal/tui` 自绘 ANSI 全屏；无 WebView，可跑 SSH | 已落地 |
| 图形 | Go 用 `go:embed` 内嵌 SolidJS 构建产物，经 HTTP + SSE 提供给浏览器页面 | 后端已落地 |
| 窗口 | `internal/launcher` 用系统现有的 Edge/Chrome `--app` 模式打开该页面，失败回退默认浏览器 | 进行中 |

架构细节见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 为什么放弃 Tauri / Rust

Rust 版后端已经冻结在 v0.4.0，继续维护 Tauri 壳等于拖着两套实现往前走：agent 循环、
会话、审批、事件契约都得在 Go 与 Rust 两端各改一遍。Go 重写后，桌面壳只剩「把一个网页
装进窗口」这一件事，不再需要独立的 Cargo workspace、Rust 构建链和 Tauri 桥接命令。

内嵌方案保留了旧前端的命令名与事件负载：`internal/gui` 提供
`POST /rpc/{command}` 与 `GET /events`（SSE），前端 `ui/src/ipc.ts` 仍解析同一组
`ready` / `agent` / `approval` / `turn_done` 帧。迁移的是传输层，不是 UI 重写。

## 为什么不做自研浏览器引擎

「单文件、无 cgo、stdlib only」是硬约束。嵌入 Chromium / WebKit 意味着 C 依赖和
几十 MB 的运行时，直接破坏交叉编译与体积目标；自己用 `net/http` + canvas 画一套 DOM
则要把布局、CSS、输入法、无障碍全部重写，代价远超收益。

系统已装的 Edge/Chrome 提供成熟的渲染、输入法与 WebView2 兼容能力，Go 进程只负责
提供页面和事件流。`internal/launcher` 只调一次带 `--app` 的浏览器进程，不做 IPC；
窗口关闭由浏览器负责，Go 侧在 context 取消时关停 HTTP 服务并 flush 会话。

## 仍待端到端验证

GUI 后端已完成，但下面几项没有全链路验证之前，不能称为「done」：

1. `internal/launcher`：找到 Edge/Chrome、以 `--app` 打开带 token 的 URL、
   找不到浏览器时回退默认浏览器，以及窗口关闭后 Go 服务能否干净退出。
2. `ui/` 传输桥接：前端从旧的 WebView `invoke()` + 单事件通道切到 HTTP `/rpc` + SSE
   `/events`，token 注入（`window.__ARGENT_TOKEN__`）、断线重连、审批往返需实测。
3. `go:embed` 构建链：`scripts/build-ui.ps1` 把 `ui/dist` 复制进 `internal/gui/dist`；
   仓库里只保留占位 `index.html` 让 embed 能编译，真实产物须本地构建生成。
4. 图形界面的内存/延迟指标尚未测量，不能引用任何 GUI 内存数字。
5. `argent gui` 目前在 CLI 里解析后明确报错退出（"not migrated yet"），
   要等启动器、UI 桥接与子命令接线一起完成后才能开放。
