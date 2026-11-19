# 前端方案

记录**当前决定**。Rust 版 `argent` 的 Tauri 壳已冻结；Go 版的前端是 **MyGo 原生 UI**，
浏览器前端已完全移除。

## 决定

| 端 | 方案 | 状态 |
|---|---|---|
| 终端 | CLI 一次性运行 + `internal/tui` 自绘 ANSI 全屏；无 WebView，可跑 SSH | 已落地 |
| 图形 | **MyGo 原生 UI**（`native/`），通过 `--rpc` 驱动 `argent.exe` 引擎 | 唯一桌面前端 |

没有 WebView、没有浏览器、没有 HTTP+SSE 桌面桥、没有 SolidJS 前端。`ui/`、`internal/gui/`、
`internal/launcher/` 均已移除。

## 架构：两个二进制

桌面体验由两个程序组成，随 Release 一起发布：

- **`argent.exe`** — CLI、TUI 和 agent 引擎。`--rpc` 在 stdio 上提供换行分隔的
  JSON-RPC（`prompt` + `prompt/delta` 流式通知、`abort`、`session.list`、`session.load`）。
  核心包仍**只用标准库**。
- **`argent-native.exe`** — MyGo 原生聊天窗口（`native/`）。启动时用 `ARGENT_EXE`
  指向的引擎二进制拉起 `--rpc` 子进程，把用户输入作为 prompt 发出，把 delta
  通过 `Window.Update` 流式渲染。它只在桌面壳这一层依赖 `github.com/egoist/mygo`。

`argent gui` 在 Windows 上启动同目录的 `argent-native.exe`；`ARGENT_CWD` 指定工作区。

## 为什么是原生

- WebView2 渲染进程常驻约 400 MB 工作集；原生 UI 只有单进程、约 76 MB，空闲 CPU 为 0
  （MyGo 的 "settled window sleeps"）。
- MyGo 原生 UI 用 Go 的 `ui` 包绘制，无 HTML/JS/WebView；侧栏、消息列表、
  工具卡片、审批条、输入框、Markdown 流式回复都由原生控件实现。
- 引擎边界是已有的 `--rpc` 传输，不新增依赖、不改核心，核心保持 stdlib-only。

## 原生前端能力（`native/`）

- 侧栏：品牌、导航网格、会话搜索与列表、状态栏。
- 聊天：`ui.List` + follow-end，欢迎态，用户气泡，工具状态卡片，
  `markdown.View`/`markdown.Stream` 渲染助手回复。
- 交互：发送（Enter）、停止（abort）、撤销/重试（Ctrl+Z/Y）、深浅主题、
  审批 Y/N。真实引擎模式下不显示演示用的假审批。
- 无引擎时（`ARGENT_EXE` 未设置或测试）用 canned Markdown 回复，便于开发。

## 依赖约束

- 核心包（`internal/{agent,session,llm,tools,security,...}`）零第三方依赖。
- 桌面壳 `native/` 使用 MyGo（v0.x），这是唯一的第三方 Go 依赖，用于换取
  原生渲染与低内存。

## 文档

- [架构](ARCHITECTURE.md) — 请求流、会话存储、权限、TLS、传输
- [路线图](ROADMAP.md) · [功能对齐](PARITY.md) · [更新日志](../CHANGELOG.md)
