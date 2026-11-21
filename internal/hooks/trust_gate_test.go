package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNeedsTrustGateAllPathsRequireDigestTrust(t *testing.T) {
	base := t.TempDir()
	global := t.TempDir()

	cases := []struct {
		name string
		path string
	}{
		{"项目根", filepath.Join(base, "hooks.json")},
		{"项目子目录", filepath.Join(base, ".claude", "hooks.json")},
		{"用户全局路径", filepath.Join(global, "hooks.json")},
		{"用户全局子目录", filepath.Join(global, "子目录", "钩子.json")},
		{"项目外路径", filepath.Join(base, "..", "outside", "hooks.json")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(tc.path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(tc.path, []byte(trustedDocument), 0o644); err != nil {
				t.Fatalf("write document: %v", err)
			}
			digest, err := DigestFile(tc.path)
			if err != nil {
				t.Fatalf("digest: %v", err)
			}

			store := NewTrustStore()
			if needsTrustGate(tc.path, digest, store) {
				t.Fatal("未授信的文档不应通过门控")
			}
			if _, err := LoadDocument(tc.path, store); !errors.Is(err, ErrUntrusted) {
				t.Fatalf("未授信文档应被拒绝: %v", err)
			}

			store.Trust(tc.path, digest)
			if !needsTrustGate(tc.path, digest, store) {
				t.Fatal("已授信的文档应通过门控")
			}
			if _, err := LoadDocument(tc.path, store); err != nil {
				t.Fatalf("已授信文档应加载成功: %v", err)
			}
		})
	}
}

func TestGlobalHookRequiresTrustAndTamperingRevokesIt(t *testing.T) {
	// 用独立的临时目录模拟用户全局配置路径；它不享有任何摘要门豁免。
	global := filepath.Join(t.TempDir(), "user", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(global, []byte(trustedDocument), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}

	store := NewTrustStore()
	if _, err := LoadDocument(global, store); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("全局钩子未授信时应被拒绝: %v", err)
	}
	if _, err := store.TrustFile(global); err != nil {
		t.Fatalf("trust file: %v", err)
	}
	if _, err := LoadDocument(global, store); err != nil {
		t.Fatalf("全局钩子授信后应加载成功: %v", err)
	}

	// 篡改内容会改变摘要，全局路径同样失效。
	if err := os.WriteFile(global, []byte(trustedDocument+" "), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := LoadDocument(global, store); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("篡改后的全局钩子应被拒绝: %v", err)
	}
}

func TestNeedsTrustGateNilStoreTrustsNothing(t *testing.T) {
	path := writeHookDocument(t, trustedDocument)
	digest, err := DigestFile(path)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if needsTrustGate(path, digest, nil) {
		t.Fatal("nil 信任存储不应授信任何文档")
	}
}

func TestTrustStoreCaseVariantsGrantOnce(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("仅 Windows 文件系统不区分大小写")
	}

	store := NewTrustStore()
	trustedPath := filepath.Join(t.TempDir(), "MyHooks", "HOOKS.json")
	const digest = "ABC123"

	store.Trust(trustedPath, digest)
	variants := []string{
		strings.ToLower(trustedPath),
		strings.ToUpper(trustedPath),
		strings.ReplaceAll(strings.ToUpper(trustedPath), `\`, "/"),
	}
	for _, variant := range variants {
		if !store.IsTrusted(variant, digest) {
			t.Errorf("同一文件的大小写/分隔符变体应命中同一授权: %q", variant)
		}
		if store.IsTrusted(variant, "other") {
			t.Errorf("不同摘要不应命中授权: %q", variant)
		}
	}
	if got := len(store.Entries()); got != 1 {
		t.Fatalf("不同大小写路径应只占一个信任键, got %d entries: %+v", got, store.Entries())
	}
}

func TestLoadDocumentCaseVariantNeedsSingleTrust(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("仅 Windows 文件系统不区分大小写")
	}

	path := filepath.Join(t.TempDir(), "MyHooks", "HOOKS.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(trustedDocument), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}

	store := NewTrustStore()
	// 用一种大小写授信，再用另一种大小写加载：应命中同一条授权。
	store.Trust(strings.ToUpper(path), Digest([]byte(trustedDocument)))
	if _, err := LoadDocument(strings.ToLower(path), store); err != nil {
		t.Fatalf("大小写变体应复用已有授信: %v", err)
	}
}

func TestNormalizePathFoldsCaseOnlyOnWindows(t *testing.T) {
	const mixed = `C:\Work\HOOKS.json`
	want := "C:/Work/HOOKS.json"
	if filepath.Separator == '\\' {
		want = "c:/work/hooks.json"
	}
	if got := normalizePath(mixed); got != want {
		t.Fatalf("normalizePath(%q) = %q, want %q", mixed, got, want)
	}

	// 非 ASCII 路径不受大小写折叠影响。
	const cjk = `配置\钩子.json`
	if got, want := normalizePath(cjk), "配置/钩子.json"; got != want {
		t.Fatalf("normalizePath(%q) = %q, want %q", cjk, got, want)
	}
}
