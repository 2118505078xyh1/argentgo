package uri

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestResolveWithFallbackRegistered(t *testing.T) {
	h, err := NewDocsHandler(fstest.MapFS{"A.md": {Data: []byte("alpha")}})
	if err != nil {
		t.Fatal(err)
	}
	old := defaultRegistry
	defaultRegistry = New()
	defer func() { defaultRegistry = old }()
	if err := RegisterDefault("docs", h); err != nil {
		t.Fatal(err)
	}
	res, err := ResolveWithFallback("argent://docs/A.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "alpha" {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestResolveWithFallbackFilesystem(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "GUIDE.md"), []byte("# Guide\nfallback content"), 0o644)
	if _, err := ResolveWithFallback("argent://unknown/x", dir); !errors.Is(err, ErrUnknownScheme) {
		t.Fatalf("unknown scheme err = %v", err)
	}
	res, err := resolveDocsFSEmptyRegistry("argent://docs/GUIDE.md", dir)
	if err != nil || res.Content != "# Guide\nfallback content" {
		t.Fatalf("fs fallback = %+v %v", res, err)
	}
}

func TestResolveWithFallbackPathEscape(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveDocsFSEmptyRegistry("argent://docs/../secret.md", dir); err == nil {
		t.Fatal("path escape should be rejected")
	}
}

func TestResolveWithFallbackMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveDocsFSEmptyRegistry("argent://docs/NOPE.md", dir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestResolveWithFallbackNoRoot(t *testing.T) {
	// 无回退根目录时，未注册 scheme 仅返回 ErrUnknownScheme。
	old := defaultRegistry
	defaultRegistry = New()
	defer func() { defaultRegistry = old }()
	if _, err := ResolveWithFallback("argent://docs/A.md", ""); !errors.Is(err, ErrUnknownScheme) {
		t.Fatalf("err = %v", err)
	}
}

func resolveDocsFSEmptyRegistry(raw, root string) (Resource, error) {
	old := defaultRegistry
	defaultRegistry = New()
	defer func() { defaultRegistry = old }()
	return ResolveWithFallback(raw, root)
}

func TestStringsContainsHelper(t *testing.T) {
	if !strings.Contains("hello world", "world") {
		t.Fatal("stdlib contains")
	}
}
