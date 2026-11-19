# 前端方案

记录**当前决定**。Rust 版 `argent` 的 Tauri 壳已随该版本冻结；Go 版不再依赖浏览器。

## 决定

| 端 | 方案 | 状态 |
|---|---|---|
| 终端 | CLI 一次性运行 + `internal/tui` 自绘 ANSI 全屏；无 WebView，可跑 SSH | 已落地 |
| 图形（默认） | `internal/nativegui` 纯 Win32 syscall 自绘聊天窗口 | 已落地 |
| 图形（可选） | `internal/gui` 内嵌 SolidJS 产物，经 HTTP + SSE 提供给浏览器，`argent gui --web` | 保留 |

架构细节见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 原生窗口为什么可行

「单文件、无 cgo、stdlib only、Windows 主机自用」让原生方案比 WebView 更合适：

- `internal/nativegui` 只用 `syscall.NewLazyDLL` 调 `user32` / `gdi32` / `dwmapi`，
  没有第三方模块、没有 cgo、不启 HTTP 服务、不生成 token。
- 窗口、侧栏、会话流、工具行、审批条、输入框全部由 `WM_PAINT` + GDI 自绘；
  唯一的子控件是输入框 `EDIT`，用子类过程把 Enter 变成「发送」（Shift+Enter 换行）。
- 引擎仍是同一套：`cmd/argent/gui.go` 创建 `guiRunner`，把
  `Events()` / `Approvals()` / `Commands()` 三个通道交给窗口。事件到达后通过
  `PostMessage(WM_APP+1)` 回到 UI 线程，状态变更与绘制都留在窗口线程。
- 视觉取 Claude / Codex 的克制风格：暖灰底（深色 `#171716`，浅色 `#f5f4ef`）、
  单一珊瑚色强调 `#d97757`、等宽工具行、宽留白。标题栏用 DWM 深色模式。

`GOOS=linux go build ./...` 仍成功：原生文件带 `//go:build windows`，非 Windows 有返回
明确错误的 stub。

## 可选 Web 界面

`argent gui --web` 保留旧路径：`go:embed` 内嵌 `internal/gui/dist`，
`POST /rpc/<command>` + `GET /events`（SSE），一次性 token 注入 `window.__ARGENT_TOKEN__`。
前端 `ui/src/ipc.ts` 解析同一组 `ready` / `agent` / `approval` / `turn_done` 帧。
这条路径适合需要多标签页、工作流/自动化等复杂页面的场景。

`scripts/build-ui.ps1` 把 `ui/dist` 复制进 `internal/gui/dist`；仓库只提交 embed 所需的
构建产物，改 `ui/` 后必须重新生成。

## 交互

- 输入区 Enter 发送，Shift+Enter 换行；点击「发送」等价。
- 审批条：`Y` 允许、`N` 拒绝、`Esc` 取消。
- 滚轮滚动会话；右上角「主题」切换深色/浅色。
- `Ctrl+Z` 从最近检查点恢复被这一轮改过的文件；`Ctrl+Y`（或 `Ctrl+Shift+Z`）从检查点
  分叉重跑。两者都走 `gui.TurnHistory`，Web 界面的 ↶/⟳ 按钮用同一实现。
- 会话侧栏点击可打开（`EventSessions` 提供列表）。

## 桌面安装与图标（Windows）

- 发布产物是单一 `argent.exe`：`.github/workflows/release.yml` 上传
  `argent_<version>_windows_amd64.zip` 和 `checksums.txt`。
- 本地安装用 `scripts/install-win.ps1`，默认装到 `%LOCALAPPDATA%\argentgo\bin`；
  桌面快捷方式指向 `argent.exe gui`，无需浏览器。
- `scripts/uninstall-win.ps1` 只删二进制，保留 `~/.argentgo` 下的会话、配置、hook 与审计日志。
- 原生窗口暂不嵌入 `.ico`（未来可用资源编译器；当前仍坚持零 cgo）。
