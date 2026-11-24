# ADR：架构选型——向谁演化，还是创造新架构

> 日期：2026-11-24。方法：四个只读代理并行深挖 pi / omp / opencode / dsh 的架构本质
> （不是功能清单），与 argentgo 现状逐条对照。所有判断附源码证据。

## 决策（先给结论）

**不照搬任何一家，也不创造全新架构。继续走"pi 式机制/策略分离内核 + 多层次扩展"，
但按 argentgo 自己的硬约束裁剪，并按 omp/opencode 已验证的几条架构主干补齐缺口。**

理由压缩成一句：argentgo 的约束（stdlib-only、零新依赖、无 cgo、单二进制、Windows 主开发、
安全是护城河）使其**不可能**照搬任何一家——它们全部建立在 argentgo 明确禁止的运行时上；
而 argentgo **已经有自己的架构**（50 包分层 + archguard + ADR 内核五能力 + pluginapi），
推翻重来既违反"不为重构而重构"，又会丢掉 26w 行已验证代码。**"创造新架构"是个伪命题：
argentgo 本身就是为这套约束新创的架构。**

## 四家架构本质（实证）

| 项目 | 架构本质 | 强项 | 代价 | 与 argentgo 约束的冲突 |
|---|---|---|---|---|
| **pi** | 机制/策略分离：~1.6k 行无策略内核（零内置工具），工具/会话/扩展全在宿主层；append-only 树 + leaf 指针；ExtensionAPI 复用内核钩子 | 内核极简、扩展表达力强（plan/subagent 写成扩展）、会话树 O(1) 分支 | TS/Node 栈；扩展无安全隔离；chord 67k 行重运行时 | 扩展加载靠 jiti（Go 无等价）；动态 npm provider；进程内热重载——均不可移植 |
| **omp** | TS 语义层 + Rust 热路径原语 + N-API 单一边界；四条通用接入通道（capability provider/marketplace/RPC+三语 SDK/进程内扩展） | 生态 9.5、工具深度 10；进程内 grep/glob/fd + 共享 stamp 缓存 + 取消透传 | 三套工具链（Bun+Rust nightly+Bazel）+ 常驻 daemon/kernel；部署仅 4 分 | N-API addon 分发矩阵、tree-sitter/PCRE2/ONNX native 依赖——直接违背零依赖/无 cgo |
| **opencode** | client-server 分离 + Effect HTTP API；一步一流、从重载历史继续、无内存工具循环；事件溯源 + 投影读模型 + inbox 投递/执行分离 | 崩溃安全（write-ahead claim）、step/turn 词汇精确、Code Mode 减少往返 | 69 万行、Effect/Bun/Drizzle、多进程/服务发现/密码协商 | 运行时全是不可移植依赖；client-server 与单二进制目标相悖 |
| **dsh** | everything-is-a-plugin（Cordis）：agent loop 本身也是插件；capability seam 三角色；model-visible ⟺ logged 不变量 | 极致可组合、可逆注册、能力接缝、LSP 可替换 | 52 万行 + Electron + 双 SDK；100% 逐文件覆盖率门禁 | Cordis 依赖 TS 类型魔法与动态加载，照搬等于重写框架 |

## 可移植架构主干（四家共识，与 argentgo 同源）

四家在"agent 架构主干"上高度一致，且 argentgo 现有路线已高度同源：

1. **无状态循环 + 从重载历史继续**（opencode AGENTS.md:217 / dsh agent.ts:734）
   —— argentgo "流式输出→工具→追加 JSONL 会话" 正是此形态。
2. **append-only 事件日志 + 投影读模型**（opencode bus.ts/projector.ts / dsh architecture.md:124）
   —— argentgo 会话即 JSONL、id/parent 相连（AGENTS.md:103）。
3. **durable 事件最小化 + model-visible ⟺ logged**（dsh AGENTS.md:139）
   —— argentgo 已有此不变量雏形，可显式化。
4. **step/turn 精确分层词汇**（opencode/dsh 一致）—— 零成本高收益，argentgo 文档未显式区分。
5. **投递与执行分离 + 幂等 ID**（opencode inbox）—— argentgo 有 internal/inbox 但未与执行闭环。
6. **进程内热路径原语 + stamp 缓存 + context 取消**（omp）—— argentgo **已吸收**：
   `search_scan.go:45` 流式扫描、`walk_parallel.go:30` 并行遍历 + sync.Pool、
   `repo_map_cache.go:25` stamp 缓存、全 I/O 首参 context.Context。
7. **机制/策略分离 + 能力注入**（pi）—— argentgo 部分具备（pluginapi 接口注入，
   但 agent 循环仍承载较多策略，见 ARCH_AUDIT：已确认"职责多但有边界"，非上帝状态机）。

## 决策判据（为什么不选"纯某一家"）

- **选 pi 为主干**：其内核哲学最契合 stdlib-only，但需自行补安全（pi 安全仅 3 分，
  argentgo 是 9 分护城河）、工具深度、provider 广度——恰是 argentgo 已领先项，不能倒退。
- **omp 的信号只取"思想"**：进程内原语、能力注册表、四条接入通道值得借鉴；
  实体（N-API/Bazel/常驻 kernel）与单二进制根本冲突，取之即失去部署 10 分优势。
- **opencode/dsh 的信号只取"主干"**：无状态循环、事件溯源、inbox 投递、崩溃 claim、
  step/turn 词汇——这些是语言无关的 agent 架构真理，且与 argentgo 同源。
- **不创造新架构**：现有 ADR（ARCHITECTURE_PI_KERNEL.md）定义的"内核五能力 + 插件三层"
  已是针对本约束的新架构，且已落地验证。再创一个是纯成本。

## 具体演化路线（只做缺口，不推翻存量）

按投入产出比，剩余可借鉴项排序（均已对照确认不是重复建设）：

1. **显式化 step/turn 词汇**：在 agent 事件与文档固定术语，零成本。
2. **model-visible ⟺ logged 不变量**：把"所有进模型的输入都有会话事件"立为守卫测试。
3. **投递/执行分离闭环**：让 internal/inbox 与 agent 唤醒真正联动（当前是纯库）。
4. **扩展体系一等公民化**：pluginapi 已有 Registry + 拓扑排序，继续把 hook/skill/command
   统一走构造器注册（ADR 既定方向），编译期注册 + 配置驱动（Go 无 jiti，放弃热重载）。
5. **能力 seam 三角色**（dsh）：能力定义/实现/消费者分离，对应 Go interface/struct/caller，
   避免孤儿抽象。
6. **不做**：client-server 主架构、进程内 TS 扩展、N-API/Bazel、chord 67k 运行时、
   内嵌 Python/Bun kernel、自研 JS 解释器 Code Mode——均与约束冲突或收益不抵成本。

## 验证

本 ADR 是决策记录，不改代码。每条"已吸收"判断均附源码路径，可随时复核；
演化路线每步独立可验证，沿用仓库快门禁。
