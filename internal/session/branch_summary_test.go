package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// branchSummaryFixture 是一棵带一条非活动分支的转录：n000003..n000007
// 从 n000002 分叉出去，n000008..n000009 构成文件最后的活动叶子。
type branchSummaryFixture struct {
	path string
	dir  string
	id   string
}

const branchSummarySecret = "sk-abcdef1234567890"

func writeBranchSummaryFixture(t *testing.T) branchSummaryFixture {
	t.Helper()
	dir := t.TempDir()
	id := "branch-fixture"
	path := filepath.Join(dir, id+fileExt)
	ts := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	secret := branchSummarySecret
	records := []Record{
		{ID: "n000001", Kind: KindHeader, TS: ts, Data: json.RawMessage(`{"title":"分支测试","created":"2026-11-21T08:00:00Z"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, TS: ts, Data: json.RawMessage(`{"content":"起点"}`)},
		{ID: "n000003", Parent: "n000002", Kind: KindAssistant, TS: ts, Data: json.RawMessage(`{"content":"先看旧实现"}`)},
		{ID: "n000004", Parent: "n000003", Kind: KindUser, TS: ts,
			Data: json.RawMessage(`{"content":"实现登录功能，密钥 ` + secret + ` 不要外泄"}`)},
		{ID: "n000005", Parent: "n000004", Kind: KindAssistant, TS: ts, Data: json.RawMessage(`{"content":"我会先改 internal/auth.go"}`)},
		{ID: "n000006", Parent: "n000005", Kind: KindTool, TS: ts,
			Data: json.RawMessage(`{"name":"edit","path":"internal/auth.go","output":"failed: permission denied","api_key":"` + secret + `"}`)},
		{ID: "n000007", Parent: "n000006", Kind: KindAssistant, TS: ts, Data: json.RawMessage(`{"content":"下一步：修复权限"}`)},
		{ID: "n000008", Parent: "n000002", Kind: KindAssistant, TS: ts, Data: json.RawMessage(`{"content":"换一条路：先写测试"}`)},
		{ID: "n000009", Parent: "n000008", Kind: KindUser, TS: ts, Data: json.RawMessage(`{"content":"继续分支 B"}`)},
	}
	writeBranchSummaryRecords(t, path, records)
	return branchSummaryFixture{path: path, dir: dir, id: id}
}

// writeBranchSummaryRecords 按 JSONL 写一组固定记录，避免依赖 append 的 id/时间戳生成。
func writeBranchSummaryRecords(t *testing.T, path string, records []Record) {
	t.Helper()
	var buf bytes.Buffer
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode record: %v", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func branchSummaryText(t *testing.T, rec Record) string {
	t.Helper()
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Data, &payload); err != nil {
		t.Fatalf("decode summary payload: %v", err)
	}
	return payload.Text
}

// TestSummarizeAbandonedBranch 覆盖摘要内容、边界、脱敏、保留策略、
// 幂等以及与 Fork 的默认隔离。每个用例都校验函数不会改写源转录。
func TestSummarizeAbandonedBranch(t *testing.T) {
	tests := []struct {
		name    string
		fixture func(*testing.T) branchSummaryFixture
		opts    []AbandonedBranchOption
		wantErr error
		check   func(*testing.T, branchSummaryFixture, Record)
	}{
		{
			name: "关键段落与CJK",
			check: func(t *testing.T, _ branchSummaryFixture, rec Record) {
				summary := branchSummaryText(t, rec)
				for _, want := range []string{
					"## Compaction", "## Goal", "## Files changed", "## Failures", "## Next steps",
					"实现登录功能", "internal/auth.go", "下一步：修复权限",
				} {
					if !strings.Contains(summary, want) {
						t.Errorf("摘要缺少 %q:\n%s", want, summary)
					}
				}
			},
		},
		{
			name: "脱敏",
			check: func(t *testing.T, _ branchSummaryFixture, rec Record) {
				summary := branchSummaryText(t, rec)
				if strings.Contains(summary, branchSummarySecret) {
					t.Fatalf("摘要泄露了密钥:\n%s", summary)
				}
				if !strings.Contains(summary, "[redacted:api key]") {
					t.Fatalf("摘要没有脱敏占位符:\n%s", summary)
				}
			},
		},
		{
			name: "有界且UTF-8完整",
			opts: []AbandonedBranchOption{WithAbandonedBranchLimit(40)},
			check: func(t *testing.T, _ branchSummaryFixture, rec Record) {
				summary := branchSummaryText(t, rec)
				if got := utf8.RuneCountInString(summary); got > 40 {
					t.Fatalf("摘要长度 %d 超过上限 40: %q", got, summary)
				}
				if !utf8.ValidString(summary) {
					t.Fatalf("摘要被截断在 UTF-8 字符中间: %q", summary)
				}
			},
		},
		{
			name: "幂等",
			check: func(t *testing.T, fx branchSummaryFixture, rec Record) {
				second, err := SummarizeAbandonedBranch(fx.path)
				if err != nil {
					t.Fatalf("second call: %v", err)
				}
				if rec.ID != second.ID || rec.Parent != second.Parent || !rec.TS.Equal(second.TS) ||
					!bytes.Equal(rec.Data, second.Data) {
					t.Fatalf("重复调用结果不同:\nfirst  = %+v\nsecond = %+v", rec, second)
				}
			},
		},
		{
			name: "RetainInThread不泄漏到子会话",
			check: func(t *testing.T, _ branchSummaryFixture, rec Record) {
				if got := GetRetention(rec.Data); got != RetainInThread {
					t.Fatalf("retention = %v, want RetainInThread", got)
				}
				if got := SelectForChildren([]Record{rec}); len(got) != 0 {
					t.Fatalf("摘要泄漏进子会话: %v", got)
				}
				if got := SelectForCompaction([]Record{rec}, 0); len(got) != 1 {
					t.Fatalf("摘要未能在压缩后保留: %v", got)
				}
			},
		},
		{
			name: "指定分支尖端",
			opts: []AbandonedBranchOption{WithAbandonedBranchTarget("n000003")},
			check: func(t *testing.T, _ branchSummaryFixture, rec Record) {
				if rec.ID != "branch-summary-n000003" || rec.Parent != "n000002" {
					t.Fatalf("record id/parent = %q/%q, want branch-summary-n000003/n000002", rec.ID, rec.Parent)
				}
			},
		},
		{
			name: "默认Fork不写摘要",
			check: func(t *testing.T, fx branchSummaryFixture, rec Record) {
				parent := "n000002"
				sess, err := OpenSession(fx.dir, fx.id)
				if err != nil {
					t.Fatalf("open session: %v", err)
				}
				forked, err := sess.Fork(parent)
				if err != nil {
					t.Fatalf("fork: %v", err)
				}
				records, err := readRecords(forked.path())
				if err != nil {
					t.Fatalf("read fork: %v", err)
				}
				for _, record := range records {
					if record.Kind == KindSummary {
						t.Fatalf("默认 Fork 不应新增摘要记录: %+v", record)
					}
				}
				// 摘要函数本身的返回值只影响调用方，源转录的字节必须逐字不变（由外层统一校验）。
				_ = rec
			},
		},
		{
			name: "线性会话无可摘要分支",
			fixture: func(t *testing.T) branchSummaryFixture {
				dir := t.TempDir()
				id := "linear-fixture"
				path := filepath.Join(dir, id+fileExt)
				ts := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
				writeBranchSummaryRecords(t, path, []Record{
					{ID: "n000001", Kind: KindHeader, TS: ts, Data: json.RawMessage(`{"title":"线性","created":"2026-11-21T08:00:00Z"}`)},
					{ID: "n000002", Parent: "n000001", Kind: KindUser, TS: ts, Data: json.RawMessage(`{"content":"只有一条路径"}`)},
				})
				return branchSummaryFixture{path: path, dir: dir, id: id}
			},
			wantErr: ErrNoAbandonedBranch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			makeFixture := tc.fixture
			if makeFixture == nil {
				makeFixture = writeBranchSummaryFixture
			}
			fx := makeFixture(t)
			before, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatalf("read transcript: %v", err)
			}
			rec, err := SummarizeAbandonedBranch(fx.path, tc.opts...)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SummarizeAbandonedBranch: %v", err)
			}
			after, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatalf("read transcript after: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("摘要函数不得写回转录")
			}
			if rec.Kind != KindSummary {
				t.Fatalf("kind = %q, want %q", rec.Kind, KindSummary)
			}
			if tc.check != nil {
				tc.check(t, fx, rec)
			}
		})
	}
}

// TestSummarizeAbandonedBranchUnknownTarget 确认未知的分支 id 会被明确拒绝。
func TestSummarizeAbandonedBranchUnknownTarget(t *testing.T) {
	fx := writeBranchSummaryFixture(t)
	_, err := SummarizeAbandonedBranch(fx.path, WithAbandonedBranchTarget("n999999"))
	if err == nil || !strings.Contains(err.Error(), "unknown abandoned branch target") {
		t.Fatalf("error = %v, want unknown target error", err)
	}
}
