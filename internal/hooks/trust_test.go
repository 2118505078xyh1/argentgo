package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// trustTempFiles 列出 SaveFile 在同一目录留下的临时文件，用于断言写入后无残留。
func trustTempFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".hooks-trust-*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	return matches
}

func TestTrustStoreSaveFileIsAtomicAndOwnerOnly(t *testing.T) {
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

	// 落盘内容与旧实现逐字节一致：只改落盘方式，不改 JSON 格式。
	const want = "{\n  \"version\": 1,\n  \"hooks\": [\n    {\n      \"path\": \"demo.json\",\n      \"digest\": \"abc123\"\n    }\n  ]\n}\n"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if string(data) != want {
		t.Fatalf("store bytes changed:\n got: %q\nwant: %q", data, want)
	}

	// 原子写成功后临时文件必须被清理，目录里不应有残留。
	if temps := trustTempFiles(t, filepath.Dir(path)); len(temps) != 0 {
		t.Fatalf("temporary files left behind: %v", temps)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("mode = %o, want 600", mode)
		}
	}
}

func TestTrustStoreSaveFileCleansTempOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	// 目标是一个目录：rename 必然失败，用于触发写入失败清理路径。
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	store := NewTrustStore()
	store.Trust("demo.json", "abc123")
	if err := store.SaveFile(path); err == nil {
		t.Fatal("save into a directory should fail")
	}
	if temps := trustTempFiles(t, dir); len(temps) != 0 {
		t.Fatalf("temporary files left behind after failure: %v", temps)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("target directory was damaged: info=%v err=%v", info, err)
	}
}

func TestTrustStoreConcurrentSaveKeepsStoreReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	store := NewTrustStore()

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("hook-%02d.json", i)
			store.Trust(name, Digest([]byte(name)))
			if err := store.SaveFile(path); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent save: %v", err)
	}

	reloaded := NewTrustStore()
	if err := reloaded.LoadFile(path); err != nil {
		t.Fatalf("load after concurrent saves: %v", err)
	}
	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("hook-%02d.json", i)
		if !reloaded.IsTrusted(name, Digest([]byte(name))) {
			t.Fatalf("grant %q lost after concurrent saves", name)
		}
	}
	if temps := trustTempFiles(t, dir); len(temps) != 0 {
		t.Fatalf("temporary files left behind: %v", temps)
	}
}

func TestTrustStoreLoadFileIgnoresPartialTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	store := NewTrustStore()
	store.Trust("demo.json", "abc123")
	if err := store.SaveFile(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 模拟上次写入在 rename 前崩溃：同目录留下半截 JSON 文件。
	// LoadFile 只读取目标路径，因此残留文件不能影响信任判定。
	partial := filepath.Join(dir, "crash-leftover.json")
	if err := os.WriteFile(partial, []byte(`{"version":1,"hooks":[`), 0o600); err != nil {
		t.Fatalf("write partial file: %v", err)
	}

	reloaded := NewTrustStore()
	if err := reloaded.LoadFile(path); err != nil {
		t.Fatalf("partial file must not break LoadFile: %v", err)
	}
	if !reloaded.IsTrusted("demo.json", "abc123") {
		t.Fatalf("old grant lost: %+v", reloaded.Entries())
	}

	// 后续正常保存仍成功，且只清理自己的临时文件。
	store.Trust("another.json", "def456")
	if err := store.SaveFile(path); err != nil {
		t.Fatalf("second save: %v", err)
	}
	final := NewTrustStore()
	if err := final.LoadFile(path); err != nil {
		t.Fatalf("final load: %v", err)
	}
	if !final.IsTrusted("another.json", "def456") {
		t.Fatalf("new grant lost: %+v", final.Entries())
	}
	if temps := trustTempFiles(t, dir); len(temps) != 0 {
		t.Fatalf("own temporary files left behind: %v", temps)
	}
}
