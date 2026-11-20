package app

import (
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// cacheWarmerEnabled reports the explicit opt-in for prompt-cache keepalive.
// Default off: only ARGENT_CACHE_WARM=1 enables it. No production path wires a
// keepalive replay today, so enabling this only arms timestamp recording for a
// future caller; it never changes a request on the wire.
func cacheWarmerEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_CACHE_WARM")) == "1"
}

// newCacheWarmer builds a cache warmer. Callers pass the explicit opt-in
// decision; the default construction path uses cacheWarmerEnabled's default
// (false), leaving the warmer disabled and every request byte-identical.
func newCacheWarmer(enabled bool) *llm.CacheWarmer {
	return &llm.CacheWarmer{Enabled: enabled}
}

// cacheWarmerProbe groups the two recording hooks a long-lived engine would
// call at its boundaries: RecordRequest at the request-send point and
// RecordIdle at the turn boundary. It is a thin, inert adapter: it never sends
// a 1-token replay and never starts a timer, and it is a no-op while the warmer
// is disabled. It is NOT installed on any production path today (see the wiring
// report); it exists so a future caller can adopt both hooks in one place and
// test them without touching the network.
type cacheWarmerProbe struct {
	warmer *llm.CacheWarmer
}

func (p *cacheWarmerProbe) recordRequest(now time.Time) {
	if p != nil && p.warmer != nil {
		p.warmer.RecordRequest(now)
	}
}

func (p *cacheWarmerProbe) recordIdle(now time.Time) {
	if p != nil && p.warmer != nil {
		p.warmer.RecordIdle(now)
	}
}

// shouldWarm mirrors llm.CacheWarmer.ShouldWarm without ever acting on the
// answer. Installing the probe cannot itself emit a keepalive.
func (p *cacheWarmerProbe) shouldWarm(now time.Time) bool {
	if p == nil || p.warmer == nil {
		return false
	}
	return p.warmer.ShouldWarm(now)
}
