// Package config resolves layered configuration for argentgo.
//
// Precedence, lowest to highest:
//
//  1. compiled defaults
//  2. $HOME/.argentgo/config.json
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
	"log"
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

// Privacy modes accepted by validatePrivacyMode. They mirror
// security.PrivacyAny / security.PrivacyLocalOnly; config deliberately stays
// independent of the security package.
const (
	PrivacyModeAny       = "any"
	PrivacyModeLocalOnly = "local-only"
)

const (
	appDir          = ".argentgo"
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
	// PrivacyMode is the network posture: PrivacyModeAny ("any", the default)
	// or PrivacyModeLocalOnly ("local-only"). local-only blocks remote endpoints
	// and web tools. It is independent of SandboxMode, which governs file tools.
	// Invalid values fail config load rather than silently widening the posture;
	// app.ApplyPrivacyPosture is the production consumer.
	PrivacyMode string
	MaxTurns    int
	Yolo        bool
	StorageDir  string

	// VerifyCommand is the opt-in post-edit verification argv (e.g.
	// ["go", "test", "./..."]). Empty disables the bounded reflection loop.
	// app.NewVerifyFunc re-validates it against a strict allowlist before use.
	VerifyCommand []string
	// VerifyTimeoutMS bounds one verification process (default 30000, capped
	// at 120000). Zero selects the default.
	VerifyTimeoutMS int
	// VerifyMaxReflections caps FAIL feedback turns; zero selects the loop's
	// own default of 3.
	VerifyMaxReflections int

	// ContextWindow is the resolved model's context window in tokens. It is
	// optional: zero means "unspecified", so callers fall back to the fixed
	// threshold. It can be set at the top level or on a model/provider entry.
	ContextWindow int
	// AutoCondenseContextPercent is the per-profile percentage of the context
	// window that triggers auto-compaction. 0 or -1 inherits the global token
	// threshold; a positive value overrides it.
	AutoCondenseContextPercent int

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

	// The fields below are unexported reload metadata. They are populated by
	// Load/LoadWithOverrides and let a long-lived caller detect on-disk changes
	// (Changed) and rebuild the config (Reload) without restarting. They are
	// additive: external packages constructing Config with keyed literals are
	// unaffected.
	loadEnv       func(string) string
	loadOverrides map[string]any
	generation    uint64
	fingerprint   configFingerprint
}

// fileStamp is the cheap, parse-free identity of one config file. mtime and
// size together catch rewrites even on filesystems with coarse timestamp
// resolution; existence and statErr catch created/removed/unreadable files.
type fileStamp struct {
	path    string
	modTime int64
	size    int64
	exists  bool
	statErr bool
}

// configFingerprint is the ordered snapshot of every config file layer. The
// file list always includes all candidate paths (even missing ones) so that a
// newly created config file is detected as a change.
type configFingerprint struct {
	files []fileStamp
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
	warn := func(format string, args ...any) { log.Printf(format, args...) }
	for _, file := range configFiles(cwd, home) {
		overlay, err := readConfig(file, env, warn)
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
		// A `-c profile=<name>` override selects a profile too. It sits at the
		// file-override layer: it beats a "profile" key in a config file, but
		// ARGENT_PROFILE (which also carries the --profile flag) still wins.
		// Only the selector is read here; every other override keeps its
		// original position after the env layer, so bringing profile
		// selection forward cannot change how those values are merged.
		profile = strings.TrimSpace(stringField(overrides, "profile", "activeProfile"))
	}
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
	if err := validatePrivacyMode(cfg.PrivacyMode); err != nil {
		return Config{}, err
	}
	cfg.ActiveProfile = profile
	// Do not fold the provider's env key into cfg.APIKey. app.Resolve already
	// lists env(provider.APIKeyEnv) as the last resort of its credential chain;
	// copying it here would make it outrank the `argent login` key stored under
	// providers.<name>.apiKey and break the documented priority.
	//
	// Attach reload metadata so a long-lived caller can poll Changed and call
	// Reload. The first successful load is generation 1; every Reload bumps it.
	cfg.loadEnv = env
	cfg.loadOverrides = cloneTree(overrides)
	cfg.generation = 1
	cfg.fingerprint = newFingerprint(cwd, home)
	return cfg, nil
}

// Validate rejects configurations the rest of the program cannot honour. It
// is kept for compatibility: use Diagnostics to see every problem at once.
func (c Config) Validate() error {
	diagnostics := c.validateDiagnostics()
	if len(diagnostics) == 0 {
		return nil
	}
	first := diagnostics[0]
	return fmt.Errorf("config: %s", first.Message)
}

// Generation returns the version of this config snapshot. The first load is
// generation 1; each successful Reload increments it by one. A caller can use
// it to notice that a peer rebuilt the config even when it did not poll Changed
// at the exact moment of the rewrite.
func (c Config) Generation() uint64 {
	return c.generation
}

// Changed reports whether any config file on disk differs from the snapshot
// this Config was loaded from. It is a cheap polling hook for long-lived
// callers: it only stats the candidate files (mtime, size, existence) and never
// parses or merges them, so it is safe to call on a hot path. When a change is
// detected, call Reload to get the new values. A Config built by hand (not via
// Load) has no snapshot to compare and always reports false.
//
// Changed only observes config files. Environment variables and flag overrides
// are captured at load time; Changed cannot see them change. Reload reuses the
// original env function, so an env function backed by os.Getenv will still pick
// up process-level changes at reload time.
//
// Note: filesystem mtime resolution is finite. A rewrite that preserves both
// size and mtime within one tick cannot be detected by this lightweight check;
// callers needing stronger guarantees should hash the file bytes themselves.
func (c Config) Changed() bool {
	if len(c.fingerprint.files) == 0 {
		return false
	}
	paths := make([]string, len(c.fingerprint.files))
	for i, stamp := range c.fingerprint.files {
		paths[i] = stamp.path
	}
	return !fingerprintPaths(paths).equal(c.fingerprint)
}

// Reload re-reads every config layer from disk using the same cwd, env and
// flag overrides as the original load, and returns a NEW Config whose
// Generation is one higher. The receiver is left untouched, and on error nil
// is returned so the caller can keep using the current config. Precedence and
// merge semantics are identical to Load because Reload delegates to
// LoadWithOverrides.
func (c Config) Reload() (*Config, error) {
	cwd := c.CWD
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	next, err := LoadWithOverrides(cwd, c.loadEnv, c.loadOverrides)
	if err != nil {
		return nil, err
	}
	next.generation = c.generation + 1
	return &next, nil
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
		// Opt-in verification is default-off: an empty argv leaves the runner's
		// Verify callback nil, so existing runs stay byte-identical. The timeout
		// still gets a compiled default so callers never see a zero deadline.
		"verifyTimeoutMS": float64(30000),
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

// newFingerprint snapshots every config file candidate for cwd/home, including
// missing files (recorded as exists=false) so that later creation is detected.
func newFingerprint(cwd, home string) configFingerprint {
	return fingerprintPaths(configFiles(cwd, home))
}

// fingerprintPaths stats each path and records a parse-free identity. A stat
// error that is not "not exist" is recorded as statErr so a transient
// unreadable file is conservatively treated as a change rather than matching
// a missing-file snapshot.
func fingerprintPaths(paths []string) configFingerprint {
	stamps := make([]fileStamp, 0, len(paths))
	for _, path := range paths {
		stamp := fileStamp{path: path}
		switch info, err := os.Stat(path); {
		case err == nil:
			stamp.exists = true
			stamp.size = info.Size()
			stamp.modTime = info.ModTime().UnixNano()
		case os.IsNotExist(err):
			// Missing file: leave the zero identity in place.
		default:
			stamp.statErr = true
		}
		stamps = append(stamps, stamp)
	}
	return configFingerprint{files: stamps}
}

// equal reports whether two fingerprints describe the same file identities.
func (f configFingerprint) equal(other configFingerprint) bool {
	if len(f.files) != len(other.files) {
		return false
	}
	for i := range f.files {
		a, b := f.files[i], other.files[i]
		if a.path != b.path || a.exists != b.exists || a.size != b.size ||
			a.modTime != b.modTime || a.statErr != b.statErr {
			return false
		}
	}
	return true
}

func homeDir(env func(string) string) string {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	return "."
}

// readConfig reads one JSONC object. Variable references are substituted and
// comments/trailing commas stripped before decoding. A missing file is not an
// error; a malformed file is, because the operator needs to know a setting is
// not in effect.
func readConfig(path string, env func(string) string, warn WarnFunc) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	// PowerShell 5.x writes "UTF-8 with BOM"; encoding/json rejects the BOM as
	// an invalid leading character. Strip it before the empty check so a
	// BOM-only file is treated exactly like an empty file.
	text := strings.TrimPrefix(string(data), "\ufeff")
	if len(strings.TrimSpace(text)) == 0 {
		return nil, nil
	}
	substituted := Substitute(text, filepath.Dir(path), env, warn)
	clean := StripJSONC(substituted)
	var tree map[string]any
	if err := json.Unmarshal([]byte(clean), &tree); err != nil {
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
	setString("privacyMode", "ARGENT_PRIVACY_MODE")
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
		Model:       stringField(tree, "model", "modelName"),
		BaseURL:     stringField(tree, "baseURL", "baseUrl", "base_url"),
		APIKey:      stringField(tree, "apiKey", "api_key"),
		Protocol:    stringField(tree, "protocol"),
		Locale:      stringField(tree, "locale"),
		SandboxMode: stringField(tree, "sandboxMode", "sandbox_mode"),
		PrivacyMode: decodePrivacyMode(tree),
		MaxTurns:    intField(tree, "maxTurns", "max_turns"),
		Yolo:        boolField(tree, "yolo"),
		StorageDir:  stringField(tree, "storageDir", "storage_dir"),

		VerifyCommand:        stringSliceField(tree, "verifyCommand", "verify_command"),
		VerifyTimeoutMS:      intField(tree, "verifyTimeoutMS", "verify_timeout_ms"),
		VerifyMaxReflections: intField(tree, "verifyMaxReflections", "verify_max_reflections"),

		ContextWindow:              contextWindowFrom(tree),
		AutoCondenseContextPercent: intField(tree, "autoCondenseContextPercent", "auto_condense_context_percent"),
		ProviderName:               stringField(tree, "provider"),
		Profiles:                   profilesFrom(tree),
		Providers:                  providersFrom(tree),
		Sources:                    sources,
		Raw:                        cloneTree(tree),
		CWD:                        cwd,
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

// stringSliceField decodes an argv-style JSON array from the merged tree. Both
// []any (from JSON/CLI overrides) and []string (constructed in tests) are
// accepted; non-string members are skipped rather than failing a whole config.
func stringSliceField(tree map[string]any, keys ...string) []string {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case []any:
			out := make([]string, 0, len(value))
			for _, item := range value {
				if text, ok := item.(string); ok {
					out = append(out, text)
				}
			}
			return out
		case []string:
			return append([]string(nil), value...)
		}
	}
	return nil
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

// decodePrivacyMode reads the privacy posture from either spelling and
// canonicalizes it (trimmed, lower-case) so it matches the security enum. An
// unset/blank value falls back to the default "any" posture. The default is
// applied here rather than in defaults() so an explicit "privacy_mode" key can
// never be shadowed by a canonical "privacyMode" key injected at the base
// layer.
func decodePrivacyMode(tree map[string]any) string {
	mode := strings.ToLower(strings.TrimSpace(stringField(tree, "privacyMode", "privacy_mode")))
	if mode == "" {
		return PrivacyModeAny
	}
	return mode
}

// validatePrivacyMode rejects values the privacy helpers cannot honor. A typo
// fails config load instead of silently falling back to the permissive "any"
// posture: security.AllowRemote already fails closed for unknown modes, and a
// load-time error keeps that invariant consistent.
func validatePrivacyMode(mode string) error {
	switch mode {
	case PrivacyModeAny, PrivacyModeLocalOnly:
		return nil
	default:
		return fmt.Errorf("config: privacyMode %q must be %q or %q", mode, PrivacyModeAny, PrivacyModeLocalOnly)
	}
}

// contextWindowFrom resolves the model context window from the merged tree.
// A top-level contextWindow (or alias) wins; then a model entry under "models"
// keyed by the configured model, then the active provider entry. Configs that
// omit these keys keep decoding to zero, so existing behaviour is unchanged.
func contextWindowFrom(tree map[string]any) int {
	if value := intField(tree, "contextWindow", "contextWindowTokens", "context_window"); value > 0 {
		return value
	}
	model := stringField(tree, "model", "modelName")
	if model != "" {
		if models, ok := tree["models"].(map[string]any); ok {
			if entry, ok := lookupFold(models, model); ok {
				if modelEntry, ok := entry.(map[string]any); ok {
					if value := intField(modelEntry, "contextWindow", "contextWindowTokens", "context_window"); value > 0 {
						return value
					}
				}
			}
		}
	}
	provider := stringField(tree, "provider")
	if provider != "" {
		if providers, ok := tree["providers"].(map[string]any); ok {
			if entry, ok := lookupFold(providers, provider); ok {
				if providerEntry, ok := entry.(map[string]any); ok {
					if value := intField(providerEntry, "contextWindow", "contextWindowTokens", "context_window"); value > 0 {
						return value
					}
				}
			}
		}
	}
	return 0
}

// CondensePercent returns the auto-condense percentage configured for profile.
// An explicit per-profile value wins over the merged/global value; 0 and -1
// are preserved so callers can inherit the global token threshold.
func (c Config) CondensePercent(profile string) int {
	if strings.TrimSpace(profile) != "" {
		if entry, ok := lookupFold(c.Profiles, profile); ok {
			for _, key := range []string{"autoCondenseContextPercent", "auto_condense_context_percent"} {
				if _, exists := entry[key]; exists {
					return intField(entry, key)
				}
			}
		}
	}
	return c.AutoCondenseContextPercent
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
