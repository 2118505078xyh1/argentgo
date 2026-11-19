package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// WarnFunc reports a non-fatal problem (a missing environment variable or an
// unreadable file reference). A nil WarnFunc discards warnings.
type WarnFunc func(format string, args ...any)

var (
	envRefRe  = regexp.MustCompile(`\{env:([^}]+)\}`)
	fileRefRe = regexp.MustCompile(`\{file:[^}]+\}`)
)

// Substitute applies {env:VAR} and {file:path} references to raw config text.
// Relative {file:} paths resolve against configDir (the config file's
// directory). Missing references substitute an empty string and invoke warn.
//
// Environment values are substituted verbatim by default so configs that
// parse today keep their meaning. If that produces malformed JSON, the whole
// document is retried with JSON-string escaping applied to env values (the
// fallback that makes Windows paths and quotes safe to embed).
func Substitute(text, configDir string, env func(string) string, warn WarnFunc) string {
	if env == nil {
		env = func(string) string { return "" }
	}
	if warn == nil {
		warn = func(string, ...any) {}
	}

	raw := replaceFiles(replaceEnv(text, env, false, warn), configDir, env, warn)
	if !strings.Contains(text, "{env:") || isJSONConfig(raw) {
		return raw
	}
	return replaceFiles(replaceEnv(text, env, true, warn), configDir, env, warn)
}

func replaceEnv(text string, env func(string) string, escape bool, warn WarnFunc) string {
	return envRefRe.ReplaceAllStringFunc(text, func(token string) string {
		name := strings.TrimPrefix(strings.TrimSuffix(token, "}"), "{env:")
		value := env(name)
		if value == "" {
			warn("config: environment variable %q referenced in config is not set", name)
		}
		if escape {
			return jsonStringBody(value)
		}
		return value
	})
}

func replaceFiles(text, configDir string, env func(string) string, warn WarnFunc) string {
	var out strings.Builder
	cursor := 0
	for _, loc := range fileRefRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		out.WriteString(text[cursor:start])
		cursor = end
		token := text[start:end]

		// A reference written inside a // line comment is not a real reference.
		lineStart := strings.LastIndex(text[:start], "\n") + 1
		prefix := strings.TrimLeft(text[lineStart:start], " \t")
		if strings.HasPrefix(prefix, "//") {
			out.WriteString(token)
			continue
		}

		resolved := resolveFileRef(strings.TrimPrefix(strings.TrimSuffix(token, "}"), "{file:"), configDir, env)
		data, err := os.ReadFile(resolved)
		if err != nil {
			warn("config: file reference %q (%s) could not be read: %v", token, resolved, err)
			continue
		}
		// File contents are always escaped: a secret may contain quotes, braces
		// or newlines that must not break the surrounding JSON string.
		out.WriteString(jsonStringBody(strings.TrimSpace(string(data))))
	}
	out.WriteString(text[cursor:])
	return out.String()
}

func resolveFileRef(ref, configDir string, env func(string) string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "~/") || strings.HasPrefix(ref, `~\`) {
		ref = filepath.Join(homeDir(env), ref[2:])
	}
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref)
	}
	if strings.TrimSpace(configDir) == "" {
		configDir = "."
	}
	return filepath.Join(configDir, ref)
}

// jsonStringBody returns value escaped for inclusion inside a JSON string,
// without the surrounding double quotes (the equivalent of
// JSON.stringify(value).slice(1, -1) in the reference implementation).
func jsonStringBody(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	text := string(encoded)
	if len(text) < 2 {
		return ""
	}
	return text[1 : len(text)-1]
}

func isJSONConfig(text string) bool {
	return json.Valid([]byte(StripJSONC(text)))
}
