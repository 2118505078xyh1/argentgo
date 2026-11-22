package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/memory"
	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// maxToolDescLen 限制发给模型的工具描述长度。过长的 MCP
// 描述是提示注入的载体并会吃掉上下文，因此 Specs 会在 schema
// 跨越传输层之前截断它。Tool 接口和 Schema 保持完整。
const maxToolDescLen = 1024

// clipDescription 最多保留 maxToolDescLen 个 rune，截断时
// 追加省略号。按 rune 切片使结果在 multibyte 字符跨越
// 限制时仍是有效 UTF-8。
func clipDescription(s string) string {
	runes := []rune(s)
	if len(runes) <= maxToolDescLen {
		return s
	}
	return string(runes[:maxToolDescLen-1]) + "…"
}

// intentSchemaEnv 用于退出在工具参数 schema 中公布 `i`（简洁意图）属性。
// 注入默认开启：agent 在读取侧剥离 `i`（agent.ExtractIntent），
// 因此每条跨越传输层的 schema 都必须列出它，API_REFERENCE
// 的"每个 schema 都接受 i"契约才能成立。
// 把它设为真值（1、true、yes、on 等）可恢复注入前的
// schema；""、0、false、no 和 off 让注入保持启用。
const intentSchemaEnv = "ARGENT_NO_INTENT_SCHEMA"

// intentSchemaDisabled 报告是否应从公布的 schema 中省略 `i`。
// 环境变量在每次调用时读取而非缓存，使测试和
// 长寿命运行时无需重启即可切换。
func intentSchemaDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(intentSchemaEnv))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// advertiseIntentSchema 给工具参数 schema 添加 `i` 意图属性，
// 除非操作员选择退出。InjectIntentSchema 是幂等的，因此已
// 携带 `i` 的 schema（例如下面的 intentSchemaTool 返回的）
// 会被原样返回；不会出现双重注入。
func advertiseIntentSchema(params json.RawMessage) json.RawMessage {
	if intentSchemaDisabled() {
		return params
	}
	return InjectIntentSchema(params)
}

// intentSchemaTool 装饰一个 Tool，使其公布的 schema 携带
// `i` 意图属性。运行时行为原样委托；只有
// 跨越传输层的 Schema 被增强。包装存储的工具 —— 而非
// 只修补 Specs —— 使包内每个 schema 读取方（Specs、
// SpecsFor、tool_search）保持一致。开关在每次调用时求值，
// 因此即使注册表已构建也能遵守 env 开关。
type intentSchemaTool struct {
	Tool
}

func wrapIntentSchema(tool Tool) Tool {
	return intentSchemaTool{Tool: tool}
}

func (t intentSchemaTool) Schema() string {
	return string(advertiseIntentSchema(json.RawMessage(t.Tool.Schema())))
}

// Meta 转发可选的回收钩子，使包装永不改变
// RunWithMeta 的行为。没有内部 Meta 的被包装工具产生零值
// ToolMeta，调用方本就把它当作"无元数据"。
func (t intentSchemaTool) Meta(args json.RawMessage, output string, err error) ToolMeta {
	if m, ok := t.Tool.(metaTool); ok {
		return m.Meta(args, output, err)
	}
	return ToolMeta{}
}

// New 返回一个以 root 为根、使用默认输出上限的注册表。
func New(root string, yolo bool) *Registry {
	return NewWithLimit(root, yolo, maxToolOutput)
}

// NewWithLimit 返回一个以 root 为根、工具输出被截断到
// maxOutput 个字符（保留头尾）的注册表。非正值恢复
// 默认值。
func NewWithLimit(root string, yolo bool, maxOutput int) *Registry {
	if maxOutput <= 0 {
		maxOutput = maxToolOutput
	}
	r := &Registry{root: root, yolo: yolo, maxOutput: maxOutput, tools: make(map[string]Tool)}
	// 默认库使记忆工具在 app 接线真实会话目录之前保持可用。
	// 一旦该目录存在，SetMemoryBank 会覆盖它。
	bank := memory.NewBank(filepath.Join(root, ".argent"))
	// 基础内置工具来自编译期静态注册表（见 builtin_register.go）。
	// 注册表按显式 order 排序，顺序与原字面量逐项一致：LSP/效率工具位于
	// 记忆工具之前，使 memory_* 尾部与末尾 tool_search 保持相邻
	// （TestMemoryToolsRegisteredAndOverridable 锁定了该顺序）。
	for _, tool := range builtinTools(bank) {
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
	// context_pack 在编辑锚点周围打包真实文件内容；在此注册
	// 使它位于 memory_* 尾部和末尾 tool_search 之前。
	registerContextPack(r)
	for _, tool := range MemoryAdvancedTools(bank) {
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
	// tool_search 读取已完成的注册表，使它能搜索每个 spec。
	r.tools["tool_search"] = toolSearchTool{registry: r}
	r.order = append(r.order, "tool_search")
	// 在每个 schema 上公布 `i` 意图字段。在此一次性包装存储的工具
	// 统一覆盖 Specs、SpecsFor 和 tool_search；稍后由 Set* 助手重建的
	// 工具也会被原位置包装。
	r.wrapSchemas()
	return r
}

// wrapSchemas 装饰每个已注册的工具，使其公布的 Schema 携带
// `i` 意图字段。它是幂等的：已包装的工具保持原样。
func (r *Registry) wrapSchemas() {
	for name, tool := range r.tools {
		if _, wrapped := tool.(intentSchemaTool); !wrapped {
			r.tools[name] = wrapIntentSchema(tool)
		}
	}
}

// SetGuard 安装调用前守卫。传入 nil 会移除它。
func (r *Registry) SetGuard(g Guard) {
	r.guard = g
}

// SetSessionDir 把有状态工具（todo）指向某个会话目录。未设置时，
// 它们回退到 <root>/.argent。
func (r *Registry) SetSessionDir(dir string) {
	r.sessionDir = dir
}

// SetMemoryBank 替换 memory_* 工具背后的库，而不改变
// 注册顺序。app 在真实会话目录已知后调用它；
// nil 让当前库保持不变。
func (r *Registry) SetMemoryBank(bank *memory.Bank) {
	if bank == nil {
		return
	}
	r.tools["memory_retain"] = newMemoryRetainTool(bank)
	r.tools["memory_recall"] = newMemoryRecallTool(bank)
	r.tools["memory_forget"] = newMemoryForgetTool(bank)
	for _, tool := range MemoryAdvancedTools(bank) {
		r.tools[tool.Name()] = tool
	}
	r.wrapSchemas()
}

// SetJobs 安装 bash 工具在命令提供 background_after_ms 时
// 使用的后台作业注册表。没有它时该参数被忽略，
// 命令保持通常的超时杀死行为。
func (r *Registry) SetJobs(j *jobs.Registry) {
	r.jobs = j
}

// SetShellSessions 安装 bash 工具在 ARGENT_PERSISTENT_SHELL=1 时使用的
// 持久化 shell 会话管理器。参数用 any 承载，使 tools 包不依赖具体会话包；
// 消费方（internal/shell）通过类型断言还原为自定义的小接口。未安装时
// （默认）命令仍走原有系统 shell 路径。
func (r *Registry) SetShellSessions(sessions any) {
	r.shellSessions = sessions
}

// SetPlanUnlock 安装 exit_plan 提交计划后调用的解锁回调。调用方在计划审批
// 闭环就绪时注入它；nil 让工具保持"未接线"状态，此时调用 exit_plan 会报错
// 而非静默成功。回调经 Registry.Run 写入 ctx，因此 tools 包不必 import agent/app。
func (r *Registry) SetPlanUnlock(fn func()) {
	r.planUnlock = fn
}

// SetLspIde 安装六个 lsp_* 工具背后的适配器。app 在
// 语言服务器可用后调用它；nil 让工具继续报告
// 集成不可用。遵循 SetMemoryBank 的模式，工具在
// 原位置重建，因此注册顺序和公布的 spec 保持稳定。
func (r *Registry) SetLspIde(adapter LspIde) {
	r.tools["lsp_symbols"] = newLspSymbolsTool(adapter)
	r.tools["lsp_references"] = newLspReferencesTool(adapter)
	r.tools["lsp_code_actions"] = newLspCodeActionsTool(adapter)
	r.tools["lsp_rename_preview"] = newLspRenamePreviewTool(adapter)
	r.tools["lsp_format"] = newLspFormatTool(adapter)
	r.tools["lsp_workspace_symbols"] = newLspWorkspaceSymbolsTool(adapter)
	r.wrapSchemas()
}

// SetFormatHook 安装可选的落盘前格式化钩子，作用于 write 与 edit。
// 传 nil 移除钩子，使两个工具恢复默认行为（这也是注册表构建后的初始状态）。
// 启用后，编辑结果先经钩子格式化，最终字节才写入磁盘，且结果文案附上
// 基于最终内容计算的差异摘要，保证模型看到的与磁盘一致。
// 遵循 SetLspIde 的原位置重建模式，注册顺序与公布的 spec 保持稳定。
func (r *Registry) SetFormatHook(hook FormatHook) {
	r.tools["write"] = writeTool{format: hook}
	r.tools["edit"] = editTool{format: hook}
	r.wrapSchemas()
}

// SetMaxTimeout 把所有计时工具解析后的超时上限钳制为 d。
// 非正值禁用上限。默认值 0 让从未 opt-in 的调用方
// 保持本表引入之前的行为。
func (r *Registry) SetMaxTimeout(d time.Duration) {
	r.maxTimeout = d
}

// Names 按注册顺序返回已注册的工具名。
func (r *Registry) Names() []string {
	names := make([]string, len(r.order))
	copy(names, r.order)
	return names
}

// Specs 以稳定顺序返回工具 schema。
func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		spec := llm.ToolSpec{Type: "function"}
		spec.Function.Name = tool.Name()
		spec.Function.Description = clipDescription(tool.Description())
		// advertiseIntentSchema 是对位于本文件之外的 Set* 助手所重建工具的安全网。
		// InjectIntentSchema 是幂等的，因此已被 wrapSchemas 包装的工具
		// 不会被二次注入。
		spec.Function.Parameters = advertiseIntentSchema(json.RawMessage(tool.Schema()))
		specs = append(specs, spec)
	}
	return specs
}

// Run 执行单次工具调用。
func (r *Registry) Run(ctx context.Context, name, args string) (string, error) {
	tool, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	raw := json.RawMessage(args)
	if err := r.checkGuard(ctx, name, raw); err != nil {
		return "", fmt.Errorf("tool %q rejected: %w", name, err)
	}
	if tool.Mutating() && !r.yolo {
		return "", fmt.Errorf("tool %q changes the workspace; rerun with --yolo", name)
	}
	// 推测查询旁路：位于守卫与变更性检查之后、真正执行工具之前。
	// 命中时直接返回预跑输出并跳过 runTool；默认 ctx 不含 lookup，
	// SpecLookupFrom 只做一次失败的类型断言，行为逐字节不变。
	if lookup := SpecLookupFrom(ctx); lookup != nil {
		if out, ok := lookup(name, args); ok {
			return out, nil
		}
	}
	ctx = context.WithValue(ctx, outputLimitKey{}, r.maxOutput)
	ctx = context.WithValue(ctx, maxTimeoutKey{}, r.maxTimeout)
	if r.sessionDir != "" {
		ctx = context.WithValue(ctx, sessionDirKey{}, r.sessionDir)
	}
	if r.jobs != nil {
		ctx = context.WithValue(ctx, jobsKey{}, r.jobs)
	}
	if r.planUnlock != nil {
		ctx = context.WithValue(ctx, planUnlockKey{}, r.planUnlock)
	}
	// 同步写入 pluginapi.ExecContext，供已迁移到 internal/shell 的命令类工具
	// 读取（它们无法访问 tools 包内未导出的 context key）。原有 key 保留不动，
	// 既有工具行为逐字节不变。
	ctx = pluginapi.WithExecContext(ctx, pluginapi.ExecContext{
		MaxOutput:     r.maxOutput,
		MaxTimeout:    r.maxTimeout,
		SessionDir:    r.sessionDir,
		Jobs:          r.jobs,
		ShellSessions: r.shellSessions,
		// 把 tools 包的 opt-in 溢出能力以回调形式注入，供已迁移到
		// internal/shell 的命令类工具使用；闭包捕获同一 ctx，
		// 因此溢出配置（含 WithOutputSpill 与 env 开关）对它们同样生效。
		Spill: func(text string) (string, bool) { return spillToolOutput(ctx, text) },
	})
	return runTool(tool, ctx, r.root, name, raw)
}

// maxPanicDescLen 限制字符串化的 panic 值，使恶意或有缺陷的
// 载荷无法通过工具错误淹没模型的上下文。
const maxPanicDescLen = 512

// describePanic 在恢复的 panic 值跨越传输层前对其进行清理：
// 控制字符（包括终端转义）变为空格，结果
// 按 rune 截断，使字符串保持有效 UTF-8。
func describePanic(v any) string {
	s := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, fmt.Sprint(v))
	runes := []rune(s)
	if len(runes) > maxPanicDescLen {
		return string(runes[:maxPanicDescLen]) + "…"
	}
	return s
}

// runTool 在 panic 边界内执行单个工具。逃逸出工具的 panic
// 会毁掉整个回合并让分派的 fillUnrun 配对
// 悬而未决，因此它被规范化为单个错误结果 —— 仿照
// hooks 的 PanicError 模式 —— 并携带工具名以供诊断。
func runTool(tool Tool, ctx context.Context, root, name string, raw json.RawMessage) (out string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out = ""
			err = fmt.Errorf("tool %q panicked: %s", name, describePanic(recovered))
		}
	}()
	return tool.Run(ctx, root, raw)
}

// RunWithMeta 执行单次工具调用，并额外返回工具的
// 回收元数据。行为和输出与 Run 相同：元数据来自
// 同一组参数和结果，绝不来自第二次执行。
func (r *Registry) RunWithMeta(ctx context.Context, name, args string) (string, ToolMeta, error) {
	out, err := r.Run(ctx, name, args)
	if err != nil {
		return out, ToolMeta{}, err
	}
	tool, ok := r.tools[name]
	if !ok {
		return out, ToolMeta{}, nil
	}
	mt, ok := tool.(metaTool)
	if !ok {
		return out, ToolMeta{}, nil
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	return out, mt.Meta(json.RawMessage(args), out, nil), nil
}

// checkGuard 运行守卫，但不让 panic 逃逸到 agent 循环中。
func (r *Registry) checkGuard(ctx context.Context, name string, args json.RawMessage) (err error) {
	if r.guard == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("guard panicked: %v", recovered)
		}
	}()
	return r.guard.Check(ctx, name, args)
}

// resolve 把所有路径保持在workspace根目录之内。仅做词法检查
// 并非边界：根目录下的符号链接或 Windows junction 可以指向
// 外部，因此在把路径交给文件系统工具之前要解析
// 真实位置。
func resolve(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", path)
	}
	if err := security.CheckPathInRoots(abs, []string{root}); err != nil {
		return "", fmt.Errorf("path %q is outside the workspace: %w", path, err)
	}
	return abs, nil
}

// outputLimitKey 把每注册表的输出上限带入工具实现。
type outputLimitKey struct{}

// sessionDirKey 携带有状态工具使用的会话目录。
type sessionDirKey struct{}

// jobsKey 携带 bash 工具使用的后台作业注册表。
type jobsKey struct{}

// planUnlockKey 携带 exit_plan 用来完成审批闭环的解锁回调。
// 使用私有类型身份意味着其他包即使定义同名同结构的 key 也读不到此值。
type planUnlockKey struct{}

func outputLimit(ctx context.Context) int {
	if limit, ok := ctx.Value(outputLimitKey{}).(int); ok && limit > 0 {
		return limit
	}
	return maxToolOutput
}

func sessionDir(ctx context.Context, root string) string {
	if dir, ok := ctx.Value(sessionDirKey{}).(string); ok && strings.TrimSpace(dir) != "" {
		return dir
	}
	return filepath.Join(root, ".argent")
}

func jobRegistry(ctx context.Context) *jobs.Registry {
	if registry, ok := ctx.Value(jobsKey{}).(*jobs.Registry); ok {
		return registry
	}
	return nil
}

// PlanUnlockFrom 取出由 Registry.Run 注入的 plan 解锁回调；未接线时返回 nil。
func PlanUnlockFrom(ctx context.Context) func() {
	if fn, ok := ctx.Value(planUnlockKey{}).(func()); ok {
		return fn
	}
	return nil
}
