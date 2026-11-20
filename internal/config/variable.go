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
// References written inside JSONC comments (both // and /* */) are left
// untouched: they are documentation, not settings. Besides avoiding spurious
// "variable not set" warnings, this matters for correctness — a substituted
// env/file value containing "*/" would otherwise close a surrounding block
// comment early and corrupt the rest of the JSONC document.
//
// Environment values are substituted verbatim by default so configs that
// parse today keep their meaning. The document is retried with JSON-string
// escaping applied to env values when the verbatim result is malformed JSON
// (a quote or control byte) OR when an env value contains a backslash: a
// backslash is the one case that can stay *syntactically* valid while silently
// changing the value, because "\t" in a Windows path such as C:\temp decodes
// as a tab rather than the two literal characters.
func Substitute(text, configDir string, env func(string) string, warn WarnFunc) string {
	if env == nil {
		env = func(string) string { return "" }
	}
	if warn == nil {
		warn = func(string, ...any) {}
	}

	rawEnvText, needsEscape := replaceEnv(text, env, false, warn)
	raw := replaceFiles(rawEnvText, configDir, env, warn)
	if !strings.Contains(text, "{env:") || (isJSONConfig(raw) && !needsEscape) {
		return raw
	}
	escapedEnvText, _ := replaceEnv(text, env, true, warn)
	return replaceFiles(escapedEnvText, configDir, env, warn)
}

// replaceEnv substitutes {env:VAR} references. The second result reports
// whether any substituted (non-comment) value contains a backslash, which the
// caller uses to decide that verbatim substitution is not value-preserving.
func replaceEnv(text string, env func(string) string, escape bool, warn WarnFunc) (string, bool) {
	var out strings.Builder
	out.Grow(len(text))
	cursor := 0
	needsEscape := false
	for _, loc := range envRefRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		out.WriteString(text[cursor:start])
		cursor = end
		token := text[start:end]
		if inJSONCComment(text, start) {
			out.WriteString(token) // a comment is documentation, not a setting
			continue
		}
		name := strings.TrimPrefix(strings.TrimSuffix(token, "}"), "{env:")
		value := env(name)
		if value == "" {
			warn("config: environment variable %q referenced in config is not set", name)
		}
		if strings.ContainsRune(value, '\\') {
			needsEscape = true
		}
		if escape {
			out.WriteString(jsonStringBody(value))
		} else {
			out.WriteString(value)
		}
	}
	out.WriteString(text[cursor:])
	return out.String(), needsEscape
}

func replaceFiles(text, configDir string, env func(string) string, warn WarnFunc) string {
	var out strings.Builder
	cursor := 0
	for _, loc := range fileRefRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		out.WriteString(text[cursor:start])
		cursor = end
		token := text[start:end]

		// A reference written inside a // or /* */ comment is not a real
		// reference; leaving it intact also keeps its value from breaking out
		// of a block comment when the referenced file contains "*/".
		if inJSONCComment(text, start) {
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

// inJSONCComment reports whether the byte at offset lies inside a // line
// comment or a /* */ block comment. It mirrors StripJSONC's scanner: quoted
// strings (including escaped quotes and backslashes) are transparent, so a
// "{file:...}" written inside a JSON string value is never mistaken for a
// commented-out reference.
func inJSONCComment(text string, offset int) bool {
	if offset > len(text) {
		offset = len(text)
	}
	inString, escaped, line, block := false, false, false, false
	for i := 0; i < offset; i++ {
		char := text[i]
		if line {
			if char == '\n' {
				line = false
			}
			continue
		}
		if block {
			if char == '*' && i+1 < len(text) && text[i+1] == '/' {
				block = false
				i++
			}
			continue
		}
		if inString {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch {
		case char == '"':
			inString = true
		case char == '/' && i+1 < len(text) && text[i+1] == '/':
			line = true
			i++
		case char == '/' && i+1 < len(text) && text[i+1] == '*':
			block = true
			i++
		}
	}
	return line || block
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
