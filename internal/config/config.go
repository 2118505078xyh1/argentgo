// Package config resolves layered configuration for argent-go.
//
// Precedence, lowest to highest:
//
//  1. compiled defaults
//  2. $HOME/.argent-go/config.json
//  3. <cwd>/.argent/config.json
//  4. <cwd>/argent.json
//  5. the selected profile (ARGENT_PROFILE, or "profile" in a file)
//  6. ARGENT_* environment variables
//  7. explicit flag overrides
//
// Unknown keys are preserved in Raw so a later setting or integration can read
// them without adding a struct field here.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sandbox modes accepted by Validate.
const (
	SandboxReadOnly         = "read-only"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxDangerFullAccess = "danger-full-access"
)

const (
	appDir          = ".argent-go"
	defaultModel    = "gpt-4o-mini"
	defaultBaseURL  = "https://api.openai.com/v1"
	defaultProtocol = "openai-chat-completions"
	defaultLocale   = "en-US"
	defaultMaxTurns = 12
)

// Config is the merged configuration.
type Config struct {
	Model       string
	BaseURL     string
	APIKey      string
	Protocol    string
	Locale      string
	SandboxMode string // read-only | workspace-write | danger-full-access
	MaxTurns    int
	Yolo        bool
	StorageDir  string

	// ProviderName is the selected provider template id.
	ProviderName string
	// ActiveProfile is the profile merged over the file layers, if any.
	ActiveProfile string
	// Profiles holds every inline profile definition, keyed by name.
	Profiles map[string]map[string]any
	// Providers holds provider templates from config, keyed by lower-case name.
	Providers map[string]Provider
	// Sources lists the config files that were merged, lowest precedence first.
	Sources []string
	// Raw is the merged JSON tree, unknown keys included.
	Raw map[string]any
	// CWD is the workspace the configuration was resolved for.
	CWD string
}

// Load merges every configuration layer for cwd. env supplies the process
// environment and is the only way Load reads it, which keeps tests hermetic.
func Load(cwd string, env func(string) string) (Config, error) {
	return LoadWithOverrides(cwd, env, nil)
}

// LoadWithOverrides is Load plus the highest-precedence flag layer. The
// overrides map uses the same camelCase keys as the config files.
func LoadWithOverrides(cwd string, env func(string) string, overrides map[string]any) (Config, error) {
	if env == nil {
		env = func(string) string { return "" }
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}

	home := homeDir(env)
	tree := defaults(home, cwd)
	sources := make([]string, 0, 3)
	for _, file := range configFiles(cwd, home) {
		overlay, err := readConfig(file)
		if err != nil {
			return Config{}, err
		}
		if overlay == nil {
			continue
		}
		mergeTree(tree, overlay)
		sources = append(sources, file)
	}

	profile := strings.TrimSpace(env("ARGENT_PROFILE"))
	if profile == "" {
		profile = stringField(tree, "profile", "activeProfile")
	}
	if profile != "" {
		profiles := profilesFrom(tree)
		if overlay, ok := lookupFold(profiles, profile); ok {
			mergeTree(tree, overlay)
		} else if len(profiles) > 0 {
			return Config{}, fmt.Errorf("config: unknown profile %q", profile)
		}
	}

	if err := applyEnv(tree, env); err != nil {
		return Config{}, err
	}
	if len(overrides) > 0 {
		mergeTree(tree, overrides)
	}

	cfg := decode(tree, cwd, sources)
	cfg.ActiveProfile = profile
	if cfg.APIKey == "" {
		if provider, ok := cfg.Provider(cfg.ProviderName); ok && provider.APIKeyEnv != "" {
			cfg.APIKey = strings.TrimSpace(env(provider.APIKeyEnv))
		}
	}
	return cfg, nil
}

// Validate rejects configurations the rest of the program cannot honour.
func (c Config) Validate() error {
	switch c.SandboxMode {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
	default:
		return fmt.Errorf(
			"config: sandboxMode %q must be one of %s, %s, %s",
			c.SandboxMode, SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess,
		)
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("config: model must not be empty")
	}
	if c.MaxTurns < 0 {
		return fmt.Errorf("config: maxTurns must be >= 0, got %d", c.MaxTurns)
	}
	return nil
}

func defaults(home, cwd string) map[string]any {
	return map[string]any{
		"model":       defaultModel,
		"baseURL":     defaultBaseURL,
		"apiKey":      "",
		"protocol":    defaultProtocol,
		"locale":      defaultLocale,
		"sandboxMode": SandboxWorkspaceWrite,
		"maxTurns":    float64(defaultMaxTurns),
		"yolo":        false,
		"storageDir":  filepath.Join(home, appDir),
		"provider":    "",
		"cwd":         cwd,
	}
}

// configFiles lists the file layers, lowest precedence first.
func configFiles(cwd, home string) []string {
	return []string{
		filepath.Join(home, appDir, "config.json"),
		filepath.Join(cwd, ".argent", "config.json"),
		filepath.Join(cwd, "argent.json"),
	}
}

func homeDir(env func(string) string) string {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	return "."
}

// readConfig reads one JSON object. A missing file is not an error; a malformed
// file is, because the operator needs to know a setting is not in effect.
func readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("config: invalid JSON in %s: %w", path, err)
	}
	if tree == nil {
		return nil, nil
	}
	return tree, nil
}

func applyEnv(tree map[string]any, env func(string) string) error {
	setString := func(key, name string) {
		if value := strings.TrimSpace(env(name)); value != "" {
			tree[key] = value
		}
	}
	setString("model", "ARGENT_MODEL")
	setString("baseURL", "ARGENT_BASE_URL")
	setString("apiKey", "ARGENT_API_KEY")
	setString("protocol", "ARGENT_PROTOCOL")
	setString("locale", "ARGENT_LOCALE")
	setString("sandboxMode", "ARGENT_SANDBOX_MODE")
	setString("storageDir", "ARGENT_STORAGE_DIR")
	setString("provider", "ARGENT_PROVIDER")

	if value := strings.TrimSpace(env("ARGENT_MAX_TURNS")); value != "" {
		turns, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config: ARGENT_MAX_TURNS=%q is not an integer", value)
		}
		tree["maxTurns"] = float64(turns)
	}
	if value := strings.TrimSpace(env("ARGENT_YOLO")); value != "" {
		flag, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("config: ARGENT_YOLO=%q is not a boolean", value)
		}
		tree["yolo"] = flag
	}
	return nil
}

func decode(tree map[string]any, cwd string, sources []string) Config {
	return Config{
		Model:        stringField(tree, "model", "modelName"),
		BaseURL:      stringField(tree, "baseURL", "baseUrl", "base_url"),
		APIKey:       stringField(tree, "apiKey", "api_key"),
		Protocol:     stringField(tree, "protocol"),
		Locale:       stringField(tree, "locale"),
		SandboxMode:  stringField(tree, "sandboxMode", "sandbox_mode"),
		MaxTurns:     intField(tree, "maxTurns", "max_turns"),
		Yolo:         boolField(tree, "yolo"),
		StorageDir:   stringField(tree, "storageDir", "storage_dir"),
		ProviderName: stringField(tree, "provider"),
		Profiles:     profilesFrom(tree),
		Providers:    providersFrom(tree),
		Sources:      sources,
		Raw:          cloneTree(tree),
		CWD:          cwd,
	}
}

// mergeTree merges overlay into base: objects merge recursively, every other
// value (arrays included) is replaced.
func mergeTree(base, overlay map[string]any) {
	for key, value := range overlay {
		if child, ok := value.(map[string]any); ok {
			if existing, ok := base[key].(map[string]any); ok {
				mergeTree(existing, child)
				continue
			}
			base[key] = cloneTree(child)
			continue
		}
		base[key] = value
	}
}

func cloneTree(tree map[string]any) map[string]any {
	out := make(map[string]any, len(tree))
	for key, value := range tree {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneTree(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return typed
	}
}

func stringField(tree map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := tree[key].(string); ok {
			return text
		}
	}
	return ""
}

func intField(tree map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case int64:
			return int(value)
		case json.Number:
			if n, err := value.Int64(); err == nil {
				return int(n)
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				return n
			}
		}
	}
	return 0
}

func boolField(tree map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case bool:
			return value
		case string:
			if flag, err := parseBool(value); err == nil {
				return flag
			}
		}
	}
	return false
}

func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("not a boolean: %q", value)
}

func profilesFrom(tree map[string]any) map[string]map[string]any {
	raw, ok := tree["profiles"].(map[string]any)
	if !ok {
		return map[string]map[string]any{}
	}
	out := make(map[string]map[string]any, len(raw))
	for name, value := range raw {
		if entry, ok := value.(map[string]any); ok {
			out[name] = entry
		}
	}
	return out
}

// lookupFold finds a map entry by name, ignoring case.
func lookupFold[V any](values map[string]V, name string) (V, bool) {
	if value, ok := values[name]; ok {
		return value, true
	}
	for key, value := range values {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	var zero V
	return zero, false
}
