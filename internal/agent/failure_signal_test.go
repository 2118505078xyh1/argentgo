package agent

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestExtractFailureSignalsKinds 覆盖四类失败信号以及稳定顺序、轮次与工具名。
func TestExtractFailureSignalsKinds(t *testing.T) {
	messages := []MessageLike{
		{Role: "user", Content: "正常运行"},                                           // 0：无信号
		{Role: "tool", ToolName: "shell", Content: "compiling...\n[exit 2]"},      // 1：非零退出码
		{Role: "tool", ToolName: "shell", Content: "command timed out after 30s"}, // 2：超时
		{Role: "tool", ToolName: "patch", Content: "ERROR: anchor not found"},     // 3：英文失败词
		{Role: "tool", ToolName: "fs", Content: "操作失败：文件不存在"},                     // 4：中文失败词
		{Role: "tool", ToolName: "shell", Content: "", IsError: true},             // 5：is_error（空文本）
		{Role: "tool", ToolName: "net", Content: "响应超时", IsError: true},           // 6：is_error 优先于失败词
		{Role: "tool", ToolName: "shell", Content: "[exit 0]"},                    // 7：零退出码不算失败
		{Role: "assistant", Content: "done"},                                      // 8：无信号
		{Role: "tool", ToolName: "shell", Content: "[EXIT 7]"},                    // 9：大小写不敏感
	}

	got := ExtractFailureSignals(messages)
	wantTurns := []int{1, 2, 3, 4, 5, 6, 9}
	wantKinds := []string{
		SignalKindExit,
		SignalKindTimeout,
		SignalKindWord,
		SignalKindWord,
		SignalKindIsError,
		SignalKindIsError,
		SignalKindExit,
	}
	wantTools := []string{"shell", "shell", "patch", "fs", "shell", "net", "shell"}

	if len(got) != len(wantTurns) {
		t.Fatalf("ExtractFailureSignals 条数 = %d, want %d: %+v", len(got), len(wantTurns), got)
	}
	for i, sig := range got {
		if sig.Turn != wantTurns[i] || sig.Kind != wantKinds[i] || sig.Tool != wantTools[i] {
			t.Errorf("信号[%d] = %+v, want turn=%d kind=%q tool=%q",
				i, sig, wantTurns[i], wantKinds[i], wantTools[i])
		}
		if sig.Text == "" {
			t.Errorf("信号[%d] 的 Text 为空", i)
		}
	}
	// is_error 且无输出文本时给出稳定占位说明。
	if got[4].Text != "is_error" {
		t.Errorf("空 is_error 的 Text = %q, want %q", got[4].Text, "is_error")
	}
}

// TestExtractFailureSignalsStableAndNonMutating 验证纯函数性：不改入参，且重复调用结果一致。
func TestExtractFailureSignalsStableAndNonMutating(t *testing.T) {
	messages := []MessageLike{
		{Role: "tool", ToolName: "shell", Content: "[exit 1]"},
		{Role: "tool", ToolName: "fs", Content: "失败"},
	}
	before := append([]MessageLike(nil), messages...)

	first := ExtractFailureSignals(messages)
	second := ExtractFailureSignals(messages)

	if !reflect.DeepEqual(messages, before) {
		t.Fatalf("ExtractFailureSignals 修改了入参：\n got %+v\nwant %+v", messages, before)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重复调用结果不稳定：\nfirst  %+v\nsecond %+v", first, second)
	}
}

// TestExtractFailureSignalsCJKAndCapping 覆盖 CJK / emoji 安全与 Text 的 rune 截断。
func TestExtractFailureSignalsCJKAndCapping(t *testing.T) {
	cjk := []MessageLike{
		{Role: "tool", ToolName: "编辑", Content: strings.Repeat("失败", 200)},
		{Role: "tool", ToolName: "emoji🔥", Content: "崩溃 🚀🚀 " + strings.Repeat("错误", 200)},
	}
	got := ExtractFailureSignals(cjk)
	if len(got) != 2 {
		t.Fatalf("条数 = %d, want 2: %+v", len(got), got)
	}
	for _, sig := range got {
		if !utf8.ValidString(sig.Text) {
			t.Errorf("Text 不是合法 UTF-8: %q", sig.Text)
		}
		if !strings.HasSuffix(sig.Text, "...") {
			t.Errorf("超长 Text 应以 ... 结尾: %q", sig.Text)
		}
		if r := utf8.RuneCountInString(sig.Text); r != signalTextRunes+3 {
			t.Errorf("Text 符文数 = %d, want %d", r, signalTextRunes+3)
		}
		// 结尾不能是半个 emoji / 多字节序列：去掉 "..." 后仍须合法。
		core := strings.TrimSuffix(sig.Text, "...")
		if !utf8.ValidString(core) {
			t.Errorf("截断后的正文不是合法 UTF-8: %q", core)
		}
	}
}

// TestExtractFailureSignalsEmpty 验证空输入返回空结果。
func TestExtractFailureSignalsEmpty(t *testing.T) {
	if got := ExtractFailureSignals(nil); len(got) != 0 {
		t.Fatalf("nil 输入结果 = %+v, want 空", got)
	}
	if got := ExtractFailureSignals([]MessageLike{}); len(got) != 0 {
		t.Fatalf("空切片结果 = %+v, want 空", got)
	}
}

// TestDeduplicateSignals 验证按 Text 去重且保序、不改入参。
func TestDeduplicateSignals(t *testing.T) {
	in := []FailureSignal{
		{Text: "A", Turn: 0, Kind: SignalKindWord},
		{Text: "B", Turn: 1, Kind: SignalKindExit},
		{Text: "A", Turn: 2, Kind: SignalKindWord}, // 与第一条同 Text
		{Text: "C", Turn: 3, Kind: SignalKindTimeout},
		{Text: "B", Turn: 4, Kind: SignalKindExit}, // 与第二条同 Text
	}
	before := append([]FailureSignal(nil), in...)

	got := DeduplicateSignals(in)
	want := []FailureSignal{
		{Text: "A", Turn: 0, Kind: SignalKindWord},
		{Text: "B", Turn: 1, Kind: SignalKindExit},
		{Text: "C", Turn: 3, Kind: SignalKindTimeout},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeduplicateSignals = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("DeduplicateSignals 修改了入参：\n got %+v\nwant %+v", in, before)
	}
}

// TestDeduplicateSignalsEmpty 验证空输入。
func TestDeduplicateSignalsEmpty(t *testing.T) {
	if got := DeduplicateSignals(nil); len(got) != 0 {
		t.Fatalf("nil 输入结果 = %+v, want 空", got)
	}
}

// TestFailureSignalSummary 验证摘要的稳定中文格式、类别计数、工具与轮次范围。
func TestFailureSignalSummary(t *testing.T) {
	sigs := []FailureSignal{
		{Text: "x", Turn: 5, Tool: "shell", Kind: SignalKindExit},
		{Text: "y", Turn: 1, Tool: "fs", Kind: SignalKindWord},
		{Text: "z", Turn: 3, Tool: "shell", Kind: SignalKindExit},
		{Text: "w", Turn: 2, Tool: "", Kind: SignalKindTimeout},
		{Text: "v", Turn: 4, Tool: "fs", Kind: SignalKindIsError},
	}
	got := FailureSignalSummary(sigs)
	want := "失败信号共 5 条，错误标记 1 条，非零退出码 2 条，超时 1 条，失败词 1 条；涉及工具 fs、shell；轮次 1-5"
	if got != want {
		t.Fatalf("FailureSignalSummary = %q, want %q", got, want)
	}
	// 相同的多重集合、不同顺序，摘要仍一致（工具排序 + 轮次范围）。
	reordered := []FailureSignal{sigs[1], sigs[4], sigs[0], sigs[3], sigs[2]}
	if again := FailureSignalSummary(reordered); again != got {
		t.Fatalf("摘要不稳定：%q != %q", again, got)
	}
}

// TestFailureSignalSummaryNoLeak 验证摘要不泄漏 Text 中的敏感串。
func TestFailureSignalSummaryNoLeak(t *testing.T) {
	const secret = "sk-SECRET-1234567890"
	sigs := []FailureSignal{
		{Text: "failed to login with token " + secret, Turn: 0, Tool: "shell", Kind: SignalKindWord},
	}
	got := FailureSignalSummary(sigs)
	if strings.Contains(got, secret) || strings.Contains(got, "login") {
		t.Fatalf("摘要泄漏了原文敏感内容: %q", got)
	}
}

// TestFailureSignalSummaryEmpty 验证空输入摘要。
func TestFailureSignalSummaryEmpty(t *testing.T) {
	if got := FailureSignalSummary(nil); got != "无失败信号" {
		t.Fatalf("空摘要 = %q, want %q", got, "无失败信号")
	}
}

// TestFailureSignalSummaryUnknownKind 验证未知类别按名字稳定输出，不丢信息。
func TestFailureSignalSummaryUnknownKind(t *testing.T) {
	sigs := []FailureSignal{
		{Text: "x", Turn: 0, Kind: "zzz"},
		{Text: "y", Turn: 1, Kind: "aaa"},
	}
	got := FailureSignalSummary(sigs)
	want := "失败信号共 2 条，aaa 1 条，zzz 1 条；轮次 0-1"
	if got != want {
		t.Fatalf("FailureSignalSummary = %q, want %q", got, want)
	}
}

// TestExtractThenDeduplicateAndSummarize 端到端：提取 -> 去重 -> 摘要。
func TestExtractThenDeduplicateAndSummarize(t *testing.T) {
	messages := []MessageLike{
		{Role: "tool", ToolName: "shell", Content: "[exit 1]"},
		{Role: "tool", ToolName: "shell", Content: "[exit 1]"}, // 重复
		{Role: "tool", ToolName: "fs", Content: "读取失败"},
	}
	sigs := DeduplicateSignals(ExtractFailureSignals(messages))
	if len(sigs) != 2 {
		t.Fatalf("去重后条数 = %d, want 2: %+v", len(sigs), sigs)
	}
	got := FailureSignalSummary(sigs)
	want := "失败信号共 2 条，非零退出码 1 条，失败词 1 条；涉及工具 fs、shell；轮次 0-2"
	if got != want {
		t.Fatalf("摘要 = %q, want %q", got, want)
	}
}
