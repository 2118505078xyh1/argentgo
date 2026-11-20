package app

import (
	"encoding/json"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// usageSnapshot is the JSON payload the `usage` tool reports. It deliberately
// mirrors the fields agent.EventUsage carries (see internal/agent/events.go):
// the 1-based turn number, this request's usage delta and the run-wide total.
// Coin/cost provenance is persisted separately in session stats; EventUsage does
// not carry a cost, so this tool reports the token data that is actually on the
// event stream rather than inventing a number.
type usageSnapshot struct {
	Turn       int       `json:"turn"`
	Usage      llm.Usage `json:"usage"`
	TotalUsage llm.Usage `json:"total_usage"`
}

// usageSource is a thread-safe holder that satisfies tools.UsageSource.
//
// The agent's live usage lives in runState (internal/agent), which internal/tools
// cannot import. Following the historySource pattern, the app-side holder keeps
// the consumer interface in tools and adapts the run data here. The Execute loop
// pushes each EventUsage into the holder via toolset.UpdateUsage; the `usage`
// tool reads it back through UsageJSON. A zero-valued holder is valid and
// reports a zero snapshot until the first event arrives.
type usageSource struct {
	mu   sync.RWMutex
	snap usageSnapshot
}

var _ tools.UsageSource = (*usageSource)(nil)

// newUsageSource returns an empty holder. It is safe to use before any Update.
func newUsageSource() *usageSource { return &usageSource{} }

// Update records the latest usage event. turn is the event's 1-based turn,
// usage is the current request's delta and total is the accumulated run total.
func (u *usageSource) Update(turn int, usage, total llm.Usage) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.snap = usageSnapshot{Turn: turn, Usage: usage, TotalUsage: total}
}

// Snapshot returns a copy of the current snapshot for callers that need the
// structured values instead of JSON.
func (u *usageSource) Snapshot() usageSnapshot {
	if u == nil {
		return usageSnapshot{}
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.snap
}

// UsageJSON implements tools.UsageSource. It always returns valid JSON with a
// stable shape, even before the first Update (all zeros).
func (u *usageSource) UsageJSON() string {
	snap := u.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		// usageSnapshot is made only of ints and llm.Usage (also ints), so this
		// cannot happen; keep the tool's contract of always returning JSON.
		return `{}`
	}
	return string(data)
}

// wireUsage installs a usage source on a registry. The tool is rebuilt in place
// by Registry.SetUsageSource, so registration order and advertised specs stay
// stable — the same guarantee SetHistorySource relies on.
func wireUsage(registry *tools.Registry, src *usageSource) {
	if registry == nil || src == nil {
		return
	}
	registry.SetUsageSource(src)
}
