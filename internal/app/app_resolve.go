package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/provider"
	"github.com/2118505078xyh1/argentgo/internal/security"
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

	// An empty option means "unset": the CLI leaves compiled defaults out of
	// Options so that config files and profiles can win. config.Load applies
	// the compiled defaults at the lowest layer, so unset() can still tell an
	// untouched value from a real choice. Flags beat config; a named provider
	// template beats the compiled OpenAI defaults, but never an explicit
	// non-default config value.
	protocol := strings.TrimSpace(opts.Protocol)
	if protocol == "" {
		protocol = cfg.Protocol
		if haveTemplate && unset(protocol, openAIProtocol) {
			protocol = template.Protocol
		}
	}
	protocol = canonicalProtocol(protocol)
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
	protocol = firstNonEmpty(protocol, openAIProtocol)
	baseURL = firstNonEmpty(baseURL, openAIBaseURL)
	model = firstNonEmpty(model, openAIModel)

	// Credential priority: explicit flag / ARGENT_* env > top-level config >
	// the key `argent login` stored under providers.<name>.apiKey > the
	// provider's own API-key environment variable.
	providerName := template.Name
	if providerName == "" {
		providerName = cfg.ProviderName
	}
	configuredProvider, _ := cfg.Provider(providerName)
	// Use the config-resolved provider's APIKeyEnv, not template's. In the
	// default path template is zero-valued (resolveTemplate cannot match the
	// "openai-chat-completions" protocol name), so template.APIKeyEnv is empty
	// and the provider-env fallback would silently fail. configuredProvider was
	// resolved above via cfg.Provider(providerName), which falls back to the
	// protocol's natural provider (openai) and has the correct APIKeyEnv.
	apiKey := firstNonEmpty(opts.APIKey, cfg.APIKey, configuredProvider.APIKey, env(configuredProvider.APIKeyEnv))

	sandboxMode := firstNonEmpty(opts.SandboxMode, cfg.SandboxMode, "workspace-write")
	sandbox, err := security.FromString(sandboxMode)
	if err != nil {
		return Resolved{}, fmt.Errorf("sandbox mode %q: %w", sandboxMode, err)
	}
	approval := strings.ToLower(firstNonEmpty(opts.Approval, cfgString(cfg.Raw, "approval"), "ask"))
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

	// Flags beat config; zero means "unset" and selects the conservative
	// default. An explicit 1 (or any negative value) keeps batches serial.
	maxParallel := firstNonZeroInt(opts.MaxParallel, intField(cfg.Raw, "maxParallel", "max-parallel"))
	if maxParallel == 0 {
		maxParallel = defaultMaxParallel
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
	system := buildSystem(root, env, opts.Thinking, schema, cfg)

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
		MaxParallel:  maxParallel,
		Mutating:     sandbox != security.SandboxReadOnly,
		MaxTokens:    firstNonZeroInt(opts.MaxTokens, intField(cfg.Raw, "maxTokens", "max-tokens")),
		MaxCostUSD:   firstNonZeroFloat(opts.MaxCost, floatField(cfg.Raw, "maxCost", "max-cost")),
		MaxSeconds:   firstNonZeroInt(opts.MaxSeconds, intField(cfg.Raw, "maxSeconds", "max-seconds")),
		AddDirs:      addDirs,
		WriteRoots:   security.WriteRootsFrom(root, addDirs),
		OutputSchema: schema,
		Thinking:     opts.Thinking,
		TrustProject: opts.TrustProject || (env("ARGENT_TRUST_PROJECT") != "" && parseTrustEnv(env("ARGENT_TRUST_PROJECT"))),
		Plugins:      append([]string(nil), opts.Plugins...),
		System:       system,
		Prices:       pricesFor(model, opts, cfg),

		AutoCompactTokens: firstNonZeroInt(
			intField(cfg.Raw, "autoCompactTokens", "auto-compact-tokens"),
			compactThresholdFrom(cfg.Raw),
		),
		NoAutoCompact: opts.NoAutoCompact,
	}
	res.Chatter = newChatter(res.Protocol, baseURL, apiKey, cacheOptionsForConfig(cfg, opts))
	if fallback := strings.TrimSpace(opts.FallbackModel); fallback != "" {
		res.Chatter = &fallbackChatter{primary: res.Chatter, fallback: fallback}
	}
	return res, nil
}

// cacheOptionsForConfig builds the wire-level cache options consumed by
// newChatter. The merged config may carry an explicit cache tier; the tier's
// subscription and endpoint-capability inputs have no source at Resolve today,
// so the production path wires only the explicit-config branch: conservative
// facts (non-subscription, long supported) leave an unset tier at the same
// short/provider-default entry the old literal produced, while an explicit
// "long"/"none"/"short" choice is honoured. The subscription long default
// belongs at a later wiring point where the credential kind and the endpoint's
// long-retention capability are available.
func cacheOptionsForConfig(cfg config.Config, opts cli.Options) llm.Options {
	configTier := cfgString(cfg.Raw, "cacheTier", "cache-tier", "promptCacheTier", "prompt-cache-tier")
	return resolveCacheOptions(configTier, false, true, opts.NoPromptCache)
}

// resolveCacheOptions maps a resolved tier onto llm.Options' cache knobs. A
// short tier — including the unset default — keeps the provider-default TTL
// (an empty CacheRetention), so the historical wire format is unchanged. Long
// emits a 1h retention hint and none is an explicit opt-out. The CLI's
// NoPromptCache always wins: llm honours it above every other cache knob.
func resolveCacheOptions(configTier string, isSubscription, supportsLong, noPromptCache bool) llm.Options {
	tier := ResolveCacheTier(configTier, isSubscription, supportsLong)
	out := llm.Options{NoPromptCache: noPromptCache}
	switch tier {
	case CacheTierLong:
		out.CacheRetention = "1h"
	case CacheTierNone:
		out.NoPromptCache = true
	}
	return out
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
	// cfg.Provider merges a config overlay over the built-in provider of the
	// same name (and already falls back to built-ins), so it must be consulted
	// first. Otherwise provider.Resolve returns the hard-coded template and an
	// override such as providers.<name>.baseURL in the config is silently lost.
	if entry, ok := cfg.Provider(name); ok {
		return providerChoice{
			Name:         entry.Name,
			Protocol:     entry.Protocol,
			BaseURL:      entry.BaseURL,
			DefaultModel: entry.DefaultModel,
			APIKeyEnv:    entry.APIKeyEnv,
		}, true
	}
	// provider.Resolve remains the fallback for aliases (claude, kimi, ...) and
	// built-in providers the config layer does not know about (deepseek, xai, ...).
	if template, ok := provider.Resolve(name); ok {
		return providerChoice{
			Name:         template.Name,
			Protocol:     string(template.Protocol),
			BaseURL:      template.BaseURL,
			DefaultModel: template.DefaultModel,
			APIKeyEnv:    template.APIKeyEnv,
		}, true
	}
	return providerChoice{}, false
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

// cfgString reads the first string key from a merged config tree.
func cfgString(tree map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := tree[key].(string); ok {
			return text
		}
	}
	return ""
}

// canonicalProtocol maps the short CLI spellings and the config wire names
// onto a single vocabulary, so unset() and the chatter factory agree on what
// "openai" means. Unknown values pass through unchanged.
func canonicalProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "openai", "openai-chat-completions":
		return openAIProtocol
	case "anthropic", "anthropic-messages", "messages":
		return string(provider.ProtocolAnthropic)
	case "responses", "openai-responses":
		return string(provider.ProtocolResponses)
	default:
		return strings.TrimSpace(value)
	}
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

	// defaultMaxParallel bounds read-only tool batches when neither a flag, an
	// environment variable nor config selects a value. It is deliberately small
	// to keep filesystem and hook load predictable.
	defaultMaxParallel = 4
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
