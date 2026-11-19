// Package app wires the migrated packages into the CLI: configuration
// resolution, provider selection, tool registry, security chain, session
// storage and the agent loop.
//
// This is the only package that knows about every other package; keep it free
// of behaviour beyond construction.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/memory"
	"github.com/2118505078xyh1/argentgo/internal/provider"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// DefaultStorage returns the storage root: $ARGENT_STORAGE, else ~/.argentgo.
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
		return ".argentgo"
	}
	return filepath.Join(home, ".argentgo")
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
	Home     string
	Storage  string
	Model    string
	Provider string
	Profile  string
	BaseURL  string
	APIKey   string
	Protocol string
	Locale   string
	Sandbox  security.SandboxMode
	Approval string
	MaxTurns int
	Mutating bool
	System   string

	// Budget and pricing.
	MaxTokens  int
	MaxCostUSD float64
	MaxSeconds int
	Prices     map[string]agent.Price

	// Extra workspace directories, an output schema request and the reasoning
	// effort hint.
	AddDirs      []string
	WriteRoots   []string
	OutputSchema string
	Thinking     string
	TrustProject bool
	Plugins      []string

	// Post-run compaction. Zero selects the default threshold; a negative
	// value disables the estimate-based trigger.
	AutoCompactTokens int
	NoAutoCompact     bool

	// Approve overrides the interactive approver; the TUI installs a modal
	// approver here.
	Approve security.Approver

	// HookDispatcher overrides the shell dispatcher used by the hooks bridge;
	// tests inject a fake so no hook process is spawned.
	HookDispatcher HookDispatcher

	Chatter llm.Chatter
}

// Resolve merges the CLI options over the layered configuration.
func Resolve(stdio cli.IO, opts cli.Options) (Resolved, error) {
	env := stdio.Env
	if env == nil {
		env = os.Getenv
	}
	// config selects a profile through ARGENT_PROFILE, so a --profile flag is
	// injected there rather than duplicated in this package.
	profileEnv := env
	if profile := strings.TrimSpace(opts.Profile); profile != "" {
		base := env
		profileEnv = func(key string) string {
			if key == "ARGENT_PROFILE" {
				return profile
			}
			return base(key)
		}
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
		cfg, err = config.LoadWithOverrides(root, profileEnv, opts.Overrides.Nested())
	} else {
		cfg, err = config.Load(root, profileEnv)
	}
	if err != nil {
		return Resolved{}, fmt.Errorf("load config: %w", err)
	}

	template, haveTemplate, err := resolveTemplate(cfg, opts)
	if err != nil {
		return Resolved{}, err
	}

	// Flags beat config; a named provider template beats the compiled OpenAI
	// defaults, but never an explicit non-default config value.
	protocol := opts.Protocol
	if protocol == "" {
		protocol = cfg.Protocol
		if haveTemplate && unset(protocol, openAIProtocol) {
			protocol = template.Protocol
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

	addDirs, err := resolveAddDirs(root, opts.AddDirs)
	if err != nil {
		return Resolved{}, err
	}
	schema, err := loadOutputSchema(root, opts.OutputSchema)
	if err != nil {
		return Resolved{}, err
	}
	system := buildSystem(root, env, opts.Thinking, schema)

	res := Resolved{
		Options:      opts,
		Cfg:          cfg,
		Root:         root,
		Home:         homeDir(env),
		Storage:      storage,
		Model:        model,
		Provider:     firstNonEmpty(template.Name, cfg.ProviderName),
		Profile:      firstNonEmpty(opts.Profile, cfg.ActiveProfile),
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Protocol:     protocol,
		Locale:       firstNonEmpty(opts.Locale, cfg.Locale),
		Sandbox:      sandbox,
		Approval:     approval,
		MaxTurns:     maxTurns,
		Mutating:     sandbox != security.SandboxReadOnly,
		MaxTokens:    firstNonZeroInt(opts.MaxTokens, intField(cfg.Raw, "maxTokens", "max-tokens")),
		MaxCostUSD:   firstNonZeroFloat(opts.MaxCost, floatField(cfg.Raw, "maxCost", "max-cost")),
		MaxSeconds:   firstNonZeroInt(opts.MaxSeconds, intField(cfg.Raw, "maxSeconds", "max-seconds")),
		AddDirs:      addDirs,
		WriteRoots:   security.WriteRootsFrom(root, addDirs),
		OutputSchema: schema,
		Thinking:     opts.Thinking,
		TrustProject: opts.TrustProject || env("ARGENT_TRUST_PROJECT") == "1",
		Plugins:      append([]string(nil), opts.Plugins...),
		System:       system,
		Prices:       pricesFor(model, opts, cfg),

		AutoCompactTokens: firstNonZeroInt(
			intField(cfg.Raw, "autoCompactTokens", "auto-compact-tokens"),
			compactThresholdFrom(cfg.Raw),
		),
		NoAutoCompact: opts.NoAutoCompact,
	}
	res.Chatter = newChatter(res.Protocol, baseURL, apiKey, llm.Options{NoPromptCache: opts.NoPromptCache})
	if fallback := strings.TrimSpace(opts.FallbackModel); fallback != "" {
		res.Chatter = &fallbackChatter{primary: res.Chatter, fallback: fallback}
	}
	return res, nil
}

// providerChoice is the resolved provider endpoint without the template type.
type providerChoice struct {
	Name         string
	Protocol     string
	BaseURL      string
	DefaultModel string
	APIKeyEnv    string
}

// resolveTemplate honours --provider, then the config provider, then the
// protocol names the older flag surface used.
func resolveTemplate(cfg config.Config, opts cli.Options) (providerChoice, bool, error) {
	if name := strings.TrimSpace(opts.Provider); name != "" {
		if choice, ok := lookupProvider(cfg, name); ok {
			return choice, true, nil
		}
		return providerChoice{}, false, fmt.Errorf("unknown provider %q (see `argent models`)", name)
	}
	for _, name := range []string{cfg.ProviderName, opts.Protocol, cfg.Protocol} {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if choice, ok := lookupProvider(cfg, name); ok {
			return choice, true, nil
		}
	}
	return providerChoice{}, false, nil
}

func lookupProvider(cfg config.Config, name string) (providerChoice, bool) {
	if template, ok := provider.Resolve(name); ok {
		return providerChoice{
			Name:         template.Name,
			Protocol:     string(template.Protocol),
			BaseURL:      template.BaseURL,
			DefaultModel: template.DefaultModel,
			APIKeyEnv:    template.APIKeyEnv,
		}, true
	}
	if entry, ok := cfg.Provider(name); ok {
		return providerChoice{
			Name:         entry.Name,
			Protocol:     entry.Protocol,
			BaseURL:      entry.BaseURL,
			DefaultModel: entry.DefaultModel,
			APIKeyEnv:    entry.APIKeyEnv,
		}, true
	}
	return providerChoice{}, false
}

func newChatter(protocol, baseURL, apiKey string, opts llm.Options) llm.Chatter {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic", "anthropic-messages", "messages":
		client := llm.NewAnthropic(baseURL, apiKey)
		client.MaxTokens = llm.DefaultAnthropicMaxTokens
		client.Options = opts
		return client
	case "responses", "openai-responses":
		client := llm.NewResponses(baseURL, apiKey)
		client.Options = opts
		return client
	default:
		client := llm.New(baseURL, apiKey)
		client.Options = opts
		return client
	}
}

// fallbackChatter retries once with --fallback-model when the primary model
// fails. Cancellation is never retried.
type fallbackChatter struct {
	primary  llm.Chatter
	fallback string
}

func (f *fallbackChatter) Chat(
	ctx context.Context,
	model string,
	messages []llm.Message,
	tools []llm.ToolSpec,
	onText func(string),
) (llm.Message, error) {
	reply, err := f.primary.Chat(ctx, model, messages, tools, onText)
	if err == nil || ctx.Err() != nil || strings.TrimSpace(f.fallback) == "" {
		return reply, err
	}
	return f.primary.Chat(ctx, f.fallback, messages, tools, onText)
}

func buildSystem(root string, env func(string) string, thinking, schema string) string {
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
	if effort := strings.TrimSpace(thinking); effort != "" {
		fmt.Fprintf(&b, "\nReasoning effort: %s. Spend accordingly.\n", effort)
	}
	if strings.TrimSpace(schema) != "" {
		b.WriteString("\nReply with a single JSON document that conforms to this JSON Schema:\n")
		b.WriteString(strings.TrimSpace(schema))
		b.WriteString("\n")
	}
	return b.String()
}

// resolveAddDirs makes --add-dir values absolute against the workspace.
func resolveAddDirs(root string, dirs []string) ([]string, error) {
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("--add-dir %q: %w", dir, err)
		}
		out = append(out, abs)
	}
	return out, nil
}

// loadOutputSchema accepts inline schema text or a file path.
func loadOutputSchema(root, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	text := value
	if !strings.HasPrefix(value, "{") {
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("--output-schema: %w", err)
		}
		text = string(data)
	}
	if !json.Valid([]byte(text)) {
		return "", fmt.Errorf("--output-schema: the schema is not valid JSON")
	}
	return text, nil
}

// intField reads the first numeric key from a merged config tree.
func intField(tree map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case json.Number:
			n, err := value.Int64()
			if err == nil {
				return int(n)
			}
		}
	}
	return 0
}

// floatField reads the first numeric key from a merged config tree.
func floatField(tree map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case float64:
			return value
		case int:
			return float64(value)
		case json.Number:
			n, err := value.Float64()
			if err == nil {
				return n
			}
		}
	}
	return 0
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroFloat(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// pricesFor builds the agent price table from --price-input/--price-output.
func pricesFor(model string, opts cli.Options, cfg config.Config) map[string]agent.Price {
	if opts.PriceInput == 0 && opts.PriceOutput == 0 {
		raw, ok := cfg.Raw["prices"].(map[string]any)
		if !ok {
			return nil
		}
		prices := make(map[string]agent.Price, len(raw))
		for name, value := range raw {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			prices[name] = agent.Price{Input: floatField(entry, "input"), Output: floatField(entry, "output")}
		}
		return prices
	}
	return map[string]agent.Price{
		model: {Input: opts.PriceInput, Output: opts.PriceOutput},
	}
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

// ActiveSessionID reports the session a runner is appending to, when it
// exposes one. Callers holding only a cli.Runner can use this to show or log
// the resumed session id.
func ActiveSessionID(r cli.Runner) string {
	if typed, ok := r.(interface{ SessionID() string }); ok {
		return typed.SessionID()
	}
	return ""
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

// SessionID reports the session the runner is currently appending to. It is
// empty before the first prompt.
func (r *runner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return ""
	}
	return r.current.ID()
}

func (r *runner) ensureSession(firstPrompt string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current, nil
	}
	sess, err := openOrCreateSession(r.store, r.res.Options, firstPrompt)
	if err != nil {
		return nil, err
	}
	r.current = sess
	return sess, nil
}

// Execute runs one prompt through a freshly wired tool registry, policy and
// agent loop. The registry is per-run so the security chain can carry the
// session id without mutable global state.
func Execute(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, prompt string, onText func(string)) (agent.Result, error) {
	if injection, ok := reviewPrompt(res.Root, prompt); ok {
		prompt = injection + "\n\nUser request: " + strings.TrimSpace(prompt)
	}
	if res.MaxSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(res.MaxSeconds)*time.Second)
		defer cancel()
	}

	bridge := newHookBridge(res, sess.ID(), stdio.Err)
	bridge.start(ctx, prompt)
	defer bridge.end()

	approver := res.Approve
	if approver == nil {
		approver = approverFor(stdio, res)
	}
	audit := security.NewAuditLog(filepath.Join(res.Storage, "audit"))
	policy := policyFor(res)
	chain := security.NewChain(policy, approver, audit, sess.ID())

	toolset := newToolset(ctx, stdio, res, sess, chain)
	defer toolset.Close()
	var runner agent.ToolRunner = toolset
	if bridge != nil {
		runner = &hookedTools{inner: toolset, bridge: bridge}
	}

	tracker := newCheckpointTracker(res.Root, sess)
	var evidence agent.CompactionEvidence
	loop := &agent.Runner{Opts: agent.Options{
		Model:    res.Model,
		MaxTurns: res.MaxTurns,
		Root:     res.Root,
		System:   res.System,
		Chatter:  res.Chatter,
		Tools:    runner,
		Approve:  approver,
		Record:   agent.RecorderFunc(sess.Append),
		Budget: agent.Budget{
			MaxTurns:       res.MaxTurns,
			MaxTotalTokens: res.MaxTokens,
			MaxCostUSD:     res.MaxCostUSD,
		},
		Prices:      res.Prices,
		SkillAuthor: skillWriter{},
		SkillLister: skillLister{toolset.skills},
		Checkpoint: func(cp agent.Checkpoint) {
			tracker.capture(cp, prompt)
		},
		MutationPaths: toolset.base.mutationPaths,
		OnCompactionEvidence: func(e agent.CompactionEvidence) {
			evidence = e
		},
		Emit: func(event agent.Event) {
			if event.Kind == agent.EventTextDelta && event.Depth == 0 && onText != nil {
				onText(event.Text)
			}
		},
	}}

	result, err := loop.Run(ctx, prompt)
	if err == nil && strings.TrimSpace(res.OutputSchema) != "" {
		if invalid := validateOutputText(res.OutputSchema, result.Text); invalid != nil {
			// One corrective turn gives the model the exact failure instead of
			// failing the whole run on a formatting slip.
			loop.Opts.System = res.System + correctiveInstruction(res.OutputSchema, invalid)
			if retried, retryErr := loop.Run(ctx, prompt); retryErr == nil {
				result = retried
				if invalid = validateOutputText(res.OutputSchema, result.Text); invalid != nil {
					err = fmt.Errorf("output schema validation failed after one retry: %w", invalid)
				}
			} else {
				err = retryErr
			}
		}
	}
	bridge.stop(ctx, prompt, result.Text)
	tracker.finish(err != nil || ctx.Err() != nil)
	recordAttempt(sess, ctx, err)
	maybeAutoCompact(res, sess, evidence)
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
