# 计划：统一扩展门面 `internal/extension`

> 日期：2026-11-24。前置：两份只读调查（发现/信任机制 + 依赖/archguard 约束）已在会话中完成，事实核对无误。
> 目标：把散落在 `internal/app` 的跨扩展适配逻辑收拢为一个门面层，统一 skills/hooks/commands/plugins
> 的**发现、只读访问、诊断、信任**门面——这是"Go 版 everything-is-a-plugin"的最小落点，不引入任何新依赖。

## 0. 硬约束（来自调查，不可违反）

- **无环**：四扩展包都不 import app；`skills → commands` 是唯一生产边；plugins/pluginapi 是零依赖叶子。
  `app → extension → 四包` 是干净的单向链。
- **门面不放 pluginapi**：commands/hooks 已 import pluginapi，塞进去会 `pluginapi → commands → pluginapi` 成环。
- **不反向 import** app/agent/tools/llm（分层惯例 + 避开 archguard 6 规则）。
- **原语门禁**：extension 的每个新增导出函数必须由 app 生产调用，或在 `archguard/primitive_allowlist.go` 登记。
- **不改默认字节**：保持 `pluginapi.Registry` 的 `(order,name)` 稳定排序与 golden 顺序一致。
- **不吞并实现**：四包内部逻辑原地保留；门面只统一"门面"，不搬实现、不重写发现算法。

## 1. 事实校正（设计依据）

原以为四套都有"name/path/digest/trust 四元组"——**实测不成立**：

| 体系 | 发现入口 | 集合容器 | 只读方法 | 信任 | 失败隔离 |
|---|---|---|---|---|---|
| skills | `Discover([]string)*Registry` (`skills.go:155`) | `Registry{skills []Skill}` | `All/Get/Len` | 无（磁盘） | 跳过 (continue) |
| hooks | `SnapshotFromSources` (`reload.go:79`) | `ConfigSnapshot` | (通过 store) | **完整** `TrustStore.IsTrusted(key,digest)` | fail-closed + last-good + panic guard |
| commands | `Discover([]Source)*Set` (`commands.go:106`) | `Set{commands []Command}` | `List/Get/Len` | 无 | 跳过 |
| plugins | `Discover(root)([]Bundle,[]Problem,error)` (`discovery.go:64`) | `Wiring` | — | app 层来源信任 | Problem + 跳过 |

**真正共有**：发现管线（来源→扫描→解析→去重→集合+诊断）+ name/path 二元组；
**信任只有 hooks 完整**，skills 的 SHA-256 是完整性（非授权），commands 无，plugins 由 app 补。
**只读方法名不统一**（`All()` vs `List()`）——门面要抹平。

## 2. 门面设计（四接口 + 一可选）

### 2.1 核心类型（extension 包）

```go
// Descriptor 是所有扩展条目的统一最小描述（四套共有的 name/path）。
type Descriptor struct {
    Kind  Kind   // Skill | Hook | Command | Plugin
    Name  string
    Path  string
    Trust TrustState // Unknown | Verified | Untrusted（可选信息）
}

// Diagnostic 统一四套的"非致命问题"（plugins.Problem / hooks warn / 静默跳过）。
type Diagnostic struct {
    Kind    DiagnosticKind // Skip | Invalid | Untrusted | Conflict
    Source  string
    Message string
}
```

### 2.2 四个共有接口

```go
// Discoverer：来源列表 → 描述符集合 + 诊断。四套共有。
type Discoverer interface {
    Discover(ctx context.Context, sources []Source) ([]Descriptor, []Diagnostic, error)
}

// Catalog：统一只读访问（抹平 All/List 差异）。
type Catalog interface {
    Get(name string) (Descriptor, bool)
    List() []Descriptor
    Len() int
}

// Validator：统一校验/诊断形状。
type Validator interface {
    Validate(data []byte) []Diagnostic
}

// Authorizer：信任门，可选分层（仅 hooks 完整实现）。
type Authorizer interface {
    Authorize(key, digest string) error
    Enabled() bool
}
```

### 2.3 Source 统一（复用 hooks.Source 形态：Key()+Bytes()）

四套各有来源抽象（skills 目录 / hooks Source / commands Source / plugins 目录）。
门面用 `Source{Key string; Open func()([]byte,error)}` 统一，
并提供从目录/虚拟 URI 适配的函数。**不改动四包内部来源类型**，只在门面层适配。

### 2.4 失败隔离策略（门面级）

`Skip`（skills/commands/plugins 默认）vs `FailClosed`（hooks 默认）枚举；
panic 边界复用 hooks.guard 的 recover 模式（门面层包一层，不侵入四包）。

## 3. 分步实施（每步独立可验证、全绿门禁）

> 原则：每一步都 `gofmt -l .` 空 + `go vet ./...` + `go test -p 4 ./... -count=1` 全绿才进下一步。
> 每步的导出函数要么被 app 调用，要么登记 primitive allowlist。

### 步骤 1：建 `internal/extension` 包骨架（纯类型 + 适配器，零行为改动）
- 新增 `internal/extension/extension.go`：Descriptor / Diagnostic / Kind / TrustState / Source / 四接口定义。
- 新增 `internal/extension/adapt.go`：把四包的发现结果/来源适配成门面类型（薄适配，无逻辑）。
- 导入四包 + pluginapi；不含任何新导出"行为函数"未被调用的情况——
  所有导出类型/接口不算 FuncDecl，不触发原语门禁；适配函数若导出需登记或由 app 调用。
- 门禁：全绿。

### 步骤 2：信任门面（包装 hooks.TrustStore）
- `internal/extension/authorize.go`：`NewAuthorizer(trust *hooks.TrustStore) Authorizer`，
  用 `hooks.Digest` + `TrustStore.IsTrusted` 实现 fail-closed。
- `NoopAuthorizer`（Enabled=false）供无信任体系。
- 导出函数由 app 调用或登记 allowlist。
- 门禁：全绿 + 单测覆盖 trusted/untrusted/key 不匹配。

### 步骤 3：发现门面（薄封装四套 Discover）
- `internal/extension/discover.go`：`DiscoverSkills/DiscoverCommands/DiscoverPlugins/DiscoverHooks`
  包装四包发现入口，返回统一 `[]Descriptor + []Diagnostic`。
- 这是薄封装，不改四包算法；诊断归一（plugins.Problem → Diagnostic 等）。
- 门禁：全绿 + 表驱动测试（每套一条 happy path + 一条诊断 path）。

### 步骤 4：app 接线（composition root 调用门面）
- 在 `internal/app` 合适位置（如 app_execute 装配或新 extension_wire.go）调用门面，
  让新增导出函数有生产调用方（满足原语门禁）。
- **本步不删除** app 原有适配逻辑——只增加门面调用点，保持行为逐字节不变。
- 门禁：全绿 + gofmt/vet。

### 步骤 5：（可选，风险更高，单独评审）收拢 app 跨扩展适配
- 把 `app/plugin_wiring.go` 的 `pluginCommandSources`/`pluginHookPaths`、
  `app/mcp.go` 的 `wirePlugins` 适配片段迁入门面。
- 仅在步骤 1-4 稳定、且确认 golden 顺序不变后做；每迁一处跑一次全量。
- 若发现破坏字节快照或引入环，回退该处。

## 4. 明确不做

- 不把门面塞进 pluginapi（成环）。
- 不让 extension import app/agent/tools/llm。
- 不重写四包发现算法、不搬实现（门面只统一门面）。
- 不引入热重载（Go 无 jiti）——保持编译期注册 + 配置驱动。
- 不改 hooks 的 fail-closed 信任语义（只包装，不放松）。
- 步骤 5 之前不动 app 现有装配代码。

## 4.5 实现期实证校正（2026-11-24）

写完骨架后按真实类型落地时发现：原计划的 `Source{Key,Open}` / `Discoverer` /
`Catalog` / `Validator` 四接口对四套并非都贴合——

- **skills/commands/plugins** 是"目录扫描 → 命名条目"，`Descriptor{Name,Path}` 统一得很好；
- **hooks** 是"事件→命令触发器"（Event/Matcher/Command），**不是命名条目**；
  强行实现 `Discoverer{Catalog}` 会削足适履。hooks 的真正独特性是信任门。

因此门面收敛为**小而诚实**的两块：
1. **输出统一**：`Descriptor` / `Diagnostic` / `Inventory`，由 `DiscoverSkills/DiscoverCommands/DiscoverPlugins`
   三个具体薄封装产出（hooks 不伪装成 Discoverer）；
2. **信任统一**：`Authorizer` 包装 hooks 的 `TrustStore.IsTrusted` fail-closed 语义。

去掉了 Source/Discoverer/Catalog/Validator 接口与 `Context` 别名（未被真实使用，属过度设计）。
这是实证驱动的收敛，不是缩减目标——门面本就只该统一"真正共有"的部分。

## 5. 验收

- `gofmt -l .` 空、`go vet ./...`、`go test -p 4 ./... -count=1` 每步全绿。
- `go test -race -p 1 ./internal/extension ./internal/app` 无竞态。
- archguard 全部通过（`go test ./internal/archguard`）：无环、无违规 import、原语门禁通过。
- 四包现有 458 个测试函数 + app 测试全部通过，默认工具目录/提示字节不变。
- 门面自身表驱动测试：Descriptor/Diagnostic 适配、Authorizer 三态、Discover 四套 happy+诊断。
