package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Overrides holds repeated `-c key=value` / `--config key=value` settings.
// Dotted keys build nested objects, e.g. `-c tools.include='["read"]'`.
type Overrides struct {
	values map[string]string
}

// Set parses one key=value override. It implements flag.Value.
func (o *Overrides) Set(spec string) error {
	key, value, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("expected key=value, got %q", spec)
	}
	key = strings.TrimSpace(key)
	if err := validateDottedKey(key); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("override %q has an empty value", key)
	}
	if o.values == nil {
		o.values = make(map[string]string)
	}
	o.values[key] = strings.TrimSpace(value)
	return nil
}

// String implements flag.Value.
func (o *Overrides) String() string {
	if o == nil || len(o.values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(o.values))
	for key := range o.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+o.values[key])
	}
	return strings.Join(parts, ",")
}

// Get reports the raw value of one override key.
func (o *Overrides) Get(key string) (string, bool) {
	if o == nil {
		return "", false
	}
	value, ok := o.values[key]
	return value, ok
}

// lookup returns the first override matching any of the given keys.
func (o *Overrides) lookup(keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := o.Get(key); ok {
			return value, true
		}
	}
	return "", false
}

// All returns a copy of the raw override map.
func (o *Overrides) All() map[string]string {
	out := make(map[string]string, len(o.values))
	for key, value := range o.values {
		out[key] = value
	}
	return out
}

// Nested turns dotted overrides into nested maps, parsing values as JSON when
// possible. It is what the config file writer consumes.
func (o *Overrides) Nested() map[string]any {
	out := make(map[string]any, len(o.values))
	for key, value := range o.values {
		setDotted(out, key, parseValue(value))
	}
	return out
}

func validateDottedKey(key string) error {
	if key == "" {
		return errors.New("override has an empty key")
	}
	for _, segment := range strings.Split(key, ".") {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("invalid dotted key %q", key)
		}
	}
	return nil
}

// parseValue decodes a CLI/config value as JSON, falling back to a string.
func parseValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return raw
}

// stringList is a repeatable string flag (--add-dir, --plugins).
type stringList struct {
	values []string
}

func (l *stringList) String() string { return strings.Join(l.values, ",") }

func (l *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("expected a non-empty value")
	}
	l.values = append(l.values, value)
	return nil
}

// optionalString is a flag whose value is optional: `--worktree` alone means
// "auto", `--worktree=name` names the worktree.
type optionalString struct {
	value string
}

func (o *optionalString) String() string { return o.value }

func (o *optionalString) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || value == "true" {
		value = "auto"
	}
	o.value = value
	return nil
}

// IsBoolFlag lets the flag package accept a bare --worktree.
func (o *optionalString) IsBoolFlag() bool { return true }

// flagValues holds one pointer per registered flag. It exists so the flag
// surface has exactly one definition, which the parity test inspects.
type flagValues struct {
	model    *string
	baseURL  *string
	apiKey   *string
	protocol *string
	provider *string
	profile  *string

	yolo        *bool
	maxTurns    *int
	maxParallel *int
	mode        *string
	cwd         *string

	sandboxMode *string
	approval    *string
	session     *string
	resume      *bool
	undo        *bool
	retry       *bool
	title       *string
	locale      *string

	jsonLines     *bool
	listProfiles  *bool
	listSessions  *bool
	listModels    *bool
	noColor       *bool
	tokens        *bool
	trustProject  *bool
	thinking      *string
	noAutoCompact *bool
	noPromptCache *bool
	theme         *string
	plugins       *stringList

	maxTokens      *int
	maxCost        *float64
	maxSeconds     *int
	priceInput     *float64
	priceOutput    *float64
	fallbackModel  *string
	secondaryModel *string
	addDirs        *stringList
	outputSchema   *string

	background      *bool
	worktree        *optionalString
	enableBrowser   *bool
	enableDesktop   *bool
	enableService   *bool
	workflowMode    *string
	workflowResume  *bool
	workflowJournal *string
	gui             *bool
	tui             *bool

	serve *bool
	rpc   *bool
	acp   *bool
	addr  *string
	port  *int
	token *string

	version *bool
	help    *bool

	overrides *Overrides
}
