package tools

import (
	"bytes"
	"encoding/json"
)

// This file implements the "concise intent" (`i`) plumbing described by omp
// G1. The model is asked to include an `i` string as the first property of every
// tool call; the agent strips it before the tool sees its arguments and keeps
// it on the resulting event so a verbose or malformed call can be explained
// after the fact. These helpers are pure and deliberately kept out of schema
// generation: the registry (out of this file's ownership) decides when, if
// ever, to advertise the extra property.

const (
	intentKey = "i"
	// intentSchemaValue is the JSON Schema fragment advertised for `i`.
	intentSchemaValue = `{"type":"string","description":"concise intent for this call"}`
)

// orderedField is one key/value pair of a JSON object. encoding/json's maps
// lose key order, so the object is decoded into an ordered slice instead: the
// caller needs `i` to be the *first* property while every other field keeps
// its original position and raw encoding.
type orderedField struct {
	key   string
	value json.RawMessage
}

// decodeObject parses raw into an ordered list of top-level fields. It reports
// false for invalid JSON or any value that is not a JSON object.
func decodeObject(raw json.RawMessage) ([]orderedField, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var fields []orderedField
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		fields = append(fields, orderedField{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, false
	}
	return fields, true
}

// encodeObject rebuilds a JSON object from ordered fields, preserving both
// their order and the raw bytes of each value.
func encodeObject(fields []orderedField) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(f.key)
		b.Write(key)
		b.WriteByte(':')
		b.Write(f.value)
	}
	b.WriteByte('}')
	return json.RawMessage(b.Bytes())
}

// field returns the raw value of the first top-level key named key.
func field(fields []orderedField, key string) (json.RawMessage, bool) {
	for _, f := range fields {
		if f.key == key {
			return f.value, true
		}
	}
	return nil, false
}

// InjectIntentSchema adds an `i` property to a tool's parameters schema. The
// property is inserted first inside "properties", and `i` is appended to the
// top-level "required" array (created when absent). Every other field keeps
// its position and raw encoding.
//
// The function is idempotent: if `i` is already a property, params is returned
// unchanged. Non-objects and invalid JSON are also returned unchanged.
func InjectIntentSchema(params json.RawMessage) json.RawMessage {
	fields, ok := decodeObject(params)
	if !ok {
		return params
	}

	// Idempotence: a schema that already advertises `i` is left untouched.
	propsRaw, hasProps := field(fields, "properties")
	if hasProps {
		props, ok := decodeObject(propsRaw)
		if !ok {
			return params
		}
		if _, exists := field(props, intentKey); exists {
			return params
		}
	}

	// Build the new "properties" object with `i` first, keeping every existing
	// property after it in its original order.
	intentProp := orderedField{key: intentKey, value: json.RawMessage(intentSchemaValue)}
	var newProps []orderedField
	newProps = append(newProps, intentProp)
	if hasProps {
		if existing, ok := decodeObject(propsRaw); ok {
			newProps = append(newProps, existing...)
		}
	}
	propsValue := encodeObject(newProps)

	// Add `i` to "required", preserving the existing entries. A malformed
	// "required" is left alone rather than silently rewritten.
	requiredRaw, hasRequired := field(fields, "required")
	var requiredValue json.RawMessage
	if hasRequired {
		var names []string
		if err := json.Unmarshal(requiredRaw, &names); err != nil {
			return params
		}
		already := false
		for _, name := range names {
			if name == intentKey {
				already = true
				break
			}
		}
		if !already {
			names = append(names, intentKey)
		}
		requiredValue, _ = json.Marshal(names)
	} else {
		requiredValue = json.RawMessage(`["i"]`)
	}

	// Replace (or append) the two affected top-level fields in place.
	out := make([]orderedField, 0, len(fields)+2)
	propsWritten, requiredWritten := false, false
	for _, f := range fields {
		switch f.key {
		case "properties":
			out = append(out, orderedField{key: "properties", value: propsValue})
			propsWritten = true
		case "required":
			out = append(out, orderedField{key: "required", value: requiredValue})
			requiredWritten = true
		default:
			out = append(out, f)
		}
	}
	if !propsWritten {
		out = append(out, orderedField{key: "properties", value: propsValue})
	}
	if !requiredWritten {
		out = append(out, orderedField{key: "required", value: requiredValue})
	}
	return encodeObject(out)
}

// ExtractIntent removes the `i` key from a tool-call arguments object and
// returns its string value. The cleaned arguments are what the tool receives.
// When there is no `i`, or the input is not an object, args is returned
// byte-for-byte unchanged. Invalid JSON behaves the same way.
func ExtractIntent(args json.RawMessage) (intent string, cleaned json.RawMessage) {
	fields, ok := decodeObject(args)
	if !ok {
		return "", args
	}

	out := make([]orderedField, 0, len(fields))
	found := false
	for _, f := range fields {
		if f.key == intentKey {
			found = true
			// A non-string `i` carries no usable intent, but is still stripped
			// so the tool never sees the internal field.
			var s string
			if err := json.Unmarshal(f.value, &s); err == nil {
				intent = s
			}
			continue
		}
		out = append(out, f)
	}
	if !found {
		return "", args
	}
	return intent, encodeObject(out)
}
