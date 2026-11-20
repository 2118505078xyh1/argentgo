package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/llm"
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
	for _, tool := range []Tool{
		readTool{}, writeTool{}, editTool{}, bashTool{}, lsTool{}, grepTool{}, findTool{}, powershellTool{},
		globTool{}, todoTool{}, multiEditTool{}, webFetchTool{}, gitStatusTool{},
		currentTimeTool{}, webSearchTool{}, repoMapTool{}, submitPlanTool{},
	} {
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
	// tool_search reads the finished registry so it can search every spec.
	r.tools["tool_search"] = toolSearchTool{registry: r}
	r.order = append(r.order, "tool_search")
	return r
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

// SetJobs installs the background-job registry used by the bash tool when a
// command supplies background_after_ms. Without it that argument is ignored
// and the command keeps the usual kill-on-timeout behaviour.
func (r *Registry) SetJobs(j *jobs.Registry) {
	r.jobs = j
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
		spec.Function.Parameters = json.RawMessage(tool.Schema())
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
	ctx = withBackgroundReporter(ctx, r.backgroundReporter)
	return tool.Run(ctx, r.root, raw)
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

// resolve keeps every path inside the workspace root.
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
