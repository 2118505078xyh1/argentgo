package config

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRawFile writes exact bytes (no JSON re-marshalling) so tests can control
// the file size down to the byte.
func writeRawFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setMTime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func stampFor(t *testing.T, fp configFingerprint, path string) fileStamp {
	t.Helper()
	for _, stamp := range fp.files {
		if stamp.path == path {
			return stamp
		}
	}
	t.Fatalf("no fingerprint stamp for %q", path)
	return fileStamp{}
}

// TestChangedDetectsSameSizeSameMtimeRewrite is the regression test for the
// codex P-high audit finding: a rewrite that preserves both size and mtime was
// invisible to the old stat-only fingerprint. The content hash must close that
// gap end-to-end through Load/Changed/Reload.
func TestChangedDetectsSameSizeSameMtimeRewrite(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	before := []byte(`{"model":"aaaa"}`)
	after := []byte(`{"model":"bbbb"}`)
	if len(before) != len(after) {
		t.Fatalf("test setup: payload sizes differ (%d vs %d)", len(before), len(after))
	}
	writeRawFile(t, path, before)

	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	setMTime(t, path, fixed)

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "aaaa" {
		t.Fatalf("model = %q, want aaaa", cfg.Model)
	}
	if cfg.Changed() {
		t.Fatal("Changed() true immediately after load")
	}
	first := statFile(t, path)
	firstStamp := stampFor(t, cfg.fingerprint, path)
	if firstStamp.hash == ([sha256.Size]byte{}) {
		t.Fatal("existing readable file has a zero hash")
	}

	// In-place rewrite with identical bytes length, then restore the exact
	// mtime so mtime and size alone cannot reveal the edit.
	writeRawFile(t, path, after)
	setMTime(t, path, fixed)

	second := statFile(t, path)
	if second.Size() != first.Size() || !second.ModTime().Equal(first.ModTime()) {
		t.Fatalf("test did not preserve stat identity: before(size=%d,mtime=%v) after(size=%d,mtime=%v)",
			first.Size(), first.ModTime(), second.Size(), second.ModTime())
	}
	if !cfg.Changed() {
		t.Fatal("Changed() false after a same-size, same-mtime rewrite (content hash not working)")
	}

	next, err := cfg.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if next.Model != "bbbb" {
		t.Errorf("reloaded model = %q, want bbbb", next.Model)
	}
	if next.Changed() {
		t.Error("Changed() true on freshly reloaded config")
	}
}

// TestFingerprintHashChangesOnOrdinaryEdit guards the normal-change path: a
// regular edit (different size) must still produce a different fingerprint.
func TestFingerprintHashChangesOnOrdinaryEdit(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeRawFile(t, path, []byte(`{"model":"short"}`))
	before := stampFor(t, newFingerprint(cwd, t.TempDir()), path)

	writeRawFile(t, path, []byte(`{"model":"a-considerably-longer-model"}`))
	after := stampFor(t, newFingerprint(cwd, t.TempDir()), path)

	if before.size == after.size {
		t.Fatal("test setup: file size did not change")
	}
	if before.hash == after.hash {
		t.Fatal("hash unchanged after an ordinary edit")
	}
}

// TestChangedNoFileFallsBackToStatIdentity verifies the no-file case: missing
// candidates carry a zero hash and rely on existence, and creating a file is
// still detected (now backed by a real hash too).
func TestChangedNoFileFallsBackToStatIdentity(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Changed() {
		t.Fatal("Changed() true before any config file exists")
	}
	for _, stamp := range cfg.fingerprint.files {
		if stamp.exists || stamp.hash != ([sha256.Size]byte{}) {
			t.Fatalf("missing file not recorded as zero identity: %+v", stamp)
		}
	}

	path := filepath.Join(cwd, "argent.json")
	writeRawFile(t, path, []byte(`{"model":"appeared"}`))
	if !cfg.Changed() {
		t.Fatal("Changed() false after a config file was created")
	}
	stamp := stampFor(t, newFingerprint(cwd, home), path)
	if stamp.hash == ([sha256.Size]byte{}) {
		t.Fatal("newly created readable file has a zero hash")
	}
}

// TestContentHashIncludesCJKRewrite checks that non-ASCII (UTF-8/CJK) content
// is hashed byte-for-byte: a one-rune CJK edit with identical size and mtime is
// detected, and the resulting hash is nonzero and stable.
func TestContentHashIncludesCJKRewrite(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	before := []byte(`{"model":"模型一"}`)
	after := []byte(`{"model":"模型二"}`)
	if len(before) != len(after) {
		t.Fatalf("test setup: CJK payload sizes differ (%d vs %d)", len(before), len(after))
	}
	writeRawFile(t, path, before)
	fixed := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	setMTime(t, path, fixed)

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "模型一" {
		t.Fatalf("model = %q, want 模型一", cfg.Model)
	}
	originalHash := stampFor(t, cfg.fingerprint, path).hash
	if originalHash == ([sha256.Size]byte{}) {
		t.Fatal("CJK content produced a zero hash")
	}
	if want := sha256.Sum256(before); originalHash != want {
		t.Fatalf("hash = %x, want SHA-256 of raw bytes %x", originalHash, want)
	}

	writeRawFile(t, path, after)
	setMTime(t, path, fixed)
	if !cfg.Changed() {
		t.Fatal("Changed() false after a CJK rewrite with identical size and mtime")
	}
	if stampFor(t, newFingerprint(cwd, home), path).hash == originalHash {
		t.Fatal("hash unchanged after a CJK character edit")
	}

	next, err := cfg.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if next.Model != "模型二" {
		t.Errorf("reloaded model = %q, want 模型二", next.Model)
	}
}

// TestContentHashStable pins determinism: hashing the same bytes twice yields
// the same SHA-256, repeated fingerprints compare equal, and equal() only flips
// when content actually differs.
func TestContentHashStable(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	data := []byte(`{"model":"stable","locale":"zh-CN"}`)
	writeRawFile(t, path, data)

	first := newFingerprint(cwd, home)
	second := newFingerprint(cwd, home)
	if !first.equal(second) {
		t.Fatal("two fingerprints of an unchanged file differ")
	}
	stamp := stampFor(t, first, path)
	if want := sha256.Sum256(data); stamp.hash != want {
		t.Fatalf("hash = %x, want %x", stamp.hash, want)
	}
	if again := stampFor(t, second, path); stamp.hash != again.hash {
		t.Fatal("hash not stable across repeated reads")
	}
}

// TestFingerprintReadFailureFallsBackToStat documents the read-failure path:
// a path that stats but cannot be read as a file (a directory here) keeps a
// zero hash, and equal() still works from the stat-only identity.
func TestFingerprintReadFailureFallsBackToStat(t *testing.T) {
	dir := t.TempDir() // stat succeeds; os.ReadFile fails with "is a directory".
	fp := fingerprintPaths([]string{dir})
	stamp := fp.files[0]
	if !stamp.exists {
		t.Fatal("directory path was not recorded as existing")
	}
	if stamp.hash != ([sha256.Size]byte{}) {
		t.Fatal("unreadable path should keep a zero hash")
	}
	if !fp.equal(fingerprintPaths([]string{dir})) {
		t.Fatal("two stat-only fingerprints of the same unreadable path differ")
	}
}
