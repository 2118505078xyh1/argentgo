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
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/hooks"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// inlineHookPath 是直接在配置中提供的文档在信任存储中的键。内联字节与文件一样被哈希，
// 因此操作人员必须在任何命令运行之前批准该摘要。
const inlineHookPath = "config:hooks"

// HookDispatcher 是 app 所需的 internal/hooks 子集。测试在这里注入伪实现；
// 生产环境使用 *hooks.Dispatcher。
type HookDispatcher interface {
	Dispatch(ctx context.Context, config *hooks.Config, payload hooks.Payload) (hooks.Result, error)
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
		return nil
	}
	dispatcher := res.HookDispatcher
	if dispatcher == nil {
		dispatcher = &hooks.Dispatcher{}
	}
	return &hookBridge{
		dispatcher: dispatcher,
		config:     config,
		sessionID:  sessionID,
		cwd:        res.Root,
		warn:       warn,
	}
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
	if b == nil || b.config == nil || b.dispatcher == nil {
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
	return b.dispatcher.Dispatch(ctx, b.config, payload)
}

func (b *hookBridge) start(ctx context.Context, prompt string) {
	b.lifecycle(ctx, hooks.SessionStart, hooks.Payload{})
	b.lifecycle(ctx, hooks.UserPromptSubmit, hooks.Payload{Prompt: prompt})
}

func (b *hookBridge) stop(ctx context.Context, prompt, text string) {
	b.lifecycle(ctx, hooks.Stop, hooks.Payload{Prompt: prompt, Output: text})
}

func (b *hookBridge) end() {
	// 被取消的运行仍会收到其结束事件。
	b.lifecycle(context.Background(), hooks.SessionEnd, hooks.Payload{})
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

	out, runErr = h.inner.Run(ctx, name, args)
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
