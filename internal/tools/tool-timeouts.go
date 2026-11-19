package tools

import (
	"context"
	"time"
)

// toolTimeouts holds the default, floor and ceiling of every tool with a
// running-time budget. Defaults are the values the tools used before this
// table, so omitting a timeout keeps the previous behaviour. "fetch" is the
// historical name of the web_fetch tool.
var toolTimeouts = map[string]struct{ Def, Min, Max time.Duration }{
	"bash":       {120 * time.Second, time.Second, 3600 * time.Second},
	"fetch":      {20 * time.Second, time.Second, 45 * time.Second},
	"web_search": {20 * time.Second, time.Second, 45 * time.Second},
	"git_status": {20 * time.Second, time.Second, 60 * time.Second},
}

// maxTimeoutKey carries the registry-wide timeout cap into tool calls.
type maxTimeoutKey struct{}

// maxTimeout returns the cap installed on the registry, or 0 when uncapped.
func maxTimeout(ctx context.Context) time.Duration {
	if d, ok := ctx.Value(maxTimeoutKey{}).(time.Duration); ok && d > 0 {
		return d
	}
	return 0
}

// clampTimeout resolves a tool's running-time budget. A non-positive raw value
// falls back to the tool's default; a positive globalMax then caps the resolved
// value (the default included), and finally the per-tool floor and ceiling
// apply. This order mirrors the upstream clampTimeout contract: the global cap
// governs calls that omit a timeout as well as explicit ones.
func clampTimeout(tool string, raw, globalMax time.Duration) time.Duration {
	limits, ok := toolTimeouts[tool]
	if ok && raw <= 0 {
		raw = limits.Def
	}
	if globalMax > 0 && raw > globalMax {
		raw = globalMax
	}
	if !ok {
		return raw
	}
	if raw < limits.Min {
		raw = limits.Min
	}
	if raw > limits.Max {
		raw = limits.Max
	}
	return raw
}
