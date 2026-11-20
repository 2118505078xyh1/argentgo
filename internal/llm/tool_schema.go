package llm

import (
	"bytes"
	"encoding/json"
	"math/big"
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
// Wired up by the agent loop (internal/agent/loop.go), which projects each
// tool's parameter schema before sending tool definitions to the provider.
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

	result, err := encodeSchemaJSON(root)
	if err != nil {
		return schemaJSON
	}
	return result
}

// encodeSchemaJSON serializes a decoded schema deterministically: object keys
// are sorted and non-ASCII UTF-8 plus <, >, & stay literal. Both projections
// share it so a no-op projection can keep the caller's original bytes.
func encodeSchemaJSON(root any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(root); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
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

// safeIntMinBounds / safeIntMaxBounds are the sentinel bounds that effect (and
// other schema generators) attach to every integer schema. They carry no
// validation meaning for a tool call, but their presence changes the schema
// bytes and busts prompt caches. The first pair is JS
// Number.MIN/MAX_SAFE_INTEGER (used by opencode's flattenAllOf); the second is
// the floor/ceiling of MinInt64/MaxInt64 divided by three, emitted by other
// "safe integer" generators.
var (
	safeIntMinBounds = [...]int64{-9007199254740991, -3074457345618258603}
	safeIntMaxBounds = [...]int64{9007199254740991, 3074457345618258602}
)

// FlattenToolSchema is a pure, opt-in projection for providers that handle
// allOf poorly. It mirrors opencode's flattenAllOf:
//
//   - integer schemas lose their safe-integer sentinel minimum/maximum;
//   - an allOf group is merged into its parent only when no key is declared by
//     more than one member (parent keys other than "allOf" included), so
//     required/type/properties semantics can never be overwritten;
//   - the walk is recursive (objects, arrays, combinators) and the merge result
//     is walked again in case it produced a new flattenable allOf.
//
// Invalid JSON, non-UTF-8 input and schemas that need no change are returned
// byte-for-byte unchanged. The function never mutates its input and does not
// alter ProjectToolSchema's default behavior; callers must opt in explicitly.
func FlattenToolSchema(schemaJSON string) string {
	if !utf8.ValidString(schemaJSON) || !json.Valid([]byte(schemaJSON)) {
		return schemaJSON
	}

	decoder := json.NewDecoder(strings.NewReader(schemaJSON))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return schemaJSON
	}

	if !flattenAllOfNode(root) {
		return schemaJSON
	}

	result, err := encodeSchemaJSON(root)
	if err != nil {
		return schemaJSON
	}
	return result
}

// flattenAllOfNode walks a decoded JSON value in place and reports whether it
// changed anything. Maps and slices are reference types, so in-place mutation
// is enough; scalars are returned untouched.
func flattenAllOfNode(node any) bool {
	switch value := node.(type) {
	case map[string]any:
		return flattenAllOfObject(value)
	case []any:
		changed := false
		for _, element := range value {
			if flattenAllOfNode(element) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func flattenAllOfObject(obj map[string]any) bool {
	changed := false

	// Drop the safe-integer sentinel range from integer schemas before looking
	// at allOf, since the sentinel may itself live inside an allOf child.
	if kind, _ := obj["type"].(string); kind == "integer" {
		for _, bound := range safeIntMinBounds {
			if numberEqualsInt64(obj["minimum"], bound) {
				delete(obj, "minimum")
				changed = true
				break
			}
		}
		for _, bound := range safeIntMaxBounds {
			if numberEqualsInt64(obj["maximum"], bound) {
				delete(obj, "maximum")
				changed = true
				break
			}
		}
	}

	// Recurse first, mirroring opencode: children (including allOf members) are
	// flattened before the parent decides whether the group can be merged.
	for _, value := range obj {
		if flattenAllOfNode(value) {
			changed = true
		}
	}

	allOf, ok := obj["allOf"].([]any)
	if !ok || !allOfHasObjects(allOf) || !canFlattenAllOf(allOf, obj) {
		return changed
	}

	// Merge every member in order, then the parent's own keys. canFlattenAllOf
	// guarantees no key appears twice, so member order cannot overwrite anything
	// and no validation keyword (required, type, ...) is lost.
	merged := make(map[string]any, len(obj)+len(allOf))
	for _, item := range allOf {
		for key, value := range item.(map[string]any) {
			merged[key] = value
		}
	}
	for key, value := range obj {
		if key != "allOf" {
			merged[key] = value
		}
	}
	for key := range obj {
		delete(obj, key)
	}
	for key, value := range merged {
		obj[key] = value
	}

	// The merge can expose another flattenable allOf (e.g. an allOf key that
	// lived inside a member). The JSON tree is finite and each pass consumes one
	// allOf layer, so this always terminates.
	flattenAllOfNode(obj)
	return true
}

func allOfHasObjects(allOf []any) bool {
	for _, item := range allOf {
		if _, ok := item.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// canFlattenAllOf reports whether every key is declared by at most one member
// of allOf and does not collide with a parent key (other than "allOf").
func canFlattenAllOf(allOf []any, parent map[string]any) bool {
	seen := make(map[string]bool, len(parent))
	for key := range parent {
		if key != "allOf" {
			seen[key] = true
		}
	}
	for _, item := range allOf {
		for key := range item.(map[string]any) {
			if seen[key] {
				return false
			}
			seen[key] = true
		}
	}
	return true
}

// numberEqualsInt64 compares a decoded json.Number with an exact integer,
// accepting both plain decimal literals ("9007199254740991") and forms that
// Int64 cannot parse such as "9.007199254740991e15" or "...991.0".
func numberEqualsInt64(value any, want int64) bool {
	num, ok := value.(json.Number)
	if !ok {
		return false
	}
	if n, err := num.Int64(); err == nil {
		return n == want
	}
	got, ok := new(big.Rat).SetString(num.String())
	if !ok {
		return false
	}
	return got.Cmp(new(big.Rat).SetInt64(want)) == 0
}
