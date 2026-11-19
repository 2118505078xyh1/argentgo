// Package app wires the migrated packages into the CLI: configuration
// resolution, provider selection, tool registry, security chain, session
// storage and the agent loop.
//
// This is the only package that knows about every other package; keep it free
// of behaviour beyond construction.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argent-go/internal/agent"
	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/config"
	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/memory"
	"github.com/2118505078xyh1/argent-go/internal/provider"
	"github.com/2118505078xyh1/argent-go/internal/security"
	"github.com/2118505078xyh1/argent-go/internal/session"
	"github.com/2118505078xyh1/argent-go/internal/skills"
	"github.com/2118505078xyh1/argent-go/internal/tools"
)

// DefaultStorage returns the storage root: $ARGENT_STORAGE, else ~/.argent-go.
func DefaultStorage(env func(string) string) string {
	if env == nil {
		env = os.Getenv
	}
	for _, key := range []string{"ARGENT_STORAGE", "ARGENT_STORAGE_DIR", "ARGENT_HOME"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return expandHome(value, env)
		}
	}
	home := homeDir(env)
	if home == "" {
		return ".argent-go"
	}
	return filepath.Join(home, ".argent-go")
}

func homeDir(env func(string) string) string {
	for _, key := range []string{"USERPROFILE", "HOME"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func expandHome(path string, env func(string) string) string {
	if strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if home := homeDir(env); home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// Resolved is everything one run needs after merging flags, config and
// provider templates.
type Resolved struct {
	Options  cli.Options
	Cfg      config.Config
	Root     string
	Storage  string
	Model    string
	BaseURL  string
	APIKey   string
	Protocol string
	Sandbox  security.SandboxMode
	Approval string
	MaxTurns int
	Mutating bool
	System   string
	Chatter  llm.Chatter
}

// Resolve merges the CLI options over the layered configuration.
func Resolve(stdio cli.IO, opts cli.Options) (Resolved, error) {
	env := stdio.Env
	if env == nil {
		env = os.Getenv
	}
	root := strings.TrimSpace(opts.Cwd)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve workspace: %w", err)
		}
		root = cwd
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve workspace: %w", err)
	}

	var cfg config.Config
	if len(opts.Overrides.All()) > 0 {
		cfg, err = config.LoadWithOverrides(root, env, opts.Overrides.Nested())
	} else {
		cfg, err = config.Load(root, env)
	}
	if err != nil {
		return Resolved{}, fmt.Errorf("load config: %w", err)
	}

	template, haveTemplate := resolveTemplate(cfg, opts)

	// Flags beat config; a named provider template beats the compiled OpenAI
	// defaults, but never an explicit non-default config value.
	protocol := opts.Protocol
	if protocol == "" {
		protocol = cfg.Protocol
		if haveTemplate && unset(protocol, openAIProtocol) {
			protocol = string(template.Protocol)
		}
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = cfg.BaseURL
		if haveTemplate && unset(baseURL, openAIBaseURL) {
			baseURL = template.BaseURL
		}
	}
	model := opts.Model
	if model == "" {
		model = cfg.Model
		if haveTemplate && unset(model, openAIModel) {
			model = template.DefaultModel
		}
	}
	protocol = firstNonEmpty(protocol, "openai")
	baseURL = firstNonEmpty(baseURL, openAIBaseURL)
	model = firstNonEmpty(model, openAIModel)
	apiKey := firstNonEmpty(opts.APIKey, cfg.APIKey, env(template.APIKeyEnv))

	sandboxMode := firstNonEmpty(opts.SandboxMode, cfg.SandboxMode, "workspace-write")
	sandbox, err := security.FromString(sandboxMode)
	if err != nil {
		return Resolved{}, fmt.Errorf("sandbox mode %q: %w", sandboxMode, err)
	}
	approval := strings.ToLower(firstNonEmpty(opts.Approval, "ask"))
	if opts.Yolo || cfg.Yolo {
		approval = "auto"
	}

	maxTurns := opts.MaxTurns
	if maxTurns == 0 {
		maxTurns = cfg.MaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 12
	}

	storage := firstNonEmpty(env("ARGENT_STORAGE"), env("ARGENT_STORAGE_DIR"), cfg.StorageDir)
	if storage == "" {
		storage = DefaultStorage(env)
	}

	res := Resolved{
		Options:  opts,
		Cfg:      cfg,
		Root:     root,
		Storage:  storage,
		Model:    model,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Protocol: protocol,
		Sandbox:  sandbox,
		Approval: approval,
		MaxTurns: maxTurns,
		Mutating: sandbox != security.SandboxReadOnly,
		System:   buildSystem(root, env),
	}
	res.Chatter = newChatter(res.Protocol, baseURL, apiKey)
	return res, nil
}

func resolveTemplate(cfg config.Config, opts cli.Options) (provider.Template, bool) {
	for _, name := range []string{cfg.ProviderName, opts.Protocol, cfg.Protocol} {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if template, ok := provider.Resolve(name); ok {
			return template, true
		}
	}
	return provider.Template{}, false
}

func newChatter(protocol, baseURL, apiKey string) llm.Chatter {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic", "anthropic-messages", "messages":
		client := llm.NewAnthropic(baseURL, apiKey)
		client.MaxTokens = llm.DefaultAnthropicMaxTokens
		return client
	case "responses", "openai-responses":
		return llm.NewResponses(baseURL, apiKey)
	default:
		return llm.New(baseURL, apiKey)
	}
}

func buildSystem(root string, env func(string) string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are argent, a concise coding assistant working on the user's machine.\nWorking directory: %s\n", root)
	b.WriteString("Use the available tools to inspect and change files. Read before writing, ")
	b.WriteString("batch independent calls, and stop once the request is satisfied.\n")

	if docs, err := memory.Load(root); err == nil {
		if block := memory.Block(docs); strings.TrimSpace(block) != "" {
			b.WriteString("\n")
			b.WriteString(block)
		}
	}
	registry := skills.Discover(skills.DefaultRoots(root, homeDir(env)))
	if registry.Len() > 0 {
		b.WriteString("\n")
		b.WriteString(registry.IndexText())
	}
	return b.String()
}

const (
	openAIProtocol = "openai-chat-completions"
	openAIBaseURL  = "https://api.openai.com/v1"
	openAIModel    = "gpt-4o-mini"
)

// unset reports whether a config value still holds the compiled default, which
// is the only signal available for "the user did not choose this".
func unset(value, compiledDefault string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || strings.EqualFold(trimmed, compiledDefault)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Factory returns the runner factory the CLI expects.
func Factory(stdio cli.IO) cli.RunnerFactory {
	return func(opts cli.Options) (cli.Runner, error) {
		resolved, err := Resolve(stdio, opts)
		if err != nil {
			return nil, err
		}
		store, err := session.Open(filepath.Join(resolved.Storage, "sessions"))
		if err != nil {
			return nil, fmt.Errorf("open sessions: %w", err)
		}
		return &runner{stdio: stdio, res: resolved, store: store}, nil
	}
}

type runner struct {
	stdio cli.IO
	res   Resolved
	store *session.Store

	mu      sync.Mutex
	cancel  context.CancelFunc
	current *session.Session
}

// Prompt satisfies cli.Runner.
func (r *runner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.cancel = nil
		r.mu.Unlock()
		cancel()
	}()

	sess, err := r.ensureSession(text)
	if err != nil {
		return "", err
	}
	result, err := Execute(ctx, r.stdio, r.res, sess, text, onText)
	return result.Text, err
}

// Abort satisfies cli.Runner.
func (r *runner) Abort() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *runner) ensureSession(firstPrompt string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current, nil
	}
	title := firstLine(firstPrompt, 60)
	if title == "" {
		title = "session"
	}
	sess, err := r.store.NewSession(title)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	r.current = sess
	return sess, nil
}

// Execute runs one prompt through a freshly wired tool registry, policy and
// agent loop. The registry is per-run so the security chain can carry the
// session id without mutable global state.
func Execute(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, prompt string, onText func(string)) (agent.Result, error) {
	registry := tools.NewWithLimit(res.Root, res.Mutating, 0)
	registry.SetSessionDir(filepath.Join(res.Storage, "sessions", sess.ID()))
	audit := security.NewAuditLog(filepath.Join(res.Storage, "audit"))
	approver := approverFor(stdio, res)
	chain := security.NewChain(security.DefaultPolicy(res.Root), approver, audit, sess.ID())
	registry.SetGuard(chain)

	loop := &agent.Runner{Opts: agent.Options{
		Model:    res.Model,
		MaxTurns: res.MaxTurns,
		Root:     res.Root,
		System:   res.System,
		Chatter:  res.Chatter,
		Tools:    registry,
		Approve:  approver,
		Record:   agent.RecorderFunc(sess.Append),
		Budget:   agent.Budget{MaxTurns: res.MaxTurns},
		Emit: func(event agent.Event) {
			if event.Kind == agent.EventTextDelta && event.Depth == 0 && onText != nil {
				onText(event.Text)
			}
		},
	}}

	result, err := loop.Run(ctx, prompt)
	_ = sess.Append("turn", map[string]any{
		"turns":   result.Turns,
		"stopped": result.Stopped,
		"usage":   result.Usage,
	})
	return result, err
}

func firstLine(text string, limit int) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

// autoApprover grants every request; it backs --yolo and approval=auto.
type autoApprover struct{}

func (autoApprover) Ask(ctx context.Context, _, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func approverFor(stdio cli.IO, res Resolved) security.Approver {
	switch res.Approval {
	case "never":
		return security.NeverApprover{}
	case "auto", "yolo":
		return autoApprover{}
	default:
		return security.NewCachedApprover(security.NewPromptApprover(stdio.In, stdio.Err))
	}
}
