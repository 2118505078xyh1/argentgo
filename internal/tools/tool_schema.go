package tools

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ModelCompat selects which model-compatibility projections ProjectToolSchema
// applies. Each rule is an independent bit flag so callers may combine them;
// the zero value performs no projection at all.
type ModelCompat uint8

const (
	compatObjectRoot ModelCompat = 1 << iota
	compatEnumType
)

const (
	// CompatNone leaves the schema byte-for-byte unchanged.
	CompatNone ModelCompat = 0
	// CompatGemini normalizes the top-level schema so it declares an object
	// type: Gemini rejects tool schemas whose root has no object type (an
	// empty schema, a bare anyOf/oneOf, or a not:null guard).
	CompatGemini = compatObjectRoot
	// CompatMoonshot gives untyped enums an inferred primitive type: Kimi /
	// Moonshot reject enum schemas that omit "type".
	CompatMoonshot = compatEnumType
	// CompatAll applies every available projection.
	CompatAll = CompatGemini | CompatMoonshot
)

// ProjectToolSchema returns a tool parameter schema adjusted for the target
// model family. It is a pure, deterministic function:
//
//   - invalid JSON, non-UTF-8 input, and schemas that need no adjustment are
//     returned exactly as supplied (so existing schemas keep their bytes);
//   - nested object/array schemas are walked through properties, items,
//     additionalProperties, not and the anyOf/oneOf/allOf combinators;
//   - equal inputs always produce equal outputs (encoding/json sorts object
//     keys and numbers are kept exact via json.Number).
//
// No call site is wired up yet. This is the projection primitive for the LLM
// layer to apply when it builds a provider's tool definitions.
func ProjectToolSchema(schemaJSON string, compat ModelCompat) string {
	if compat == CompatNone || !utf8.ValidString(schemaJSON) || !json.Valid([]byte(schemaJSON)) {
		return schemaJSON
	}

	decoder := json.NewDecoder(strings.NewReader(schemaJSON))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return schemaJSON
	}

	if !projectSchemaNode(root, compat, true) {
		return schemaJSON
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false) // keep non-ASCII UTF-8 and <, >, & literal
	if err := encoder.Encode(root); err != nil {
		return schemaJSON
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// projectSchemaNode walks a decoded JSON value in place and reports whether it
// changed anything. Maps and slices are reference types, so mutating them is
// enough; scalars are returned untouched.
func projectSchemaNode(node any, compat ModelCompat, isRoot bool) bool {
	switch value := node.(type) {
	case map[string]any:
		return projectSchemaObject(value, compat, isRoot)
	case []any:
		changed := false
		for _, element := range value {
			if projectSchemaNode(element, compat, false) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func projectSchemaObject(obj map[string]any, compat ModelCompat, isRoot bool) bool {
	changed := false

	// Gemini: a tool schema must declare an object type at the root. Empty
	// schemas, bare anyOf/oneOf groups and not:null guards all lack one.
	if isRoot && compat&compatObjectRoot != 0 {
		if _, hasType := obj["type"]; !hasType {
			obj["type"] = "object"
			changed = true
		}
	}

	// Kimi / Moonshot: infer a primitive type for an enum that omits "type".
	if compat&compatEnumType != 0 {
		if _, hasType := obj["type"]; !hasType {
			if enum, hasEnum := obj["enum"]; hasEnum {
				if inferred, ok := inferEnumType(enum); ok {
					obj["type"] = inferred
					changed = true
				}
			}
		}
	}

	if properties, ok := obj["properties"].(map[string]any); ok {
		for _, property := range properties {
			if projectSchemaNode(property, compat, false) {
				changed = true
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not"} {
		if child, ok := obj[key]; ok {
			if projectSchemaNode(child, compat, false) {
				changed = true
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if children, ok := obj[key].([]any); ok {
			if projectSchemaNode(children, compat, false) {
				changed = true
			}
		}
	}

	return changed
}

// inferEnumType returns the single primitive type shared by every enum value.
// A mix of unrelated types (or null/compound values) is not determinable and
// is left for the caller to skip. Integer and number are treated as compatible
// and widened to number.
func inferEnumType(enum any) (string, bool) {
	values, ok := enum.([]any)
	if !ok || len(values) == 0 {
		return "", false
	}

	inferred := ""
	for _, value := range values {
		var kind string
		switch typed := value.(type) {
		case string:
			kind = "string"
		case json.Number:
			kind = "number"
			if _, err := typed.Int64(); err == nil {
				kind = "integer"
			}
		case bool:
			kind = "boolean"
		default:
			return "", false
		}

		switch {
		case inferred == "":
			inferred = kind
		case inferred == kind:
			// values agree
		case (inferred == "integer" && kind == "number") || (inferred == "number" && kind == "integer"):
			inferred = "number"
		default:
			return "", false
		}
	}
	if inferred == "" {
		return "", false
	}
	return inferred, true
}
