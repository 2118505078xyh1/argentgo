// Package provider endpoint health probing.
//
// This file ports the endpoint-filtering half of opencode's vLLM provider
// health check without any of its I/O: no HTTP client, no timers, no side
// effects. A caller owns the actual probe (an HTTP GET to <baseURL>/health, a
// TCP dial, or anything else) and injects it as a function. ProbeHealthy
// applies that policy to a candidate list of base URLs and returns the subset
// that answered healthy. Keeping the policy pure makes it deterministic,
// order-preserving, and safe to call from any goroutine.
package provider

// ProbeHealthy filters baseURLs down to the endpoints that pass an injected
// probe.
//
// For each unique URL, in input order, probe(url) is called exactly once: a
// nil error marks the endpoint healthy and it is kept, any non-nil error drops
// it. Duplicate URLs are probed once and appear at most once in the result, at
// the position of their first occurrence. The returned slice is freshly
// allocated and never aliases baseURLs; a nil or empty input yields an empty
// (non-nil) slice.
//
// probe is always invoked sequentially on the calling goroutine, so it need
// not be concurrency-safe; it only has to be safe to call repeatedly. A nil
// probe is a no-op filter: every unique URL is treated as healthy, which is
// useful to callers that only want de-duplication.
func ProbeHealthy(baseURLs []string, probe func(url string) error) []string {
	seen := make(map[string]struct{}, len(baseURLs))
	healthy := make([]string, 0, len(baseURLs))
	for _, url := range baseURLs {
		if _, duplicate := seen[url]; duplicate {
			continue
		}
		seen[url] = struct{}{}
		if probe == nil || probe(url) == nil {
			healthy = append(healthy, url)
		}
	}
	return healthy
}
