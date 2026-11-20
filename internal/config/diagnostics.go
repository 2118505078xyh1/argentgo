package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// DiagnosticKind classifies a configuration diagnostic. The three kinds match
// the ones reported by the reference implementation: a value that cannot be
// honoured (invalid), two recognized keys that disagree (conflict), and a
// recognized but unsupported legacy setting (unsupported).
type DiagnosticKind string

const (
	DiagnosticInvalid     DiagnosticKind = "invalid"
	DiagnosticConflict    DiagnosticKind = "conflict"
	DiagnosticUnsupported DiagnosticKind = "unsupported"
)

// Diagnostic is one problem found while inspecting a resolved Config. Path is
// a JSON path such as "$.sandboxMode" or "$.providers.anthropic.baseURL".
// Unlike Validate, Diagnostics never stops at the first problem.
type Diagnostic struct {
	Path    string         `json:"path"`
	Kind    DiagnosticKind `json:"kind"`
	Message string         `json:"message"`
}

// Diagnostics reports every problem it can find in c at once. Value-level
// problems (the ones Validate reports) come first, in a stable order,
// followed by problems discovered in the merged Raw tree.
func (c Config) Diagnostics() []Diagnostic {
	out := c.validateDiagnostics()
	return append(out, treeDiagnostics(c.Raw)...)
}

// validateDiagnostics is the exact set of hard failures Validate has always
// reported. Keeping it separate prevents new informational diagnostics from
// changing Validate's compatibility contract.
func (c Config) validateDiagnostics() []Diagnostic {
	var out []Diagnostic

	switch c.SandboxMode {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
	default:
		out = append(out, Diagnostic{
			Path: "$.sandboxMode",
			Kind: DiagnosticInvalid,
			Message: fmt.Sprintf("sandboxMode %q must be one of %s, %s, %s",
				c.SandboxMode, SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess),
		})
	}
	if strings.TrimSpace(c.Model) == "" {
		out = append(out, Diagnostic{
			Path: "$.model", Kind: DiagnosticInvalid, Message: "model must not be empty",
		})
	}
	if c.MaxTurns < 0 {
		out = append(out, Diagnostic{
			Path:    "$.maxTurns",
			Kind:    DiagnosticInvalid,
			Message: fmt.Sprintf("maxTurns must be >= 0, got %d", c.MaxTurns),
		})
	}

	return out
}

// configAliases lists every recognized key family. The first entry is the
// canonical spelling decode() prefers; the rest are accepted aliases.
var configAliases = map[string][]string{
	"model":       {"model", "modelName"},
	"baseURL":     {"baseURL", "baseUrl", "base_url"},
	"apiKey":      {"apiKey", "api_key"},
	"protocol":    {"protocol"},
	"locale":      {"locale"},
	"sandboxMode": {"sandboxMode", "sandbox_mode"},
	"maxTurns":    {"maxTurns", "max_turns"},
	"yolo":        {"yolo"},
	"storageDir":  {"storageDir", "storage_dir"},
	"provider":    {"provider"},
}

// unsupportedKeys are recognized legacy settings that argentgo deliberately
// ignores. Unknown keys are preserved in Raw and stay valid; only these are
// called out, mirroring the reference implementation's unsupported list.
var unsupportedKeys = []string{"logLevel", "subagent_depth", "layout", "server"}

func treeDiagnostics(raw map[string]any) []Diagnostic {
	if len(raw) == 0 {
		return nil
	}
	var out []Diagnostic

	canonical := make([]string, 0, len(configAliases))
	for key := range configAliases {
		canonical = append(canonical, key)
	}
	sort.Strings(canonical)

	for _, field := range canonical {
		aliases := configAliases[field]
		present := make([]string, 0, len(aliases))
		for _, key := range aliases {
			if _, ok := raw[key]; ok {
				present = append(present, key)
			}
		}
		if len(present) == 0 {
			continue
		}

		// decode() reads aliases in order and returns the first value with the
		// right type, skipping an ill-typed canonical key. Mirror that rule so
		// the "using X" message names the value that actually reached Config;
		// present[0] is only a fallback when every alias is ill-typed.
		winner := present[0]
		for _, key := range present {
			if configValueTypeOK(field, raw[key]) {
				winner = key
				break
			}
		}
		winnerValue := raw[winner]
		for _, key := range present {
			if !configValueTypeOK(field, raw[key]) {
				out = append(out, Diagnostic{
					Path:    "$." + key,
					Kind:    DiagnosticInvalid,
					Message: fmt.Sprintf("%s must be %s, got %T", key, expectedType(field), raw[key]),
				})
			}
		}
		if len(present) > 1 {
			for _, key := range present {
				if key == winner {
					continue
				}
				if !reflect.DeepEqual(raw[key], winnerValue) {
					out = append(out, Diagnostic{
						Path: "$." + field,
						Kind: DiagnosticConflict,
						Message: fmt.Sprintf("conflicting values for %s; using %s",
							strings.Join(present, ", "), winner),
					})
					break
				}
			}
		}
	}

	for _, key := range unsupportedKeys {
		if _, ok := raw[key]; ok {
			out = append(out, Diagnostic{
				Path:    "$." + key,
				Kind:    DiagnosticUnsupported,
				Message: "unsupported legacy setting; ignored",
			})
		}
	}
	return out
}

func expectedType(field string) string {
	switch field {
	case "maxTurns":
		return "an integer"
	case "yolo":
		return "a boolean"
	default:
		return "a string"
	}
}

func configValueTypeOK(field string, value any) bool {
	switch field {
	case "maxTurns":
		switch typed := value.(type) {
		case float64, int, int64, json.Number:
			return true
		case string:
			_, err := strconv.Atoi(strings.TrimSpace(typed))
			return err == nil
		}
		return false
	case "yolo":
		if _, ok := value.(bool); ok {
			return true
		}
		if text, ok := value.(string); ok {
			_, err := parseBool(text)
			return err == nil
		}
		return false
	default:
		_, ok := value.(string)
		return ok
	}
}
