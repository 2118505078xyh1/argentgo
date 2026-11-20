package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// runConfig implements `config get|set` over dotted keys.
func runConfig(app App, _ context.Context, args []string) int {
	if len(args) == 0 {
		configUsage(app.IO.Err)
		return 2
	}
	path := app.configPath()
	switch args[0] {
	case "-h", "--help":
		configUsage(app.IO.Out)
		return 0
	case "path":
		fmt.Fprintln(app.IO.Out, path)
		return 0
	case "get":
		if len(args) != 2 {
			configUsage(app.IO.Err)
			return 2
		}
		cfg, err := loadConfigFile(path)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		value, ok := getDotted(cfg, args[1])
		if !ok {
			fmt.Fprintf(app.IO.Err, "error: %s is not set in %s\n", args[1], path)
			return 1
		}
		fmt.Fprintln(app.IO.Out, formatConfigValue(value))
		return 0
	case "set":
		if len(args) != 3 {
			configUsage(app.IO.Err)
			return 2
		}
		if err := validateDottedKey(args[1]); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 2
		}
		cfg, err := loadConfigFile(path)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		warnJSONCRewrite(app.IO.Err, path)
		setDotted(cfg, args[1], parseValue(args[2]))
		if err := saveConfigFile(path, cfg); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "set %s in %s\n", args[1], path)
		return 0
	default:
		fmt.Fprintf(app.IO.Err, "argent: unknown config command %q\n", args[0])
		configUsage(app.IO.Err)
		return 2
	}
}

func formatConfigValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

// loadConfigFile reads a JSONC object, treating a missing file as empty. It
// reuses the same JSONC cleaner as the main config loader so subcommands and
// the engine accept exactly the same syntax (comments, trailing commas).
func loadConfigFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	clean := config.StripJSONC(string(data))
	cfg := map[string]any{}
	if err := json.Unmarshal([]byte(clean), &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

// warnJSONCRewrite tells the operator when `config set` is about to discard
// JSONC syntax that the loader accepts. saveConfigFile writes strict JSON, so
// comments and trailing commas in a hand-edited file cannot survive.
func warnJSONCRewrite(w io.Writer, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if config.StripJSONC(string(data)) == string(data) {
		return
	}
	fmt.Fprintln(w, "warning: config set rewrites as strict JSON; comments and trailing commas are not preserved")
}

// saveConfigFile writes a JSON object atomically with owner-only permissions.
func saveConfigFile(path string, cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".argent-config-*")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// setDotted stores value at a dotted path, creating intermediate objects.
func setDotted(root map[string]any, key string, value any) {
	segments := strings.Split(key, ".")
	current := root
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
	current[segments[len(segments)-1]] = value
}

// getDotted reads a dotted path.
func getDotted(root map[string]any, key string) (any, bool) {
	var current any = root
	for _, segment := range strings.Split(key, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func configUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent config get <dotted.key>
       argent config set <dotted.key> <value>
       argent config path

Values are parsed as JSON when possible, so `+"`set model gpt-4o`"+` stores a string
and `+"`set maxTurns 20`"+` stores a number. Config files may contain comments and
trailing commas (JSONC). Note: `+"`set`"+` rewrites the file as strict JSON; comments
and trailing commas in the original file are removed.
`)
}
