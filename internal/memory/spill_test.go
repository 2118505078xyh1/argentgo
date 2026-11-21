package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSpillThresholdDecision 用表驱动验证阈值判定：全文严格大于阈值才落盘，
// 等于阈值时保持内联；默认阈值取 maxTextBytes，恰好覆盖旧实现会丢失的正文。
func TestSpillThresholdDecision(t *testing.T) {
	cases := []struct {
		name      string
		threshold int // 0 表示使用默认值
		textLen   int
		wantSpill bool
	}{
		{"below threshold", 20, 10, false},
		{"exact threshold", 20, 20, false},
		{"one over threshold", 20, 21, true},
		{"default threshold exact", 0, DefaultSpillThreshold, false},
		{"default threshold over", 0, DefaultSpillThreshold + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bank := NewBank(t.TempDir())
			bank.SetSpill(&SpillOptions{Threshold: tc.threshold, Preview: 64})
			text := strings.Repeat("a", tc.textLen)
			id, err := bank.Retain(text, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			all := bank.All()
			if len(all) != 1 {
				t.Fatalf("all = %#v, want one entry", all)
			}
			got := all[0]
			spilled := got.SpillURI != ""
			if spilled != tc.wantSpill {
				t.Fatalf("spilled = %v, want %v (text=%d threshold=%d)", spilled, tc.wantSpill, tc.textLen, tc.threshold)
			}
			if !spilled {
				if got.Text != boundedText(text) {
					t.Fatalf("inline text = %q, want %q", got.Text, boundedText(text))
				}
				if got.SpillBytes != 0 {
					t.Fatalf("spill bytes = %d, want 0", got.SpillBytes)
				}
				return
			}
			if got.SpillBytes != len(text) {
				t.Fatalf("spill bytes = %d, want %d", got.SpillBytes, len(text))
			}
			full, err := bank.Recover(id)
			if err != nil || full != text {
				t.Fatalf("recover = %q, %v; want the full text", full, err)
			}
			if _, err := os.Stat(bank.spillPath(id)); err != nil {
				t.Fatalf("spill file missing: %v", err)
			}
		})
	}
}

// TestSpillPreviewIsBoundedAndRuneSafe 验证有界预览：长度不超过配置值、
// 始终是全文前缀，且中文、emoji 与组合字簇都不会被切坏。
func TestSpillPreviewIsBoundedAndRuneSafe(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		preview int
	}{
		{"ascii", strings.Repeat("abcdefghij", 20), 10},
		{"cjk", strings.Repeat("你好世界", 30), 7},
		{"emoji", strings.Repeat("ab😀cd", 20), 5},
		{"grapheme cluster", "e\u0301🚀" + strings.Repeat("x", 200), 7},
		{"preview larger than full", "hello world", 256},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bank := NewBank(t.TempDir())
			bank.SetSpill(&SpillOptions{Threshold: 4, Preview: tc.preview})
			id, err := bank.Retain(tc.text, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			all := bank.All()
			if len(all) != 1 || all[0].SpillURI == "" {
				t.Fatalf("all = %#v, want a spilled entry", all)
			}
			preview := all[0].Text
			if len(preview) > tc.preview {
				t.Fatalf("preview is %d bytes, limit is %d: %q", len(preview), tc.preview, preview)
			}
			if !utf8.ValidString(preview) {
				t.Fatalf("preview is not valid UTF-8: %q", preview)
			}
			full := spillBody(tc.text)
			if !strings.HasPrefix(full, preview) {
				t.Fatalf("preview %q is not a prefix of %q", preview, full)
			}
			recovered, err := bank.Recover(id)
			if err != nil || recovered != full {
				t.Fatalf("recover = %q, %v; want %q", recovered, err, full)
			}
		})
	}
}

// TestSpillRecover 表驱动验证 URI 恢复：落盘条目返回全文、内联条目返回
// 原文，未知 id、空 id、非法 URI 与缺失文件都得到错误而非静默错误结果。
func TestSpillRecover(t *testing.T) {
	const payload = "spilled-payload-"
	cases := []struct {
		name       string
		setup      func(t *testing.T, bank *Bank) string
		want       string
		wantErr    error
		wantAnyErr bool
	}{
		{
			name: "spilled record returns full text",
			setup: func(t *testing.T, bank *Bank) string {
				bank.SetSpill(&SpillOptions{Threshold: 8, Preview: 4})
				id, err := bank.Retain(strings.Repeat(payload, 30), nil, "")
				if err != nil {
					t.Fatal(err)
				}
				return id
			},
			want: strings.Repeat(payload, 30),
		},
		{
			name: "inline record returns its text",
			setup: func(t *testing.T, bank *Bank) string {
				id, err := bank.Retain("short inline text", nil, "")
				if err != nil {
					t.Fatal(err)
				}
				return id
			},
			want: "short inline text",
		},
		{
			name: "unknown id",
			setup: func(t *testing.T, bank *Bank) string {
				return "does-not-exist"
			},
			wantErr: ErrMemoryNotFound,
		},
		{
			name: "empty id",
			setup: func(t *testing.T, bank *Bank) string {
				return "   "
			},
			wantAnyErr: true,
		},
		{
			name: "invalid uri is rejected",
			setup: func(t *testing.T, bank *Bank) string {
				writeRecordWithURI(t, bank, "fake-id", "memspill:../escaped")
				return "fake-id"
			},
			wantAnyErr: true,
		},
		{
			name: "missing spill file is an error",
			setup: func(t *testing.T, bank *Bank) string {
				writeRecordWithURI(t, bank, "fake-id", "memspill:gone1234")
				return "fake-id"
			},
			wantAnyErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bank := NewBank(t.TempDir())
			id := tc.setup(t, bank)
			got, err := bank.Recover(id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if tc.wantAnyErr {
				if err == nil {
					t.Fatalf("recover = %q, want an error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("recover = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestSpillCleanup 表驱动验证落盘清理：Forget 与容量淘汰删除全文文件，
// 作废（墓碑）与 CleanSpill 保留被引用文件，孤立文件则被回收。
func TestSpillCleanup(t *testing.T) {
	cases := []struct {
		name     string
		setupCap int
		act      func(t *testing.T, bank *Bank, id string)
		wantFile bool
	}{
		{
			name: "forget removes spill file",
			act: func(t *testing.T, bank *Bank, id string) {
				if err := bank.Forget(id); err != nil {
					t.Fatal(err)
				}
			},
			wantFile: false,
		},
		{
			name:     "cap eviction removes spill file",
			setupCap: 1,
			act: func(t *testing.T, bank *Bank, id string) {
				if _, err := bank.Retain(strings.Repeat("new", 20), nil, ""); err != nil {
					t.Fatal(err)
				}
			},
			wantFile: false,
		},
		{
			name: "invalidate keeps spill file as tombstone",
			act: func(t *testing.T, bank *Bank, id string) {
				if err := bank.Invalidate(id, "stale"); err != nil {
					t.Fatal(err)
				}
			},
			wantFile: true,
		},
		{
			name: "clean spill keeps referenced file",
			act: func(t *testing.T, bank *Bank, id string) {
				if _, err := bank.CleanSpill(); err != nil {
					t.Fatal(err)
				}
			},
			wantFile: true,
		},
		{
			name: "clean spill removes orphan file",
			act: func(t *testing.T, bank *Bank, id string) {
				orphan := bank.spillPath("orphan1234")
				if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				n, err := bank.CleanSpill()
				if err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Fatalf("CleanSpill removed %d files, want 1", n)
				}
			},
			wantFile: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bank := NewBank(t.TempDir())
			bank.SetSpill(&SpillOptions{Threshold: 8, Preview: 4})
			if tc.setupCap > 0 {
				bank.cap = tc.setupCap
			}
			id, err := bank.Retain(strings.Repeat("payload", 10), nil, "")
			if err != nil {
				t.Fatal(err)
			}
			path := bank.spillPath(id)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("spill file missing before action: %v", err)
			}
			tc.act(t, bank, id)
			_, statErr := os.Stat(path)
			gotExists := statErr == nil
			if gotExists != tc.wantFile {
				t.Fatalf("spill file exists = %v, want %v (stat err: %v)", gotExists, tc.wantFile, statErr)
			}
		})
	}
}

// TestSpillURIRejectsTraversal 确认恶意恢复 URI 无法让清理或恢复越界到
// spill 目录之外。
func TestSpillURIRejectsTraversal(t *testing.T) {
	bank := NewBank(t.TempDir())
	outside := filepath.Join(bank.dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecordWithURI(t, bank, "fake-id", "memspill:../outside")
	if err := bank.Forget("fake-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("traversal URI deleted a file outside the spill dir: %v", err)
	}
	if _, err := bank.Recover("fake-id"); err == nil {
		t.Fatal("Recover must reject a traversal URI")
	}
}

// TestSpillDisabledKeepsBytesUnchanged 守护默认契约：未启用落盘时，
// 日志行的字段集合、内联文本与磁盘文件都与本功能加入之前一致。
func TestSpillDisabledKeepsBytesUnchanged(t *testing.T) {
	bank := NewBank(t.TempDir())
	if bank.SpillEnabled() {
		t.Fatal("spill must be disabled by default")
	}
	text := strings.Repeat("x", DefaultSpillThreshold+100)
	id, err := bank.Retain(text, []string{"tag"}, "src")
	if err != nil {
		t.Fatal(err)
	}
	all := bank.All()
	if len(all) != 1 || all[0].SpillURI != "" || all[0].SpillBytes != 0 {
		t.Fatalf("all = %#v, want a plain inline entry", all)
	}
	if all[0].Text != boundedText(text) {
		t.Fatalf("text = %q, want %q", all[0].Text, boundedText(text))
	}
	if _, err := os.Stat(filepath.Join(bank.dir, spillDirName)); !os.IsNotExist(err) {
		t.Fatalf("spill dir must not be created when disabled (stat err: %v)", err)
	}

	data, err := os.ReadFile(bank.path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &obj); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"id", "text", "tags", "ts", "source"}
	if len(obj) != len(wantKeys) {
		t.Fatalf("log keys = %v, want exactly %v", obj, wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := obj[key]; !ok {
			t.Fatalf("log key %q missing from %v", key, obj)
		}
	}

	// 重写路径（Forget 触发 writeAllLocked）同样不得引入 spill 字段。
	id2, err := bank.Retain("second", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := bank.Forget(id); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(bank.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "spill") {
		t.Fatalf("spill fields leaked into default log: %s", data)
	}
	got, err := bank.Recover(id2)
	if err != nil || got != "second" {
		t.Fatalf("recover inline = %q, %v; want second", got, err)
	}
}

// writeRecordWithURI 向日志写入一条带指定恢复 URI 的记录，用于构造恶意或
// 损坏的输入，而不依赖正常的落盘流程。
func writeRecordWithURI(t *testing.T, bank *Bank, id, uri string) {
	t.Helper()
	mem := Memory{
		ID:         id,
		Text:       "preview",
		TS:         time.Now().UTC(),
		SpillURI:   uri,
		SpillBytes: 999,
	}
	line, err := json.Marshal(mem)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bank.path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
