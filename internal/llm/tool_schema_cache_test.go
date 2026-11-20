package llm

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestProjectToolSchemaCachedMatchesProject proves the cache layer is
// byte-for-byte transparent for every compat mode, including inputs that need
// no projection and malformed ones that are returned as supplied.
func TestProjectToolSchemaCachedMatchesProject(t *testing.T) {
	inputs := []string{
		`{"type":"object","properties":{"path":{"type":"string"}}}`,
		`{}`,
		`{"not":{"type":"null"}}`,
		`{"anyOf":[{"required":["command"]}]}`,
		`{"enum":["a","b"]}`,
		`{"enum":[1,2,3]}`,
		`{"properties":{"模式":{"enum":["快速","慢速"]}}}`,
		`{"type":"object","properties":{"status":{"enum":["pending","done"]}}}`,
		`{"type":"integer","allOf":[{"minimum":0}]}`,
		`{"type":"object",`,
		``,
		"{\"description\":\"" + string([]byte{0xff, 0xfe}) + "\"}",
	}
	modes := []ModelCompat{CompatNone, CompatGemini, CompatMoonshot, CompatAll}

	for _, compat := range modes {
		for _, input := range inputs {
			want := ProjectToolSchema(input, compat)
			// Exercise both the injectable cache and the exported wrapper so
			// the memo layer and its public entry point are both covered.
			cache := newSchemaProjectionCache(defaultSchemaCacheCapacity, nil)
			if got := cache.project(input, compat); got != want {
				t.Fatalf("cached projection differs for compat=%d input=%q\n got: %q\nwant: %q", compat, input, got, want)
			}
			if got := ProjectToolSchemaCached(input, compat); got != want {
				t.Fatalf("ProjectToolSchemaCached differs for compat=%d input=%q\n got: %q\nwant: %q", compat, input, got, want)
			}
		}
	}
}

// TestProjectToolSchemaCacheHitAvoidsRecompute verifies that a repeated key
// reuses the compiled result and that both dimensions of the key (schema and
// compat) are honored.
func TestProjectToolSchemaCacheHitAvoidsRecompute(t *testing.T) {
	var calls atomic.Int64
	cache := newSchemaProjectionCache(16, func(schema string, compat ModelCompat) string {
		calls.Add(1)
		return ProjectToolSchema(schema, compat)
	})

	a := `{}`
	b := `{"enum":["a","b"]}`

	firstA := cache.project(a, CompatGemini)
	if calls.Load() != 1 {
		t.Fatalf("first projection calls = %d, want 1", calls.Load())
	}
	if again := cache.project(a, CompatGemini); again != firstA {
		t.Fatalf("cache hit returned different bytes: %q vs %q", again, firstA)
	}
	if calls.Load() != 1 {
		t.Fatalf("same (schema, compat) recomputed: calls = %d, want 1", calls.Load())
	}

	// Same schema, different compat: distinct key, must recompute.
	if got := cache.project(a, CompatMoonshot); got != ProjectToolSchema(a, CompatMoonshot) {
		t.Fatalf("compat-specific result wrong: %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("different compat did not recompute: calls = %d, want 2", calls.Load())
	}

	// Different schema: distinct key, must recompute.
	if got := cache.project(b, CompatGemini); got != ProjectToolSchema(b, CompatGemini) {
		t.Fatalf("schema-specific result wrong: %q", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("different schema did not recompute: calls = %d, want 3", calls.Load())
	}
}

// TestProjectToolSchemaCacheBoundsGrowth ensures the table never grows past
// its capacity even when many distinct schemas stream through it.
func TestProjectToolSchemaCacheBoundsGrowth(t *testing.T) {
	const capacity = 8
	cache := newSchemaProjectionCache(capacity, ProjectToolSchema)

	for i := 0; i < capacity*6; i++ {
		schema := fmt.Sprintf(`{"title":"tool-%d","enum":["a","b"]}`, i)
		got := cache.project(schema, CompatMoonshot)
		if want := ProjectToolSchema(schema, CompatMoonshot); got != want {
			t.Fatalf("projection wrong for %q: got %q want %q", schema, got, want)
		}
		if len(cache.entries) > capacity {
			t.Fatalf("cache grew to %d entries, capacity is %d", len(cache.entries), capacity)
		}
	}
}

// TestProjectToolSchemaCacheConcurrent projects the same keys from many
// goroutines and checks every result is correct. Run with -race to catch any
// synchronization bug; the mutex also makes this safe under normal runs.
func TestProjectToolSchemaCacheConcurrent(t *testing.T) {
	cache := newSchemaProjectionCache(defaultSchemaCacheCapacity, ProjectToolSchema)
	keys := []struct {
		schema string
		compat ModelCompat
	}{
		{`{}`, CompatGemini},
		{`{"enum":["a","b"]}`, CompatMoonshot},
		{`{"not":{"type":"null"}}`, CompatAll},
		{`{"type":"object","properties":{"x":{"enum":[1,2]}}}`, CompatMoonshot},
	}

	const goroutines = 64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for _, key := range keys {
				want := ProjectToolSchema(key.schema, key.compat)
				if got := cache.project(key.schema, key.compat); got != want {
					t.Errorf("goroutine %d: got %q, want %q", n, got, want)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
