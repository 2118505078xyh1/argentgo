package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/session"
	"example.com/argentgo/internal/textutil"
)

// continuationBriefCompactionEntry 把压缩载荷投影成 session.Messages() 产出的那种
// system Entry：Role="system"，Content 为摘要文本，Data 为压缩 JSON。
func continuationBriefCompactionEntry(t *testing.T, c session.Compaction) session.Entry {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal compaction: %v", err)
	}
	return session.Entry{Role: "system", Content: c.Summary, Data: data}
}

// continuationBriefFullCompaction 构造一个四段俱全的压缩载荷，模拟 Summarize 的输出。
func continuationBriefFullCompaction() session.Compaction {
	return session.Compaction{
		Summary: "## Compaction\n- dropped 3 message(s), kept the last 2\n\n" +
			"## Goal\n- 修复登录鉴权绕过\n\n" +
			"## Files changed\n- internal/auth/login.go\n- internal/auth/middleware.go\n\n" +
			"## Failures\n- go test 失败: undefined token\n\n" +
			"## Next steps\n- 补单测\n- 跑 vet\n",
		Files:     []string{"internal/auth/login.go", "internal/auth/middleware.go"},
		Failures:  []string{"go test 失败: undefined token"},
		NextSteps: []string{"补单测", "跑 vet"},
	}
}

// TestBuildContinuationBriefNoHistory 确认 nil/空条目返回空串。
func TestBuildContinuationBriefNoHistory(t *testing.T) {
	if got := BuildContinuationBrief(nil, 2000); got != "" {
		t.Fatalf("nil entries = %q, want empty", got)
	}
	if got := BuildContinuationBrief([]session.Entry{}, 2000); got != "" {
		t.Fatalf("empty entries = %q, want empty", got)
	}
}

// TestBuildContinuationBriefFourSectionsFixedOrder 锁定固定格式：四段标签各出现一次、
// 顺序固定，且内容取自正确的字段。
func TestBuildContinuationBriefFourSectionsFixedOrder(t *testing.T) {
	entries := []session.Entry{continuationBriefCompactionEntry(t, continuationBriefFullCompaction())}
	got := BuildContinuationBrief(entries, 2000)

	labels := []string{"## 会话续接", "- 目标: ", "- 改动文件: ", "- 失败: ", "- 下一步: "}
	prev := -1
	for i, label := range labels {
		if n := strings.Count(got, label); n != 1 {
			t.Fatalf("标签 %q 出现 %d 次，want 1；全文=%q", label, n, got)
		}
		at := strings.Index(got, label)
		if i > 0 && at <= prev {
			t.Fatalf("标签 %q 顺序错误：at=%d prev=%d；全文=%q", label, at, prev, got)
		}
		prev = at
	}

	for _, want := range []string{
		"修复登录鉴权绕过",
		"internal/auth/login.go；internal/auth/middleware.go",
		"go test 失败: undefined token",
		"补单测；跑 vet",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("摘要缺少 %q；全文=%q", want, got)
		}
	}
}

// TestBuildContinuationBriefBoundedAndClusterSafe 验证总长按 maxChars 钳制，且用
// textutil.ClipRunes 在字簇边界截断：合法 UTF-8、不切半 emoji/ZWJ 链。
func TestBuildContinuationBriefBoundedAndClusterSafe(t *testing.T) {
	family := "👨‍👩‍👧‍👦" // ZWJ 链，单个字簇含多个 rune
	c := session.Compaction{
		Summary: "## Compaction\n\n## Goal\n- " + strings.Repeat(family, 20) + "\n",
	}
	entries := []session.Entry{continuationBriefCompactionEntry(t, c)}

	full := BuildContinuationBrief(entries, 10000) // 不截断的完整串
	rs := []rune(full)
	cut := -1
	for i, r := range rs {
		if r == 0x200D { // 第一个 ZWJ，令切点落在其后的字形中间
			cut = i + 2
			break
		}
	}
	if cut < 0 {
		t.Fatalf("完整串中未找到 ZWJ；full=%q", full)
	}

	got := BuildContinuationBrief(entries, cut)
	if got == full {
		t.Fatal("截断版与完整版相同，maxChars 未生效")
	}
	if n := utf8.RuneCountInString(got); n >= cut {
		t.Fatalf("RuneCount=%d，want < cut=%d（应回退到簇边界）", n, cut)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果是非法 UTF-8：%q", got)
	}
	if strings.HasSuffix(got, "\u200d") {
		t.Fatalf("截断结果以 ZWJ 结尾，切开了字簇：%q", got)
	}
	if want := textutil.ClipRunes(full, cut); got != want {
		t.Fatalf("截断结果 = %q, want %q（应与 ClipRunes 一致）", got, want)
	}
}

// TestBuildContinuationBriefDefaultMaxChars 确认 maxChars<=0 使用默认上界。
func TestBuildContinuationBriefDefaultMaxChars(t *testing.T) {
	entries := []session.Entry{continuationBriefCompactionEntry(t, continuationBriefFullCompaction())}
	for _, maxChars := range []int{0, -1} {
		got := BuildContinuationBrief(entries, maxChars)
		if n := utf8.RuneCountInString(got); n > continuationBriefDefaultMaxChars {
			t.Fatalf("maxChars=%d 时 RuneCount=%d 超出默认上界 %d", maxChars, n, continuationBriefDefaultMaxChars)
		}
		if !strings.HasPrefix(got, "## 会话续接") {
			t.Fatalf("maxChars=%d 时摘要为空或格式错误：%q", maxChars, got)
		}
	}
}

// TestBuildContinuationBriefFallsBackToRecentUser 确认无压缩记录时回退到最近 user
// 消息的首行，且忽略更早的 user 消息。
func TestBuildContinuationBriefFallsBackToRecentUser(t *testing.T) {
	entries := []session.Entry{
		{Role: "user", Content: "第一条旧消息\n忽略这一行"},
		{Role: "assistant", Content: "已收到"},
		{Role: "user", Content: "  继续上次的重构  \r\n第二行不该出现  "},
	}
	got := BuildContinuationBrief(entries, 2000)

	if !strings.Contains(got, "- 目标: 继续上次的重构") {
		t.Fatalf("未取到最近 user 消息的首行；全文=%q", got)
	}
	if strings.Contains(got, "第一条旧消息") || strings.Contains(got, "第二行不该出现") {
		t.Fatalf("回退误取了其他行；全文=%q", got)
	}
	for _, label := range []string{"- 改动文件: ", "- 失败: ", "- 下一步: "} {
		if !strings.Contains(got, label+"（无）") {
			t.Fatalf("空段未用（无）占位：%q；全文=%q", label, got)
		}
	}
}

// TestBuildContinuationBriefUsesMostRecentCompaction 确认取最近的一条压缩记录。
func TestBuildContinuationBriefUsesMostRecentCompaction(t *testing.T) {
	older := session.Compaction{
		Summary:   "## Goal\n- 旧目标\n",
		Files:     []string{"old.go"},
		Failures:  []string{"旧失败"},
		NextSteps: []string{"旧下一步"},
	}
	newer := session.Compaction{
		Summary:   "## Goal\n- 新目标\n",
		Files:     []string{"new.go"},
		Failures:  []string{"新失败"},
		NextSteps: []string{"新下一步"},
	}
	entries := []session.Entry{
		continuationBriefCompactionEntry(t, older),
		continuationBriefCompactionEntry(t, newer),
	}
	got := BuildContinuationBrief(entries, 2000)
	for _, want := range []string{"新目标", "new.go", "新失败", "新下一步"} {
		if !strings.Contains(got, want) {
			t.Fatalf("未取到最近压缩记录的内容 %q；全文=%q", want, got)
		}
	}
	for _, stale := range []string{"旧目标", "old.go", "旧失败", "旧下一步"} {
		if strings.Contains(got, stale) {
			t.Fatalf("误用了较旧压缩记录的内容 %q；全文=%q", stale, got)
		}
	}
}

// TestBuildContinuationBriefFallsBackWhenStructuredDrifts 确认摘要格式漂移、
// 权威字段也为空时回退到 user 消息，而不是产出四段全空的摘要。
func TestBuildContinuationBriefFallsBackWhenStructuredDrifts(t *testing.T) {
	entries := []session.Entry{
		continuationBriefCompactionEntry(t, session.Compaction{Summary: "只是散文，没有可识别章节"}),
		{Role: "user", Content: "漂移后继续做这件事"},
	}
	got := BuildContinuationBrief(entries, 2000)
	if !strings.Contains(got, "- 目标: 漂移后继续做这件事") {
		t.Fatalf("格式漂移未回退到 user 消息；全文=%q", got)
	}
}

// TestBuildContinuationBriefCJKAndEmoji 确认 CJK 与 emoji 原样保留、不被转义或破坏。
func TestBuildContinuationBriefCJKAndEmoji(t *testing.T) {
	c := session.Compaction{
		Summary:   "## Goal\n- 重构中文模块 🚀\n",
		Files:     []string{"docs/文档🧪.md"},
		Failures:  []string{"编译失败 😵"},
		NextSteps: []string{"补测试 ✅"},
	}
	entries := []session.Entry{continuationBriefCompactionEntry(t, c)}
	got := BuildContinuationBrief(entries, 2000)

	for _, want := range []string{"重构中文模块 🚀", "docs/文档🧪.md", "编译失败 😵", "补测试 ✅"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CJK/emoji 内容丢失 %q；全文=%q", want, got)
		}
	}
	if !utf8.ValidString(got) {
		t.Fatalf("结果非法 UTF-8：%q", got)
	}
}

// TestBuildContinuationBriefDoesNotMutateInput 确认纯函数不改入参（含 Data 字节）。
func TestBuildContinuationBriefDoesNotMutateInput(t *testing.T) {
	entries := []session.Entry{
		{Role: "user", Content: "保留我"},
		continuationBriefCompactionEntry(t, continuationBriefFullCompaction()),
	}
	orig := make([]session.Entry, len(entries))
	copy(orig, entries)
	for i := range entries {
		if len(entries[i].Data) > 0 {
			orig[i].Data = append([]byte(nil), entries[i].Data...)
		}
	}

	_ = BuildContinuationBrief(entries, 2000)

	if !reflect.DeepEqual(entries, orig) {
		t.Fatalf("入参被修改：\n got=%+v\nwant=%+v", entries, orig)
	}
}

// TestBuildContinuationBriefDeterministic 确认重复调用输出一致、条目按输入顺序渲染。
func TestBuildContinuationBriefDeterministic(t *testing.T) {
	entries := []session.Entry{continuationBriefCompactionEntry(t, continuationBriefFullCompaction())}
	first := BuildContinuationBrief(entries, 2000)
	for i := 0; i < 5; i++ {
		if got := BuildContinuationBrief(entries, 2000); got != first {
			t.Fatalf("第 %d 次调用结果不同：\n got=%q\nwant=%q", i, got, first)
		}
	}
	if strings.Index(first, "补单测") >= strings.Index(first, "跑 vet") {
		t.Fatalf("条目顺序不稳定：%q", first)
	}
}

// TestContinuationBriefEnabled 确认 env 门控默认开启，仅 "0" 紧急关闭。
func TestContinuationBriefEnabled(t *testing.T) {
	envOf := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env 默认开启", nil, true},
		{"变量缺失默认开启", envOf(map[string]string{}), true},
		{"值为 1", envOf(map[string]string{continuationBriefEnvKey: "1"}), true},
		{"值带空白 1", envOf(map[string]string{continuationBriefEnvKey: " 1 "}), true},
		{"值为 0 关闭", envOf(map[string]string{continuationBriefEnvKey: "0"}), false},
		{"带空白的 0 关闭", envOf(map[string]string{continuationBriefEnvKey: " \t0\n"}), false},
		{"其他值保持开启", envOf(map[string]string{continuationBriefEnvKey: "true"}), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := continuationBriefEnabled(tc.env); got != tc.want {
				t.Fatalf("continuationBriefEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}
