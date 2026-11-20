// Package provider model-catalog refresh strategy.
//
// This file ports the policy half of omp's model-manager.ts without any of its
// I/O: no HTTP discovery, no cache database, no clock/side effects. A caller
// owns the actual fetch and persistence; it calls ShouldRefresh to decide
// whether to fetch, Merge to combine a fetched roster with the static table,
// and FingerprintStatic to invalidate caches written against an older static
// catalog. Keeping the policy pure makes it deterministic and safe to call from
// any goroutine.
package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Default catalog freshness knobs, matching omp's DEFAULT_CACHE_TTL_MS (2h)
// and NON_AUTHORITATIVE_RETRY_MS (5min). Callers may pass their own values.
const (
	DefaultCatalogTTL       = 2 * time.Hour
	DefaultStaleRetryWindow = 5 * time.Minute
)

// RefreshPolicy controls when dynamic model discovery is allowed to run.
// It mirrors omp's ModelRefreshStrategy.
type RefreshPolicy string

// Supported refresh strategies.
const (
	// RefreshOnline always refreshes from the dynamic source, even when a
	// fresh authoritative snapshot exists.
	RefreshOnline RefreshPolicy = "online"
	// RefreshOffline never refreshes; only static data and an existing cache
	// are used.
	RefreshOffline RefreshPolicy = "offline"
	// RefreshOnlineIfUncached refreshes when there is no snapshot, when the
	// snapshot exceeded its TTL, or when a non-authoritative snapshot aged past
	// the short retry window. This is the default strategy.
	RefreshOnlineIfUncached RefreshPolicy = "online-if-uncached"
)

// Model is the minimal catalog row the policy layer needs: a stable identity
// for merging/de-duplicating and a display name so a discovered row can carry
// a new name over a static one. The full wire model lives in internal/llm;
// callers map between the two at the integration boundary.
type Model struct {
	ID   string
	Name string
}

// CatalogSnapshot is a point-in-time view of a provider's model catalog.
// Authoritative reports that the snapshot came from a complete discovery
// result (not a fallback after a failed fetch); Stale marks a snapshot that
// must not enjoy the full TTL. FetchedAt is zero when no snapshot has ever
// been stored.
type CatalogSnapshot struct {
	Models        []Model
	Authoritative bool
	Stale         bool
	FetchedAt     time.Time
}

// ParseRefreshPolicy normalizes a config/CLI string into a RefreshPolicy.
// The boolean reports whether the value was recognized.
func ParseRefreshPolicy(value string) (RefreshPolicy, bool) {
	policy := RefreshPolicy(strings.ToLower(strings.TrimSpace(value)))
	switch policy {
	case RefreshOnline, RefreshOffline, RefreshOnlineIfUncached:
		return policy, true
	default:
		return "", false
	}
}

// ShouldRefresh decides whether dynamic discovery should run now.
//
// Policy semantics:
//   - offline: never.
//   - online: always.
//   - online-if-uncached (and any unrecognized policy, treated as the
//     conservative default): refresh when there is no snapshot, when its age
//     exceeds ttl, or when it is non-authoritative and its age reached
//     staleRetryWindow.
//
// A snapshot counts as authoritative only when Authoritative is true and Stale
// is false, so a stale snapshot is refreshed on the short retry window rather
// than waiting out the full TTL. A zero FetchedAt means "uncached". Negative
// ages (a clock moved backwards) are clamped to zero. Passing a non-positive
// staleRetryWindow makes non-authoritative snapshots refresh immediately.
func ShouldRefresh(snapshot CatalogSnapshot, now time.Time, ttl, staleRetryWindow time.Duration, policy RefreshPolicy) bool {
	if policy == RefreshOffline {
		return false
	}
	if policy == RefreshOnline {
		return true
	}
	if snapshot.FetchedAt.IsZero() {
		return true
	}

	age := now.Sub(snapshot.FetchedAt)
	if age < 0 {
		age = 0
	}
	if age > ttl {
		return true
	}
	if snapshot.Stale || !snapshot.Authoritative {
		return age >= staleRetryWindow
	}
	return false
}

// Merge overlays discovered models on the static catalog by ID. Static rows
// keep their order; a discovered row with a known ID replaces that row in
// place, and discovered-only IDs are appended. Empty IDs are ignored, and the
// last duplicate of an ID wins (matching the map-based merge in omp). The
// returned slice is fresh and never aliases either input.
func Merge(static, discovered []Model) []Model {
	order := make([]string, 0, len(static)+len(discovered))
	byID := make(map[string]Model, len(static)+len(discovered))
	put := func(models []Model) {
		for _, model := range models {
			if model.ID == "" {
				continue
			}
			if _, exists := byID[model.ID]; !exists {
				order = append(order, model.ID)
			}
			byID[model.ID] = model
		}
	}
	put(static)
	put(discovered)

	out := make([]Model, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// FingerprintStatic returns a stable content identity for a static catalog.
// A cached snapshot written against one fingerprint must be discarded when
// the current fingerprint differs (a static-table upgrade or policy change).
// Length-prefixing each field avoids ambiguity between model ids and names.
func FingerprintStatic(models []Model) string {
	var b strings.Builder
	for _, model := range models {
		b.WriteString(strconv.Itoa(len(model.ID)))
		b.WriteByte(':')
		b.WriteString(model.ID)
		b.WriteString(strconv.Itoa(len(model.Name)))
		b.WriteByte(':')
		b.WriteString(model.Name)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "catalog-v1:" + hex.EncodeToString(sum[:])
}
