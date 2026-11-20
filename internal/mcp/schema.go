package mcp

import (
	"bytes"
	"encoding/json"
)

// normalizeInputSchema cleans one tools/list inputSchema before it is exposed
// as Tool.Schema. Providers reject an entire tool-request batch when a single
// advertised parameters object is malformed, so the decoder repairs the three
// shapes real MCP servers get wrong:
//
//   - no "type": force "object" (JSON Schema parameters need an explicit type);
//   - "properties" that is not a JSON object: drop it;
//   - "required" that is not an array of strings: drop it.
//
// Every other member is preserved. A schema that already passes all three
// checks is returned byte-for-byte, so a well-behaved server sees no change
// at all in key order, whitespace or encoding.
func normalizeInputSchema(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' || !json.Valid(t) {
		// No object members worth preserving (empty, non-object, or broken).
		return json.RawMessage(`{"type":"object"}`)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(t, &fields); err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	changed := false

	if typ, ok := fields["type"]; !ok || isJSONNull(typ) {
		fields["type"] = json.RawMessage(`"object"`)
		changed = true
	}
	if props, ok := fields["properties"]; ok && !isJSONObject(props) {
		delete(fields, "properties")
		changed = true
	}
	if required, ok := fields["required"]; ok && !isStringArray(required) {
		delete(fields, "required")
		changed = true
	}
	if !changed {
		return raw // clean schema: keep the original encoding exactly
	}
	out, err := json.Marshal(fields)
	if err != nil {
		// RawMessage values are valid JSON, so this cannot happen; keep the
		// original rather than inventing a schema if it ever did.
		return raw
	}
	return out
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 4 && string(t) == "null"
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

func isStringArray(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '[' || !json.Valid(t) {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(t, &items); err != nil {
		return false
	}
	for _, item := range items {
		var text string
		if err := json.Unmarshal(item, &text); err != nil {
			return false
		}
	}
	return true
}
