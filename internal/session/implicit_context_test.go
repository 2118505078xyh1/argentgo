package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// TestExtractImplicitContextAssistantTails 验证末 tailN 条 assistant 正文尾被按原顺序抽取。
func TestExtractImplicitContextAssistantTails(t *testing.T) {
	dropped := []Entry{
		{Role: "user", Content: "开工"},
		{Role: "assistant", Content: "第一条计划"},
		{Role: "tool", Content: "ok", Data: []byte(`{"output":"ok"}`)},
		{Role: "assistant", Content: "第二条计划"},
		{Role: "assistant", Content: "第三条计划"},
	}
	ic := ExtractImplicitContext(dropped, 2, 0)
	want := []string{"第二条计划", "第三条计划"}
	if !reflect.DeepEqual(ic.AssistantTails, want) {
		t.Fatalf("AssistantTails = %q, want %q", ic.AssistantTails, want)
	}
	if len(ic.UnmarkedFailures) != 0 {
		t.Fatalf("UnmarkedFailures = %q, want empty", ic.UnmarkedFailures)
	}
}

// TestExtractImplicitContextTakesTail 验证超长正文只保留末尾有界片段，且折叠为单行。
func TestExtractImplicitContextTakesTail(t *testing.T) {
	long := strings.Repeat("甲", 200) + "\n" + strings.Repeat("乙", 20)
	ic := ExtractImplicitContext([]Entry{{Role: "assistant", Content: long}}, 1, 0)
	if len(ic.AssistantTails) != 1 {
		t.Fatalf("AssistantTails len = %d, want 1", len(ic.AssistantTails))
	}
	got := ic.AssistantTails[0]
	if strings.Contains(got, "\n") {
		t.Fatalf("tail should be a single line, got %q", got)
	}
	if r := utf8.RuneCountInString(got); r > implicitTailRunes {
		t.Fatalf("tail runes = %d, want <= %d", r, implicitTailRunes)
	}
	if !strings.HasSuffix(got, strings.Repeat("乙", 20)) {
		t.Fatalf("tail lost its newest suffix: %q", got)
	}
	if !strings.Contains(got, "甲") {
		t.Fatalf("tail lost the earlier context entirely: %q", got)
	}
}

// TestExtractImplicitContextFailureSignals 覆盖三种失败信号与常见负例。
func TestExtractImplicitContextFailureSignals(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		content string
		want    bool
	}{
		{"is_error bool", `{"name":"bash","is_error":true,"output":"boom"}`, "boom", true},
		{"is_error string", `{"name":"bash","is_error":"true","output":"boom"}`, "boom", true},
		{"exit code", `{"name":"bash","output":"done [exit 2]"}`, "done [exit 2]", true},
		{"english error word", `{"name":"bash","output":"error: boom"}`, "error: boom", true},
		{"chinese error word", `{"name":"编译","output":"编译失败"}`, "编译失败", true},
		{"is_error false", `{"name":"bash","is_error":false,"output":"ok"}`, "ok", false},
		{"exit zero", `{"name":"bash","output":"done [exit 0]"}`, "done [exit 0]", false},
		{"plain output", `{"name":"bash","output":"all good"}`, "all good", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasImplicitFailureSignal(tc.content, jsonRaw(tc.data))
			if got != tc.want {
				t.Fatalf("hasImplicitFailureSignal(%q, %q) = %v, want %v", tc.content, tc.data, got, tc.want)
			}
		})
	}
}

// TestExtractImplicitContextSkipsMarkedFailures 确认带 _retention 标记的工具结果被视为
// "已显式保留"，不会作为隐式失败重复抽取。
func TestExtractImplicitContextSkipsMarkedFailures(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "bash", Content: "boom", Data: []byte(`{"is_error":true,"output":"boom","_retention":"always"}`)},
		{Role: "tool", Name: "编译", Content: "编译失败", Data: []byte(`{"output":"编译失败","_retention":"in_thread"}`)},
		{Role: "tool", Name: "test", Content: "error: boom", Data: []byte(`{"output":"error: boom"}`)},
	}
	ic := ExtractImplicitContext(dropped, 0, 5)
	if len(ic.UnmarkedFailures) != 1 {
		t.Fatalf("UnmarkedFailures = %q, want exactly one unmarked failure", ic.UnmarkedFailures)
	}
	if !strings.Contains(ic.UnmarkedFailures[0], "test") || !strings.Contains(ic.UnmarkedFailures[0], "error") {
		t.Fatalf("UnmarkedFailures[0] = %q, want the unmarked tool's failure", ic.UnmarkedFailures[0])
	}
}

// TestExtractImplicitContextCJKAndEmoji 验证 CJK / emoji 文本安全，不产生非法 UTF-8。
func TestExtractImplicitContextCJKAndEmoji(t *testing.T) {
	dropped := []Entry{
		{Role: "assistant", Content: strings.Repeat("中文计划🚀", 60)},
		{Role: "tool", Name: "编译", Content: strings.Repeat("编译失败😵", 60), Data: []byte(`{"output":"编译失败😵"}`)},
	}
	ic := ExtractImplicitContext(dropped, 1, 1)
	if len(ic.AssistantTails) != 1 || len(ic.UnmarkedFailures) != 1 {
		t.Fatalf("unexpected ic: %+v", ic)
	}
	for _, s := range ic.AssistantTails {
		if !utf8.ValidString(s) {
			t.Fatalf("invalid UTF-8 in assistant tail: %q", s)
		}
		if r := utf8.RuneCountInString(s); r > implicitTailRunes {
			t.Fatalf("assistant tail too long (%d runes): %q", r, s)
		}
	}
	for _, s := range ic.UnmarkedFailures {
		if !utf8.ValidString(s) {
			t.Fatalf("invalid UTF-8 in failure preview: %q", s)
		}
		// 预览本身有界；失败项目符号额外附带工具名前缀（" — " 之前的部分）。
		preview := s
		if i := strings.Index(s, " — "); i >= 0 {
			preview = s[i+len(" — "):]
		}
		if r := utf8.RuneCountInString(preview); r > implicitFailureRunes {
			t.Fatalf("failure preview too long (%d runes): %q", r, preview)
		}
	}
}

// TestGraphemeTailRespectsClusterBoundary 验证裁剪起点不会落在组合符或 ZWJ 序列中间。
func TestGraphemeTailRespectsClusterBoundary(t *testing.T) {
	t.Run("combining mark", func(t *testing.T) {
		// runes: 10x'a', 'e', U+0301, 10x'b'。limit=11 时起点正落在组合符上。
		text := strings.Repeat("a", 10) + "e\u0301" + strings.Repeat("b", 10)
		got := graphemeTail(text, 11)
		if want := strings.Repeat("b", 10); got != want {
			t.Fatalf("graphemeTail = %q, want %q", got, want)
		}
		if strings.ContainsRune(got, '\u0301') {
			t.Fatalf("orphan combining mark in %q", got)
		}
	})

	t.Run("emoji zwj", func(t *testing.T) {
		// 👩‍💻 是三个 rune 组成的单个字素簇；limit=5 时起点正落在 ZWJ 上。
		text := strings.Repeat("a", 3) + "👩‍💻" + strings.Repeat("b", 3)
		got := graphemeTail(text, 5)
		if want := strings.Repeat("b", 3); got != want {
			t.Fatalf("graphemeTail = %q, want %q", got, want)
		}
		if strings.ContainsRune(got, textutil.ClusterZWJ) {
			t.Fatalf("orphan ZWJ in %q", got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8: %q", got)
		}
	})
}

// TestExtractImplicitContextNoMutation 验证抽取过程不修改入参（含 Content / Data 切片）。
func TestExtractImplicitContextNoMutation(t *testing.T) {
	dropped := []Entry{
		{Role: "assistant", Content: "计划"},
		{Role: "tool", Content: "error: boom", Data: []byte(`{"output":"error: boom"}`)},
	}
	before := make([]Entry, len(dropped))
	for i, e := range dropped {
		before[i] = e
		before[i].Data = append([]byte(nil), e.Data...)
	}
	ExtractImplicitContext(dropped, 5, 5)
	if !reflect.DeepEqual(dropped, before) {
		t.Fatalf("dropped mutated: got %+v, want %+v", dropped, before)
	}
}

// TestExtractImplicitContextLimits 验证 tailN / failN <= 0 时不做抽取。
func TestExtractImplicitContextLimits(t *testing.T) {
	dropped := []Entry{
		{Role: "assistant", Content: "计划"},
		{Role: "tool", Content: "error: boom", Data: []byte(`{"output":"error: boom"}`)},
	}
	for _, tc := range []struct{ tailN, failN int }{{0, 0}, {-1, -1}, {0, 5}, {5, 0}} {
		ic := ExtractImplicitContext(dropped, tc.tailN, tc.failN)
		if tc.tailN <= 0 && len(ic.AssistantTails) != 0 {
			t.Errorf("tailN=%d: AssistantTails = %q, want empty", tc.tailN, ic.AssistantTails)
		}
		if tc.failN <= 0 && len(ic.UnmarkedFailures) != 0 {
			t.Errorf("failN=%d: UnmarkedFailures = %q, want empty", tc.failN, ic.UnmarkedFailures)
		}
		if tc.tailN > 0 && len(ic.AssistantTails) != 1 {
			t.Errorf("tailN=%d: AssistantTails = %q, want one", tc.tailN, ic.AssistantTails)
		}
		if tc.failN > 0 && len(ic.UnmarkedFailures) != 1 {
			t.Errorf("failN=%d: UnmarkedFailures = %q, want one", tc.failN, ic.UnmarkedFailures)
		}
	}
}

// TestExtractImplicitContextStableOrder 验证失败按输入顺序保留最近 failN 项。
func TestExtractImplicitContextStableOrder(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "one", Content: "error 1", Data: []byte(`{"output":"error 1"}`)},
		{Role: "tool", Name: "two", Content: "error 2", Data: []byte(`{"output":"error 2"}`)},
		{Role: "tool", Name: "three", Content: "error 3", Data: []byte(`{"output":"error 3"}`)},
	}
	ic := ExtractImplicitContext(dropped, 0, 2)
	if len(ic.UnmarkedFailures) != 2 {
		t.Fatalf("UnmarkedFailures = %q, want two", ic.UnmarkedFailures)
	}
	if !strings.Contains(ic.UnmarkedFailures[0], "two") || !strings.Contains(ic.UnmarkedFailures[1], "three") {
		t.Fatalf("UnmarkedFailures out of stable order: %q", ic.UnmarkedFailures)
	}
}

// TestMergeImplicitIntoSummary 验证内容被追加到正确的章节。
func TestMergeImplicitIntoSummary(t *testing.T) {
	summary := Summarize([]Entry{
		{Role: "user", Content: "目标：修复构建"},
		{Role: "tool", Name: "bash", Content: "[exit 1]", Data: []byte(`{"output":"[exit 1]"}`)},
	}, 0).Summary
	ic := ImplicitContext{
		AssistantTails:   []string{"隐式计划：下一步重构缓存"},
		UnmarkedFailures: []string{"implicit-tool — 隐式失败"},
	}
	merged := MergeImplicitIntoSummary(summary, ic)

	failures := sectionBody(t, merged, "Failures")
	if !strings.Contains(failures, "implicit-tool — 隐式失败") {
		t.Fatalf("implicit failure not in Failures section: %q", failures)
	}
	next := sectionBody(t, merged, "Next steps")
	if !strings.Contains(next, "隐式计划") {
		t.Fatalf("implicit tail not in Next steps section: %q", next)
	}
	if !strings.Contains(merged, implicitContextMarker) {
		t.Fatalf("merge marker missing: %q", merged)
	}
}

// TestMergeImplicitIntoSummaryIdempotent 验证同一摘要合并两次不会重复追加。
func TestMergeImplicitIntoSummaryIdempotent(t *testing.T) {
	summary := Summarize(nil, 0).Summary
	ic := ImplicitContext{
		AssistantTails:   []string{"计划"},
		UnmarkedFailures: []string{"失败"},
	}
	first := MergeImplicitIntoSummary(summary, ic)
	second := MergeImplicitIntoSummary(first, ic)
	if first != second {
		t.Fatalf("merge is not idempotent:\nfirst:  %q\nsecond: %q", first, second)
	}
}

// TestMergeImplicitIntoSummaryEmptyIC 验证空上下文保持摘要逐字节不变。
func TestMergeImplicitIntoSummaryEmptyIC(t *testing.T) {
	summary := Summarize(nil, 0).Summary
	if got := MergeImplicitIntoSummary(summary, ImplicitContext{}); got != summary {
		t.Fatalf("empty ic changed the summary:\ngot:  %q\nwant: %q", got, summary)
	}
}

// TestMergeImplicitIntoSummaryNewSections 验证摘要缺少相应章节时会新建章节。
func TestMergeImplicitIntoSummaryNewSections(t *testing.T) {
	merged := MergeImplicitIntoSummary("# Hand written\n", ImplicitContext{
		AssistantTails:   []string{"尾部计划"},
		UnmarkedFailures: []string{"尾部失败"},
	})
	if !strings.Contains(merged, "## Failures\n- 尾部失败\n") {
		t.Fatalf("Failures section not created: %q", merged)
	}
	if !strings.Contains(merged, "## Next steps\n- 尾部计划\n") {
		t.Fatalf("Next steps section not created: %q", merged)
	}
	if !utf8.ValidString(merged) {
		t.Fatalf("merged summary is not valid UTF-8: %q", merged)
	}
}

// jsonRaw 是测试里把字符串转为 json.RawMessage 的小工具。
func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

// sectionBody 返回 markdown 摘要中指定章节的正文，供断言使用。
func sectionBody(t *testing.T, summary, title string) string {
	t.Helper()
	heading := "## " + title
	lines := strings.Split(summary, "\n")
	start := -1
	for i, line := range lines {
		if line == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("section %q not found in %q", heading, summary)
	}
	var b strings.Builder
	for i := start; i < len(lines) && !strings.HasPrefix(lines[i], "## "); i++ {
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	return b.String()
}
