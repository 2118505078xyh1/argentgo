package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const trustedDocument = `{"hooks":{"PreToolUse":[{"matcher":"bash","hooks":[{"command":"echo checked"}]}]}}`

func writeHookDocument(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	return path
}

func TestDigestIsTheSHA256OfTheBytes(t *testing.T) {
	// SHA-256("abc")
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Digest([]byte("abc")); got != want {
		t.Fatalf("Digest = %q, want %q", got, want)
	}
	path := writeHookDocument(t, trustedDocument)
	fileDigest, err := DigestFile(path)
	if err != nil {
		t.Fatalf("digest file: %v", err)
	}
	if fileDigest != Digest([]byte(trustedDocument)) {
		t.Fatal("DigestFile disagrees with Digest")
	}
}

func TestLoadDocumentRequiresTrust(t *testing.T) {
	path := writeHookDocument(t, trustedDocument)
	store := NewTrustStore()

	_, err := LoadDocument(path, store)
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("untrusted document should be refused: %v", err)
	}

	digest, err := store.TrustFile(path)
	if err != nil {
		t.Fatalf("trust file: %v", err)
	}
	document, err := LoadDocument(path, store)
	if err != nil {
		t.Fatalf("trusted document should load: %v", err)
	}
	if document.Digest != digest || len(document.Config.Commands) != 1 {
		t.Fatalf("unexpected document: %+v", document)
	}

	// 编辑文档会使授权失效。
	if err := os.WriteFile(path, []byte(trustedDocument+" "), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := LoadDocument(path, store); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("tampered document should be refused: %v", err)
	}
}

func TestTrustStoreIgnoresSeparators(t *testing.T) {
	store := NewTrustStore()
	store.Trust(`C:\work\hooks.json`, "ABC123")
	if !store.IsTrusted("C:/work/hooks.json", "abc123") {
		t.Fatal("separator and case differences should match")
	}
	if store.IsTrusted("C:/work/hooks.json", "other") {
		t.Fatal("a different digest must not match")
	}
}

func TestTrustStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust", "store.json")

	store := NewTrustStore()
	store.Trust("demo.json", "abc123")
	if err := store.SaveFile(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded := NewTrustStore()
	if err := reloaded.LoadFile(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reloaded.IsTrusted("demo.json", "abc123") {
		t.Fatalf("grant lost: %+v", reloaded.Entries())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for _, want := range []string{`"version": 1`, `"path": "demo.json"`, `"digest": "abc123"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("store missing %q:\n%s", want, data)
		}
	}

	reloaded.RevokeAll()
	if len(reloaded.Entries()) != 0 {
		t.Fatal("revoke did not clear the store")
	}
}

func TestTrustStoreMissingFileIsEmpty(t *testing.T) {
	store := NewTrustStore()
	if err := store.LoadFile(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("a missing store is not an error: %v", err)
	}
	if len(store.Entries()) != 0 {
		t.Fatal("expected an empty store")
	}
}

func TestLoadDocumentWithoutStoreFails(t *testing.T) {
	path := writeHookDocument(t, trustedDocument)
	if _, err := LoadDocument(path, nil); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("a nil store trusts nothing: %v", err)
	}
}
