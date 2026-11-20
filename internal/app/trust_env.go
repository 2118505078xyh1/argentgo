package app

import "strings"

// parseTrustEnv interprets an ARGENT_TRUST_PROJECT value as a boolean trust
// decision for project-level instructions, MCP servers and plugins.
//
// Recognised false spellings are 0, false, no and off; recognised true
// spellings are 1, true, yes and on. Matching is case-insensitive and ignores
// surrounding whitespace. An unset (empty) value defaults to true so existing
// projects keep their AGENTS.md/CLAUDE.md in the system prompt. Any other
// value fails closed (untrusted), matching the strict `== "1"` check Resolve
// historically used for Resolved.TrustProject.
//
// Both the prompt path (trustFromEnv) and Resolve's Resolved.TrustProject
// decision should call this helper, so one ARGENT_TRUST_PROJECT value cannot
// produce opposite trust conclusions on the two paths.
func parseTrustEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return false
	}
}
