package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Problem describes one malformed mcpServers entry or field. DecodeConfig
// skips bad entries (and ignores bad fields) rather than failing the whole
// config, and reports them here for a caller that wants to print a warning.
type Problem struct {
	// Server is the server name, or a positional label such as "#2".
	Server string
	// Field is the offending key, empty for an entry-level problem.
	Field string
	// Reason is a human-readable explanation.
	Reason string
}

// Error implements error so a Problem can be passed to the app's fail-soft
// note writer unchanged.
func (p Problem) Error() string {
	if p.Field == "" {
		return fmt.Sprintf("mcpServers %q: %s", p.Server, p.Reason)
	}
	return fmt.Sprintf("mcpServers %q.%s: %s", p.Server, p.Field, p.Reason)
}

// DecodeConfig decodes the merged config's `mcpServers` object (a list form is
// accepted too) into specs. Both camelCase and snake_case spellings are read
// for the permission fields. Specs and problems are sorted by name/field so
// output is stable regardless of Go's map iteration order.
//
// Entries that are not objects, or that have no name, are skipped and reported
// in problems. A badly typed field is ignored, reported, and the rest of that
// entry is still decoded. An entry with neither command nor URL remains a spec;
// Connect will report the missing target when the server is used.
func DecodeConfig(raw map[string]any) ([]Spec, []Problem) {
	var specs []Spec
	var problems []Problem
	if raw == nil {
		return nil, nil
	}
	value, exists := raw["mcpServers"]
	if !exists {
		return nil, nil
	}
	problem := func(server, field, reason string) {
		problems = append(problems, Problem{Server: server, Field: field, Reason: reason})
	}
	add := func(name string, body map[string]any) {
		name = strings.TrimSpace(name)
		if name == "" {
			problem(name, "", "missing server name; entry skipped")
			return
		}
		spec := Spec{Name: name}
		if v, key, ok := pick(body, "command"); ok {
			if text, valid := v.(string); valid {
				spec.Command = strings.TrimSpace(text)
			} else {
				problem(name, key, fmt.Sprintf("want a string, got %T; ignored", v))
			}
		}
		if v, key, ok := pick(body, "url"); ok {
			if text, valid := v.(string); valid {
				spec.URL = strings.TrimSpace(text)
			} else {
				problem(name, key, fmt.Sprintf("want a string, got %T; ignored", v))
			}
		}
		if v, key, ok := pick(body, "transport"); ok {
			if text, valid := v.(string); valid {
				spec.Transport = strings.TrimSpace(text)
			} else {
				problem(name, key, fmt.Sprintf("want a string, got %T; ignored", v))
			}
		}
		if v, key, ok := pick(body, "args"); ok {
			list, valid := decodeStrings(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an array of strings, got %T; ignored", v))
			} else {
				spec.Args = list
			}
		}
		if v, key, ok := pick(body, "env"); ok {
			list, valid := decodeEnv(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an object or array of strings, got %T; ignored", v))
			} else {
				spec.Env = list
			}
		}
		if v, key, ok := pick(body, "headers"); ok {
			headers, valid := decodeHeaders(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an object of strings, got %T; ignored", v))
			} else {
				spec.Headers = headers
			}
		}
		if v, key, ok := pick(body, "timeout"); ok {
			seconds, valid := decodeSeconds(v)
			if !valid || seconds <= 0 {
				problem(name, key, "want a positive number of seconds; ignored")
			} else {
				spec.Timeout = time.Duration(seconds * float64(time.Second))
			}
		}
		if v, key, ok := pick(body, "enabled", "Enabled"); ok {
			flag, valid := decodeBool(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want a boolean, got %T; ignored", v))
			} else {
				spec.Enabled = &flag
			}
		}
		if v, key, ok := pick(body, "readOnly", "read_only"); ok {
			flag, valid := decodeBool(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want a boolean, got %T; ignored", v))
			} else {
				spec.ReadOnly = flag
			}
		}
		if v, key, ok := pick(body, "allowedTools", "allowed_tools"); ok {
			list, valid := decodeStrings(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an array of strings, got %T; ignored", v))
			} else {
				spec.AllowedTools = list
			}
		}
		if v, key, ok := pick(body, "deniedTools", "denied_tools"); ok {
			list, valid := decodeStrings(v)
			if !valid {
				problem(name, key, fmt.Sprintf("want an array of strings, got %T; ignored", v))
			} else {
				spec.DeniedTools = list
			}
		}
		specs = append(specs, spec)
	}

	switch value := value.(type) {
	case map[string]any:
		for name, entry := range value {
			body, ok := entry.(map[string]any)
			if !ok {
				problem(name, "", fmt.Sprintf("entry must be an object, got %T; skipped", entry))
				continue
			}
			add(name, body)
		}
	case []any:
		for i, entry := range value {
			body, ok := entry.(map[string]any)
			if !ok {
				problem(fmt.Sprintf("#%d", i+1), "", fmt.Sprintf("entry must be an object, got %T; skipped", entry))
				continue
			}
			var name string
			if v, _, found := pick(body, "name"); found {
				name, _ = v.(string)
			}
			if strings.TrimSpace(name) == "" {
				name = fmt.Sprintf("server-%d", i+1)
			}
			add(name, body)
		}
	default:
		problem("", "", fmt.Sprintf("mcpServers must be an object or array, got %T; skipped", value))
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Server != problems[j].Server {
			return problems[i].Server < problems[j].Server
		}
		if problems[i].Field != problems[j].Field {
			return problems[i].Field < problems[j].Field
		}
		return problems[i].Reason < problems[j].Reason
	})
	return specs, problems
}

// SpecsFromConfig is the thin decoder the app layer calls in place of its
// former local mcpSpecsFromConfig helper. It returns only the specs; use
// DecodeConfig when the skipped-entry problems should be surfaced to the user.
func SpecsFromConfig(raw map[string]any) []Spec {
	specs, _ := DecodeConfig(raw)
	return specs
}

// pick returns the first present key so camelCase wins over snake_case when a
// config happens to declare both.
func pick(body map[string]any, keys ...string) (any, string, bool) {
	for _, key := range keys {
		if v, ok := body[key]; ok {
			return v, key, true
		}
	}
	return nil, "", false
}

func decodeBool(v any) (bool, bool) {
	switch value := v.(type) {
	case bool:
		return value, true
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func decodeStrings(v any) ([]string, bool) {
	switch value := v.(type) {
	case []string:
		return append([]string(nil), value...), true
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, strings.TrimSpace(text))
		}
		return out, true
	case string:
		return []string{strings.TrimSpace(value)}, true
	}
	return nil, false
}

func decodeEnv(v any) ([]string, bool) {
	switch value := v.(type) {
	case map[string]any:
		out := make([]string, 0, len(value))
		for key, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, key+"="+text)
		}
		sort.Strings(out)
		return out, true
	case map[string]string:
		out := make([]string, 0, len(value))
		for key, text := range value {
			out = append(out, key+"="+text)
		}
		sort.Strings(out)
		return out, true
	case []any, []string:
		return decodeStrings(v)
	}
	return nil, false
}

func decodeHeaders(v any) (map[string]string, bool) {
	entries, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(entries))
	for key, item := range entries {
		text, valid := item.(string)
		if !valid {
			return nil, false
		}
		out[key] = text
	}
	return out, true
}

func decodeSeconds(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		n, err := value.Float64()
		return n, err == nil
	case string:
		var n float64
		_, err := fmt.Sscanf(strings.TrimSpace(value), "%f", &n)
		return n, err == nil
	}
	return 0, false
}
