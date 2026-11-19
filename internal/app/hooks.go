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

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/hooks"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// inlineHookPath is the trust-store key for a document supplied directly in
// config. Inline bytes are hashed exactly like a file, so the operator must
// grant the digest before a single command runs.
const inlineHookPath = "config:hooks"

// HookDispatcher is the slice of internal/hooks the app needs. Tests inject a
// fake here; production uses *hooks.Dispatcher.
type HookDispatcher interface {
	Dispatch(ctx context.Context, config *hooks.Config, payload hooks.Payload) (hooks.Result, error)
}

// hookBridge loads trusted documents and fires lifecycle events around one
// Execute. A nil bridge means "no hooks configured" and every method is a
// no-op, so callers never need a nil check beyond the constructor.
type hookBridge struct {
	dispatcher HookDispatcher
	config     *hooks.Config
	sessionID  string
	cwd        string
	warn       io.Writer
}

// newHookBridge returns nil when no trusted hook document exists. Broken trust
// files, untrusted documents and malformed JSON are reported and skipped: a
// hook problem must never stop an otherwise valid run.
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

// loadHookDocuments merges every trusted source: inline/path entries from
// config.Raw["hooks"], then the standard project and user files.
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
	for _, path := range hookFilePaths(res) {
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

// configHookSources accepts a path, a list of paths, or an inline document.
// An object with "paths" may combine both, as plugin manifests do.
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

// hookFilePaths lists the standard documents, project first. Both the legacy
// ~/.argent and current ~/.argentgo homes are honoured.
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

// fire dispatches one event, filling in the run context. Panics from a hook
// implementation are converted into an error instead of escaping into the
// agent loop.
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
	// A cancelled run still gets its closing event.
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

// hookedTools dispatches PreToolUse/PostToolUse around every real tool call.
// Specs is delegated unchanged so hook wiring never alters the model's view.
type hookedTools struct {
	inner  agent.ToolRunner
	bridge *hookBridge
}

func (h *hookedTools) Specs() []llm.ToolSpec { return h.inner.Specs() }

func (h *hookedTools) Run(ctx context.Context, name, args string) (string, error) {
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

	out, runErr := h.inner.Run(ctx, name, args)
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
		// A post hook must never swallow a successful tool result; surface the
		// failure next to it so the model can read both.
		return appendHookNote(out, "PostToolUse hook failed: "+err.Error()), nil
	}
	if post.Message != "" {
		out = appendHookNote(out, post.Message)
	}
	return out, nil
}

// noteTool attaches non-blocking hook failures to the tool output so the model
// can read them; they never replace the tool's own error.
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

func noteHook(warn io.Writer, what string, err error) {
	if warn == nil || err == nil {
		return
	}
	fmt.Fprintf(warn, "%s: %v (skipped)\n", what, err)
}
