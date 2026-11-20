package provider_test

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

func TestShouldRefreshPolicies(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ttl := time.Hour
	window := 5 * time.Minute

	uncached := provider.CatalogSnapshot{}
	freshAuthoritative := provider.CatalogSnapshot{
		Models:        []provider.Model{{ID: "m"}},
		Authoritative: true,
		FetchedAt:     now.Add(-30 * time.Minute),
	}

	cases := []struct {
		name     string
		policy   provider.RefreshPolicy
		snapshot provider.CatalogSnapshot
		want     bool
	}{
		{"offline skips an uncached catalog", provider.RefreshOffline, uncached, false},
		{"offline skips an expired catalog", provider.RefreshOffline, provider.CatalogSnapshot{FetchedAt: now.Add(-48 * time.Hour)}, false},
		{"online refreshes an uncached catalog", provider.RefreshOnline, uncached, true},
		{"online refreshes even a fresh authoritative cache", provider.RefreshOnline, freshAuthoritative, true},
		{"online-if-uncached refreshes when uncached", provider.RefreshOnlineIfUncached, uncached, true},
		{"online-if-uncached keeps a fresh authoritative cache", provider.RefreshOnlineIfUncached, freshAuthoritative, false},
		{"unknown policy falls back to online-if-uncached", provider.RefreshPolicy("mode"), freshAuthoritative, false},
		{"unknown policy refreshes an uncached catalog", provider.RefreshPolicy(""), uncached, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provider.ShouldRefresh(tc.snapshot, now, ttl, window, tc.policy); got != tc.want {
				t.Fatalf("ShouldRefresh(%q) = %v, want %v", tc.policy, got, tc.want)
			}
		})
	}
}

func TestShouldRefreshTTLExpiry(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ttl := 2 * time.Hour
	window := 5 * time.Minute

	for _, tc := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"just inside ttl stays fresh", ttl - time.Nanosecond, false},
		{"exactly at ttl stays fresh", ttl, false},
		{"just past ttl expires", ttl + time.Nanosecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := provider.CatalogSnapshot{
				Models:        []provider.Model{{ID: "m"}},
				Authoritative: true,
				FetchedAt:     now.Add(-tc.age),
			}
			if got := provider.ShouldRefresh(snapshot, now, ttl, window, provider.RefreshOnlineIfUncached); got != tc.want {
				t.Fatalf("age %v: ShouldRefresh = %v, want %v", tc.age, got, tc.want)
			}
		})
	}
}

func TestShouldRefreshNonAuthoritativeUsesShortRetryWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ttl := 2 * time.Hour
	window := 5 * time.Minute

	cases := []struct {
		name          string
		age           time.Duration
		authoritative bool
		want          bool
	}{
		{"non-authoritative inside retry window is kept", 4 * time.Minute, false, false},
		{"non-authoritative at retry window refreshes", 5 * time.Minute, false, true},
		{"non-authoritative well inside ttl still refreshes", 30 * time.Minute, false, true},
		{"authoritative at the same age is kept", 30 * time.Minute, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := provider.CatalogSnapshot{
				Models:        []provider.Model{{ID: "m"}},
				Authoritative: tc.authoritative,
				FetchedAt:     now.Add(-tc.age),
			}
			if got := provider.ShouldRefresh(snapshot, now, ttl, window, provider.RefreshOnlineIfUncached); got != tc.want {
				t.Fatalf("age %v authoritative=%v: ShouldRefresh = %v, want %v",
					tc.age, tc.authoritative, got, tc.want)
			}
		})
	}
}

func TestShouldRefreshStaleFlagForcesShortRetryWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ttl := 2 * time.Hour
	window := 5 * time.Minute

	snapshot := provider.CatalogSnapshot{
		Models:        []provider.Model{{ID: "m"}},
		Authoritative: true,
		Stale:         true,
		FetchedAt:     now.Add(-6 * time.Minute),
	}
	if !provider.ShouldRefresh(snapshot, now, ttl, window, provider.RefreshOnlineIfUncached) {
		t.Fatal("stale snapshot inside ttl must refresh on the short retry window")
	}
	snapshot.FetchedAt = now.Add(-time.Minute)
	if provider.ShouldRefresh(snapshot, now, ttl, window, provider.RefreshOnlineIfUncached) {
		t.Fatal("stale snapshot inside the retry window must be kept")
	}
}

func TestShouldRefreshClampsFutureAndZeroRetryWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ttl := 2 * time.Hour

	snapshot := provider.CatalogSnapshot{
		Authoritative: true,
		FetchedAt:     now.Add(time.Hour), // clock moved backwards
	}
	if provider.ShouldRefresh(snapshot, now, ttl, 5*time.Minute, provider.RefreshOnlineIfUncached) {
		t.Fatal("a future FetchedAt (negative age) must not expire a fresh snapshot")
	}

	snapshot.Authoritative = false
	if !provider.ShouldRefresh(snapshot, now, ttl, 0, provider.RefreshOnlineIfUncached) {
		t.Fatal("a zero retry window must refresh a non-authoritative snapshot immediately")
	}
}

func TestMergeOverlaysDiscoveredByIDAndDeduplicates(t *testing.T) {
	static := []provider.Model{
		{ID: "a", Name: "Static A"},
		{ID: "b", Name: "Static B"},
		{ID: "", Name: "ignored"},
	}
	discovered := []provider.Model{
		{ID: "b", Name: "Live B"},
		{ID: "c", Name: "Live C"},
		{ID: "c", Name: "Live C again"}, // last duplicate wins
	}

	got := provider.Merge(static, discovered)
	want := []provider.Model{
		{ID: "a", Name: "Static A"},
		{ID: "b", Name: "Live B"},
		{ID: "c", Name: "Live C again"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %#v, want %#v", got, want)
	}

	got[0].Name = "mutated"
	if static[0].Name != "Static A" {
		t.Fatal("Merge output aliased the static input")
	}
}

func TestMergeReturnsStaticUnchangedWhenDiscoveriesAreEmpty(t *testing.T) {
	static := []provider.Model{{ID: "a", Name: "A"}, {ID: "a", Name: "A2"}}
	got := provider.Merge(static, nil)
	want := []provider.Model{{ID: "a", Name: "A2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge with no discoveries = %#v, want %#v", got, want)
	}
	if len(provider.Merge(nil, nil)) != 0 {
		t.Fatal("merging two empty catalogs must return an empty catalog")
	}
}

func TestFingerprintStaticChangesWithCatalogContent(t *testing.T) {
	catalog := []provider.Model{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	baseline := provider.FingerprintStatic(catalog)

	if again := provider.FingerprintStatic([]provider.Model{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}); again != baseline {
		t.Fatal("fingerprint must be stable for identical static catalogs")
	}
	for name, changed := range map[string][]provider.Model{
		"reordered":  {{ID: "b", Name: "B"}, {ID: "a", Name: "A"}},
		"new model":  {{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}},
		"renamed":    {{ID: "a", Name: "A"}, {ID: "b", Name: "B2"}},
		"removed id": {{ID: "a", Name: "A"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := provider.FingerprintStatic(changed); got == baseline {
				t.Fatal("a changed static catalog must invalidate the old fingerprint")
			}
		})
	}
	if provider.FingerprintStatic(nil) == "" {
		t.Fatal("even an empty catalog must have a stable fingerprint")
	}
}

func TestParseRefreshPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want provider.RefreshPolicy
		ok   bool
	}{
		{"online", provider.RefreshOnline, true},
		{" OFFLINE ", provider.RefreshOffline, true},
		{"Online-If-Uncached", provider.RefreshOnlineIfUncached, true},
		{"", "", false},
		{"sometimes", "", false},
	}
	for _, tc := range cases {
		got, ok := provider.ParseRefreshPolicy(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("ParseRefreshPolicy(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPolicyFunctionsAreSafeForConcurrentUse(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	snapshot := provider.CatalogSnapshot{
		Models:        []provider.Model{{ID: "a", Name: "A"}},
		Authoritative: false,
		FetchedAt:     now.Add(-10 * time.Minute),
	}
	static := []provider.Model{{ID: "a", Name: "A"}}
	discovered := []provider.Model{{ID: "b", Name: "B"}}
	fingerprint := provider.FingerprintStatic(static)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if provider.ShouldRefresh(snapshot, now, 2*time.Hour, 5*time.Minute, provider.RefreshOnlineIfUncached) != true {
					errs <- "ShouldRefresh gave an inconsistent answer"
					return
				}
				if got := provider.Merge(static, discovered); len(got) != 2 || got[1].ID != "b" {
					errs <- "Merge gave an inconsistent result"
					return
				}
				if provider.FingerprintStatic(static) != fingerprint {
					errs <- "FingerprintStatic gave an inconsistent result"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
