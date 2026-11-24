# 计划：用户交互与 TUI 优化（2026-11-24）

> 方法：两份只读调研（当前 TUI 精确基线 + 四竞品交互对标）已完成。
> 原则：**交叉去重**——竞品建议里 argentgo 已做的（差速渲染、状态栏五项、审批 diff、
> 工具折叠预览、推理块折叠、补全浮层、路径/@补全、首屏欢迎、统计行、错误可展开详情）
> 一律不重复；只做"当前确有缺口 且 竞品有更好做法 且 契合 stdlib-only 单二进制"的交集。
> 所有优化默认开启、逐字节可控（`=0` 紧急回退），不破坏 locale 逐字节守护。

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

### 批次 B — 高价值体验增量（借鉴竞品，低成本）

**B1. 自适应情境提示（执行 N 次后退休）**（S-M，借鉴 omp）
- 空输入框右侧显示一条情境提示（如"←← 查看 3 个运行中 agent"、"输入 / 查看命令"），用户执行该动作 3 次后永久消失。
- 价值：破解"快捷键零可发现"（hint bar 被移除后的头号痛点），且不恢复常驻 hint bar。
- 落点：`internal/tui/composer_hint.go`（新）+ `Model` 加 hint 状态 + render 输入框右侧。
- 默认开启，`ARGENT_TUI_COMPOSER_HINT=0` 回退；hint 状态不入会话 JSONL（纯 UI）。
- 验证：表驱动测试（计数退休、CJK 安全、门控）；帧字节在无 hint 时不变。

**B2. 由键位表生成 `/hotkeys` 帮助页**（S，借鉴 omp）
- 现状：`/hotkeys` 与 i18n `tui.help` 超长字符串手工维护，易漂移（A1 就是漂移后果）。
- 改：以 `keys.go` 的 `HintBindings` 为单一事实源，生成结构化帮助行（键 + 描述 + 状态过滤 `helpStateEnabled`），`/hotkeys` 渲染它。
- 与 B1 协同：B1 的情境提示也复用同一份 `KeyBinding` 表取键名，杜绝硬编码键串。
- 验证：`hotkeys` 输出与 `HintBindings` 一致的断言；locale parity。

**B3. 逐条消息操作：复制 / 重试 快捷键 + 跳到最新**（S-M，借鉴 native/opencode）
- TUI 现状：只有 Ctrl+X 复制最后一条助手正文；无逐条、无"跳到最新"（native 有）。
- 改：
  - 消息导航：`Ctrl+Home/End` 已有滚动到顶/底；增加 `Alt+End` 跳到最后一条用户消息（opencode 范式）。
  - 逐条复制：鼠标选中任意文本已支持；增加 `Enter` 在"仅查看模式"下复制当前消息（若不与提交冲突，用 `Alt+C` 更安全）。
  - 跳到最新按钮：状态栏滚动位 `↑N` 可点击跳底（复用现有鼠标 hit-test）。
- 验证：键位解码 + 行为测试；不破坏现有提交/滚动语义。

**B4. 审批键位与提交分离 + Escape=拒绝**（S-M，借鉴 omp/opencode）
- 现状：`y/n/a` 单键审批，`Enter` 确认默认项（危险时默认拒绝）。omp 用 Ctrl+Enter/Ctrl+Q 审批（避免与输入框 Enter 冲突），opencode `Esc`=reject。
- 改：保留 y/n/a；**增加** `Enter` 在审批态恒等于"确认默认项"（已实现），`Esc` 恒等于拒绝（已实现），补一条文档/帮助说明即可。
- 评估：现状其实已符合"Escape=拒绝 + 危险默认拒绝"，与竞品差距主要是"和弦键审批"——但 TUI 审批弹窗是独占态（输入框不接收 Enter），单键不会误触，和弦键收益低。→ **降级为文档澄清，不改键位**。

**B5. 工具输出/推理展示增强默认化评估**（S，借鉴 pi/omp）
- 现状：`StructuredToolResults`/`HeadTailToolPreview`/`ReasoningPlaceholder`/`ReasoningTailWindow` 默认关闭（`toolresult_format.go:14-19`、`render.go:784-788`）。
- 用户定调"状态栏默认可见、参数取最优值"——这些是改善体验的展示增强，应考虑默认开启。
- 但：它们改变帧字节，可能破坏 golden 快照。→ 本批只**评估 + 给出默认化建议**，不直接改；若开，需逐项跑 golden 并确认默认路径。
- 风险：中。单独评审。

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
