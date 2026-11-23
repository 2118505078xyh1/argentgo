package astcache

import (
	"go/parser"
	"go/token"
	"sync"
	"testing"
)

const sampleGo = "package p\nfunc F() int { return 1 }\n"

func TestParseCachesByIdentity(t *testing.T) {
	c := New(Options{})
	src := []byte(sampleGo)

	f1, fs1, err := c.Parse("a.go", "go", src, parser.ParseComments)
	if err != nil || f1 == nil {
		t.Fatalf("first parse: %v", err)
	}
	f2, fs2, err := c.Parse("b.go", "go", []byte(sampleGo), parser.ParseComments)
	if err != nil || f2 == nil {
		t.Fatalf("second parse: %v", err)
	}
	// 相同内容（即便 path 不同）应命中缓存，返回同一 AST 对象。
	if f1 != f2 {
		t.Fatal("same content did not hit cache: different *ast.File")
	}
	if fs1 != fs2 {
		t.Fatal("same content did not hit cache: different *token.FileSet")
	}
	if got := c.Stats().Entries; got != 1 {
		t.Fatalf("entries = %d, want 1", got)
	}
}

func TestParseDifferentContentDifferentEntry(t *testing.T) {
	c := New(Options{})
	c.Parse("a.go", "go", []byte(sampleGo), 0)
	c.Parse("b.go", "go", []byte("package p\nfunc G() {}"), 0)
	if got := c.Stats().Entries; got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
}

func TestParseModeInKey(t *testing.T) {
	c := New(Options{})
	src := []byte("package p\n// comment\nfunc F() {}")
	c.Parse("a.go", "go", src, 0)
	c.Parse("a.go", "go", src, parser.ParseComments)
	if got := c.Stats().Entries; got != 2 {
		t.Fatalf("different modes must be different entries, got %d", got)
	}
}

func TestByteForByteComparison(t *testing.T) {
	c := New(Options{})
	src := []byte(sampleGo)
	c.Parse("a.go", "go", src, 0)
	// 即使 sha256 碰撞（此处无法构造），逐字节比对保证不同内容不误命中。
	// 这里用相同长度不同内容模拟：必须新建条目。
	other := []byte(sampleGo)
	other[len("package p\nfunc F() int { return ")] = '2'
	c.Parse("b.go", "go", other, 0)
	if got := c.Stats().Entries; got != 2 {
		t.Fatalf("different bytes must not share entry, got %d", got)
	}
}

func TestLRUEviction(t *testing.T) {
	c := New(Options{MaxBytes: 1, MaxEntries: 2}) // 极小预算强制驱逐
	c.Parse("a.go", "go", []byte("package p\nvar A = 1"), 0)
	c.Parse("b.go", "go", []byte("package p\nvar B = 2"), 0)
	if got := c.Stats().Entries; got > 2 {
		t.Fatalf("entries exceed MaxEntries: %d", got)
	}
	c.Parse("c.go", "go", []byte("package p\nvar C = 3"), 0)
	if got := c.Stats().Entries; got > 2 {
		t.Fatalf("entries exceed MaxEntries after third insert: %d", got)
	}
}

func TestPreviousVersionRetained(t *testing.T) {
	c := New(Options{})
	c.Parse("a.go", "go", []byte(sampleGo), 0)
	// 同路径不同内容解析：新版本条目应通过 byPath 保留上一版本。
	_, _, _ = c.Parse("a.go", "go", []byte("package p\nfunc F() int { return 2 }"), 0)
	c.mu.Lock()
	defer c.mu.Unlock()
	mostRecent := c.byPath["a.go"]
	if mostRecent == nil {
		t.Fatal("no byPath entry")
	}
	if mostRecent.prev == nil {
		t.Fatal("previous version not retained on most recent entry")
	}
}

func TestTicketFencing(t *testing.T) {
	c := New(Options{})
	t1 := c.NextTicket("a.go")
	t2 := c.NextTicket("a.go")
	if t1 >= t2 {
		t.Fatalf("tickets must increase: %d -> %d", t1, t2)
	}
	if !c.IsCurrentTicket("a.go", t2) {
		t.Fatal("latest ticket must be current")
	}
	if c.IsCurrentTicket("a.go", t1) {
		t.Fatal("stale ticket must not be current")
	}
}

func TestConcurrentParse(t *testing.T) {
	c := New(Options{})
	src := []byte(sampleGo)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, _, err := c.Parse("a.go", "go", src, parser.ParseComments)
			if err != nil || f == nil {
				t.Errorf("concurrent parse failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := c.Stats().Entries; got != 1 {
		t.Fatalf("concurrent parses should share one entry, got %d", got)
	}
}

func TestParseDefaultUsesSharedCache(t *testing.T) {
	src := []byte("package p\nfunc H() {}")
	f1, _, _ := ParseDefault("x.go", "go", src, 0)
	f2, _, _ := ParseDefault("y.go", "go", []byte("package p\nfunc H() {}"), 0)
	if f1 != f2 {
		t.Fatal("default cache did not dedupe")
	}
}

func BenchmarkCacheHit(b *testing.B) {
	c := New(Options{})
	src := []byte(sampleGo)
	c.Parse("a.go", "go", src, parser.ParseComments)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = c.Parse("a.go", "go", src, parser.ParseComments)
	}
}

func BenchmarkParserParse(b *testing.B) {
	src := []byte(sampleGo)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parser.ParseFile(token.NewFileSet(), "", src, parser.ParseComments)
	}
}
