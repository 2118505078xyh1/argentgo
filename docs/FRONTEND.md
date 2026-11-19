# 前端方案

记录**当前决定**。Rust 版 `argent` 的 Tauri 壳已随该版本冻结；Go 版不再依赖浏览器。

## 决定

| 端 | 方案 | 状态 |
|---|---|---|
| 终端 | CLI 一次性运行 + `internal/tui` 自绘 ANSI 全屏；无 WebView，可跑 SSH | 已落地 |
| 图形（默认） | **Wails v3 + WebView2** 窗口壳，加载 `internal/gui` 的本地 HTTP+SSE 服务 | 已落地 |
| 图形（可选） | `argent gui --web` 用 Edge/Chrome `--app` 打开同一页面；`--native` 为实验性纯 Win32 自绘窗口 | 保留 |

架构细节见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 为什么用 Wails v3

自绘 Win32 窗口（`internal/nativegui`，`--native`）在消息循环/GDI 上反复出现偶发卡死，
维护成本远高于收益。Wails v3 提供成熟的 WebView2 窗口壳：

- `cmd/argent/wails.go` 先启动现有的 `internal/gui` HTTP+SSE 桥，拿到随机端口与一次性
  token 的本地 URL，再把它交给 `application.WebviewWindowOptions.URL`。
- 前端、桥接、事件契约、审批往返全部沿用原实现，无需为桌面壳改写 IPC。
- Wails 只负责窗口与 WebView2；引擎仍是 `guiRunner` 的
  `Events()` / `Approvals()` / `Commands()` 三个通道。
- 发布产物仍为单一 `argent.exe`，并用 `-H=windowsgui` 编译，桌面快捷方式不会再弹出黑色控制台。
- `--native` 保留纯 syscall 自绘实验路径；`--web` 保留 Edge/Chrome `--app` 路径。

**依赖约束变化**：核心包仍只用标准库，但桌面壳引入了第三方 Go module
`github.com/wailsapp/wails/v3`（以及它的传递依赖）。这是为了稳定的 WebView2 渲染而做的
有意取舍，不影响 agent / session / llm / tools 等核心包的无依赖约束。

`GOOS=linux go build ./...` 仍成功：Wails 入口带 `//go:build windows`，非 Windows 有返回
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
