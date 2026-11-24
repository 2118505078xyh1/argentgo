# 计划：用户交互与 TUI 优化（2026-11-24）

> 方法：两份只读调研（当前 TUI 精确基线 + 四竞品交互对标）已完成。
> 原则：**交叉去重**——竞品建议里 argentgo 已做的（差速渲染、状态栏五项、审批 diff、
> 工具折叠预览、推理块折叠、补全浮层、路径/@补全、首屏欢迎、统计行、错误可展开详情）
> 一律不重复；只做"当前确有缺口 且 竞品有更好做法 且 契合 stdlib-only 单二进制"的交集。
> 所有优化默认开启、逐字节可控（`=0` 紧急回退），不破坏 locale 逐字节守护。
>
> **实施状态（2026-11-24）**：批次 A（文案与键位矛盾修正）已落地（`3aadc4e`）；
> 批次 B 经交叉去重后只落实 B6（非 Windows OSC 52 剪贴板）；B1-B5/B7 经核对与现有
> 机制重叠或风险>收益，明确不做（见批次 B 表格）。批次 C 的 keybinding 注册表
> 已落地（`e4f80ff`）：`Key.Canonical()` 桥接解码 Key 与规范键串、`ActionID`+
> `DefaultBindings()` 单一事实源、`Conflict()` 冲突检测、`BindingsFromRaw()` 从配置
> 读取重绑定；用户重绑定通过"动作翻译回默认键再走主 switch"执行（零主分派改动）；
> 默认无配置时逐字节不变（`TestNoBindingsFrameByteIdentical` 守护）。另修复了
> 工具/推理事件到 transcript 的断链（`9e7a8b7`）、resume 历史加载与 `--session`
> 直入 TUI（`2390465`）。

## 0. 基线

- HEAD `3688bba`（工作树干净时）；TUI 生产代码 ~15.4k 行，106 个测试文件，`go test ./internal/tui` 1402 passed。
- 渲染：Buffer 差速（`buffer.go`）+ frame cache（`ARGENT_FRAME_CACHE=1` opt-in）+ 条目级缓存。
- 输入：Emacs kill-ring + vim 模式 + 路径/@/slash 补全 + 括号粘贴。
- 交互：Ctrl/Alt 约 25 个键位、5 个侧栏面板、审批弹窗（diff + 危险默认拒绝）、鼠标 SGR 选择复制。

## 1. 优化清单（按 ROI 排序，分三批）

### 批次 A — 一致性修复（纯 bug，最高优先，零设计成本）

这类是**实锤错误**：文案与实际键位矛盾、照提示按键无效果。用户信任感直接受损。

**A1. 修正状态文案与实际键位矛盾**（S，零风险）
- `thinking_all_on/off` 写 "ctrl+x" → 实际切换思考是 `Ctrl+T`（Ctrl+X 是复制）。
  `i18n.go:229-230` + `locale_parity_test.go:63-64`。
- `workspace_on` 写 "ctrl+b 隐藏" → 实际是 `Alt+B`（Ctrl+B 是左移光标）。
  `i18n.go:236` + `locale_parity_test.go:70`。
- 验证：改 i18n 两处 + locale parity 两处，跑 `go test ./internal/tui -run 'Locale|Reasoning'`。
- 注意：`reasoning_toggle_test.go:79-80` 引用 thinking_all_on 文本，但只检查包含关系，改文案后面测试自动跟随（它读同一 i18n 键）。

**A2. 审批作用域文案诚实化收尾**（S）
- 现状：`a` 键实际是"精确单次调用允许"（`approval.go:98-103`），但 i18n `allow_session`/`hint_modal` 仍写"本会话允许"（`i18n.go:144,193`）；侧壁已用 `local_strings.hint_modal_local` 规避（`render.go:1280-1281`），但 i18n 文案仍漂移。
- 改：把两处 i18n 文案改为"仅此调用不再询问"，与 `render.go` 注释和实际语义一致；保留 local_strings 以最小化字节影响。
- 验证：locale parity 同步；审批相关测试。

**A3. 清理死代码**（S，维护噪声）
- `rootLineLocked`/`statusRuleLocked`/`hintLine`（`render.go:260-305,1283-1290`）生产无调用，仅测试引用。
- 不删除的行为价值：保留但标注；本批只确认不影响门禁即可，**不建议删**（有测试依赖，删了要同步改测试且无用户价值）。→ 降级为"记录不处理"。

### 批次 B — 实施期交叉去重的结论（重要）

写完计划后逐项核对源码，发现**竞品的多数 P0/P1 建议 argentgo 已实现**，严格去重后大幅收敛：

| 原候选 | 核对结果 | 处置 |
|---|---|---|
| B1 自适应情境提示 | 欢迎卡已显示 "/help"（`welcome.go:17`），且注释明确"刻意不堆快捷键，把首屏留给对话"——违背已定设计 | **不做**（尊重既有决定） |
| B2 键位表生成帮助 | `/hotkeys` 已经由 `HintBindings(text("help"))` 生成（`commands_local.go:38`、`help.go:84`），单一来源已是 i18n 字符串 | **已实现**；根治漂移需 C1，非 B2 |
| B3 跳到最新 | `Ctrl+End` 已是跳底（`model_input.go:164`），状态栏 `↑N` 显示离底距离 | **已实现** |
| B4 审批和弦键 | 审批弹窗独占态，`y/n/a/Enter/Esc` 已符合"Escape=拒绝+危险默认拒绝"，单键不会误触 | **不改**（和弦键收益低） |
| B5 展示增强默认化 | 改变帧字节、破坏 golden，需单独评审 | **暂缓**（风险 > 收益） |
| 逐条消息复制 | 鼠标选择复制 + Alt+R 重试已覆盖；逐条重试需 transcript 索引，中等功能 | **降级 P2** |

**真正未做且契合的缺口**（实施对象）：

**B6. 非 Windows 的 OSC 52 剪贴板**（S-M，Stdlib-only，价值明确）
- 现状：`clipboard_other.go:1-8` 非 Windows 是空操作——Linux/macOS 下 Ctrl+X 复制与鼠标选择复制**静默失效**。
- 改：用 OSC 52 终端剪贴板协议（写 `\x1b]52;c;<base64>\x07` 到输出流），纯标准库 `encoding/base64`。
- 需要把 `CopyToClipboard` 的写入目标从"无"接到 TUI 的输出流（`frameWriter`/`Run` 持有 out）。
- 终端能力门控：仅当 `TERM` 非 dumb 时尝试；失败静默降级（不报错）。
- 验证：`osc52` 编码表驱动测试（含 CJK/base64 边界）；Windows 路径不受影响。
- 这是跨平台用户可感知的真实缺口，且零新依赖。

**B7. CSI 同步输出防异步撕裂**（M，借鉴 pi）
- 现状：Buffer 差速 + EL 清行，但无 CSI 2026 同步输出；高并发流式下理论上可能撕裂。
- 评估：TUI 是单 goroutine 渲染（run 循环串行 select），撕裂风险实际很低；pi 用它是因为组件异步更新。
- **暂缓**（argentgo 渲染模型不同，收益不明确）。

**B8. IME 光标定位**（M，中文用户高价值）
- 零宽 APC 标记 + 硬件光标，解决中文候选窗错位；Windows 终端需实测。
- **P2**（需真实终端验证，无法只靠单测保证）。

### 批次 C — 架构级增量（价值高、成本高，需单独评审）

**C1. 用户可配置 keybinding 注册表**（M-L，借鉴 pi/omp）
- 现状：`KeyBinding` 只是 help 文案结构（`help.go:5`），`ResolveKeyAction` 仅别名解析（`binding_resolver.go:118`）——没有"动作 ID → 多键 → 用户覆盖 → 冲突检测"的真实注册表。
- 价值：后续所有快捷键、帮助、情境提示都依赖它，是 pi/omp 交互能力的地基。
- 风险：触及 25 个键位的分派逻辑（`model_input.go:66-397`），改错影响面大。
- 决策：放在 B 批稳定后，作为独立评审项。若做，必须先建注册表 + 冲突检测，再逐键迁移，每键全绿。

**C2. 全屏搜索 + OSC 133 prompt 跳转**（M，借鉴 pi）
- 长会话导航，价值明确但需新浮层 + 状态机。P2，排期。

**C3. IME 光标定位**（M，借鉴 pi；中文用户高价值）
- 零宽 APC 标记 + 硬件光标，解决中文候选窗错位。Windows 终端需实测。P2。

**C4. turn 级 token 明细弹层**（M，借鉴 dsh）
- 点击统计行展开 cache/reasoning/模型路由明细。P2。

### 明确不做
- 图片协议（Kitty/iTerm2/sixel）、native overlay、Web 槽位 DI——违反 stdlib-only/单二进制。
- opentui/React/Electron 绑定的一切。
- 恢复常驻底部 hint bar——用户已明确要求移除。

## 2. 实施纪律

- 每批每项独立提交，每步 `gofmt -l .` 空 + `go vet ./...` + `go test -p 4 ./... -count=1` 全绿。
- 涉及文案：`i18n.go` 与 `locale_parity_test.go` 同步改（逐字节守护）；只改键值、不改 key。
- 涉及帧输出：默认路径字节不变；新功能用 env 门控，默认开时新增字节必须有测试明确断言。
- 涉及键位：先补解码/行为测试再改分派；`keys.go` 的 Key kind 不轻易加。
- 慢验证（race）只在批次收尾跑：`go test -race ./internal/tui ./internal/app`。

## 3. 验收

- A 批：文案与键位一致（逐条对照 `model_input.go` 实际分派）；locale parity 全绿。
- B 批：情境提示计数退休 + 门控测试；`/hotkeys` 与键位表一致性；消息导航/跳最新行为测试。
- 全量：`go test -p 4 ./...` exit=0；`go test -race ./internal/tui ./internal/app` 无竞态；默认帧字节除明确新增外不变。
