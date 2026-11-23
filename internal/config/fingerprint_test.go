package config

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRawFile 写入精确字节（不做 JSON 重新编组），使测试可以
// 把文件大小精确控制到字节级。
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

// TestChangedDetectsSameSizeSameMtimeRewrite 是针对
// codex P-high 审计发现的回归测试：保留 size 和 mtime 的重写
// 对旧的仅 stat 指纹不可见。内容 hash 必须通过 Load/Changed/Reload
// 端到端地弥补这一缺口。
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

	// 原地重写且字节长度相同，然后恢复精确
	// mtime，使仅靠 mtime 和 size 无法发现该编辑。
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

// TestFingerprintHashChangesOnOrdinaryEdit 守护正常变更路径：
// 普通编辑（大小不同）仍须产生不同的指纹。
func TestFingerprintHashChangesOnOrdinaryEdit(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeRawFile(t, path, []byte(`{"model":"short"}`))
	before := stampFor(t, newFingerprint(cwd, t.TempDir(), t.TempDir()), path)

	writeRawFile(t, path, []byte(`{"model":"a-considerably-longer-model"}`))
	after := stampFor(t, newFingerprint(cwd, t.TempDir(), t.TempDir()), path)

	if before.size == after.size {
		t.Fatal("test setup: file size did not change")
	}
	if before.hash == after.hash {
		t.Fatal("hash unchanged after an ordinary edit")
	}
}

// TestChangedNoFileFallsBackToStatIdentity 验证无文件情形：缺失
// 的候选文件携带零 hash 并依赖存在性，创建文件
// 仍会被检测到（现在也有真实 hash 支撑）。
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
	stamp := stampFor(t, newFingerprint(cwd, home, home), path)
	if stamp.hash == ([sha256.Size]byte{}) {
		t.Fatal("newly created readable file has a zero hash")
	}
}

// TestContentHashIncludesCJKRewrite 检查非 ASCII（UTF-8/CJK）内容
// 是否按字节逐字 hash：等长、等 mtime 的单字符 CJK 编辑
// 会被检测到，且得到的 hash 非零且稳定。
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
	if stampFor(t, newFingerprint(cwd, home, home), path).hash == originalHash {
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

// TestContentHashStable 固定确定性：对相同字节 hash 两次得到
// 相同的 SHA-256，重复的指纹比较相等，且只有内容
// 真正不同时 equal() 才会翻转。
func TestContentHashStable(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	data := []byte(`{"model":"stable","locale":"zh-CN"}`)
	writeRawFile(t, path, data)

	first := newFingerprint(cwd, home, home)
	second := newFingerprint(cwd, home, home)
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

// TestFingerprintReadFailureFallsBackToStat 记录读取失败路径：
// 能 stat 但无法作为文件读取的路径（此处为目录）保持
// 零 hash，且 equal() 仍能基于仅 stat 的身份正常工作。
func TestFingerprintReadFailureFallsBackToStat(t *testing.T) {
	dir := t.TempDir() // stat 成功；os.ReadFile 因"is a directory"失败。
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
