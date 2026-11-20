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
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// maxToolDescLen bounds a tool description shipped to the model. A long MCP
// description is a prompt-injection vector and eats context, so Specs clips it
// before the schema crosses the wire. The Tool interface and Schema are intact.
const maxToolDescLen = 1024

// clipDescription keeps at most maxToolDescLen runes, appending an ellipsis
// when it truncates. Slicing by runes keeps the result valid UTF-8 even when a
// multibyte character straddles the limit.
func clipDescription(s string) string {
	runes := []rune(s)
	if len(runes) <= maxToolDescLen {
		return s
	}
	return string(runes[:maxToolDescLen-1]) + "…"
}

// intentSchemaEnv opts out of advertising the `i` (concise intent) property in
// tool parameter schemas. Injection is ON by default: the agent strips `i` on
// the read side (agent.ExtractIntent), so every schema crossing the wire must
// list it for the API_REFERENCE "every schema accepts i" contract to hold.
// Set this to a truthy value (1, true, yes, on, ...) to restore pre-injection
// schemas; "", 0, false, no and off leave injection enabled.
const intentSchemaEnv = "ARGENT_NO_INTENT_SCHEMA"

// intentSchemaDisabled reports whether `i` should be omitted from advertised
// schemas. The env var is read on every call rather than cached so tests and
// long-lived runtimes can toggle it without a restart.
func intentSchemaDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(intentSchemaEnv))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// advertiseIntentSchema adds the `i` intent property to a tool parameter
// schema unless the operator opted out. InjectIntentSchema is idempotent, so a
// schema that already carries `i` (e.g. one returned by intentSchemaTool below)
// is returned unchanged; there is no double-injection.
func advertiseIntentSchema(params json.RawMessage) json.RawMessage {
	if intentSchemaDisabled() {
		return params
	}
	return InjectIntentSchema(params)
}

// intentSchemaTool decorates a Tool so the schema it advertises carries the
// `i` intent property. Runtime behaviour is delegated unchanged; only the
// Schema crossing the wire is augmented. Wrapping the stored tools — rather
// than patching only Specs — keeps every schema reader in the package (Specs,
// SpecsFor, tool_search) consistent. The gate is evaluated per call so the
// env toggle is honoured even for an already-built registry.
type intentSchemaTool struct {
	Tool
}

func wrapIntentSchema(tool Tool) Tool {
	return intentSchemaTool{Tool: tool}
}

func (t intentSchemaTool) Schema() string {
	return string(advertiseIntentSchema(json.RawMessage(t.Tool.Schema())))
}

// Meta forwards the optional reclamation hook so wrapping never changes
// RunWithMeta behaviour. A wrapped tool without an inner Meta yields the zero
// ToolMeta, which callers already treat as "no metadata".
func (t intentSchemaTool) Meta(args json.RawMessage, output string, err error) ToolMeta {
	if m, ok := t.Tool.(metaTool); ok {
		return m.Meta(args, output, err)
	}
	return ToolMeta{}
}

// New returns a registry rooted at root with the default output cap.
func New(root string, yolo bool) *Registry {
	return NewWithLimit(root, yolo, maxToolOutput)
}

// NewWithLimit returns a registry rooted at root whose tool output is clipped to
// maxOutput characters (head and tail kept). A non-positive limit restores the
// default.
func NewWithLimit(root string, yolo bool, maxOutput int) *Registry {
	if maxOutput <= 0 {
		maxOutput = maxToolOutput
	}
	r := &Registry{root: root, yolo: yolo, maxOutput: maxOutput, tools: make(map[string]Tool)}
	// A default bank keeps the memory tools usable before the app wires a real
	// session directory. SetMemoryBank overrides it once that directory exists.
	bank := memory.NewBank(filepath.Join(root, ".argent"))
	for _, tool := range []Tool{
		readTool{}, writeTool{}, editTool{}, bashTool{}, lsTool{}, grepTool{}, findTool{}, powershellTool{},
		globTool{}, todoTool{}, multiEditTool{}, webFetchTool{}, gitStatusTool{},
		currentTimeTool{}, webSearchTool{}, repoMapTool{}, submitPlanTool{},
		// LSP IDE tools are always advertised. Until the app wires an adapter
		// with SetLspIde they report "not available" without failing the turn.
		// They are registered before the memory tools so the memory_* tools stay
		// adjacent to the trailing tool_search (see TestMemoryToolsRegisteredAndOverridable).
		newLspSymbolsTool(nil), newLspReferencesTool(nil), newLspCodeActionsTool(nil),
		newLspRenamePreviewTool(nil), newLspFormatTool(nil), newLspWorkspaceSymbolsTool(nil),
		securityScanTool{},
		// Stdlib-portable productivity tools. usage/history start with nil
		// sources and report unavailable until the app calls SetUsageSource /
		// SetHistorySource. Registered before the memory tools so the memory_*
		// tail stays adjacent to tool_search (TestMemoryToolsRegisteredAndOverridable).
		&diffTool{}, &usageTool{}, &historyTool{},
		newMemoryRetainTool(bank), newMemoryRecallTool(bank), newMemoryForgetTool(bank),
	} {
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
	// context_pack packs real file contents around an edit anchor; register it
	// here so it sits before the memory_* tail and the trailing tool_search.
	registerContextPack(r)
	for _, tool := range MemoryAdvancedTools(bank) {
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
	// tool_search reads the finished registry so it can search every spec.
	r.tools["tool_search"] = toolSearchTool{registry: r}
	r.order = append(r.order, "tool_search")
	// Advertise the `i` intent field on every schema. Wrapping the stored tools
	// once here covers Specs, SpecsFor and tool_search uniformly; tools rebuilt
	// later by Set* helpers are wrapped in place as well.
	r.wrapSchemas()
	return r
}

// wrapSchemas decorates every registered tool so its advertised Schema carries
// the `i` intent field. It is idempotent: an already-wrapped tool is left as-is.
func (r *Registry) wrapSchemas() {
	for name, tool := range r.tools {
		if _, wrapped := tool.(intentSchemaTool); !wrapped {
			r.tools[name] = wrapIntentSchema(tool)
		}
	}
}

// SetGuard installs the pre-call guard. Passing nil removes it.
func (r *Registry) SetGuard(g Guard) {
	r.guard = g
}

// SetSessionDir points state tools (todo) at a session directory. When unset,
// they fall back to <root>/.argent.
func (r *Registry) SetSessionDir(dir string) {
	r.sessionDir = dir
}

// SetMemoryBank replaces the bank behind the memory_* tools without changing
// registration order. The app calls this once the real session directory is
// known; nil leaves the current bank in place.
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

// SetJobs installs the background-job registry used by the bash tool when a
// command supplies background_after_ms. Without it that argument is ignored
// and the command keeps the usual kill-on-timeout behaviour.
func (r *Registry) SetJobs(j *jobs.Registry) {
	r.jobs = j
}

// SetLspIde installs the adapter behind the six lsp_* tools. The app calls this
// once a language server is available; nil leaves the tools reporting that the
// integration is unavailable. Following the SetMemoryBank pattern, the tools
// are rebuilt in place so registration order and advertised specs stay stable.
func (r *Registry) SetLspIde(adapter LspIde) {
	r.tools["lsp_symbols"] = newLspSymbolsTool(adapter)
	r.tools["lsp_references"] = newLspReferencesTool(adapter)
	r.tools["lsp_code_actions"] = newLspCodeActionsTool(adapter)
	r.tools["lsp_rename_preview"] = newLspRenamePreviewTool(adapter)
	r.tools["lsp_format"] = newLspFormatTool(adapter)
	r.tools["lsp_workspace_symbols"] = newLspWorkspaceSymbolsTool(adapter)
	r.wrapSchemas()
}

// SetMaxTimeout caps the resolved timeout of every timed tool at d. A
// non-positive value disables the cap. The default of 0 keeps the pre-table
// behaviour unchanged for callers that never opt in.
func (r *Registry) SetMaxTimeout(d time.Duration) {
	r.maxTimeout = d
}

// Names returns the registered tool names in registration order.
func (r *Registry) Names() []string {
	names := make([]string, len(r.order))
	copy(names, r.order)
	return names
}

// Specs returns the tool schemas in a stable order.
func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		spec := llm.ToolSpec{Type: "function"}
		spec.Function.Name = tool.Name()
		spec.Function.Description = clipDescription(tool.Description())
		// advertiseIntentSchema is the safety net for tools rebuilt by Set*
		// helpers living outside this file. InjectIntentSchema is idempotent,
		// so tools already wrapped by wrapSchemas are not injected twice.
		spec.Function.Parameters = advertiseIntentSchema(json.RawMessage(tool.Schema()))
		specs = append(specs, spec)
	}
	return specs
}

// Run executes one tool call.
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
	ctx = context.WithValue(ctx, outputLimitKey{}, r.maxOutput)
	ctx = context.WithValue(ctx, maxTimeoutKey{}, r.maxTimeout)
	if r.sessionDir != "" {
		ctx = context.WithValue(ctx, sessionDirKey{}, r.sessionDir)
	}
	if r.jobs != nil {
		ctx = context.WithValue(ctx, jobsKey{}, r.jobs)
	}
	return runTool(tool, ctx, r.root, name, raw)
}

// maxPanicDescLen bounds the stringified panic value so a hostile or buggy
// payload cannot flood the model's context through a tool error.
const maxPanicDescLen = 512

// describePanic sanitizes a recovered panic value before it crosses the wire:
// control characters (including terminal escapes) become spaces and the result
// is clipped by runes so the string stays valid UTF-8.
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

// runTool executes one tool behind a panic boundary. A panic escaping a tool
// would blow up the whole turn and leave the dispatch's fillUnrun pairing
// unsettled, so it is normalized into a single error result — mirroring the
// hooks PanicError pattern — that carries the tool name for diagnosis.
func runTool(tool Tool, ctx context.Context, root, name string, raw json.RawMessage) (out string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out = ""
			err = fmt.Errorf("tool %q panicked: %s", name, describePanic(recovered))
		}
	}()
	return tool.Run(ctx, root, raw)
}

// RunWithMeta executes one tool call and additionally returns the tool's
// reclamation metadata. Behaviour and output are identical to Run: the meta is
// derived from the same arguments and result, never from a second execution.
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

// checkGuard runs the guard without letting a panic escape into the agent loop.
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

// resolve keeps every path inside the workspace root. The lexical check alone
// is not a boundary: a symlink or Windows junction under the root can point
// outside, so the real location is resolved before the path is handed to a
// filesystem tool.
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

// outputLimitKey carries the per-registry output cap into tool implementations.
type outputLimitKey struct{}

// sessionDirKey carries the session directory used by state tools.
type sessionDirKey struct{}

// jobsKey carries the background-job registry used by the bash tool.
type jobsKey struct{}

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
