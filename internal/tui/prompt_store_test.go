package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// writePromptLines 直接写入原始 JSONL 行，用于构造坏文件。
func writePromptLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	var buf strings.Builder
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("write prompt lines: %v", err)
	}
}

// promptLine 用固定时间戳构造一行合法 JSONL。
func promptLine(t *testing.T, text string, count int, at time.Time) string {
	t.Helper()
	b, err := json.Marshal(promptEntry{Text: text, Count: count, Time: at.UnixNano()})
	if err != nil {
		t.Fatalf("marshal prompt entry: %v", err)
	}
	return string(b)
}

func assertTexts(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("texts = %q, want %q", got, want)
	}
}

func TestPromptStoreAppendLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	s := NewPromptStore(path)
	texts := []string{"first prompt", "你好，世界", "😀 emoji 🚀", "a\tb\nc"}

	for _, text := range texts {
		if err := s.Append(text); err != nil {
			t.Fatalf("Append(%q): %v", text, err)
		}
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertTexts(t, s.Recent(len(texts)+1), texts)

	// 磁盘上确实是四行合法 JSON，且每行都能解析回原文本。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != len(texts) {
		t.Fatalf("line count = %d, want %d", len(lines), len(texts))
	}
	for i, line := range lines {
		var e promptEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if e.Text != texts[i] {
			t.Errorf("line %d text = %q, want %q", i, e.Text, texts[i])
		}
		if e.Count != 1 || e.Time == 0 {
			t.Errorf("line %d metadata = (count %d, time %d)", i, e.Count, e.Time)
		}
	}
}

func TestPromptStoreRecentDedupKeepsLatestPosition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	s := NewPromptStore(path)
	for _, text := range []string{"a", "b", "a", "c", "b", "d"} {
		if err := s.Append(text); err != nil {
			t.Fatalf("Append(%q): %v", text, err)
		}
	}
	// 去重后按最近一次出现的位置排序，返回顺序为从旧到新：
	// a 的最近一次在第 2 位、c 在第 3 位、b 在第 4 位、d 在第 5 位。
	assertTexts(t, s.Recent(10), []string{"a", "c", "b", "d"})
	assertTexts(t, s.Recent(2), []string{"b", "d"})
	assertTexts(t, s.Recent(0), nil)
	assertTexts(t, s.Recent(-3), nil)
}

func TestPromptStoreRecentSkipsEmptyAndOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	now := time.Now()
	oversized := strings.Repeat("长", maxPromptRunes+1)
	writePromptLines(t, path,
		`{"text":"keep me","count":1,"time":1}`,
		`{"text":"   ","count":1,"time":2}`,
		notJSONLine(),
		promptLine(t, oversized, 1, now),
		`{"text":"  好  ","count":1,"time":3}`,
		"",
	)
	s := NewPromptStore(path)
	// 条目文本原样保留（不替调用方裁剪），只有纯空白条目被过滤。
	assertTexts(t, s.Recent(10), []string{"keep me", "  好  "})
}

func TestPromptStoreLoadHealsBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	now := time.Now()
	good := promptLine(t, "有效的 中文 👍", 2, now)
	writePromptLines(t, path,
		notJSONLine(),
		good,
		`{"text":"","count":1,"time":1}`,
		`{"text":123}`,
		`[1,2,3]`,
		"",
	)
	s := NewPromptStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read healed file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("healed line count = %d, want 1: %q", len(lines), data)
	}
	if lines[0] != good {
		t.Errorf("healed line = %q, want %q", lines[0], good)
	}
	// 自愈后再次 Load 不应引入额外写入或改动。
	if err := s.Load(); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	assertTexts(t, s.Recent(10), []string{"有效的 中文 👍"})
}

func TestPromptStoreCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	s := NewPromptStore(path)
	for i := range 10 {
		if err := s.Append(fmt.Sprintf("prompt-%02d", i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	s.Cap(3)
	assertTexts(t, s.Recent(10), []string{"prompt-07", "prompt-08", "prompt-09"})

	// 再次 Cap 到更大值不应“复活”被丢弃的历史。
	s.Cap(20)
	assertTexts(t, s.Recent(10), []string{"prompt-07", "prompt-08", "prompt-09"})

	s.Cap(0)
	assertTexts(t, s.Recent(10), nil)
}

func TestPromptStoreCapHealsBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	writePromptLines(t, path, notJSONLine(), promptLine(t, "ok", 1, time.Now()))
	s := NewPromptStore(path)
	s.Cap(10)
	assertTexts(t, s.Recent(10), []string{"ok"})
}

func TestPromptStoreRecentByFrecency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	now := time.Unix(1_700_000_000, 0).UTC()
	writePromptLines(t, path,
		promptLine(t, "old", 10, now.Add(-48*time.Hour)), // 10 * 0.25 = 2.5
		promptLine(t, "fresh", 1, now),                   // 1
		promptLine(t, "mid", 3, now.Add(-24*time.Hour)),  // 3 * 0.5 = 1.5
		promptLine(t, "old", 1, now),                     // 再 +1，仍居首
	)
	s := NewPromptStore(path)
	got := s.recentByFrecencyAt(10, now)
	assertTexts(t, got, []string{"old", "mid", "fresh"})
	assertTexts(t, s.recentByFrecencyAt(2, now), []string{"old", "mid"})
	assertTexts(t, s.recentByFrecencyAt(0, now), nil)

	// 公开入口用当前时间，也应返回全部且顺序确定（同分时按文本兜底）。
	if public := s.RecentByFrecency(10); len(public) != 3 {
		t.Errorf("RecentByFrecency len = %d, want 3", len(public))
	}
}

func TestPromptStoreFrecencyTieBreakByRecency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	now := time.Unix(1_700_000_000, 0).UTC()
	writePromptLines(t, path,
		promptLine(t, "older", 1, now.Add(-time.Hour)),
		promptLine(t, "newer", 1, now),
	)
	s := NewPromptStore(path)
	assertTexts(t, s.recentByFrecencyAt(10, now), []string{"newer", "older"})
}

func TestPromptStoreCJKAndEmojiRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	s := NewPromptStore(path)
	texts := []string{
		"中文提示词 🚀",
		"👨‍👩‍👧‍👦 组合 emoji",
		strings.Repeat("界", 100),
	}
	for _, text := range texts {
		if err := s.Append(text); err != nil {
			t.Fatalf("Append(%q): %v", text, err)
		}
	}
	got := s.Recent(len(texts))
	if !slices.Equal(got, texts) {
		t.Fatalf("round trip = %q, want %q", got, texts)
	}
	for i, text := range got {
		if !utf8.ValidString(text) {
			t.Errorf("text %d is not valid UTF-8", i)
		}
	}
	// 恰好等于上限的提示可以写入，超过一条即拒绝。
	atLimit := strings.Repeat("字", maxPromptRunes)
	if err := s.Append(atLimit); err != nil {
		t.Fatalf("Append at limit: %v", err)
	}
	if err := s.Append(atLimit + "字"); !errors.Is(err, ErrInvalidPrompt) {
		t.Errorf("oversized Append error = %v, want ErrInvalidPrompt", err)
	}
}

func TestPromptStoreRejectsEmptyAndOversized(t *testing.T) {
	s := NewPromptStore(filepath.Join(t.TempDir(), "prompts.jsonl"))
	for _, text := range []string{"", "   \t\n", strings.Repeat("x", maxPromptRunes+1)} {
		if err := s.Append(text); !errors.Is(err, ErrInvalidPrompt) {
			t.Errorf("Append(invalid) error = %v, want ErrInvalidPrompt", err)
		}
	}
	if got := s.Recent(10); len(got) != 0 {
		t.Errorf("Recent after rejected appends = %q, want empty", got)
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Errorf("rejected appends created the file: stat err = %v", err)
	}
}

func TestPromptStoreConcurrentAppendAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	s := NewPromptStore(path)
	const workers = 64

	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Append(fmt.Sprintf("并发-%02d 🚀", i)); err != nil {
				errs <- fmt.Errorf("append %d: %w", i, err)
			}
			_ = s.Recent(10)
			_ = s.RecentByFrecency(10)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load after concurrent writes: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != workers {
		t.Errorf("line count = %d, want %d", lines, workers)
	}
	got := s.Recent(workers)
	if len(got) != workers {
		t.Fatalf("Recent len = %d, want %d", len(got), workers)
	}
	seen := make(map[string]bool, workers)
	for _, text := range got {
		if seen[text] {
			t.Errorf("duplicate text in Recent: %q", text)
		}
		seen[text] = true
	}
	for i := range workers {
		if !seen[fmt.Sprintf("并发-%02d 🚀", i)] {
			t.Errorf("missing text for worker %d", i)
		}
	}
}

func TestPromptStoreConcurrentRewriteKeepsFileValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	s := NewPromptStore(path)
	const rounds = 40

	var wg sync.WaitGroup
	errs := make(chan error, rounds*2)
	for i := range rounds {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := s.Append(fmt.Sprintf("append-%02d", i)); err != nil {
				errs <- err
			}
		}(i)
		go func() {
			defer wg.Done()
			s.Cap(1000) // 不应丢数据，但会触发原子重写。
			if err := s.Load(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := len(s.Recent(1000)); got != rounds {
		t.Errorf("Recent len = %d, want %d", got, rounds)
	}
	assertNoTempFiles(t, path)
}

func TestPromptStoreAtomicRewriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompts.jsonl")
	s := NewPromptStore(path)
	for i := range 5 {
		if err := s.Append(fmt.Sprintf("line-%d", i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	s.Cap(2)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertNoTempFiles(t, path)

	// 目标是一个目录时重命名必然失败：原目录必须完好且临时文件被清理。
	targetDir := filepath.Join(dir, "not-a-file")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bad := NewPromptStore(targetDir)
	if err := bad.Append("x"); err == nil {
		t.Error("Append onto a directory should fail")
	}
	if entries, err := os.ReadDir(targetDir); err != nil || len(entries) != 0 {
		t.Errorf("target dir changed: entries = %v, err = %v", entries, err)
	}
	assertNoTempFiles(t, path)
}

func TestPromptStoreDoesNotMutateInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	s := NewPromptStore(path)
	raw := []byte("不要改动这段输入 🍎")
	text := string(raw)
	if err := s.Append(text); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if string(raw) != "不要改动这段输入 🍎" || text != string(raw) {
		t.Errorf("input was mutated: raw = %q, text = %q", raw, text)
	}

	// 返回值是独立切片，改动它不影响后续读取。
	got := s.Recent(10)
	got[0] = "篡改"
	assertTexts(t, s.Recent(10), []string{"不要改动这段输入 🍎"})
}

func TestPromptStoreEmptyPath(t *testing.T) {
	s := NewPromptStore("")
	if err := s.Append("x"); err == nil {
		t.Error("Append with empty path should fail")
	}
	if err := s.Load(); err == nil {
		t.Error("Load with empty path should fail")
	}
	if got := s.Recent(10); len(got) != 0 {
		t.Errorf("Recent with empty path = %q, want empty", got)
	}
	s.Cap(3) // 不应 panic，也不产生任何文件。
}

// notJSONLine 返回一行明确非法的 JSON，用于坏行容错测试。
func notJSONLine() string { return `{"text":` }

// assertNoTempFiles 断言 path 所在目录没有遗留临时文件。
func assertNoTempFiles(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("leftover temp files: %v", matches)
	}
}
