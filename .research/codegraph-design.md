# 本地确定性代码图谱（codegraph）设计草案（只读调研，2026-11-22）

> 调研对象：当前主树 `27c5aeb`（HEAD，R53，单树）。所有行号均为本次实测引用；本轮**只读**，未改动任何代码文件，未执行 git add/commit/push。
> 风格参照 `.research/field-feedback.md`：逐项列证据、明确"核实状态"、末尾给独立判断。
> 竞品思想仅作方向参考（本地确定性解析、无向量库、每条边可解释来源），**未访问任何竞品源码，不抄代码**。

---

## 0. 一句话结论（先说诚实评估）

**技术上完全可做，且比预想的更接近现状——但"模型问 X 在哪被调用只能 grep"这个前提只对了一半，价值被部分高估。**

- 反证：项目**已经有** `lsp_symbols` / `lsp_references` / `lsp_workspace_symbols` 六个只读工具（`internal/tools/builtin_register.go:53-58`、`internal/tools/tools_lsp_ide.go:259-273,331-345`），生产装配点 `internal/app/mcp.go:123,135` 已注入 `NewLspIdeAdapter`（`internal/app/lsp_adapter.go:160-172`），LSP 服务器注册表默认含 `gopls`（`internal/lsp/servers.go:15`）。在装了 gopls 的环境里，"X 在哪被调用"**已经能查**，且比纯 AST 名字匹配更准（能解析跨包、别名、接口、方法接收者）。
- 但 LSP 有两个真实缺口：① 查询必须给**位置**（path + 0-based line/character），模型往往要两步（先 `lsp_workspace_symbols` 找位置，再 `lsp_references`）；② 依赖**外部 gopls 进程**（`ARGENT_LSP=0` 可关掉，未安装则不可用），冷启动与索引时间不可控。codegraph 的增量价值是**符号名一步到位 + 零外部进程 + 每条边带本地证据**，这是真实但**中等**的价值，不是 P0 级"环境税"。
- 另一个强证据：`repo_map` 已在**生产路径**用 `go/parser` 做全 AST 符号抽取（`internal/tools/repo_map_symbols.go:77-142`）并带 mtime+size 缓存（`internal/tools/repo_map_cache.go:11-37,48-99`）。所以"用 go/parser 建图"不是从零开始，可复用度约 **60%**；新增部分只是"引用/调用边 + 跨文件索引 + 查询工具"。
- 与 `.research/field-feedback.md:18-19` 的独立判断对照：那份报告认为最该做的是中文入站解码、输出完整性、零 key 搜索、技能发现路径这四项"每次新会话重付的环境税"。codegraph 改善的是**上下文相关性**，不是**上下文纯净度**；它不该挤占这四项的优先级。
- **建议结论**：值得作为 R55 主题，但必须先做一个 ≤0.5 天的精度 spike（见 §9 证伪条件）；precision 不达标就降级为"定义查找 + 同包引用候选"，并明确宣传为 LSP 的**补充**而非替代。

---

## 1. 现状核实（逐项给行号）

### 1.1 go/parser 用法能复用多少

**证据**

| 位置 | 用法 | 可复用性 |
|---|---|---|
| `internal/archguard/arch_test.go:10-19` | import `go/ast`、`go/parser`、`go/token` | 完全相同的技术栈 |
| `internal/archguard/arch_test.go:60-80` | `token.NewFileSet()` + `parser.ParseFile(fset, abs, nil, parser.ImportsOnly)` | **部分**：`ImportsOnly` 只取 import，建图需要完整 AST（去掉该 flag）；fset + 错误容忍模式可直接抄思想 |
| `internal/archguard/arch_test.go:132-167` | 按文件解析、构建 `graph[pkg]map[pkg]bool` | **高**：包级图 → 符号级图的数据结构思路一致 |
| `internal/archguard/arch_test.go:169-226` | 三色 DFS 检测 import 环 | **高**：未来做"调用环/影响面"查询可直接复用算法 |
| `internal/archguard/arch_test.go:231-253` | `filepath.Walk` 剪枝 `.git/dist/vendor/node_modules` | **高**：建图的文件遍历剪枝清单可沿用 |
| `internal/archguard/arch_test.go:276-311` | 从工作目录向上找 `go.mod`、读 module path | **高**：本仓已有 `findRepoRoot`/`readModulePath` 纯函数范式，建图要判定"自有模块边界"可直接参考 |
| `internal/tools/repo_map_symbols.go:77-142` | **生产代码**：`parser.ParseFile(fset, rel, src, 0)` 全 AST，遍历 `file.Decls` 抽 `FuncDecl`/`GenDecl`（func/method/type/const/var），`fset.Position(...).Line` 取行号，`ast.IsExported` 判导出 | **最高**：这是定义侧抽取的现成蓝本，`repoSymbol{File,Name,Receiver,Line,Kind,Exported}`（`:27-34`）几乎可直接扩展成 `Symbol` |
| `internal/tools/repo_map_symbols.go:41-59` | `repoReceiverName` 解包 `*T`/`T[P]`/括号接收者 | **高**：方法符号的接收者规范化可原样复用 |
| `internal/tools/repo_map_symbols.go:61-71` + `internal/tools/repo_map_cache.go:48-99` | `extractRepoSymbols` → `cachedRepoSymbolsForFiles`，按 mtime+size 指纹缓存解析结果、返回副本 | **最高**：增量更新的缓存骨架现成；`repoFileStamp{modTime,size}`（`:11-17`）、`repoCacheKey{root,rel,stamp}`（`:19-26`）、按 root 失效（`:113-123`）均可推广到全仓图 |
| `internal/tools/repo_map.go:96-125,149-156` | `filepath.WalkDir` + `repoMapIgnoredDirs`（`:22-26`）+ 只取前 80 个文件做符号 | **中**：遍历可复用；**必须去掉 80 文件上限**（`repoMapMaxFiles`，`:18`），否则覆盖不了"X 在哪被调用" |

**核实状态**：完全成立，且比任务描述更乐观——`archguard` 只是**测试**里的 `ImportsOnly` 解析；**生产代码里已有全 AST 解析**（`repo_map_symbols.go`），并已解决"语法错误容忍"（`:73-81` 注释与 `file == nil` 防护）、"缓存命中不读文件"（`repo_map_cache.go:39-46,67-72`）、"返回副本防外部改写"（`:71,80`）。

**独立判断**：可复用代码量按"定义抽取 + 缓存 + 遍历 + 错误容忍"计约 **550-650 行**（`repo_map_symbols.go` 306 行 + `repo_map_cache.go` 171 行 + archguard 解析/DFS 约 100 行）中的思想和骨架；真正新写的是**引用侧抽取**（`ast.CallExpr`/`ast.SelectorExpr` → 边）、** resolver **（名字冲突消解）、**索引与查询**。不要重造 `parseGoSource`，应把它从 `internal/tools` 抽到新的零依赖叶子包 `internal/codegraph`，让 `repo_map_symbols.go` 改为调用它（这是一次**重构**，需单独一轮，不能塞进 R55 主改动里悄悄做）。

### 1.2 `internal/session` 的上下文注入点

**证据**

- `internal/session/implicit_context.go:31-38`：`ImplicitContext{AssistantTails, UnmarkedFailures}` 结构；`:45-86` `ExtractImplicitContext`（纯函数）；`:147-164` `MergeImplicitIntoSummary`（幂等标记 `implicitContextMarker`，`:27-29`）。
- `internal/app/implicit_context_wire.go:7-13` 明确写着"刻意保持零调用方"；`:24-29` `implicitContextEnabled(reg)`（nil → fail-closed 关闭）；`:39-45` `enrichWithImplicit` 是纯函数。
- **核实**：全仓 grep 确认 `enrichWithImplicit` 的生产调用方为 **0**，只有测试（`compaction_default_guard_test.go:235`、`implicit_context_wire_test.go:114,130,...`）调用。这是**骨架模式**，不是已接线能力。
- 系统提示的真实注入链在 `internal/app/app_prompt.go`：`buildStaticSystem:45-80`（角色 + 平台事实 + 项目指令 + 技能索引）、`buildDynamicContext:105-121`、`buildSystemWithTrust:131-148`；可选规则段 `ruleDiscoveryBlock:150-176` / `renderRuleDiscoveryBlock:181-209`。
- 运行时的可选提示追加在 `internal/app/app_execute.go:679-696`：`ARGENT_EDIT_GUIDE`（`:682-684`）、`repoMapHintEnabled`（`:685-689`）、`ARGENT_VERIFY_AUTORUN_HINT`（`:690-696`），统一写法 `loop.Opts.System += "\n" + hint()`。辅助原语见 `internal/app/app_repo_map_hint.go:5-16`。

**独立判断**：图谱**不应**注入 session 的压缩摘要（`implicit_context.go` 是长对话保真机制，与代码结构无关）。正确注入点是 `app_execute.go:685-689` 那条"系统提示尾部 opt-in 一行提示"的既有模式；图谱内容本身**绝不进提示**，只在模型主动调用工具时作为工具结果回灌。

### 1.3 `internal/tools` 的只读工具注册机制

**证据**

- `internal/pluginapi/tool.go:13-19`：契约 `Tool{Name,Description,Schema,Mutating,Run(ctx,root,args)}`。
- `internal/tools/builtin_register.go:14` `builtinFactory`；`:21` `pluginapi.Registry[builtinFactory]`；`:26-28` `registerBuiltin`（order 按 10 递增，**顺序被 golden 快照逐字节锁定**，`:17-20`）；`:30-71` `init()` 静态注册清单；`:76-82` `builtinTools` 每次 New 重新实例化。
- **opt-in 工具的正确姿势**：`internal/tools/tools_grep_ex.go:11-15` 注释明确"刻意不进入 `builtin_register.go`，默认 Specs()/Names() 逐字节不变"；`:143-157` `RegisterGrepEx(r *Registry)` 幂等、nil 空操作、尾部追加。同类：`RegisterApplyPatch`（`tools_apply_patch.go:250`）、`RegisterExitPlan`（`exit_plan.go:55`）。
- **适配器注入姿势**：`internal/tools/tools_registry.go:186-204` `SetLspIde` 保持原位置重建工具，使公布顺序稳定；`tools_lsp_ide.go:152-162` 定义消费侧接口 `LspIde`，`internal/app` 提供适配器（`lsp_adapter.go:152-171`）。AGENTS.md:86 明确推崇这种"类型归属与依赖注入分离"。

**独立判断**：codegraph 工具应**完全照抄 `grep_ex` 的 opt-in 注册形态**（默认不注册）+ **照抄 LSP 的适配器注入形态**（tools 里定义中立接口 `CodeGraph` 与 DTO，app 里 `SetCodeGraph(adapter)`）。这样 `tools` 不 import `codegraph`/`agent`/`lsp`，默认 `Specs()` golden 不动，且接线只改 `mcp.go` 几行。

### 1.4 `internal/app` 的装配点

**证据**

- `internal/app/mcp.go:94-107` `wireOptInTools(registry, reg)`：当前仅 `OptInApplyPatch` → `RegisterApplyPatch`、`OptInGrepEx` → `RegisterGrepEx`。**这就是新工具的唯一注册装配点。**
- `internal/app/mcp.go:109-144` `newMultiRootTools`：每个 root 一个 `tools.NewWithLimit`（`:120,132`），依次 `SetSessionDir`、`SetGuard`、`SetLspIde`、`wireCheckpointDiff`、`wireHistory`、`wireUsage`、`wireOptInTools`（`:121-127` 与 `:133-139`）。
- `internal/app/mcp.go:261-319` `newToolset` 总装；`:564-580` `Specs()`；`:583-628` `Run()` 路由（MCP → use_skill → 内置注册表）。
- `internal/app/optin_registry.go:12-14` `OptInKey`；`:16-65` 现有 16 个键；`:67-84` `allOptInKeys`；`:86-87` 环境变量 `ARGENT_OPT_INS`；`:89-129` 线程安全注册表；`:163-175` `ParseOptInEnv`。
- `internal/app/app_config_optins.go:17-27` `configOptInKeyMap`（配置可用的 9 键交集）；`:36-45` `configOptIns`；`:50-59` `mergeOptIns`（环境 OR 配置）。
- `internal/config/optin_config.go:36-54` `optInDefinitions` 白名单；`internal/cli/optin_metadata.go:3-8,30+` 独立门控单一真源 + `docs/OPT_INS.md` 漂移守卫。

**独立判断**：新增 `OptInCodeGraph` 需同时登记 4 处（app 键表 + allOptInKeys、config 白名单、app↔config 映射、`docs/OPT_INS.md`），这 4 处必须由**同一个代理**独占，避免并行写冲突。若只想走 env-only，可仿 `OptInCommandRisk`（`optin_registry.go:59-64,141-157`）跳过 config 三处，降低改动面。

### 1.5 现有 grep/search 工具的调用接口

**证据**

- `internal/tools/tools_builtin_search.go:54-103` `grepTool`：
  - `Name()="grep"`、描述、`Mutating()=false`（`:56-58`）；
  - `Schema()`（`:59-61`）：`{"pattern":string,"path":string}`（`pattern` 必填）；
  - `Run(ctx, root, raw)`（`:63-103`）：`json.Unmarshal` → `regexp.Compile` → `resolve(root, args.Path)` 收工作区边界 → `walkFiles` → 逐行匹配，输出 `path:line: trimmed text`，`maxMatches` 上限，`clip(ctx, ...)` 裁剪，空结果 `"(no matches)"`；
  - `Meta()`（`:105-111`）把空结果标 `ToolMeta{Useless:true}`。
- `internal/tools/grep_ex.go`：`GrepExOptions:28-41`、`GrepExMatch:43-51`、`GrepEx:67-189`（literal/regex、include glob、context、HeadLimit、排序）、`GrepExSummary:194-237`；工具壳 `tools_grep_ex.go:23-83`，schema 在 `:31-33`（pattern/path/include/context/before/after/case_sensitive/output_mode）。
- `ls`/`find` 同文件 `:16-50`、`:115-156`；`walkFiles` `:176-198` 跳过 `.git/node_modules/target/dist`。
- 工具结果治理：`clip(ctx, ...)`、`spillToolOutput(ctx, text)`（`tools_grep_ex.go:78-82`）。

**独立判断**：`codegraph_query` 的**参数解析、路径收边界、输出行数上限、`clip`/spill、`Meta` 空结果标记**应直接沿用这套接口与惯例，不另造机制。它返回的是**结构化证据文本**（`file:line [rule=...] snippet`），比 grep 多一层"解释"，但走同一条输出治理链。

### 1.6 两个"来源封装"原语的封装基础与一个限制

- `internal/tools/result_provenance.go:9-19` `Provenance{Tool,Sanitized,Clipped,Bytes}`；`:27-34` `AnnotateResult`；`:47-61` `Header()` 输出稳定单行 `[tool:read sanitized clipped bytes=123]`；`:84-92` `PrependHeader`。注释明确"纯数据原语、无生产调用方"。→ **codegraph 工具可直接用（同包）**。
- `internal/agent/untrusted_result.go:27-34` `UntrustedResult{Tool,Content,Trust}`；`:38-40` `WrapToolResult`；`:51-64` `Render()` 用 `<tool_result tool="..." trust="untrusted">` 边界；`:75-115` `UnwrapToolResult`；`:117-140` 边界转义。→ **不能由 `internal/tools` 直接 import**：`internal/tools` 永不 import `internal/agent`（AGENTS.md:85、`archguard/arch_test.go:52`）。
- **核实状态**：两个原语当前都是零生产调用方的"骨架"（与 1.2 的 implicit context 同一模式）。

**独立判断**：codegraph 的"每条边可解释"**不能只靠 `Provenance.Header()`**（它只描述"哪个工具、是否裁剪"），必须在结果里携带**逐边的 source location + 提取规则 ID + 置信度**——这才是 graphify 思想的落地点。`UntrustedResult` 若要用，只能由 app 层在回灌时套；阶段 1 可先不接（尊重 opt-in 逐字节不变），在文档中标为阶段 2 的可选加固。

### 1.7 关键反证：LSP 已覆盖大部分"查引用"场景

- `builtin_register.go:53-58` 六个 `lsp_*` 工具**无条件注册**（与 codegraph 的 opt-in 形成对比），未接线适配器时返回 `lspIdeUnavailable`（`tools_lsp_ide.go:59,180-183`）而非报错。
- `lsp_references`（`:259-273`）需要 `path,line,character,includeDeclaration`；`lsp_workspace_symbols`（`:331-345`）只需 `query`；`lsp_symbols`（`:243-257`）需要 `path`。
- `internal/app/lsp_adapter.go:163-172`：`ARGENT_LSP=0` 时适配器为 nil（工具不可用）；否则懒启动客户端。`internal/lsp/servers.go:15` 默认注册 `gopls`。

**独立判断**：这是本设计最大的"价值折扣"因素。codegraph 的差异化必须说清楚：**名字一步查询、无外部进程、全仓纯 stdlib 确定性、逐边证据**；不要宣称"替代 LSP"。若装上 gopls 且 `lsp_workspace_symbols` 工作正常，LLM 用现有工具两步也能解决同一问题。

---

## 2. 数据结构（建议，尚未创建）

### 2.1 核心类型（`internal/codegraph/types.go`，零依赖叶子包）

```go
type FileKey string            // 工作区相对、斜杠分隔路径，与 repoSymbol.File 同口径
type RuleID string             // 提取规则标识，如 "call.ident.v1"
type Confidence uint8          // 0=exact, 1=likely, 2=candidate

type Pos struct {
    File FileKey
    Line int                   // 1-based，来自 fset.Position
    Col  int                   // 1-based 字节列
}

type Symbol struct {
    ID        uint64             // 稳定键：kind+qualified+file+line 的哈希（不用 map 迭代序）
    Name      string             // 裸标识符 "Run"
    Qualified string             // "Runner.Run" / "pkg.Run"
    Kind      SymbolKind         // Func | Method | Type | Const | Var
    Receiver  string             // 经 repoReceiverName 规范化；非方法为空
    Def       Pos
    Signature string             // 从 AST 拼的简短签名，供模型阅读
    Exported  bool
}

type EdgeKind uint8             // Call | MethodCall | TypeRef（不做 Implements）

type Edge struct {
    Kind      EdgeKind
    From      uint64             // 引用所在的 enclosing 符号；0=文件级（如 init）
    To        uint64             // 目标符号 ID；0=未解析（保留为候选）
    At        Pos                // ★ 引用源码位置（每条边必带）
    Rule      RuleID             // ★ 提取规则标识（每条边必带）
    Conf      Confidence         // ★ 置信度，诚实标注
    Text      string             // 触发边的一行源码片段（裁剪到 120 字节）
}

type Graph struct {
    Root     string
    Files    map[FileKey]FileStamp
    Symbols  []Symbol
    ByName   map[string][]uint64        // 名字 → 符号 ID（含歧义集合）
    Incoming map[uint64][]Edge          // 被引用：谁调用我
    Outgoing map[uint64][]Edge          // 引用：我调用谁
    Rules    map[RuleID]string          // 规则 ID → 中文解释
    Stats    BuildStats                 // 文件数、符号数、边数、跳过数、错误数、耗时
}
```

### 2.2 "每条边可解释"如何落地

1. **必备三元组**：`At`（`file:line:col`）+ `Rule`（稳定 ID）+ `Conf`。三者缺一不可；查询输出逐边渲染，例如：
   `CALL internal/agent/loop.go:214 [rule=call.selector.samepkg.v1 conf=likely] enclosing=runTurn: s.Run(ctx)`
2. **规则注册表**（`Rules` 字段，随图序列化）：
   - `decl.func.v1` / `decl.method.v1` / `decl.type.v1` / `decl.value.v1`：定义抽取（对应 `parseGoSource` 的 switch 分支）；
   - `call.ident.v1`：同包内 `Foo(...)` 的 `ast.Ident` 调用；
   - `call.selector.samepkg.v1`：`x.Foo(...)` 且能证明 `x` 为同包类型（阶段 2 才敢标 exact，阶段 1 一律 likely）；
   - `ref.type_spec.v1`：`ast.TypeSpec` 中的类型引用；
   - `unresolved.import.v1`：跨包调用但无法确定性解析（阶段 1 保留为目的名 + 位置的**候选边**，不伪造 `To`）。
3. **歧义显式化**：同名多定义时 `ByName` 返回整组，查询输出 `ambiguous=N`，并把无法判定的边标 `conf=candidate`；**宁可少给精确结论，不假装消解成功**。
4. **不变量**：图构建完成后，所有切片按 `(File, Line, Col, Rule, ID)` 排序（吸收 `archguard/arch_test.go:190` 的 `sort.Strings(deps)` 稳定输出思想），禁止依赖 map 遍历顺序；相同输入 → 相同序列化字节 → 可做 golden 断言与 sha256。

### 2.3 纯 stdlib 序列化方案

- **格式**：`encoding/json`，单一 `graph.json` 落到 `<Storage>/codegraph/`（**不写工作区**，与 `repo_map_cache.go:28-30` 的"只在内存"相比多一步显式 opt-in 持久化；阶段 1 甚至可完全不落盘）。DTO 用独立导出结构 + snake_case tag，避免把内部 map/uint64 直接暴露。
- **可解释性优先**：JSON 里保留 `rule` 与 `pos`，人可直接读；不用 `encoding/gob`（虽是 stdlib 但跨版本不稳、不可读）。
- **完整性**：对规范化后的字节算 `sha256`，写入 `meta.json` 的 `digest`；加载时校验不符则整图丢弃重建（fail-closed）。
- **版本**：`schemaVersion` 字段，升级即失效重建。
- **增量持久化**：可选 JSONL（一行一文件的符号与边），按 `FileKey` 覆盖写，避免每次全量重写；阶段 2 再做。

---

## 3. 构建与增量

### 3.1 何时建图

- **默认：不建**。opt-in 关闭时零文件扫描、零解析、零新工具 spec（完全遵循 `grep_ex` 先例）。
- **开启后：懒建**。第一次 `codegraph_query` 触发全仓 `.go` 解析；`codegraph_status` 不触发构建（只读状态）。理由：`app_execute.go:679-696` 的所有可选能力都在运行期追加提示，启动期不该付解析税；LSP 也是懒启动（`lsp_adapter.go:180-181`）。
- **手动触发**：阶段 1 提供 `codegraph_query` 的 `refresh:true` 参数（仅重建内存索引、不写工作区），或 CLI 子命令（阶段 2）。`Mutating()` 仍为 false——它不改工作区文件，不进安全审批链；需在描述里写清楚这一语义，避免争议。
- **文件范围**：工作区 root 全仓 `.go`，剪枝清单沿用 `archguard/arch_test.go:238-243` 与 `tools/repo_map.go:22-26`（`.git`、`vendor`、`node_modules`、`dist`、`target`、`.argent`）；**必须移除 `repoMapMaxFiles=80` 上限**。

### 3.2 文件改动后如何增量更新

**主方案（阶段 1，零侵入）：查询时校验文件戳。**
- 复用 `repoFileStamp{modTime,size}`（`repo_map_cache.go:11-17`）与 `statRepoFileStamp`（`:39-46`）：每次查询对已索引文件做 `os.Stat`，戳变化的文件重新 `ParseFile` 并替换其符号/边；新文件补入，消失文件删除。
- 关键收益：**完全不动 `internal/agent` 和 `internal/session`**，避免接线风险。2000 次 `os.Stat` 在 Windows 上约几十到两百毫秒，对一次工具调用可接受；可加"距离上次全量校验 <30s 则跳过"的时间窗降低频率。
- 缓存语义沿用 `cachedRepoSymbols`（`repo_map_cache.go:48-81`）：未命中时持锁读+解析、返回副本、按 root 失效（`:113-123`）。

**可选优化（阶段 2）：利用 session 的改动文件列表。**
- 证据链：`agent/verify_loop.go:96-107` `verifyChangedPaths(turn)` 从 `EventCheckpointReady` 的 `e.Checkpoint.Paths` 取路径；`session/checkpoint.go:37-45` `Checkpoint{Files []CheckpointFile}`、`:27-35` `CheckpointFile{Path, SHA256, Before, After...}`。
- 若要把这个列表喂给图谱，需要在 `internal/agent/runner.go:212` 附近的 `Verify` 注入点旁边再加一个 `OnChangedPaths` 回调，或在 app 层监听 checkpoint 事件——这是**核心热路径改动**，AGENTS.md:28 要求改后跑 smoke；收益只是省下 `os.Stat`（毫秒级），**阶段 1 明确不做**。
- 注意缺口：checkpoint 只覆盖工具发起的改动；shell 间接改动需 `ARGENT_CHECKPOINT_WORKSPACE_DIFF`（`cli/optin_metadata.go:67`）。因此即便接了事件，也**仍需**文件戳校验兜底。

### 3.3 大仓库（2000+ .go）耗时与内存评估

> 估算基线：2000 文件 × 平均 500 行 / 15 KB ≈ 30 MB 源码；宿主 24 核（AGENTS.md:30）。**以下为估算，不是实测结论。**

| 指标 | 单线程 | 4-8 worker（建议） | 备注 |
|---|---:|---:|---|
| 全量解析 | 2-8 s | 0.5-2 s | `go/parser` 每 500 行约 1-4 ms，Windows 文件系统更慢 |
| 索引内存（常驻） | — | 40-100 MB | 每文件约 50 定义 + 150 引用，记录约 80 B，map 开销约 2×；须 intern 文件名/符号名，并给每符号边数设上限（如 200） |
| 解析期瞬时堆 | — | 30-80 MB | **逐文件解析后立刻丢弃 AST**，只保留抽取结果；绝不把所有 `*ast.File` 同时留住 |
| 查询延迟 | — | <5 ms | 名字查表 O(1) + 最多 k 条边（k 上限 100） |
| 戳校验 | — | 30-200 ms | 2000 次 `os.Stat` |

**实测建议（临时 benchmark，不写进代码）**：在 `t.TempDir()` 生成 2000 个合成 `.go`（每文件 20 func + 50 call 点，覆盖同包/跨包/重名三种情况），临时 benchmark 同时记录 `time.Since` 与 `runtime.ReadMemStats().HeapAlloc` 前后差；分别量冷建图、戳校验、单次查询。阈值建议先用"冷建图 <5 s、常驻堆增量 <150 MB"作为告警线。
**重要**：AGENTS.md:28 明确"基准测试也是测试，断言在 go test 下运行，过时断言会让构建失败"——所以这条 benchmark **不要提交为带硬断言的 `*_test.go`**；作为一次性测量脚本/eval 场景使用（与用户要求一致：只给思路，不写进代码）。

---

## 4. 查询工具 API（模型怎么查）

### 4.1 工具一：`codegraph_query`（主查询）

**签名**（遵循 `pluginapi.Tool`，`Mutating()==false`）：

```go
Schema: {
  "type":"object",
  "properties":{
    "symbol":{"type":"string","description":"符号名；支持裸名 Run 或限定名 Type.Method / pkg.Run"},
    "kind":{"type":"string","enum":["any","func","method","type","const","var"],"description":"默认 any"},
    "direction":{"type":"string","enum":["both","callers","callees","definitions"],"description":"默认 both"},
    "limit":{"type":"integer","description":"每类边最多返回条数，默认 20，硬上限 100"},
    "refresh":{"type":"boolean","description":"仅刷新内存索引（校验文件戳、重解析变更文件），不写工作区"}
  },
  "required":["symbol"]
}
```

**输出格式**（人类可读文本 + 逐边证据，先过 `clip(ctx, ...)`/spill，再由 `AnnotateResult` + `PrependHeader` 加来源头）：

```
[tool:codegraph_query clipped bytes=1843]
symbol "Run" — definitions: 2, callers: 18 (显示 20/37), callees: 3, ambiguous: 1
DEF  internal/app/app_runner.go:41:9  [rule=decl.method.v1 conf=exact] func (p *Runner) Run(ctx context.Context) error
DEF  internal/agent/loop.go:88:2     [rule=decl.func.v1 conf=exact] func runTurn(...)
CALL internal/agent/loop.go:214:16  [rule=call.selector.samepkg.v1 conf=likely] enclosing=runTurn: s.Run(ctx)
CALL internal/app/sub.go:57:9       [rule=unresolved.import.v1 conf=candidate] enclosing=start: other.Run(ctx)
... [17 more callers omitted by limit=20]
rules: decl.method.v1=方法定义; call.selector.samepkg.v1=同包选择器调用（未做类型推断）; unresolved.import.v1=跨包调用，仅保留候选位置
```

要点：
- 每条边必带 `file:line` + `rule` + `conf`；`enclosing` 给出调用点所在函数，便于模型定位上下文。
- `ambiguous` 与 `omitted` 显式报数，不隐瞒精度损失。
- `rules:` 行逐条解释本次用到的规则 ID（来自 `Graph.Rules`），实现"可解释"闭环。
- 空结果返回 `"(no matches)"`，`Meta()` 标 `Useless:true`（照 `grepTool.Meta:105-111`）。
- 图谱未构建/未接线时返回稳定的 `codegraph is not available`（仿 `lspIdeUnavailable`，`tools_lsp_ide.go:59`），**不报错**。

### 4.2 工具二：`codegraph_status`（低成本自省）

- Schema：`{}`（可选 `verbose:boolean`）。
- 输出：`[tool:codegraph_status bytes=...] codegraph: enabled | built=false | files=0 | symbols=0 | edges=0 | stale=0 | errors=0 | lastBuild=<time>`。
- 用途：模型在决定是否调用 `codegraph_query` 前先看能力是否可用；也便于评测/诊断确认 opt-in 生效。
- 两者都通过 `tools.RegisterCodeGraph(r)` 尾部追加、`registry.SetCodeGraph(adapter)` 注入（照 `SetLspIde` 模式，`tools_registry.go:186-204`）。

### 4.3 结果封装边界

- **直接用** `tools.Provenance`（`result_provenance.go:14-19,27-34,47-61,87-92`）给整条工具结果加 `[tool:... clipped bytes=...]` 头。
- **逐边证据**是更强的解释层，二者互补，不是二选一。
- `agent.UntrustedResult`（`untrusted_result.go`）**不能**在 tools 包内调用（分层禁止）；阶段 2 若要做不可信边界，由 app 在回灌侧套 `WrapToolResult/Render`，或把边界原语下沉到 `internal/pluginapi`（需单独一轮重构）。

---

## 5. 注入策略

1. **图谱本身不进提示**：不把符号表/边列表塞进 system prompt，不进压缩摘要（`implicit_context.go` 那条路只服务长对话保真，语义不符）。
2. **按需查询优先**：模型通过工具主动查；系统提示只加**一行能力声明**，写在 `app_execute.go:685-689` 的 opt-in 追加段：
   ```
   可用 codegraph_query(symbol) 查询符号定义与调用/引用位置（本地静态分析、只读，结果附源码位置与提取规则）。
   ```
   仿 `repoMapReadOnlyHint()`（`app_repo_map_hint.go:5-16`）做成纯函数 `codeGraphCapabilityHint()` + `codeGraphHintEnabled(env)`，阶段 1 也可直接复用 `ARGENT_OPT_INS=codegraph` 一个开关，不新增独立环境变量。
3. **避免撑爆上下文**：三层限流——① 查询侧 `limit`（默认 20/硬上限 100）；② 构建侧每符号边数上限与 `maxMatches` 风格的全局上限；③ 输出侧 `clip(ctx,...)` / `spillToolOutput`（`tools_grep_ex.go:78-82`）与 `Meta` 空结果标记。
4. **opt-in 默认关、逐字节不变**（对齐 `grep_ex`、`implicit_context`、`untrusted_result` 的既有纪律）：
   - opt-in 关：无注册、无 spec 变化（golden 快照不动）、无提示追加、无文件扫描、无缓存目录创建；
   - 开关登记：`optin_registry.go`（`OptInCodeGraph` + `allOptInKeys`）、若走配置则 `app_config_optins.go:17-27` + `config/optin_config.go:36-54` + `docs/OPT_INS.md`（漂移守卫，`cli/optin_metadata.go:3-8`）；
   - 接线只在 `mcp.go:94-107` 的 `wireOptInTools` 加一个 `if`，`SetCodeGraph(adapter)` 可放在 `newMultiRootTools` 内（`:121-127`/`:133-139`），与 `SetLspIde` 并列。

---

## 6. 分阶段

### 阶段 1：最小可行（建议作为 R55 范围）

- **范围**：全仓 `.go` 索引（**不是单包**——"X 在哪被调用"天然跨文件；单包反而要先做包归属判定，省不了事），但只解析定义 + 同包内的直接调用/类型引用；跨包调用保留为 `candidate` 边（带位置，不伪造目标）。
- **交付**：新包 `internal/codegraph`（types/parse/resolve/build/query/format/serialize/incremental）、2 个 opt-in 工具、1 行系统提示、opt-in 登记与文档。
- **代码量估算**：**1,200-1,800 行**（含表驱动测试）。参照：`repo_map_symbols.go` 306 + `repo_map_cache.go` 171 + `tools_grep_ex.go` 157 行；新增 resolver/query/序列化/接线/测试约 700-1,100 行。
- **风险**：最大风险是**同名误报**（`New`、`Run`、`String` 在 2000 文件里几十个定义）。缓解：边标 `conf=candidate` + `ambiguous` 报数 + 输出 `enclosing` + 只把 `exact` 边计入"callers"主列表，候选另列。
- **不做**：跨包解析、方法接收者类型推断、调用传递闭包、磁盘快照（阶段 1 纯内存 + 戳校验）。

### 阶段 2：完整

- 跨包解析：`go/types` + `go/importer`（stdlib）的 source importer 作为 opt-in 子开关；或显式调用 `go list` 获取包目录（外部进程，需在文档说清不再是"零进程"）。
- 方法调用 `x.Foo()` 的接收者解析；`implements` 边（三色 DFS 可复用于环检测）。
- 磁盘快照（JSONL 增量持久化 + sha256）、checkpoint 路径事件驱动失效（§3.2）。
- 第三个工具 `codegraph_impact(symbol, depth)`（影响面/调用闭包），深度硬上限防爆炸。
- 不可信结果边界封装（§4.3）。
- **代码量估算**：再 **2,000-3,000 行**；主要风险是 `go/types` source importer 在 module 模式下的性能与可移植性（交叉编译目标无源码时失败），必须 fail-closed 且默认关。

---

## 7. 不做项（硬边界）

- ❌ 向量库 / embedding / 语义搜索 / "相似代码"召回——本设计只做符号级确定性图。
- ❌ 跨语言：只解析 `.go`（`repo_map_symbols.go:61-65` 已明确当前无其他语言轻量词法器）。
- ❌ 网络请求、远程索引服务、下载模型/依赖；不访问竞品源码，不抄任何竞品实现。
- ❌ 新依赖、cgo；必须保持 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 可过（AGENTS.md:32,79-82）。
- ❌ 修改类工具：两个查询工具 `Mutating()==false`；`refresh` 只改内存，不碰工作区文件。
- ❌ 默认注册进 `builtin_register.go`，默认进系统提示，默认启动时建图——任何一项都会破坏 golden/逐字节不变纪律（AGENTS.md 与 `builtin_register.go:17-20`）。
- ❌ 把图谱当"编辑授权"：沿用 `repoMapReadOnlyHint`（`app_repo_map_hint.go:5-10`）的语义——出现在图里不等于可以改，编辑前仍须 read。
- ❌ 把 `internal/tools` 改成 import `internal/agent` / `internal/llm` / 新重依赖包；保持分层（AGENTS.md:83-86）。

---

## 8. 落地建议

### 8.1 作为 R55 主题是否值得

**有条件值得。** 当前 HEAD 是 R53（`27c5aeb kernel: round 53`），R55 是合理排期。理由：

- ✅ 技术同源、可复用约 60% 现成代码，stdlib-only 可做，符合项目"零新依赖"铁律。
- ✅ 命中第一优先级"上下文"：把"符号 → 引用位置"从 grep 的**文本猜测**升级为**带证据的结构化查询**，且逐边可解释。
- ⚠️ 但价值被 LSP 部分覆盖（§1.7），且不属于 `field-feedback.md:18-19` 认定的四项"环境税"。因此它应是**R55 的主题之一**，不该独占整轮，更不该挤掉中文入站解码等 P0。
- **前置门槛（≤0.5 天 spike）**：在本仓抽 20 个常见符号名（`Run`、`New`、`String`、`execute`、`handle`…），用 `go/parser` 跑一遍同包解析，统计"名字命中但目标不唯一"的比例。若 >30%，阶段 1 必须降级为"只返回候选位置 + 不宣称调用者"；若 >60%，建议**直接放弃**并把 R55 让给其他主题。

**关于"先问 MCP/skill 能否做"（AGENTS.md:95）**：MCP server 确实能零核心改动地提供查询工具（外部进程 + `mcp.json`），适合做 spike；但它拿不到 session checkpoint 的改动文件列表、无法复用 `repo_map_cache.go` 的进程内缓存、还要多一层 IPC 与进程生命周期。属于 AGENTS.md:95 允许的"需要深度集成"例外。**建议：spike 阶段可用 MCP 验证模型是否真会用这个工具；正式落地仍走核心包 + opt-in。**

### 8.2 17 代理拆分建议（文件隔离优先）

> 原则（AGENTS.md:92,95）：模块边界是 `internal/<pkg>`；新包内文件可高度并行，**共享文件必须单一所有者**。依赖顺序：types → parse → resolve → build → query → tool → wiring。建议先由 1 个代理冻结接口 stub（30 分钟），其余代理按冻结接口并行。

| # | 代理任务 | 独占文件（互不重叠） | 并行性 |
|---:|---|---|---|
| 1 | 核心类型 + 规则 ID 常量 + DTO/JSON tag | `internal/codegraph/types.go`, `types_test.go` | 先做（冻结接口） |
| 2 | 定义抽取（移植 `parseGoSource`，保持函数纯） | `internal/codegraph/parse_decl.go`, `_test.go` | 与 3-8 并行 |
| 3 | 引用/调用边抽取（`CallExpr`/`SelectorExpr`/`TypeSpec`） | `internal/codegraph/parse_ref.go`, `_test.go` | 并行 |
| 4 | 同包 resolver + 歧义/置信度判定 | `internal/codegraph/resolve.go`, `_test.go` | 并行 |
| 5 | 全仓遍历 + 并发 worker pool + 确定性排序 | `internal/codegraph/build.go`, `_test.go` | 并行 |
| 6 | 文件戳校验 + 增量重解析 + root 失效 | `internal/codegraph/incremental.go`, `_test.go` | 并行 |
| 7 | JSON 序列化 + sha256 + schemaVersion（纯函数） | `internal/codegraph/serialize.go`, `_test.go` | 并行 |
| 8 | 查询引擎（ByName → 定义/入边/出边 + 限流） | `internal/codegraph/query.go`, `_test.go` | 并行 |
| 9 | 证据链格式化 + 规则中文解释表 | `internal/codegraph/format.go`, `_test.go` | 并行 |
| 10 | 工具壳 `codegraph_query`/`codegraph_status` + Meta | `internal/tools/tool_codegraph.go`, `_test.go` | 依赖 8/9 接口（可用 stub） |
| 11 | 注册器 `RegisterCodeGraph` + 工具包内中立接口/DTO | `internal/tools/register_codegraph.go`, `_test.go` | 与 10 同包、不同文件 |
| 12 | opt-in 键登记（env + config + 文档三处） | `internal/app/optin_registry.go`, `internal/app/app_config_optins.go`, `internal/config/optin_config.go`, `docs/OPT_INS.md` | **独占共享文件**，单独一个代理 |
| 13 | app 适配器（包装 `*codegraph.Graph`） | `internal/app/codegraph_adapter.go`, `_test.go` | 与 12 并行 |
| 14 | 接线：`wireOptInTools` + `SetCodeGraph` | `internal/app/mcp.go` | **独占**，需在 10-13 接口冻结后 |
| 15 | 系统提示一行 hint（纯函数 + 开关） | `internal/app/app_codegraph_hint.go`, `_test.go` | 与 14 并行 |
| 16 | 默认零变更守护：golden Specs/提示逐字节断言 + archguard 规则补充 | `internal/archguard/arch_test.go`, `internal/tools/catalog_test.go`（仅这两个文件的独占权） | 并行 |
| 17 | 临时性能测量（合成 2000 文件，不提交 benchmark）+ 精度 spike 报告 | 仅 `.research/` 下临时笔记（不提交 benchmark 代码） | 并行，产出喂给 §9 决策 |

**关键串行点**：#1（接口冻结）→ #10/#11/#13 → #14；#12 独占 opt-in 四文件；#16 独占两个守护文件。`app_execute.go` 在阶段 1 **不需要改**（hint 可挂 opt-in 后由 #14 在 `mcp.go` 内统一处理，或若坚持追加提示则由 #15 独占 `app_execute.go:685-689` 一处，不能与 #12/#14 并发写同一文件）。

---

## 9. 风险与证伪条件（独立判断汇总）

| 风险 | 证据/推理 | 缓解 / 证伪条件 |
|---|---|---|
| **价值被 LSP 覆盖** | `builtin_register.go:53-58` + `mcp.go:123,135` + `servers.go:15`；装了 gopls 就能查引用 | spike 先验证目标环境是否有 gopls；若有且 `lsp_references` 好用，R55 降级或只做"名一步查询"差异化 |
| **同名误报导致误导** | Go 中 `New`/`Run` 大量重名；纯 AST 无类型信息 | `conf` + `ambiguous` 显式标注；spike 中同名率 >30% 降级、>60% 放弃（§8.1） |
| **跨包解析做不了** | 需要 `go/types` source importer 或 `go list`，非纯语法 | 阶段 1 只保留 candidate 边，不伪造 `To`；阶段 2 才碰且 opt-in |
| **2000 文件性能/内存失控** | 30 MB 源码，AST 膨胀数倍 | 逐文件丢弃 AST、intern 字符串、边数上限、临时 benchmark 实测；不写带硬断言的提交测试（AGENTS.md:28） |
| **破坏默认逐字节不变** | golden Specs、系统提示断言、`docs/OPT_INS.md` 漂移守卫 | 完全走 `grep_ex` opt-in 路径（tools_grep_ex.go:11-15）；#12/#16 独占守护文件 |
| **分层违规** | tools 不能 import agent（`arch_test.go:52`）；new 包须零依赖 | `internal/codegraph` 只 import stdlib；tools 用中立接口 + app 适配器（LSP 先例） |
| **接线热路径回归** | AGENTS.md:28 要求改 session/agent/tools 热路径后跑 smoke | 本设计阶段 1 不改 agent/session 热路径；改 tools 注册表后跑 `go test -bench . -benchtime=10x ./internal/tools` smoke |

**最终独立判断**：这是一个**工程风险低、精度风险中、差异化价值中等**的方向。技术可行性没有疑问（现成 `go/parser` + 缓存骨架），真正决定它是否值得 R55 的不是"能不能做"，而是 §8.1 的 0.5 天精度 spike 与 LSP 覆盖度核查。**不要因为"竞品有图谱所以我们要有"而立项**；要因为"装了 gopls 也还要两步、没装 gopls 就完全没得查"这个具体缺口而立项。若 spike 证明这个缺口在目标环境不显著，诚实的选择是把它挪到 R56 或永久归档。

---

*本文件为只读调研产出。未改动任何 `.go` 文件，未执行 git add/commit/push。*
