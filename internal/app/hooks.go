package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/hooks"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/security"
)

// inlineHookPath 是直接在配置中提供的文档在信任存储中的键。内联字节与文件一样被哈希，
// 因此操作人员必须在任何命令运行之前批准该摘要。
const inlineHookPath = "config:hooks"

// HookDispatcher 是 app 所需的 internal/hooks 子集。测试在这里注入伪实现；
// 生产环境使用 *hooks.Dispatcher。
type HookDispatcher interface {
	Dispatch(ctx context.Context, config *hooks.Config, payload hooks.Payload) (hooks.Result, error)
}

// hookVerdictEnv 是启用钩子权限裁决折叠与内置中间件的独立环境变量。
// 与其它一次性门控一致：仅值为 "1"（允许首尾空白）时开启，默认关闭。
const hookVerdictEnv = "ARGENT_HOOK_VERDICT"

// hookReloadEnv 是启用钩子文档热重载轮询的独立环境变量。
// 与其它一次性门控一致：仅值为 "1"（允许首尾空白）时开启，默认关闭。
const hookReloadEnv = "ARGENT_HOOK_RELOAD"

// hookVerdictEnabled 报告本次运行是否启用钩子裁决折叠。
// env 为 nil、缺环境变量或值非 "1" 时返回 false（fail-closed）；不读取进程环境，
// 不修改入参。它是纯函数，便于测试两条激活状态而不污染进程全局。
func hookVerdictEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(hookVerdictEnv)) == "1"
}

// hookRewriteEnv 是启用钩子 updatedInput/updatedOutput 改写的独立门控。
const hookRewriteEnv = "ARGENT_HOOK_REWRITE"

func hookRewriteEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(hookRewriteEnv)) == "1"
}

// hookReloadEnabled 报告本次运行是否启用钩子热重载。
// 与 hookVerdictEnabled 同构：env 为 nil、缺环境变量或值非 "1" 时返回 false
// （fail-closed）；纯函数，不读进程环境、不改入参，便于两条激活状态的测试。
func hookReloadEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(hookReloadEnv)) == "1"
}

// hookVerdictMiddlewares 构造 opt-in 开启时注入的内置中间件链：
//   - 为绕过 fire() 的直接分发（DispatchDocument/FireFileChanged）补齐 sessionId/cwd，
//     与 fire():240-245 的填充规则一致；已有值不覆盖；
//   - 分发完成后向 warn 写一条有界的审计记录（事件、是否拦截、是否失败），
//     使被折叠后的裁决在 stderr 留痕。warn 为 nil 时审计为空操作。
//
// 它只在 opt-in 时构造；默认路径不创建中间件、不写审计，逐字节不变。
func hookVerdictMiddlewares(sessionID, cwd string, warn io.Writer) []hooks.Middleware {
	return []hooks.Middleware{
		func(_ context.Context, event hooks.Event, args *hooks.Payload, next func() (hooks.Result, error)) (hooks.Result, error) {
			if args.SessionID == "" {
				args.SessionID = sessionID
			}
			if args.CWD == "" {
				args.CWD = cwd
			}
			result, err := next()
			if warn != nil {
				fmt.Fprintf(warn, "hook audit: event=%s blocked=%t failed=%t\n", event, result.Blocked, err != nil)
			}
			return result, err
		},
	}
}

// hookBridge 加载受信任的文档，并在一次 Execute 的前后触发生命周期事件。
// nil 的 bridge 表示"没有配置钩子"，其所有方法都是空操作，
// 因此调用方除了在构造函数处之外不需要做 nil 检查。
type hookBridge struct {
	dispatcher HookDispatcher
	config     *hooks.Config
	sessionID  string
	cwd        string
	warn       io.Writer
	// hookCtxQ 非 nil 时，fire() 把 result.AdditionalContexts 入队，
	// 供 agent 在轮次边界抽取并注入下一轮模型上下文。nil（默认）时整条路径为空操作，
	// 默认运行逐字节不变。队列由 app 侧在每次根运行时创建并通过 WithHookContextQueue 附着到 ctx。
	hookCtxQ *agent.HookContextQueue

	// 以下字段只在 ARGENT_HOOK_RELOAD=1（reloadEnabled）时接线。
	// store 持有当前生效快照；sources 是重新求指纹的来源集合；trust 是同一份信任门。
	// reloadEnabled 为 false（默认）时三者为零值，fire() 仍走原来的静态 config 路径。
	store         *hooks.SnapshotStore
	sources       []hooks.Source
	trust         *hooks.TrustStore
	reloadEnabled bool
	reloadEvery   time.Duration // 轮询间隔；零值由 startReload 兜底为默认值
	reloadOnce    sync.Once     // 保证每个 bridge 只启动一个轮询 goroutine
}

// 不存在受信任的钩子文档时，newHookBridge 返回 nil。损坏的信任文件、不受信任的文档和
// 格式错误的 JSON 会被报告并跳过：钩子问题绝不能中断一个原本有效的运行。
func newHookBridge(res Resolved, sessionID string, warn io.Writer) *hookBridge {
	trust := hooks.NewTrustStore()
	if strings.TrimSpace(res.Storage) != "" {
		if err := trust.LoadFile(filepath.Join(res.Storage, "hooks-trust.json")); err != nil {
			noteHook(warn, "hooks trust", err)
		}
	}
	config := loadHookDocuments(res, trust, warn)
	if config == nil {
		// 常驻扩展（opt-in ARGENT_HOOK_DAEMON）是"虚拟钩子"：即使没有普通钩子
		// 文档，也需要一个非空 config 让 fire() 真正派发 daemon 事件。
		// 未设置该 env 时仍按原逻辑返回 nil，默认路径逐字节不变。
		if strings.TrimSpace(os.Getenv(hooks.DaemonEnv)) == "" {
			return nil
		}
		config = &hooks.Config{}
	}
	dispatcher := res.HookDispatcher
	if dispatcher == nil {
		// 默认路径仍是零值 Dispatcher；仅在 opt-in 时开启 allow 解除拦截并注入
		// 内置中间件（审计 + 补 session/cwd）。StopBlocks 本轮刻意不开启：其
		// Continuation 尚未被 app 消费，提前打开会产生半截行为。
		built := &hooks.Dispatcher{}
		if hookVerdictEnabled(os.Getenv) {
			built.VerdictEnabled = true
			built.Middlewares = hookVerdictMiddlewares(sessionID, res.Root, warn)
		}
		dispatcher = built
		// 常驻外部进程扩展（opt-in ARGENT_HOOK_DAEMON）：只挂到本函数自己
		// 构造的 dispatcher 上，避免影响外部注入的 HookDispatcher。未配置时
		// AttachDaemon 是纯 no-op，不读文件、不派生进程。
		if err := hooks.AttachDaemon(built, os.Getenv, trust, warn); err != nil {
			noteHook(warn, "hooks daemon", err)
		}
	}

	// 热重载是独立 opt-in：开启时用与首次加载完全一致的来源与信任门构造 store，
	// 并把已受信任的静态 config 作为第 1 代快照先放进 store，使首次 fire 即可用它。
	// 关闭时下列字段保持零值，fire() 逐字节沿用原来的静态 config 路径。
	reloadEnabled := hookReloadEnabled(os.Getenv)
	var store *hooks.SnapshotStore
	var sources []hooks.Source
	if reloadEnabled {
		sources = hookReloadSources(res)
		store = &hooks.SnapshotStore{}
		// Fingerprint 留空：首次 poll 会与当前来源字节比对并采纳为第 2 代；
		// 若来源读取失败，SnapshotFromSources 返回错误，last-good 仍为这里放入的 config。
		store.Swap(hooks.ConfigSnapshot{Config: config, Generation: 1}, 1)
	}
	return &hookBridge{
		dispatcher:    dispatcher,
		config:        config,
		sessionID:     sessionID,
		cwd:           res.Root,
		warn:          warn,
		store:         store,
		sources:       sources,
		trust:         trust,
		reloadEnabled: reloadEnabled,
	}
}

// hookReloadSources 按与 loadHookDocuments 相同的枚举顺序构造可重新读取的来源：
// 配置里的内联/路径条目，再是标准项目与用户文件（含已加入的插件路径）。
// 信任键与首次加载保持一致：文件路径用解析后的路径，内联仍用 inlineHookPath；
// 因此同一份信任存储既管首次加载，也管后续每次 poll 的信任门。
func hookReloadSources(res Resolved) []hooks.Source {
	var sources []hooks.Source
	for _, source := range configHookSources(res) {
		if source.path != "" {
			path := resolveHookPath(res, source.path)
			sources = append(sources, hooks.NewKeyedSource(path, hooks.SourceFunc(func() ([]byte, error) {
				return os.ReadFile(path)
			})))
			continue
		}
		inline := source.inline
		sources = append(sources, hooks.NewKeyedSource(inlineHookPath, hooks.SourceFunc(func() ([]byte, error) {
			return json.Marshal(inline)
		})))
	}
	paths := hookFilePaths(res)
	paths = append(paths, pluginHookPaths(res)...)
	for _, path := range paths {
		key := path
		sources = append(sources, hooks.NewKeyedSource(key, hooks.SourceFunc(func() ([]byte, error) {
			return os.ReadFile(key)
		})))
	}
	return sources
}

// loadHookDocuments 合并每一个受信任的来源：来自 config.Raw["hooks"] 的内联/路径条目，
// 然后是标准的项目和用户文件。
func loadHookDocuments(res Resolved, trust *hooks.TrustStore, warn io.Writer) *hooks.Config {
	merged := &hooks.Config{}
	found := false
	add := func(document *hooks.Document) {
		if document == nil || document.Config == nil || len(document.Config.Commands) == 0 {
			return
		}
		merged.Merge(document.Config)
		found = true
	}
	for _, source := range configHookSources(res) {
		if source.path != "" {
			path := resolveHookPath(res, source.path)
			document, err := hooks.LoadDocument(path, trust)
			if err != nil {
				noteHook(warn, "hooks "+path, err)
				continue
			}
			add(document)
			continue
		}
		data, err := json.Marshal(source.inline)
		if err != nil {
			noteHook(warn, "hooks inline", err)
			continue
		}
		if !trust.IsTrusted(inlineHookPath, hooks.Digest(data)) {
			noteHook(warn, inlineHookPath, hooks.ErrUntrusted)
			continue
		}
		config, err := hooks.ParseConfig(data)
		if err != nil {
			noteHook(warn, inlineHookPath, err)
			continue
		}
		add(&hooks.Document{Path: inlineHookPath, Digest: hooks.Digest(data), Config: config})
	}
	paths := hookFilePaths(res)
	// 插件钩子文档与命令/MCP 在同一信任门槛下加入标准文件：用户插件总是加入，
	// 项目插件仅在受信任时加入。信任中的 SHA-256 批准仍适用于下面的每条路径。
	paths = append(paths, pluginHookPaths(res)...)
	for _, path := range paths {
		document, err := hooks.LoadDocument(path, trust)
		if err != nil {
			noteHook(warn, "hooks "+path, err)
			continue
		}
		add(document)
	}
	if !found {
		return nil
	}
	return merged
}

type hookSource struct {
	path   string
	inline map[string]any
}

// configHookSources 接受单个路径、路径列表或内联文档。带有 "paths" 的对象可以同时包含两者，
// 就像插件清单那样。
func configHookSources(res Resolved) []hookSource {
	raw := res.Cfg.Raw["hooks"]
	var sources []hookSource
	addPaths := func(value any) {
		for _, text := range policyStrings(value) {
			sources = append(sources, hookSource{path: text})
		}
	}
	switch value := raw.(type) {
	case string:
		addPaths(value)
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				sources = append(sources, hookSource{path: text})
			}
		}
	case []string:
		for _, text := range value {
			if strings.TrimSpace(text) != "" {
				sources = append(sources, hookSource{path: text})
			}
		}
	case map[string]any:
		inline := make(map[string]any, len(value))
		for key, item := range value {
			inline[key] = item
		}
		if paths, ok := inline["paths"]; ok {
			addPaths(paths)
			delete(inline, "paths")
		}
		if len(inline) == 0 {
			break
		}
		if _, wrapped := inline["hooks"]; !wrapped {
			inline = map[string]any{"hooks": inline}
		}
		sources = append(sources, hookSource{inline: inline})
	}
	return sources
}

// hookFilePaths 列出标准文档，项目优先。旧版 ~/.argent 和当前的 ~/.argentgo 主目录都会被考虑。
func hookFilePaths(res Resolved) []string {
	seen := make(map[string]bool)
	var paths []string
	add := func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			paths = append(paths, path)
		}
	}
	add(filepath.Join(res.Root, ".argent", "hooks.json"))
	entries, _ := os.ReadDir(filepath.Join(res.Root, ".argent", "hooks"))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		add(filepath.Join(res.Root, ".argent", "hooks", name))
	}
	home := res.Home
	if strings.TrimSpace(home) == "" {
		home = homeDir(nil)
	}
	if strings.TrimSpace(home) != "" {
		add(filepath.Join(home, ".argent", "hooks.json"))
		add(filepath.Join(home, ".argentgo", "hooks.json"))
	}
	return paths
}

func resolveHookPath(res Resolved, value string) string {
	path := strings.TrimSpace(value)
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home := res.Home
		if strings.TrimSpace(home) == "" {
			home = homeDir(nil)
		}
		if home != "" {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(res.Root, path)
	}
	return filepath.Clean(path)
}

// fire 派发一个事件，并填充运行上下文。钩子实现中的 panic 会被转换为错误，
// 而不是逃逸到 agent 循环中。
func (b *hookBridge) fire(ctx context.Context, event hooks.Event, payload hooks.Payload) (result hooks.Result, err error) {
	if b == nil {
		return hooks.Result{}, nil
	}
	// 热重载开启时每次派发都从 store 取当前生效快照，使长会话里改过的文档
	// 无需重启即可生效；关闭时 config 就是原来的静态指针，路径逐字节不变。
	config := b.config
	if b.reloadEnabled && b.store != nil {
		config = b.store.Load().Config
		// 轮询 goroutine 惰性启动：app 侧把每次运行的 ctx 传给 fire，第一次
		// fire（start 的 SessionStart）即确定其生命周期，ctx 取消时自动退出。
		b.startReload(ctx)
	}
	if config == nil || b.dispatcher == nil {
		return hooks.Result{}, nil
	}
	if payload.Event == "" {
		payload.Event = string(event)
	}
	if payload.SessionID == "" {
		payload.SessionID = b.sessionID
	}
	if payload.CWD == "" {
		payload.CWD = b.cwd
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = hooks.Result{}
			err = fmt.Errorf("hooks: %s dispatch panicked: %v", payload.Event, recovered)
		}
	}()
	result, err = b.dispatcher.Dispatch(ctx, config, payload)
	// 把钩子声明的附加上下文交给 agent 队列。b.hookCtxQ 为 nil（未接线）时
	// 整段为空操作；result 无附加上下文时 range 不执行，默认路径逐字节不变。
	// 队列的 Add 已做空白/去重/上限约束，这里逐条投递即可。
	if err == nil && b.hookCtxQ != nil {
		for _, text := range result.AdditionalContexts {
			b.hookCtxQ.Add(text)
		}
	}
	return result, err
}

// defaultHookReloadInterval 是轮询的默认周期，只在 opt-in 时用到。
const defaultHookReloadInterval = time.Second

// poll 执行一次热重载探测：只对来源字节变化（指纹变化）的快照做二次校验，
// 通过后用单调递增的代数采纳进 store；读取/信任/解析/校验任一步失败都只记账
// 并写 warn，store 保留上一次成功的快照（last-good），绝不把失败内容投入运行。
func (b *hookBridge) poll(ctx context.Context) {
	if b == nil || !b.reloadEnabled || b.store == nil || len(b.sources) == 0 {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	last := b.store.Load()
	snap, changed, err := hooks.SnapshotFromSources(b.sources, b.trust, last.Fingerprint)
	if err != nil {
		b.store.NoteFailure(err)
		noteHook(b.warn, "hooks reload", err)
		return
	}
	if !changed {
		return
	}
	// 采纳前的纵深防御：当前 ConfigSnapshot 不含逐源信息，ValidateBeforeAdopt
	// 走空凭据分支放行；信任已在 SnapshotFromSources 的 LoadSource 阶段校验。
	if err := hooks.ValidateBeforeAdopt(snap, b.trust); err != nil {
		b.store.NoteFailure(err)
		noteHook(b.warn, "hooks reload validate", err)
		return
	}
	// 用 last-good 的代数 +1 覆盖快照 Generation，再以同一个单调递增代数 Swap，
	// 使迟到的旧换代无法覆盖已生效的新快照。
	snap.Generation = last.Generation + 1
	if !b.store.Swap(snap, uint64(snap.Generation)) {
		return
	}
}

// startReload 惰性启动唯一一个轮询 goroutine。只有 opt-in、且确有来源与 store
// 时才启动；ctx 取消后 runReloadLoop 自行退出，不泄漏 goroutine。
func (b *hookBridge) startReload(ctx context.Context) {
	if b == nil || !b.reloadEnabled || b.store == nil || len(b.sources) == 0 || ctx == nil {
		return
	}
	b.reloadOnce.Do(func() {
		interval := b.reloadEvery
		if interval <= 0 {
			interval = defaultHookReloadInterval
		}
		go b.runReloadLoop(ctx, interval, nil)
	})
}

// runReloadLoop 按 interval 周期调用 poll，并在 ctx 取消时退出；done 非 nil 时
// 退出前关闭它，便于测试确定性地断言 goroutine 已停止。
func (b *hookBridge) runReloadLoop(ctx context.Context, interval time.Duration, done chan<- struct{}) {
	if interval <= 0 {
		interval = defaultHookReloadInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if done != nil {
		defer close(done)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.poll(ctx)
		}
	}
}

func (b *hookBridge) start(ctx context.Context, prompt string) {
	b.lifecycle(ctx, hooks.SessionStart, hooks.Payload{})
	b.lifecycle(ctx, hooks.UserPromptSubmit, hooks.Payload{Prompt: prompt})
}

func (b *hookBridge) stop(ctx context.Context, prompt, text string) {
	b.lifecycle(ctx, hooks.Stop, hooks.Payload{Prompt: prompt, Output: text})
}

func (b *hookBridge) end() {
	if b == nil {
		return
	}
	// 被取消的运行仍会收到其结束事件。
	b.lifecycle(context.Background(), hooks.SessionEnd, hooks.Payload{})
	// 会话结束时回收常驻扩展进程；未挂载时 StopDaemon 是 no-op。
	// dispatcher 是接口类型，只有本函数构造的 *hooks.Dispatcher 才可能挂了 daemon。
	if d, ok := b.dispatcher.(*hooks.Dispatcher); ok {
		hooks.StopDaemon(d)
	}
}

func (b *hookBridge) lifecycle(ctx context.Context, event hooks.Event, payload hooks.Payload) {
	result, err := b.fire(ctx, event, payload)
	if err != nil {
		noteHook(b.warn, string(event)+" hook", err)
		return
	}
	if result.Message != "" && !result.Blocked {
		noteHook(b.warn, string(event)+" hook", errors.New(result.Message))
	}
}

// hookedTools 在每次真实工具调用的前后派发 PreToolUse/PostToolUse。
// Specs 被原样委托，因此钩子接线绝不会改变模型看到的内容。
type hookedTools struct {
	inner  agent.ToolRunner
	bridge *hookBridge
	// mutationPaths 报告变更型工具调用所改动的工作区相对路径。
	// 它由工具集注入，使 FileChanged 钩子只看到真实的、边界内的文件变更；
	// nil（例如没有注册表的测试）会禁用该事件。它刻意是一个窄函数，而非整个注册表。
	mutationPaths func(name, args string) []string
	// auditSink 是结构化审计上下文的可选接收端。默认 nil（未接线）时 Run 不
	// 生成上下文、不产生额外输出，默认运行逐字节不变。接入方按需注入回调，
	// 例如把映射写入 security.AuditLog；总开关由接入方掌握。
	auditSink func(map[string]string)
	// sourceAnnotator 是来源 URL 标注的可选接线点。默认 nil（未接线）时 Run
	// 绝不改写工具输出，默认运行逐字节不变；注入 annotateSourceURL 后仅在
	// web_fetch / web_search 成功结果前加一行来源标注。总开关由接入方掌握。
	sourceAnnotator func(toolName, args, output string) string
	// autoDiagnostics 是编辑后诊断的可注入开关（R25 opt-in auto_verify）。
	// 零值 false 时完全退化为旧的 EnableEditDiagnostics 全局开关行为；
	// 置 true 时即使全局开关关闭也走诊断路径。公共 SetXxx 不变。
	autoDiagnostics bool
	// preApproval 在 ARGENT_HOOK_VERDICT=1 时，把 PreToolUse 的 allow 裁决打成
	// 安全链的预审批章，跳过审批弹窗（策略与审计照跑）。默认 false。
	preApproval bool
	// rewrite 在 ARGENT_HOOK_REWRITE=1 时应用钩子声明的 updatedInput/updatedOutput。
	// 默认 false，工具参数与输出逐字节不变。
	rewrite bool
	// lspFileNotifier 是编辑后主动通知语言服务器文件变更的可选接线点。默认 nil
	// 时 Run 不产生任何通知，成功路径逐字节不变；注入后仅在变更型工具成功返回、
	// 且 mutationPaths 报告了路径时调用。通知失败绝不影响工具结果。
	lspFileNotifier func(root string, paths []string)
}

func (h *hookedTools) Specs() []llm.ToolSpec { return h.inner.Specs() }

func (h *hookedTools) Run(ctx context.Context, name, args string) (out string, runErr error) {
	started := time.Now()
	// 可选的审计上下文接线：默认 auditSink 为 nil，直接返回，不增加任何
	// 调用或输出。接线后在每次调用（含被钩子拦截/失败的调用）结束时投递一份
	// 脱敏后的结构化上下文，绝不替换工具自身的结果。
	defer func() {
		if h.auditSink == nil {
			return
		}
		h.auditSink(toolAuditContext(name, args, toolAuditResult{
			output: out, err: runErr, elapsed: time.Since(started),
		}))
	}()
	raw := json.RawMessage(args)
	pre, err := h.bridge.fire(ctx, hooks.PreToolUse, hooks.Payload{Tool: name, Args: raw})
	if err != nil {
		return "", fmt.Errorf("PreToolUse hook: %w", err)
	}
	if pre.Blocked {
		detail := strings.TrimSpace(pre.Message)
		if detail == "" {
			return "", fmt.Errorf("the %s tool call was blocked by a PreToolUse hook", name)
		}
		return "", fmt.Errorf("the %s tool call was blocked by a PreToolUse hook: %s", name, detail)
	}

	// 输入改写（opt-in ARGENT_HOOK_REWRITE）：审批与实际执行必须看到同一份字节，
	// 因此改写发生在调用 inner.Run（其中运行安全链）之前。
	runArgs := args
	if h.rewrite && len(pre.UpdatedInput) > 0 && json.Valid(pre.UpdatedInput) {
		runArgs = string(pre.UpdatedInput)
	}
	// allow 预审批章（opt-in ARGENT_HOOK_VERDICT）：按工具名与改写后的参数绑定，
	// 安全链在 Ask 之前查章跳过弹窗。没有章时该调用是纯粹的 ctx 原样传递。
	if h.preApproval && pre.Verdict == hooks.VerdictAllow {
		ctx = security.WithHookApproval(ctx, name, runArgs)
	}

	out, runErr = h.inner.Run(ctx, name, runArgs)
	// 输出改写（opt-in ARGENT_HOOK_REWRITE）：PreToolUse 声明的 updatedOutput
	// 在工具成功后替换回灌给模型的结果正文（限长脱敏已在 hooks 层完成）。
	if h.rewrite && runErr == nil && len(pre.UpdatedOutput) > 0 {
		var text string
		if json.Unmarshal(pre.UpdatedOutput, &text) == nil {
			out = text
		}
	}
	// PreToolUse 钩子可能成功，但仍携带给模型的建议。像 PostToolUse 注释一样把它附加到工具结果上，
	// 使该消息不会被静默丢弃。消息为空时 appendHookNote 是空操作。
	out = appendHookNote(out, pre.Message)
	if runErr != nil {
		h.noteTool(ctx, hooks.PostToolUseFailure, hooks.Payload{
			Tool: name, Args: raw, Error: runErr.Error(),
		}, &out)
		return out, runErr
	}
	post, err := h.bridge.fire(ctx, hooks.PostToolUse, hooks.Payload{
		Tool: name, Args: raw, Output: out,
	})
	if err != nil {
		// 后置钩子绝不能吞掉成功的工具结果；把失败与该结果并列呈现，使模型能同时读到两者。
		return appendHookNote(out, "PostToolUse hook failed: "+err.Error()), nil
	}
	if post.Message != "" {
		out = appendHookNote(out, post.Message)
	}
	// 可选的编辑后诊断：总开关默认关闭，关闭时零调用；仅在写类工具成功返回后
	// 按 mutationPaths 报告的路径拉取 LSP 错误诊断并附加，绝不替换原输出。
	h.appendEditDiagnostics(ctx, name, args, &out)
	// 在一次成功的变更型工具调用之后，把实际改动的文件通知给订阅者。
	// 这是端到端选择性启用的：没有订阅 FileChanged 的受信任钩子文档时，桥接派发是空操作；
	// 路径来自注册表的 MutationPaths 接缝，因此只读工具和边界外写入永远不会产生事件。
	// 默认运行不变。
	h.fireFileChanged(ctx, name, args, &out)
	// 可选的编辑后 LSP 文件变更通知：仅在成功分支触发，只通知已运行的客户端，
	// 没有注入 notifier 或没有变更路径时零调用，且结果绝不影响工具输出。
	h.notifyLSPFileChanged(name, args)
	// 可选的来源 URL 标注：默认 sourceAnnotator 为 nil，直接返回，成功工具的
	// 输出逐字节不变；接入方按需注入 annotateSourceURL，只改 web_fetch /
	// web_search 的结果，绝不替换内容本身。
	if h.sourceAnnotator != nil {
		out = h.sourceAnnotator(name, args, out)
	}
	return out, nil
}

// fireFileChanged 为注册表报告为被成功工具调用修改的每条路径派发一个 FileChanged 事件。
// 它复用 noteTool，使失败或给出建议的钩子附加到工具输出上而绝不替换它，
// 且事件的 Path（而非 Tool）驱动分发器中的匹配器过滤。
func (h *hookedTools) fireFileChanged(ctx context.Context, name, args string, out *string) {
	if h.mutationPaths == nil {
		return
	}
	for _, path := range h.mutationPaths(name, args) {
		h.noteTool(ctx, hooks.FileChanged, hooks.Payload{Path: path}, out)
	}
}

// noteTool 把非阻塞的钩子失败附加到工具输出上，使模型能读到它们；
// 它们绝不会替换工具自身的错误。
func (h *hookedTools) noteTool(ctx context.Context, event hooks.Event, payload hooks.Payload, out *string) {
	result, err := h.bridge.fire(ctx, event, payload)
	if err != nil {
		*out = appendHookNote(*out, err.Error())
		return
	}
	if result.Message != "" {
		*out = appendHookNote(*out, result.Message)
	}
}

func appendHookNote(out, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return out
	}
	if out == "" {
		return "[hook] " + message
	}
	return out + "\n[hook] " + message
}

// AppendVerifyNote 把一条紧凑、脱敏的验证结论附加到工具输出上。
// 它复用 agent.VerificationFeedback，使注释保持有界且凭据安全，
// 并复用 appendHookNote，使它看起来与其他 PostToolUse 注释一样。
//
// 这是一个可选的构建块，不属于默认钩子路径：本层没有验证命令运行器。要接线它，
// 拥有验证命令的组件应在 PostToolUse 事件之后调用它，例如：
//
//	out = AppendVerifyNote(out, "post-edit verify", passed, verifyOutput)
//
// 并将其置于配置开关或 ARGENT_POST_EDIT_VERIFY 环境变量标志之后（默认关闭），
// 使没有配置验证命令的运行保持不变。
func AppendVerifyNote(out string, label string, passed bool, output string) string {
	return appendHookNote(out, agent.VerificationFeedback(label, passed, output, 0))
}

func noteHook(warn io.Writer, what string, err error) {
	if warn == nil || err == nil {
		return
	}
	fmt.Fprintf(warn, "%s: %v (skipped)\n", what, err)
}
