package proc

import (
	"fmt"
	"strings"
)

// secretKeyIndicators are case-insensitive substrings of a KEY=VALUE token's
// key whose value must never reach a log line. Separators in the key are
// normalised to '_' before matching, so API-KEY, --api-key and API_KEY are all
// recognised. Deliberately conservative: over-redacting a diagnostic line is
// cheap, while leaking a credential into a log is not.
var secretKeyIndicators = []string{
	"password", "passwd", "pwd", "secret", "token",
	"credential", "api_key", "access_key", "private_key", "session_key",
}

const redactedValue = "***"

// CommandSummary returns a compact, single-line, log-safe description of c:
// the process path and arguments (with credential-like values redacted), its
// timeout and its tree-kill mode. It is a pure diagnostic helper for logs and
// debug output; it never mutates c or spawns anything.
//
// The format is:
//
//	cmd="<path> <arg1> <arg2>..." timeout=<duration|none> killTree=<bool> env=<count>
//
// Each argument of the form KEY=VALUE or --KEY=VALUE is redacted to KEY=***
// when KEY looks credential-like; other tokens pass through unchanged. %q keeps
// CJK and other printable Unicode readable in the quoted command line. Env
// entries are only counted, never printed, so a secret in c.Env cannot leak
// through this summary either. A nil command summarises as "<nil>".
func CommandSummary(c *Command) string {
	if c == nil {
		return "<nil>"
	}

	parts := make([]string, 0, 1+len(c.Args))
	parts = append(parts, c.Path)
	for _, arg := range c.Args {
		parts = append(parts, redactToken(arg))
	}

	timeout := "none"
	if c.Timeout > 0 {
		timeout = c.Timeout.String()
	}
	return fmt.Sprintf("cmd=%q timeout=%s killTree=%t env=%d",
		strings.Join(parts, " "), timeout, c.KillTree, len(c.Env))
}

// redactToken redacts the value of a KEY=VALUE / --KEY=VALUE token when KEY
// looks credential-like. Tokens without a '=', with an empty value, or with an
// ordinary key are returned unchanged.
func redactToken(token string) string {
	key, value, ok := strings.Cut(token, "=")
	if !ok || value == "" {
		return token
	}
	if looksSecret(strings.TrimLeft(key, "-")) {
		return key + "=" + redactedValue
	}
	return token
}

// looksSecret reports whether key denotes a credential. Comparison is
// case-insensitive and treats '-' and '.' as '_', so --api-key, API-KEY and
// api.key all normalise to api_key and match the same indicator.
func looksSecret(key string) bool {
	normalised := strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(key))
	for _, indicator := range secretKeyIndicators {
		if strings.Contains(normalised, indicator) {
			return true
		}
	}
	return false
}
