package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Limits mirror goose's recipe parameter caps. They keep a workflow small
// enough to share and stop a malformed declaration from exhausting memory at
// validation time.
const (
	maxWorkflowParams        = 32
	maxTotalSelectOptions    = 200
	maxSerializedParamsBytes = 128 << 10 // 128 KiB
	maxParamValueBytes       = 128 << 10 // 128 KiB
)

// nextPlaceholder returns the first {{...}} token in text. start is the index
// of "{{", tokenEnd is the index just past "}}", and name is the trimmed inner
// text. ok is false when text has no complete token. This is the single source
// of truth for placeholder syntax shared by ExtractPlaceholders (validation)
// and Substitute (rendering).
func nextPlaceholder(text string) (start, tokenEnd int, name string, ok bool) {
	start = strings.Index(text, "{{")
	if start < 0 {
		return 0, 0, "", false
	}
	rel := strings.Index(text[start:], "}}")
	if rel < 0 {
		return 0, 0, "", false
	}
	closeAt := start + rel
	return start, closeAt + 2, strings.TrimSpace(text[start+2 : closeAt]), true
}

// ExtractPlaceholders returns the deduplicated, sorted names inside every
// {{...}} token in s. Unterminated and empty tokens are ignored.
func ExtractPlaceholders(s string) []string {
	seen := make(map[string]bool)
	var names []string
	rest := s
	for {
		_, tokenEnd, name, ok := nextPlaceholder(rest)
		if !ok {
			break
		}
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		rest = rest[tokenEnd:]
	}
	sort.Strings(names)
	return names
}

// isBuiltinPlaceholder reports whether name is one of the runner's reserved
// placeholders rather than a declared parameter.
func isBuiltinPlaceholder(name string) bool {
	return name == "input" || strings.HasPrefix(name, "steps.")
}

// ValidateParameters checks parameter declarations and, when a workflow
// declares parameters, enforces bidirectional agreement between declarations
// and the placeholders used in prompts and tool args.
//
// Every error names a field/key and a rule; declared values (defaults and
// options) are never included, so a workflow can be shared and its validation
// errors pasted into an issue without leaking a secret.
func ValidateParameters(def Definition) error {
	if len(def.Parameters) == 0 {
		// Legacy workflows have no parameters: their templates use only the
		// built-in {{input}} / {{steps...}} placeholders (and may intentionally
		// leave other tokens for the model), so no new checks apply.
		return nil
	}
	if len(def.Parameters) > maxWorkflowParams {
		return fmt.Errorf("workflow: too many parameters (%d, max %d)", len(def.Parameters), maxWorkflowParams)
	}
	// Only the serialized size matters for the cap; never echo the content.
	encoded, err := json.Marshal(def.Parameters)
	if err != nil {
		return fmt.Errorf("workflow: parameters[%d].default cannot be serialized", len(def.Parameters)-1)
	}
	if len(encoded) > maxSerializedParamsBytes {
		return fmt.Errorf("workflow: parameters exceed %d bytes when serialized", maxSerializedParamsBytes)
	}

	declared := make(map[string]bool, len(def.Parameters))
	optionCount := 0
	for i, p := range def.Parameters {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			return fmt.Errorf("workflow: parameters[%d].key is required", i)
		}
		if declared[key] {
			return fmt.Errorf("workflow: parameters[%d].key %q is duplicated", i, key)
		}
		declared[key] = true
		if isBuiltinPlaceholder(key) {
			return fmt.Errorf("workflow: parameters[%d].key %q is reserved", i, key)
		}
		switch p.InputType {
		case ParamTypeString, ParamTypeNumber, ParamTypeBoolean, ParamTypeDate, ParamTypeFile, ParamTypeSelect:
		default:
			return fmt.Errorf("workflow: parameters[%d].input_type is invalid (use %s, %s, %s, %s, %s or %s)",
				i, ParamTypeString, ParamTypeNumber, ParamTypeBoolean, ParamTypeDate, ParamTypeFile, ParamTypeSelect)
		}
		switch p.Requirement {
		case ParamRequired, ParamOptional, ParamUserPrompt:
		default:
			return fmt.Errorf("workflow: parameters[%d].requirement is invalid (use %s, %s or %s)",
				i, ParamRequired, ParamOptional, ParamUserPrompt)
		}
		hasDefault := p.Default != nil
		switch {
		case p.InputType == ParamTypeSelect && len(p.Options) == 0:
			return fmt.Errorf("workflow: parameters[%d] (%s) needs options for a select input", i, key)
		case p.InputType == ParamTypeFile && hasDefault:
			return fmt.Errorf("workflow: parameters[%d] (%s) file input cannot have a default", i, key)
		case p.Requirement == ParamRequired && hasDefault:
			return fmt.Errorf("workflow: parameters[%d] (%s) required input cannot have a default", i, key)
		}
		if p.InputType == ParamTypeSelect {
			optionCount += len(p.Options)
		}
	}
	if optionCount > maxTotalSelectOptions {
		return fmt.Errorf("workflow: select options exceed %d in total", maxTotalSelectOptions)
	}

	// Every non-builtin placeholder in a prompt or args must be declared.
	used := make(map[string]bool, len(def.Parameters))
	for _, step := range def.Steps {
		for _, field := range []string{step.Prompt, step.Args} {
			for _, name := range ExtractPlaceholders(field) {
				if isBuiltinPlaceholder(name) {
					continue
				}
				used[name] = true
				if !declared[name] {
					return fmt.Errorf("workflow: placeholder %q has no parameter declaration", name)
				}
			}
		}
	}
	// And every declaration must actually be referenced.
	for _, p := range def.Parameters {
		key := strings.TrimSpace(p.Key)
		if !used[key] {
			return fmt.Errorf("workflow: parameter %q is declared but never used in a prompt or args", key)
		}
	}
	return nil
}

// ResolveParameters fills a value for every parameter from provided input or
// its declaration default. Values are coerced to strings and size-capped.
// Errors are secret-safe: they name the parameter and the rule, never a value.
func ResolveParameters(def Definition, provided map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(def.Parameters))
	for _, p := range def.Parameters {
		key := strings.TrimSpace(p.Key)
		if value, ok := provided[key]; ok && value != "" {
			if len(value) > maxParamValueBytes {
				return nil, fmt.Errorf("workflow: parameter %q value exceeds %d bytes", key, maxParamValueBytes)
			}
			resolved[key] = value
			continue
		}
		if value, ok := stringifyDefault(p.Default); ok {
			if len(value) > maxParamValueBytes {
				return nil, fmt.Errorf("workflow: parameter %q value exceeds %d bytes", key, maxParamValueBytes)
			}
			resolved[key] = value
			continue
		}
		if p.Requirement == ParamRequired {
			return nil, fmt.Errorf("workflow: parameter %q is required but no value was provided", key)
		}
		// Optional and user_prompt parameters with no value are simply absent.
	}
	return resolved, nil
}

// stringifyDefault coerces a declaration default to its template string. A
// nil default means "no value".
func stringifyDefault(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case json.Number:
		return x.String(), true
	default:
		return fmt.Sprint(x), true
	}
}
