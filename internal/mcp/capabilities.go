package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ServerCapabilities is the capability set a server advertises in its
// initialize response. A nil pointer means the capability was not declared.
// MCP encodes capability values as objects (for example {"listChanged":true}),
// so ParseCapabilities maps the presence of any non-null value to true; an
// explicit JSON false is preserved as false.
type ServerCapabilities struct {
	Tools     *bool
	Prompts   *bool
	Resources *bool

	// declared records whether the server sent a capabilities object at all.
	// It distinguishes "the server said nothing" (legacy servers: keep probing)
	// from "the server advertised capabilities but not this one" (skip the
	// request instead of earning an empty result and stdout noise).
	declared bool
}

// Declared reports whether the server returned a capabilities object. When it
// is false the server said nothing about its capabilities, so callers should
// keep probing rather than treating every missing flag as unsupported.
func (c ServerCapabilities) Declared() bool { return c.declared }

// Enumerated reports whether the server named at least one capability we
// understand. An empty capabilities object ("{}") is common among servers that
// still serve tools, so it is treated as "no usable information": callers keep
// probing. Once a server lists a capability, an omitted one is a meaningful
// signal that the corresponding list method is unsupported.
func (c ServerCapabilities) Enumerated() bool {
	return c.Tools != nil || c.Prompts != nil || c.Resources != nil
}

// Supports reports whether the named capability is explicitly declared as
// supported. Names are matched case-insensitively. A nil flag (not declared)
// and an explicit false both return false: undeclared means unsupported.
// Callers that must tolerate servers with no capabilities object should check
// Declared before relying on this.
func (c ServerCapabilities) Supports(cap string) bool {
	var flag *bool
	switch strings.ToLower(strings.TrimSpace(cap)) {
	case "tools":
		flag = c.Tools
	case "prompts":
		flag = c.Prompts
	case "resources":
		flag = c.Resources
	default:
		return false
	}
	return flag != nil && *flag
}

// ParseCapabilities decodes an initialize response's capabilities member.
// An absent, null, non-object or malformed payload yields an undeclared set
// (Declared()==false and every flag nil) so callers keep probing. Keys are
// matched case-insensitively; any non-null value other than false means the
// server supports that capability.
func ParseCapabilities(raw json.RawMessage) ServerCapabilities {
	var caps ServerCapabilities
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || isJSONNull(t) || t[0] != '{' || !json.Valid(t) {
		return caps
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(t, &fields); err != nil {
		return caps
	}
	caps.declared = true
	for key, value := range fields {
		flag := capabilityFlag(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "tools":
			caps.Tools = flag
		case "prompts":
			caps.Prompts = flag
		case "resources":
			caps.Resources = flag
		}
	}
	return caps
}

// capabilityFlag turns one capability value into a support bit: false stays
// false, null/absent stays nil, and every other value (including the objects
// MCP normally sends) means supported.
func capabilityFlag(raw json.RawMessage) *bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || isJSONNull(t) {
		return nil
	}
	var b bool
	if err := json.Unmarshal(t, &b); err == nil {
		return &b
	}
	supported := true
	return &supported
}
