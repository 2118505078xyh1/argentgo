// Package tools implements the filesystem and shell tools exposed to the model.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/proc"
)

const (
	maxToolOutput = 40000
	maxMatches    = 200
)

// Tool is one callable capability.
type Tool interface {
	Name() string
	Description() string
	Schema() string
	Mutating() bool
	Run(ctx context.Context, root string, args json.RawMessage) (string, error)
}

// Guard is consulted before every tool call. A non-nil error is returned to the
// model as a tool error; the tool never runs.
type Guard interface {
	Check(ctx context.Context, name string, args json.RawMessage) error
}

// Registry owns the tool set and enforces the workspace boundary.
type Registry struct {
	root               string
	yolo               bool
	maxOutput          int
	sessionDir         string
	jobs               *jobs.Registry
	guard              Guard
	backgroundReporter BackgroundReporter
	tools              map[string]Tool
	order              []string
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
		readTool{}, writeTool{}, editTool{}, bashTool{}, lsTool{}, grepTool{}, findTool{},
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
		spec.Function.Description = tool.Description()
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
	if r.sessionDir != "" {
		ctx = context.WithValue(ctx, sessionDirKey{}, r.sessionDir)
	}
	if r.jobs != nil {
		ctx = context.WithValue(ctx, jobsKey{}, r.jobs)
	}
	ctx = withBackgroundReporter(ctx, r.backgroundReporter)
	return tool.Run(ctx, r.root, raw)
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

func clip(ctx context.Context, text string) string {
	return clipTo(text, outputLimit(ctx))
}

// clipTo keeps the head and the tail of an oversized output.
func clipTo(text string, limit int) string {
	if limit <= 0 {
		limit = maxToolOutput
	}
	if len(text) <= limit {
		return text
	}
	head := limit * 2 / 3
	tail := limit - head
	head = trimPartialRune(text, head)
	tail = trimPartialRune(text, tail)
	return text[:head] + "\n... [truncated] ...\n" + text[len(text)-tail:]
}

// trimPartialRune backs a byte offset off the middle of a UTF-8 sequence.
func trimPartialRune(text string, offset int) int {
	if offset <= 0 {
		return 0
	}
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}

// ---- read ----

type readTool struct{}

func (readTool) Name() string        { return "read" }
func (readTool) Description() string { return "Read a UTF-8 text file, optionally a line range." }
func (readTool) Mutating() bool      { return false }
func (readTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based first line"},"limit":{"type":"integer","description":"number of lines"}},"required":["path"]}`
}

func (readTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("read: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not UTF-8 text", args.Path)
	}
	lines := strings.Split(string(data), "\n")
	start := args.Offset
	if start < 1 {
		start = 1
	}
	if start > len(lines) {
		return "", nil
	}
	end := len(lines)
	if args.Limit > 0 && start-1+args.Limit < end {
		end = start - 1 + args.Limit
	}
	return clip(ctx, strings.Join(lines[start-1:end], "\n")), nil
}

// ---- write ----

type writeTool struct{}

func (writeTool) Name() string        { return "write" }
func (writeTool) Description() string { return "Create or overwrite a file with the given content." }
func (writeTool) Mutating() bool      { return true }
func (writeTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`
}

func (writeTool) Run(_ context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("write: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
}

// ---- edit ----

type editTool struct{}

func (editTool) Name() string { return "edit" }
func (editTool) Description() string {
	return "Replace one unique occurrence of old text with new text."
}
func (editTool) Mutating() bool { return true }
func (editTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}},"required":["path"]}`
}

func (editTool) Run(_ context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Old     string `json:"old"`
		New     string `json:"new"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("edit: bad arguments: %w", err)
	}
	if args.Old == "" {
		args.Old = args.OldText
	}
	if args.New == "" {
		args.New = args.NewText
	}
	if args.Old == "" {
		return "", fmt.Errorf("edit: old must not be empty")
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	count := strings.Count(text, args.Old)
	switch count {
	case 0:
		return "", fmt.Errorf("edit: old text not found in %s", args.Path)
	case 1:
	default:
		// The count phrase matches the frozen Rust build's tool error, which
		// the eval suite asserts on; the rest explains what the model must do.
		return "", fmt.Errorf("edit: old text appears %d times in %s (not unique); a unique match is required", count, args.Path)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(text, args.Old, args.New, 1)), 0o644); err != nil {
		return "", err
	}
	return "edited " + args.Path, nil
}

// ---- bash ----

type bashTool struct{}

func (bashTool) Name() string        { return "bash" }
func (bashTool) Description() string { return "Run a shell command in the workspace root." }
func (bashTool) Mutating() bool      { return true }
func (bashTool) Schema() string {
	return `{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"},"background_after_ms":{"type":"integer","description":"If the command is still running after this many milliseconds, promote it to a background job instead of killing it on timeout."}},"required":["command"]}`
}

func (bashTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Command           string `json:"command"`
		TimeoutSeconds    int    `json:"timeout_seconds"`
		BackgroundAfterMS int    `json:"background_after_ms"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("bash: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("bash: command must not be empty")
	}
	timeout := 120 * time.Second
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	path, argv := shellInvocation(args.Command)
	reportBackground(ctx, args.Command, args.BackgroundAfterMS)

	// Promotion is opt-in and needs somewhere to record the job. Without a
	// registry (or the argument), keep the original hard-kill behaviour.
	if args.BackgroundAfterMS > 0 {
		if registry := jobRegistry(ctx); registry != nil {
			return runPromotableBash(ctx, root, registry, args.Command, path, argv,
				timeout, time.Duration(args.BackgroundAfterMS)*time.Millisecond)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, argv...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	text := clip(ctx, string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("bash: timed out after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("bash: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return "(no output)", nil
	}
	return text, nil
}

// shellInvocation returns the program and leading arguments that execute a
// command line. It is shared by the hard-timeout and promotable paths so a
// command is spelled the same way in both.
func shellInvocation(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err == nil {
			return "bash", []string{"-lc", command}
		}
		return "cmd", []string{"/c", command}
	}
	return "sh", []string{"-lc", command}
}

// runPromotableBash waits at most backgroundAfter for the command. On that
// deadline the still-running child is handed to the jobs registry; the model
// gets a normal tool result with the job id, so the turn is never blocked.
func runPromotableBash(ctx context.Context, root string, registry *jobs.Registry,
	command, path string, argv []string, hardTimeout, backgroundAfter time.Duration) (string, error) {
	// The caller's context and the hard timeout still bound the whole call; the
	// soft deadline is what turns a slow child into a job.
	runCtx, cancel := context.WithTimeout(ctx, hardTimeout)
	defer cancel()

	res, live, err := proc.RunWithTimeout(runCtx, proc.Command{
		Path:          path,
		Args:          argv,
		Dir:           root,
		Timeout:       backgroundAfter,
		KeepOnTimeout: true,
		NoWindow:      runtime.GOOS == "windows",
	})

	var timeoutErr *proc.TimeoutError
	if errors.As(err, &timeoutErr) && live != nil {
		logFile, fileErr := os.CreateTemp("", "argent-job-*.log")
		if fileErr != nil {
			live.Kill()
			return "", fmt.Errorf("bash: create job log: %w", fileErr)
		}
		logPath := logFile.Name()
		// Flush what was captured before the timeout and send the rest of the
		// child's output straight to the log the job will tail.
		live.SetOutput(logFile)
		// The job watcher reads the same Done channel; once it closes, every
		// write to the log is finished and the file can be released.
		go func() { <-live.Done(); _ = logFile.Close() }()

		job, jobErr := registry.PromoteFromTimeout(command, live, logPath)
		if jobErr != nil {
			live.Kill()
			return "", fmt.Errorf("bash: promote job: %w", jobErr)
		}
		out := fmt.Sprintf("started background job %s (pid %d): %s\nstill running; check with `argent jobs` (output: `argent jobs tail %s`).",
			job.ID, job.PID, command, job.ID)
		if partial := strings.TrimSpace(res.Combined()); partial != "" {
			out += "\nOutput so far:\n" + clip(ctx, partial)
		}
		return out, nil
	}

	if err != nil {
		return clip(ctx, res.Combined()), fmt.Errorf("bash: %w", err)
	}
	text := clip(ctx, res.Combined())
	if strings.TrimSpace(text) == "" {
		return "(no output)", nil
	}
	return text, nil
}

// ---- ls ----

type lsTool struct{}

func (lsTool) Name() string        { return "ls" }
func (lsTool) Description() string { return "List a directory. Directories end with /." }
func (lsTool) Mutating() bool      { return false }
func (lsTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"}}}`
}

func (lsTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("ls: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return clip(ctx, strings.Join(names, "\n")), nil
}

// ---- grep ----

type grepTool struct{}

func (grepTool) Name() string        { return "grep" }
func (grepTool) Description() string { return "Search file contents with a regular expression." }
func (grepTool) Mutating() bool      { return false }
func (grepTool) Schema() string {
	return `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`
}

func (grepTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("grep: bad arguments: %w", err)
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: bad pattern: %w", err)
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	var matches []string
	walkFiles(base, func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil || strings.ContainsRune(string(data), 0) {
			return true
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line)))
				if len(matches) >= maxMatches {
					return false
				}
			}
		}
		return true
	})
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	return clip(ctx, strings.Join(matches, "\n")), nil
}

// ---- find ----

type findTool struct{}

func (findTool) Name() string        { return "find" }
func (findTool) Description() string { return "Find files whose name matches a glob pattern." }
func (findTool) Mutating() bool      { return false }
func (findTool) Schema() string {
	return `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`
}

func (findTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("find: bad arguments: %w", err)
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	var found []string
	walkFiles(base, func(path string) bool {
		matched, matchErr := filepath.Match(args.Pattern, filepath.Base(path))
		if matchErr == nil && matched {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			found = append(found, filepath.ToSlash(rel))
			if len(found) >= maxMatches {
				return false
			}
		}
		return true
	})
	if len(found) == 0 {
		return "(no matches)", nil
	}
	sort.Strings(found)
	return clip(ctx, strings.Join(found, "\n")), nil
}

// walkFiles visits regular files under base, skipping noisy directories.
// The callback returns false to stop the walk.
func walkFiles(base string, visit func(string) bool) {
	skip := map[string]bool{".git": true, "node_modules": true, "target": true, "dist": true}
	_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != base && skip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if !visit(path) {
			return filepath.SkipAll
		}
		return nil
	})
}
