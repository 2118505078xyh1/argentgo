package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// clipGraphemeCluster 是四个调用点共用的语料：组合序列（e + U+0301）、
// 四字节 emoji 与汉字各一个，前面再放一个 ASCII 'a'，便于让 limit 精确
// 落在组合序列中间（'e' 与附加符之间）。
const clipGraphemeCluster = "a" + "e\u0301" + "🚀" + "界"

// TestTruncateRunesSnapsToClusterBoundary 验证折叠后的头部裁剪不会切断
// 组合序列，且 TrimSpace 与“放得下就原样返回”的语义保持不变。
func TestTruncateRunesSnapsToClusterBoundary(t *testing.T) {
	input := "  " + clipGraphemeCluster + "   " + strings.Repeat("x", 20) + "  "
	cleaned := strings.Join(strings.Fields(strings.ReplaceAll(input, "\n", " ")), " ")

	for _, tc := range []struct {
		name  string
		limit int
		want  string
	}{
		{"cut inside combining sequence", 2, "a"},
		{"cut after whole cluster", 5, clipGraphemeCluster},
		{"trim trailing space after cut", 6, clipGraphemeCluster},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(input, tc.limit)
			if got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", input, tc.limit, got, tc.want)
			}
			if got != strings.TrimSpace(got) {
				t.Fatalf("truncateRunes result is not TrimSpace'd: %q", got)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateRunes produced invalid UTF-8: %q", got)
			}
		})
	}

	// 放得下时原样返回折叠后的文本，不重编码、不加省略号。
	if got := truncateRunes(input, utf8.RuneCountInString(cleaned)); got != cleaned {
		t.Fatalf("truncateRunes on fitting input = %q, want %q", got, cleaned)
	}
}

// TestCapProgressRunesSnapsToClusterBoundary 验证进度裁剪不会切断字簇，
// 同时保留“不做 TrimSpace、放得下就原样返回”的既有语义。
func TestCapProgressRunesSnapsToClusterBoundary(t *testing.T) {
	text := "  " + clipGraphemeCluster + strings.Repeat("x", 20) + "  "

	// limit=4 的默认切点恰好落在 'e' 与附加符之间；吸附后应停在 'e' 之前。
	got := capProgressRunes(text, 4)
	want := "  a"
	if got != want {
		t.Fatalf("capProgressRunes(%q, 4) = %q, want %q", text, got, want)
	}
	if !strings.HasPrefix(text, got) {
		t.Fatalf("capProgressRunes result %q is not a prefix of %q", got, text)
	}
	if textutil.GluedAt(text, len(got)) {
		t.Fatalf("capProgressRunes result %q ends mid-cluster", got)
	}
	// 不 TrimSpace：前导空格必须保留。
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("capProgressRunes trimmed leading spaces: %q", got)
	}

	// 放得下时逐字节原样返回（含首尾空格）。
	if got := capProgressRunes(text, utf8.RuneCountInString(text)); got != text {
		t.Fatalf("capProgressRunes on fitting input = %q, want %q", got, text)
	}
}

// writeClipBranchFixture 写一棵只含一条非活动分支的最小转录，
// 分支里放一段含多 rune 字簇的文本，供摘要裁剪测试使用。
func writeClipBranchFixture(t *testing.T, content string) branchSummaryFixture {
	t.Helper()
	dir := t.TempDir()
	id := "clip-grapheme-fixture"
	path := filepath.Join(dir, id+fileExt)
	ts := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)

	payload := func(text string) json.RawMessage {
		raw, err := json.Marshal(map[string]string{"content": text})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	records := []Record{
		{ID: "n000001", Kind: KindHeader, TS: ts, Data: json.RawMessage(`{"title":"cluster","created":"2026-11-21T08:00:00Z"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, TS: ts, Data: payload("起点")},
		{ID: "n000003", Parent: "n000002", Kind: KindAssistant, TS: ts, Data: payload("旧分支回复")},
		{ID: "n000004", Parent: "n000003", Kind: KindUser, TS: ts, Data: payload(content)},
		{ID: "n000005", Parent: "n000002", Kind: KindAssistant, TS: ts, Data: payload("活动分支")},
	}
	writeBranchSummaryRecords(t, path, records)
	return branchSummaryFixture{path: path, dir: dir, id: id}
}

// TestBranchSummaryClipSnapsToClusterBoundary 验证分支摘要进入上下文时，
// 符文上限的切点若落在组合序列中间，会吸附到簇边界而非劈开字簇。
func TestBranchSummaryClipSnapsToClusterBoundary(t *testing.T) {
	content := clipGraphemeCluster + strings.Repeat("x", 40)
	fx := writeClipBranchFixture(t, content)

	fullRec, err := SummarizeAbandonedBranch(fx.path)
	if err != nil {
		t.Fatalf("SummarizeAbandonedBranch (no limit): %v", err)
	}
	full := branchSummaryText(t, fullRec)
	at := strings.Index(full, clipGraphemeCluster)
	if at < 0 {
		t.Fatalf("摘要未包含待裁剪语料: %q", full)
	}

	// 'e' 在语料内的符文偏移为 1；limit 取 'e' 之后的符文数，
	// 使默认切点落在 'e' 与 U+0301 之间。
	limit := utf8.RuneCountInString(full[:at]) + 2
	clippedRec, err := SummarizeAbandonedBranch(fx.path, WithAbandonedBranchLimit(limit))
	if err != nil {
		t.Fatalf("SummarizeAbandonedBranch (limit=%d): %v", limit, err)
	}
	got := branchSummaryText(t, clippedRec)

	if got == full {
		t.Fatalf("摘要未被裁剪（limit=%d）: %q", limit, got)
	}
	if !strings.HasPrefix(full, got) {
		t.Fatalf("裁剪后的摘要 %q 不是原文 %q 的前缀", got, full)
	}
	if textutil.GluedAt(full, len(got)) {
		t.Fatalf("裁剪后的摘要以字簇内部结尾: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("裁剪后的摘要包含非法 UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > limit {
		t.Fatalf("摘要符文数 %d 超过上限 %d", n, limit)
	}
}

// TestContextWindowSnapsBothEdgesToClusterBoundary 验证搜索片段窗口的
// 起止两端都会吸附到字簇边界，同时保留 "…" 标记与脱敏顺序。
func TestContextWindowSnapsBothEdgesToClusterBoundary(t *testing.T) {
	// 起点：'e' 在符文 0、U+0301 在符文 1，needle 在符文 49，
	// 因此 start = 49-48 = 1 落在组合序列中间，应被向前吸附到符文 2。
	// 终点：needle 在 49..54，55..149 是 95 个 'y'，'e' 在 150、U+0301 在 151，
	// 因此 end = 49+6+96 = 151 也落在组合序列中间，应被向后吸附到符文 150。
	value := "e\u0301" + strings.Repeat("x", 47) + "needle" +
		strings.Repeat("y", 95) + "e\u0301" + strings.Repeat("y", 50)
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	snippet, ok := snippetFor(data, "needle")
	if !ok {
		t.Fatal("snippetFor found no hit")
	}
	want := "…" + strings.Repeat("x", 47) + "needle" + strings.Repeat("y", 95) + "…"
	if snippet != want {
		t.Fatalf("snippet = %q (%d runes), want %q (%d runes)",
			snippet, utf8.RuneCountInString(snippet), want, utf8.RuneCountInString(want))
	}
	if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
		t.Fatalf("片段两端的省略号标记丢失: %q", snippet)
	}
	if !utf8.ValidString(snippet) {
		t.Fatalf("片段包含非法 UTF-8: %q", snippet)
	}

	// 完整语料置于起点切点：start = 50-48 = 2 落在 'e'+U+0301 中间，
	// 吸附后整簇（含 🚀 与界）被完整跳过，不会留下半个 emoji 或悬空附加符。
	fullValue := clipGraphemeCluster + strings.Repeat("x", 45) + "needle" + strings.Repeat("y", 200)
	fullData, err := json.Marshal(fullValue)
	if err != nil {
		t.Fatal(err)
	}
	fullSnippet, ok := snippetFor(fullData, "needle")
	if !ok {
		t.Fatal("snippetFor found no hit on full corpus")
	}
	// 吸附只越过 e+U+0301 这一个字簇，🚀 与界各自成簇、保持完整。
	fullWant := "…" + "🚀界" + strings.Repeat("x", 45) + "needle" + strings.Repeat("y", 96) + "…"
	if fullSnippet != fullWant {
		t.Fatalf("full corpus snippet = %q, want %q", fullSnippet, fullWant)
	}

	// 脱敏仍先于最终截断：长 token 的占位符保持完整。
	secretValue := "e\u0301" + strings.Repeat("x", 47) + "token = 2118505078xyh1" +
		strings.Repeat("y", 200)
	secretData, err := json.Marshal(secretValue)
	if err != nil {
		t.Fatal(err)
	}
	secretSnippet, ok := snippetFor(secretData, "token")
	if !ok {
		t.Fatal("snippetFor found no token hit")
	}
	if strings.Contains(secretSnippet, "2118505078xyh1") {
		t.Fatalf("片段泄露了密钥: %q", secretSnippet)
	}
	if !strings.Contains(secretSnippet, "[redacted:token]") {
		t.Fatalf("片段缺少脱敏占位符: %q", secretSnippet)
	}
}
