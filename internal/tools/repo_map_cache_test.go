package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRepoSymbolCacheHitOnSameStamp(t *testing.T) {
	clearRepoSymbolCache()
	defer clearRepoSymbolCache()
	root := t.TempDir()
	writeSymFile(t, root, "alpha.go", "package alpha\n\ntype Client struct{}\n\nfunc New() {}\n")
	mtime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(filepath.Join(root, "alpha.go"), mtime, mtime); err != nil {
		t.Fatal(err)
	}

	first, hit := cachedRepoSymbols(root, "alpha.go")
	if hit {
		t.Fatal("first lookup unexpectedly hit the cache")
	}
	if len(first) != 2 {
		t.Fatalf("first lookup returned %d symbols, want 2: %+v", len(first), first)
	}

	second, hit := cachedRepoSymbols(root, "alpha.go")
	if !hit {
		t.Fatal("second lookup with identical mtime/size missed the cache")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached symbols differ:\n%+v\n%+v", first, second)
	}
	stats := repoSymbolCacheStats()
	if stats.entries != 1 || stats.misses != 1 || stats.hits != 1 {
		t.Fatalf("stats = %+v, want entries=1 misses=1 hits=1", stats)
	}

	// non-Go files are ignored and never enter the cache.
	if syms, hit := cachedRepoSymbols(root, "notes.txt"); hit || len(syms) != 0 {
		t.Fatalf("non-Go file = (%+v, %v), want (nil, false)", syms, hit)
	}
	if got := repoSymbolCacheStats().entries; got != 1 {
		t.Fatalf("entries after non-Go lookup = %d, want 1", got)
	}
}

func TestRepoSymbolCacheInvalidatesOnChange(t *testing.T) {
	clearRepoSymbolCache()
	defer clearRepoSymbolCache()
	root := t.TempDir()
	path := filepath.Join(root, "alpha.go")
	writeSymFile(t, root, "alpha.go", "package alpha\n\ntype Old struct{}\n")
	mtime1 := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, mtime1, mtime1); err != nil {
		t.Fatal(err)
	}
	first, hit := cachedRepoSymbols(root, "alpha.go")
	if hit || len(first) != 1 {
		t.Fatalf("first lookup = (%+v, %v), want 1 symbol and a miss", first, hit)
	}
	findRepoSym(t, first, "Old")

	// New content, different size, and a different mtime: the stamp changes.
	writeSymFile(t, root, "alpha.go", "package alpha\n\ntype BrandNewType struct{}\n\nfunc Fresh() {}\n")
	mtime2 := time.Unix(1_700_000_500, 0)
	if err := os.Chtimes(path, mtime2, mtime2); err != nil {
		t.Fatal(err)
	}
	second, hit := cachedRepoSymbols(root, "alpha.go")
	if hit {
		t.Fatal("changed file should miss the cache")
	}
	if len(second) != 2 {
		t.Fatalf("changed file yielded %d symbols, want 2: %+v", len(second), second)
	}
	findRepoSym(t, second, "BrandNewType")
	findRepoSym(t, second, "Fresh")

	stats := repoSymbolCacheStats()
	if stats.misses != 2 || stats.entries != 2 {
		t.Fatalf("stats = %+v, want misses=2 entries=2 (old stamp retained but unused)", stats)
	}
	// The new stamp is now cached and hits.
	if _, hit := cachedRepoSymbols(root, "alpha.go"); !hit {
		t.Fatal("new file stamp should now hit the cache")
	}
}

func TestRepoSymbolCacheConcurrentAccess(t *testing.T) {
	clearRepoSymbolCache()
	defer clearRepoSymbolCache()
	root := t.TempDir()
	writeSymFile(t, root, "alpha.go", "package alpha\n\ntype Shared struct{}\n\nfunc Work() {}\n")

	const n = 32
	type result struct {
		syms []repoSymbol
		hit  bool
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			syms, hit := cachedRepoSymbols(root, "alpha.go")
			results[i] = result{syms: syms, hit: hit}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if len(r.syms) != 2 {
			t.Fatalf("worker %d got %d symbols, want 2: %+v", i, len(r.syms), r.syms)
		}
		if !reflect.DeepEqual(results[0].syms, r.syms) {
			t.Fatalf("worker %d got different symbols than worker 0", i)
		}
	}
	findRepoSym(t, results[0].syms, "Shared")

	stats := repoSymbolCacheStats()
	if stats.entries != 1 {
		t.Fatalf("entries = %d, want 1", stats.entries)
	}
	if stats.misses != 1 {
		t.Fatalf("misses = %d, want exactly 1 (one parse under the mutex)", stats.misses)
	}
	if stats.hits != n-1 {
		t.Fatalf("hits = %d, want %d", stats.hits, n-1)
	}
}

func TestCachedRepoSymbolsForFilesMatchesExtract(t *testing.T) {
	clearRepoSymbolCache()
	defer clearRepoSymbolCache()
	root := t.TempDir()
	writeSymFile(t, root, "a.go", "package a\n\ntype Alpha struct{}\n")
	writeSymFile(t, root, "b.go", "package b\n\nfunc Beta() {}\n")
	writeSymFile(t, root, "note.txt", "not go\n")
	files := []string{"b.go", "note.txt", "a.go"}

	want := extractRepoSymbols(root, files)
	got := cachedRepoSymbolsForFiles(root, files)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("cached symbols differ from uncached:\n%+v\n%+v", want, got)
	}
	// A second pass served entirely from the cache is still identical.
	if got2 := cachedRepoSymbolsForFiles(root, files); !reflect.DeepEqual(want, got2) {
		t.Fatalf("cache hit pass differs:\n%+v\n%+v", want, got2)
	}
}

func TestFitBudgetSelectsLargestPrefixWithinTolerance(t *testing.T) {
	lines := []string{
		strings.Repeat("a", 24), //  6 tokens
		strings.Repeat("b", 20), //  5 tokens -> cumulative 11 (budget 10 + 15% = 11)
		strings.Repeat("c", 40), // 10 tokens -> cumulative 21 > 11
	}
	got := fitBudget(lines, 10)
	if len(got) != 2 || !reflect.DeepEqual(got, lines[:2]) {
		t.Fatalf("fitBudget kept %d lines, want the first 2: %q", len(got), got)
	}
	// The 15% tolerance is exactly what lets the 5-token second line through:
	// a strict 10-token limit would keep only the first 6-token line.
	limit := 10 + 10*fitBudgetTolerancePercent/100
	cost := 0
	for _, line := range got {
		cost += estimateRepoTokens(line)
	}
	if cost > limit {
		t.Fatalf("selected prefix costs %d tokens, over tolerated limit %d", cost, limit)
	}
	nextCost := cost + estimateRepoTokens(lines[len(got)])
	if nextCost <= limit {
		t.Fatalf("fitBudget stopped early: adding line %d costs %d <= %d", len(got), nextCost, limit)
	}
}

func TestFitBudgetMatchesLinearOracle(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = strings.Repeat("x", 4*(i%7+1)) // costs 1..7 tokens, cyclic
	}
	for _, budget := range []int{1, 6, 17, 40, 120} {
		limit := budget + budget*fitBudgetTolerancePercent/100
		want := 0
		sum := 0
		for _, line := range lines {
			next := sum + estimateRepoTokens(line)
			if next > limit {
				break
			}
			sum = next
			want++
		}
		got := fitBudget(lines, budget)
		if len(got) != want {
			t.Fatalf("budget %d: kept %d lines, want %d", budget, len(got), want)
		}
		if want > 0 && !reflect.DeepEqual(got, lines[:want]) {
			t.Fatalf("budget %d: result is not a prefix of lines", budget)
		}
	}
}

func TestFitBudgetCJK(t *testing.T) {
	lines := []string{
		"函数：处理用户请求数据",
		"类型：用户结构体定义",
		"方法：校验输入并返回结果",
		"常量：默认超时时间配置",
	}
	// Budget 30 calibrates to the CJK-aware estimate (each line costs ~10-12
	// tokens, not the old byte/4 value of ~8). It must cut a non-empty prefix.
	const budget = 30
	got := fitBudget(lines, budget)
	if len(got) == 0 || len(got) >= len(lines) {
		t.Fatalf("fitBudget kept %d of %d CJK lines, want a non-empty proper prefix", len(got), len(lines))
	}
	if !reflect.DeepEqual(got, lines[:len(got)]) {
		t.Fatal("CJK result is not a prefix of lines")
	}
	out := strings.Join(got, "\n")
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("CJK result is not valid UTF-8: %q", out)
	}
	limit := budget + budget*fitBudgetTolerancePercent/100
	cost := 0
	for _, line := range got {
		cost += estimateRepoTokens(line)
	}
	if cost > limit {
		t.Fatalf("CJK prefix costs %d tokens, over tolerated limit %d", cost, limit)
	}
	if all := fitBudget(lines, 100000); len(all) != len(lines) {
		t.Fatalf("generous budget kept %d of %d CJK lines", len(all), len(lines))
	}
}

func TestFitBudgetEmptyInput(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		budget int
	}{
		{"nil lines", nil, 100},
		{"empty lines", []string{}, 100},
		{"zero budget", []string{"a"}, 0},
		{"negative budget", []string{"a"}, -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fitBudget(tc.lines, tc.budget); len(got) != 0 {
				t.Fatalf("fitBudget = %q, want empty", got)
			}
		})
	}
}

func TestFitBudgetCJKNoLongerUnderestimates(t *testing.T) {
	// Same byte length, very different real cost: len/4 used to score both
	// lines 30 tokens, so a 30-token budget accepted the Chinese one too.
	ascii := strings.Repeat("a", 120) // 30 ASCII tokens
	cjk := strings.Repeat("中", 40)    // 120 bytes but 40 tokens

	if got := estimateRepoTokens(ascii); got != 30 {
		t.Fatalf("ASCII estimate = %d, want 30", got)
	}
	if got := estimateRepoTokens(cjk); got != 40 {
		t.Fatalf("CJK estimate = %d, want 40", got)
	}
	const budget = 30
	limit := budget + budget*fitBudgetTolerancePercent/100 // 34
	if got := fitBudget([]string{ascii}, budget); len(got) != 1 {
		t.Fatalf("ASCII line should fit, got %q", got)
	}
	if got := fitBudget([]string{cjk}, budget); len(got) != 0 {
		t.Fatalf("CJK line of %d tokens must not fit a %d-token budget (limit %d): %q",
			estimateRepoTokens(cjk), budget, limit, got)
	}
	// A generous budget still keeps the CJK line intact and UTF-8 safe.
	if got := fitBudget([]string{cjk}, 100); len(got) != 1 || got[0] != cjk {
		t.Fatalf("generous budget did not keep the CJK line: %q", got)
	}
}
