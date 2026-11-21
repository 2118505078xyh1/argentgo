package agent

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("x", 400), 100},
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Errorf("EstimateTokens(%d chars) = %d, want %d", len(tc.text), got, tc.want)
		}
	}
}

func TestEstimateMessageCountsOverheadAndCalls(t *testing.T) {
	plain := llm.Message{Role: "user", Content: strings.Repeat("x", 400)}
	if got := EstimateMessage(plain); got != 104 {
		t.Fatalf("estimate = %d, want 104 (100 text + 4 overhead)", got)
	}
	withCall := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c", "read", `{"path":"a"}`)}}
	if got := EstimateMessage(withCall); got <= messageOverheadTokens {
		t.Fatalf("a tool call must add to the estimate, got %d", got)
	}
}

// trimFixture 构建一个包含旧工具结果、中间轮次和最新轮次的对话，除工具主体外都很小。
// （接上行）
func trimFixture() []llm.Message {
	big := strings.Repeat("x", 4000)
	return []llm.Message{
		{Role: "system", Content: "system rules"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "step one", ToolCalls: []llm.ToolCall{toolCall("c1", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "step two", ToolCalls: []llm.ToolCall{toolCall("c2", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c2", Content: big},
		{Role: "assistant", Content: "step three", ToolCalls: []llm.ToolCall{toolCall("c3", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c3", Content: big},
	}
}

func TestTrimKeepsSystemFirstUserAndLastTurns(t *testing.T) {
	messages := trimFixture()
	before := EstimateMessages(messages)
	result := Trim(messages, TrimOptions{MaxTokens: 400, KeepTurns: 1, KeepToolTokens: 1})

	if len(result.Messages) != len(messages) {
		t.Fatalf("messages = %d, want %d: trimming must not remove entries", len(result.Messages), len(messages))
	}
	if result.Pruned == 0 {
		t.Fatal("expected at least one pruned tool result")
	}
	if result.Freed <= 0 {
		t.Fatalf("freed = %d, want > 0", result.Freed)
	}
	if after := EstimateMessages(result.Messages); after >= before {
		t.Fatalf("estimate did not shrink: %d -> %d", before, after)
	}
	if result.Messages[0].Content != "system rules" {
		t.Fatal("system prompt was trimmed")
	}
	if result.Messages[1].Content != "first question" {
		t.Fatal("first user message was trimmed")
	}
	if result.Messages[6].Content != "step three" {
		t.Fatal("last assistant turn was trimmed")
	}
	if result.Messages[7].Content == "" || strings.HasPrefix(result.Messages[7].Content, trimmedPrefix) {
		t.Fatal("most recent tool result was trimmed")
	}
	if !strings.HasPrefix(result.Messages[3].Content, trimmedPrefix) {
		t.Fatalf("oldest tool result = %q, want a stub", result.Messages[3].Content)
	}
	// 裁剪两次不得对已有占位条目再次替换。
	again := Trim(result.Messages, TrimOptions{MaxTokens: 400, KeepTurns: 1, KeepToolTokens: 1})
	if again.Pruned != 0 {
		t.Fatalf("second pass pruned %d already-stubbed results", again.Pruned)
	}
}

func TestTrimKeepsLastNTurns(t *testing.T) {
	messages := trimFixture()
	result := Trim(messages, TrimOptions{MaxTokens: 400, KeepTurns: 3, KeepToolTokens: 1})
	for i := 4; i < len(messages); i++ {
		if result.Messages[i].Content != messages[i].Content {
			t.Fatalf("message %d inside the last 3 turns changed", i)
		}
	}
}

func TestTrimDoesNothingWhenUnderBudget(t *testing.T) {
	messages := trimFixture()
	result := Trim(messages, TrimOptions{MaxTokens: 100000})
	if result.Pruned != 0 {
		t.Fatalf("pruned %d results under budget", result.Pruned)
	}
}

func TestEstimateTokensCountsWideRunesIndividually(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"你好世界", 4},                 // 4 个宽 rune，每个一个 token
		{"hello你好", 4},              // 5 个窄字符 -> 2，加 2 个宽字符
		{strings.Repeat("界", 8), 8}, // 对 CJK 不做字符数/4 的低估
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

// metaFixture 构建一段对话：早期读取 a.go，之后成功读取同一文件，最后一条助手轮次。
// （接上行）
func metaFixture() []llm.Message {
	big := strings.Repeat("x", 400)
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "inspect"},
		{Role: "assistant", Content: "first", ToolCalls: []llm.ToolCall{toolCall("c1", "read", `{"path":"a.go"}`)}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "again", ToolCalls: []llm.ToolCall{toolCall("c2", "read", `{"path":"a.go:10"}`)}},
		{Role: "tool", ToolCallID: "c2", Content: big},
		{Role: "assistant", Content: "done"},
	}
}

func TestTrimSupersedesOldRead(t *testing.T) {
	messages := metaFixture()
	meta := map[string]ToolMeta{
		"c1": {SupersedeKey: "read|a.go"},
		"c2": {SupersedeKey: "read|a.go"},
	}
	result := Trim(messages, TrimOptions{ToolMeta: meta})

	if len(result.Messages) != len(messages) {
		t.Fatalf("messages = %d, want %d", len(result.Messages), len(messages))
	}
	if got := result.Messages[3].Content; got != supersededStub {
		t.Fatalf("old read = %q, want %q", got, supersededStub)
	}
	if got := result.Messages[5].Content; got == supersededStub {
		t.Fatal("the newest read must stay verbatim")
	}
	if result.Pruned != 1 || result.Freed <= 0 {
		t.Fatalf("pruned/freed = %d/%d, want 1/>0", result.Pruned, result.Freed)
	}
	// 配对：每个工具结果仍紧跟其助手轮次。
	for i, msg := range result.Messages {
		if msg.Role == "tool" && (i == 0 || result.Messages[i-1].Role != "assistant") {
			t.Fatalf("tool result at %d is no longer paired", i)
		}
	}
}

func TestTrimDoesNotSupersedeOnFailedLaterRead(t *testing.T) {
	messages := metaFixture()
	messages[5].Content = "error: permission denied"
	meta := map[string]ToolMeta{
		"c1": {SupersedeKey: "read|a.go"},
		"c2": {SupersedeKey: "read|a.go"},
	}
	result := Trim(messages, TrimOptions{ToolMeta: meta})
	if got := result.Messages[3].Content; got == supersededStub {
		t.Fatal("an error result must not supersede an older successful read")
	}
}

// TestTrimPreservesErrorResultsUnderBudgetPressure 锁定预算循环对错误结果的保护：
// 当老结果超出 KeepToolTokens 窗口、且缓存后缀在保护阈值内时，它本应被 stub 回收；
// 但错误结果必须保留原文，否则就违反了压缩"必须保留失败记录"的契约。
func TestTrimPreservesErrorResultsUnderBudgetPressure(t *testing.T) {
	errorBody := "error: permission denied\n" + strings.Repeat("E", 4000)
	recentBody := strings.Repeat("y", 100)
	messages := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", ToolCalls: []llm.ToolCall{toolCall("c1", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: errorBody}, // 老的错误结果：预算下的回收候选
		{Role: "assistant", Content: "b", ToolCalls: []llm.ToolCall{toolCall("c2", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c2", Content: recentBody}, // 最新结果，占用 KeepToolTokens 窗口
		{Role: "assistant", Content: "done"},
	}
	opts := TrimOptions{MaxTokens: 100, KeepTurns: 1, KeepToolTokens: 1}

	// 先证明预算压力真实存在：同尺寸的非错误结果会被 stub，且 Freed > 0。
	// 这样下面对错误结果的断言才不是"根本没有触发裁剪"的假阳性。
	normal := append([]llm.Message(nil), messages...)
	normal[3].Content = strings.Repeat("E", len(errorBody))
	baseline := Trim(normal, opts)
	if baseline.Pruned == 0 || baseline.Freed <= 0 {
		t.Fatalf("precondition: non-error result should be trimmed (pruned/freed = %d/%d)", baseline.Pruned, baseline.Freed)
	}
	if got := baseline.Messages[3].Content; !strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("precondition: non-error result = %q, want a stub", got)
	}

	result := Trim(messages, opts)
	if got := result.Messages[3].Content; got != errorBody {
		t.Fatalf("error result was rewritten: %q", got)
	}
	if strings.HasPrefix(result.Messages[3].Content, trimmedPrefix) {
		t.Fatal("error result must not be stubbed into a trimmed placeholder")
	}
	// 错误结果被跳过后没有任何可回收对象，Freed 相应降为 0。
	if result.Pruned != 0 || result.Freed != 0 {
		t.Fatalf("error result should not be pruned: pruned/freed = %d/%d", result.Pruned, result.Freed)
	}
}

func TestTrimElidesUselessResultAfterAssistant(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "find"},
		{Role: "assistant", Content: "searching", ToolCalls: []llm.ToolCall{toolCall("c1", "grep", `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: "(no matches)"},
		{Role: "assistant", Content: "nothing found"},
	}
	meta := map[string]ToolMeta{"c1": {Useless: true}}
	result := Trim(messages, TrimOptions{ToolMeta: meta})
	if got := result.Messages[3].Content; got != uselessStub {
		t.Fatalf("useless result = %q, want %q", got, uselessStub)
	}

	// 没有更晚的助手轮次，模型就还没有消费该结果。
	shorter := messages[:4]
	result = Trim(shorter, TrimOptions{ToolMeta: meta})
	if got := result.Messages[3].Content; got != "(no matches)" {
		t.Fatalf("unconsumed result changed to %q", got)
	}
}

func TestLoopTrimsOldToolOutput(t *testing.T) {
	big := strings.Repeat("y", 4000)
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(context.Context, string, string) (string, error) {
			return big, nil
		},
	}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		if n < 5 {
			return toolReply(toolCall(fmt.Sprintf("c%d", n), "read", `{}`)), nil
		}
		return textReply("final"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         6,
		MaxContextTokens: 900,
		KeepTurns:        1,
		KeepToolTokens:   1,
	}}

	res, err := runner.Run(context.Background(), "explore")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "final" {
		t.Fatalf("text = %q", res.Text)
	}
	trimmed := false
	for _, msg := range res.Messages {
		if strings.HasPrefix(msg.Content, trimmedPrefix) {
			trimmed = true
		}
	}
	if !trimmed {
		t.Fatal("no old tool result was trimmed")
	}
	for _, request := range chat.requestsSeen() {
		if request[0].Role != "system" {
			t.Fatal("system prompt missing from a request")
		}
		if request[1].Role != "user" || request[1].Content != "explore" {
			t.Fatal("first user message missing from a request")
		}
	}
}

// cacheGuardFixture 构建一段对话，其旧工具结果（索引 3）位于一个大尾部之前，因此其缓存后缀至少与 tailTokens 一样宽。
// （接上行）
// 让尾部可配置，使测试无需巨大样本就能同时演练默认防护（12000）和提高后的防护。
// （接上行）
func cacheGuardFixture(tailTokens int) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "do work"},
		{Role: "assistant", Content: "old step", ToolCalls: []llm.ToolCall{toolCall("old", "read", `{}`)}},
		{Role: "tool", ToolCallID: "old", Content: strings.Repeat("x", 4000)},
		{Role: "assistant", Content: "recent step", ToolCalls: []llm.ToolCall{toolCall("recent", "read", `{}`)}},
		{Role: "tool", ToolCallID: "recent", Content: strings.Repeat("y", 2000)},
		{Role: "assistant", Content: strings.Repeat("z", tailTokens*tokenize.CharsPerToken)},
	}
}

// TestTrimBudgetGuardKeepsWarmPrefixToolResult 验证预算循环拒绝替换缓存后缀超过默认 12000 token 防护的结果。
// （接上行）
func TestTrimBudgetGuardKeepsWarmPrefixToolResult(t *testing.T) {
	messages := cacheGuardFixture(12500) // suffix(3) 约 13k > 默认 12000
	opts := TrimOptions{MaxTokens: 500, KeepTurns: 1, KeepToolTokens: 100}

	result := Trim(messages, opts)
	if got := result.Messages[3].Content; strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("budget trim rewrote a tool result inside the warm prefix: %q", got)
	}

	// 提高防护必须使同一样本替换该结果，证明是防护（而非缺乏预算压力）让它保持完整。
	// （接上行）
	opts.CacheProtectSuffixTokens = 100000
	unguarded := Trim(messages, opts)
	if got := unguarded.Messages[3].Content; !strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("unguarded budget trim = %q, want a stub", got)
	}
}

// TestTrimCacheGuardConsistentAcrossPaths 检查在防护生效时，语义（meta）路径和预算路径都让温热前缀结果保持原样，
// 且在防护解除后两者都会重写它。
// （接上行）
func TestTrimCacheGuardConsistentAcrossPaths(t *testing.T) {
	messages := cacheGuardFixture(12500)
	// 两个读取结果共享一个取代键；较新的一个取代索引 3。
	meta := map[string]ToolMeta{
		"old":    {SupersedeKey: "read|a.go"},
		"recent": {SupersedeKey: "read|a.go"},
	}

	semantic := Trim(messages, TrimOptions{ToolMeta: meta})
	if got := semantic.Messages[3].Content; got == supersededStub || strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("meta reclaim rewrote a warm-prefix result: %q", got)
	}

	budget := Trim(messages, TrimOptions{MaxTokens: 500, KeepTurns: 1, KeepToolTokens: 100})
	if got := budget.Messages[3].Content; strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("budget trim rewrote a warm-prefix result: %q", got)
	}

	unguardedMeta := Trim(messages, TrimOptions{ToolMeta: meta, CacheProtectSuffixTokens: 100000})
	if got := unguardedMeta.Messages[3].Content; got != supersededStub {
		t.Fatalf("unguarded meta reclaim = %q, want %q", got, supersededStub)
	}
	unguardedBudget := Trim(messages, TrimOptions{MaxTokens: 500, KeepTurns: 1, KeepToolTokens: 100, CacheProtectSuffixTokens: 100000})
	if got := unguardedBudget.Messages[3].Content; !strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("unguarded budget trim = %q, want a stub", got)
	}
}

func TestPruneToolResultMiddleKeepsHeadAndTail(t *testing.T) {
	head := strings.Repeat("H", toolResultPruneHead+10)
	tail := strings.Repeat("T", toolResultPruneTail+10)
	content := head + strings.Repeat("m", 5000) + tail

	pruned := pruneToolResultMiddle(content)
	if pruned == content {
		t.Fatal("expected an over-budget result to be pruned")
	}
	idx := strings.Index(pruned, toolResultPruneMarker)
	if idx < 0 {
		t.Fatalf("pruned result is missing the marker: %q", pruned)
	}
	if got, want := pruned[:idx], strings.Repeat("H", toolResultPruneHead); got != want {
		t.Fatalf("retained head = %d runes, want %d", len([]rune(got)), len([]rune(want)))
	}
	after := pruned[idx+len(toolResultPruneMarker):]
	if got, want := after, strings.Repeat("T", toolResultPruneTail); got != want {
		t.Fatalf("retained tail = %d runes, want %d", len([]rune(got)), len([]rune(want)))
	}
	if len([]rune(pruned)) >= len([]rune(content)) {
		t.Fatal("pruned result must be smaller than the original")
	}
}

func TestPruneToolResultMiddleUnderBudget(t *testing.T) {
	for _, content := range []string{
		"short",
		strings.Repeat("x", toolResultPruneThreshold-1),
		strings.Repeat("x", toolResultPruneThreshold),
	} {
		if got := pruneToolResultMiddle(content); got != content {
			t.Errorf("under-budget content changed: %d -> %d runes", len([]rune(content)), len([]rune(got)))
		}
	}
}

func TestPruneToolResultMiddleIsRuneSafe(t *testing.T) {
	// 把一个 4 字节 emoji 作为最后保留的头部 rune，一个 CJK 汉字作为最先保留的尾部 rune。按字节切片会同时切断两者。
	// （接上行）
	head := strings.Repeat("a", toolResultPruneHead-1) + "🚀"
	tail := "界" + strings.Repeat("b", toolResultPruneTail-1)
	content := head + strings.Repeat("中", 4000) + tail

	pruned := pruneToolResultMiddle(content)
	if !utf8.ValidString(pruned) {
		t.Fatal("pruned result is not valid UTF-8")
	}
	if strings.ContainsRune(pruned, utf8.RuneError) {
		t.Fatal("pruned result contains a replacement rune: a UTF-8 sequence was cut")
	}
	idx := strings.Index(pruned, toolResultPruneMarker)
	if idx < 0 {
		t.Fatal("marker missing from pruned result")
	}
	before := []rune(pruned[:idx])
	if len(before) == 0 || string(before[len(before)-1]) != "🚀" {
		t.Fatal("head boundary does not end with the intact emoji")
	}
	after := []rune(pruned[idx+len(toolResultPruneMarker):])
	if len(after) == 0 || string(after[0]) != "界" {
		t.Fatal("tail boundary does not start with the intact CJK rune")
	}
}

func TestTrimStubKeepsHeadAndTailForLargeResult(t *testing.T) {
	head := strings.Repeat("H", toolResultPruneHead+50)
	tail := strings.Repeat("T", toolResultPruneTail+50)
	big := head + strings.Repeat("m", 4000) + tail
	messages := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", ToolCalls: []llm.ToolCall{toolCall("c1", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "b", ToolCalls: []llm.ToolCall{toolCall("c2", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c2", Content: big},
		{Role: "assistant", Content: "c"},
	}
	opts := TrimOptions{
		MaxTokens:                100,
		KeepTurns:                1,
		KeepToolTokens:           1,
		CacheProtectSuffixTokens: 1 << 30,
	}
	result := Trim(messages, opts)

	stub := result.Messages[3].Content
	if !strings.HasPrefix(stub, trimmedPrefix) {
		t.Fatalf("old tool result = %q, want a stub", stub)
	}
	if !strings.Contains(stub, toolResultPruneMarker) {
		t.Fatal("stub dropped the head/tail details")
	}
	if !strings.Contains(stub, strings.Repeat("H", toolResultPruneHead)) {
		t.Fatal("stub does not retain the head")
	}
	if !strings.Contains(stub, strings.Repeat("T", toolResultPruneTail)) {
		t.Fatal("stub does not retain the tail")
	}
	if got := result.Messages[5].Content; got != big {
		t.Fatal("most recent tool result must stay verbatim")
	}
}

// trimmedBytes 是裁剪后记录的确定性指纹。Trim 只重写工具结果主体，因此对消息字段计数能在保持 O(messages) 的同时捕获任何行为漂移。
// （接上行）
// （接上行）
func trimmedBytes(messages []llm.Message) int {
	n := 0
	for _, m := range messages {
		n += len(m.Role) + len(m.Content) + len(m.Reasoning) + len(m.ToolCallID)
		for _, call := range m.ToolCalls {
			n += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return n
}

// referenceReclaimBytes 逐字镜像原始扫描式 reclaimByMeta 的查询。它是基准测试的判据：预计算表必须产生完全相同的记录，
// 而不只是更快的记录。
// （接上行）
func referenceReclaimBytes(messages []llm.Message, meta map[string]ToolMeta, protect int) int {
	newestSuccessful := make(map[string]int)
	for i, msg := range messages {
		if msg.Role != "tool" || isErrorResult(msg.Content) {
			continue
		}
		if key := meta[msg.ToolCallID].SupersedeKey; key != "" {
			newestSuccessful[key] = i
		}
	}
	work := append([]llm.Message(nil), messages...)
	for i, msg := range messages {
		if msg.Role != "tool" || isErrorResult(msg.Content) {
			continue
		}
		if msg.Content == supersededStub || msg.Content == uselessStub || strings.HasPrefix(msg.Content, trimmedPrefix) {
			continue
		}
		m := meta[msg.ToolCallID]
		if m == (ToolMeta{}) {
			continue
		}
		stub := ""
		if m.SupersedeKey != "" {
			if later, ok := newestSuccessful[m.SupersedeKey]; ok && later > i {
				stub = supersededStub
			}
		}
		if stub == "" && m.Useless {
			for j := i + 1; j < len(work); j++ {
				if work[j].Role == "assistant" {
					stub = uselessStub
					break
				}
			}
		}
		if stub == "" {
			continue
		}
		suffix := 0
		for j := i + 1; j < len(work); j++ {
			suffix += EstimateMessage(work[j])
		}
		if suffix > protect {
			continue
		}
		replacement := msg
		replacement.Content = stub
		work[i] = replacement
	}
	return trimmedBytes(work)
}

// BenchmarkReclaimByMetaWithLargeToolMeta 检查行为（对照原始 O(n^2) 算法）和预计算后缀/助手表的伸缩性。输入是确定的，因此 ns/op 应大致随 n 线性增长；不断言墙上时钟阈值，只有 b.N 子基准。
// （接上行）
// （接上行）
// （接上行）
func BenchmarkReclaimByMetaWithLargeToolMeta(b *testing.B) {
	const protect = 1 << 30 // 让每个较旧的结果都能被回收
	body := strings.Repeat("x", 16)
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			messages := make([]llm.Message, 0, 2*n+3)
			messages = append(messages,
				llm.Message{Role: "system", Content: "rules"},
				llm.Message{Role: "user", Content: "inspect"},
			)
			meta := make(map[string]ToolMeta, n)
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("c%d", i)
				messages = append(messages,
					llm.Message{Role: "assistant", Content: "step", ToolCalls: []llm.ToolCall{toolCall(id, "read", `{}`)}},
					llm.Message{Role: "tool", ToolCallID: id, Content: body},
				)
				if i%2 == 0 {
					meta[id] = ToolMeta{SupersedeKey: "read|shared"}
				} else {
					meta[id] = ToolMeta{Useless: true}
				}
			}
			messages = append(messages, llm.Message{Role: "assistant", Content: "done"})

			want := referenceReclaimBytes(messages, meta, protect)
			opts := TrimOptions{ToolMeta: meta, CacheProtectSuffixTokens: protect}
			if got := trimmedBytes(Trim(messages, opts).Messages); got != want {
				b.Fatalf("reclaim output bytes = %d, want %d", got, want)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := trimmedBytes(Trim(messages, opts).Messages); got != want {
					b.Fatalf("iteration %d: reclaim output bytes = %d, want %d", i, got, want)
				}
			}
		})
	}
}

// ratioFixture 构建一段包含 n 个工具结果的对话，每个都携带相同主体。除最新的以外每个结果都是裁剪候选；调用方禁用缓存防护，以隔离比率行为。
// （接上行）
// （接上行）
func ratioFixture(n int, body string) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "q"},
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: "step", ToolCalls: []llm.ToolCall{toolCall(id, "read", `{}`)}},
			llm.Message{Role: "tool", ToolCallID: id, Content: body},
		)
	}
	msgs = append(msgs, llm.Message{Role: "assistant", Content: "final"})
	return msgs
}

// oneTrimFreed 是替换一个主体所节省的估算 token。
func oneTrimFreed(body string) int {
	before := llm.Message{Role: "tool", Content: body}
	after := before
	after.Content = toolStub(body)
	return EstimateMessage(before) - EstimateMessage(after)
}

func ratioTrimOpts(max int, ratio float64) TrimOptions {
	return TrimOptions{
		MaxTokens:                max,
		KeepTurns:                1,
		KeepToolTokens:           1,
		CacheProtectSuffixTokens: 1 << 30,
		TargetRatio:              ratio,
	}
}

func TestBudgetTarget(t *testing.T) {
	cases := []struct {
		name  string
		max   int
		ratio float64
		want  int
	}{
		{"zero keeps max", 1000, 0, 1000},
		{"negative keeps max", 1000, -0.5, 1000},
		{"one keeps max", 1000, 1, 1000},
		{"above one keeps max", 1000, 1.2, 1000},
		{"seventy percent", 1000, 0.7, 700},
		{"three quarters", 1000, 0.75, 750},
		{"tiny clamps to one", 1, 0.1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := budgetTarget(tc.max, tc.ratio); got != tc.want {
				t.Fatalf("budgetTarget(%d, %v) = %d, want %d", tc.max, tc.ratio, got, tc.want)
			}
		})
	}
}

// TestTrimTargetRatioLeavesHeadroom 验证 P0 迟滞：触发点保持在 MaxTokens，但循环裁剪到其下方，使下一轮次在再次触发裁剪并破坏提示缓存之前有增长空间。
// （接上行）
// （接上行）
func TestTrimTargetRatioLeavesHeadroom(t *testing.T) {
	body := strings.Repeat("x", 2000)
	msgs := ratioFixture(3, body)
	freed := oneTrimFreed(body)
	if freed <= 0 {
		t.Fatalf("fixture does not free tokens when stubbed: %d", freed)
	}
	// max 恰好略高于一次替换达到的水平：默认在第一次裁剪后停止，而 0.7 目标必须再回收第二个结果。
	// （接上行）
	max := EstimateMessages(msgs) - freed + 1
	const ratio = 0.7
	target := int(float64(max) * ratio)

	defaultRes := Trim(msgs, ratioTrimOpts(max, 0))
	hysteretic := Trim(msgs, ratioTrimOpts(max, ratio))

	if defaultRes.Pruned != 1 {
		t.Fatalf("default pruned = %d, want 1", defaultRes.Pruned)
	}
	if hysteretic.Pruned != 2 {
		t.Fatalf("ratio pruned = %d, want 2 (more thorough)", hysteretic.Pruned)
	}
	defaultAfter := EstimateMessages(defaultRes.Messages)
	hystereticAfter := EstimateMessages(hysteretic.Messages)
	if defaultAfter <= target {
		t.Fatalf("precondition: default trim already below target (%d <= %d)", defaultAfter, target)
	}
	if hystereticAfter > target {
		t.Fatalf("ratio trim = %d, want <= target %d (headroom below %d)", hystereticAfter, target, max)
	}
	if hystereticAfter >= defaultAfter {
		t.Fatalf("ratio trim (%d) must be smaller than default (%d)", hystereticAfter, defaultAfter)
	}
}

// TestTrimTargetRatioDoesNotOverTrim 检查循环一旦达到目标就停止，让其他候选保持原样。
// （接上行）
func TestTrimTargetRatioDoesNotOverTrim(t *testing.T) {
	body := strings.Repeat("x", 2000)
	msgs := ratioFixture(4, body)
	max := EstimateMessages(msgs) - 1
	const ratio = 0.7
	target := int(float64(max) * ratio)
	res := Trim(msgs, ratioTrimOpts(max, ratio))

	if got := EstimateMessages(res.Messages); got > target {
		t.Fatalf("trim = %d, want <= target %d", got, target)
	}
	if res.Pruned >= 3 {
		t.Fatalf("pruned %d results, want fewer than the 3 candidates", res.Pruned)
	}
	// 最旧的候选（c0，索引 3）保持原样：达到目标后裁剪就停止了，而不是遍历每个候选。
	// （接上行）
	if got := res.Messages[3].Content; got != body {
		t.Fatalf("oldest tool result was over-trimmed: %q", got)
	}
}

// TestTrimTargetRatioZeroPreservesDefault 锁定向后兼容性：零值和任何 >= 1 的比例必须与旧的精确裁剪到 MaxTokens 的行为无法区分。
// （接上行）
// （接上行）
func TestTrimTargetRatioZeroPreservesDefault(t *testing.T) {
	body := strings.Repeat("x", 2000)
	msgs := ratioFixture(3, body)
	max := EstimateMessages(msgs) - 1

	baseline := Trim(msgs, ratioTrimOpts(max, 0))
	for _, ratio := range []float64{0, 1, 1.5, -1} {
		got := Trim(msgs, ratioTrimOpts(max, ratio))
		if got.Pruned != baseline.Pruned || got.Freed != baseline.Freed {
			t.Fatalf("ratio=%v: pruned/freed = %d/%d, baseline %d/%d", ratio, got.Pruned, got.Freed, baseline.Pruned, baseline.Freed)
		}
		if n, want := trimmedBytes(got.Messages), trimmedBytes(baseline.Messages); n != want {
			t.Fatalf("ratio=%v changed the transcript: %d vs %d", ratio, n, want)
		}
	}
}

// TestTrimTargetRatioWithCJK 用宽 rune 演练比率目标，EstimateTokens 对宽 rune 逐个计数而非按 charsPerToken。
// （接上行）
func TestTrimTargetRatioWithCJK(t *testing.T) {
	body := strings.Repeat("界", 900) // 900 个宽 rune 约等于 900 token
	msgs := ratioFixture(3, body)
	freed := oneTrimFreed(body)
	if freed <= 0 {
		t.Fatalf("CJK body does not free tokens: %d", freed)
	}
	max := EstimateMessages(msgs) - freed + 1
	const ratio = 0.7
	target := int(float64(max) * ratio)
	res := Trim(msgs, ratioTrimOpts(max, ratio))

	if res.Pruned != 2 {
		t.Fatalf("CJK ratio pruned = %d, want 2", res.Pruned)
	}
	if got := EstimateMessages(res.Messages); got > target {
		t.Fatalf("CJK trim = %d, want <= target %d", got, target)
	}
	for _, m := range res.Messages {
		if !utf8.ValidString(m.Content) || strings.ContainsRune(m.Content, utf8.RuneError) {
			t.Fatalf("invalid UTF-8 after trimming CJK content: %q", m.Content)
		}
	}
}

// TestRunStateTrimUsesHeadroomRatio 验证循环把迟滞比率接入它构建的 TrimOptions，使每次预算裁剪都留出余量，而非精确裁到阈值
// （那会在下一轮再次裁剪并破坏提示缓存）。
// （接上行）
// （接上行）
func TestRunStateTrimUsesHeadroomRatio(t *testing.T) {
	state := newRunState(Options{MaxContextTokens: 1000}, "q", func(Event) {})
	if got := state.trim.TargetRatio; got != defaultTrimTargetRatio {
		t.Fatalf("TrimOptions.TargetRatio = %v, want %v", got, defaultTrimTargetRatio)
	}
}

// TestLoopTrimLeavesHeadroom 是端到端对应：循环预算裁剪触发后，实际发送给模型的对话降到配置上下文预算的约 70%。小而多的工具结果使裁剪足够细粒度，
// 以至于 1.0 的比率（精确裁到阈值）会停留在上限之上，因此如果接线被移除，这个测试会失败。
// （接上行）
// （接上行）
// （接上行）
func TestLoopTrimLeavesHeadroom(t *testing.T) {
	const (
		maxContext = 3000
		numCalls   = 30
	)
	body := strings.Repeat("x", 400) // 每个工具结果约 100 估算 token
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(context.Context, string, string) (string, error) {
			return body, nil
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n <= numCalls {
			// 不同的参数使重复防护器和卡住检测器保持静默。
			return toolReply(toolCall(fmt.Sprintf("c%d", n), "read", fmt.Sprintf(`{"n":%d}`, n))), nil
		}
		return textReply("final"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:            "m",
		System:           "rules",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         numCalls + 2,
		MaxContextTokens: maxContext,
		KeepTurns:        1,
		KeepToolTokens:   1,
		MaxRepeats:       numCalls + 1,
	}}

	res, err := runner.Run(context.Background(), "explore")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "final" {
		t.Fatalf("text = %q, want final", res.Text)
	}

	ceiling := int(float64(maxContext) * defaultTrimTargetRatio)
	trimmedRequests := 0
	smallest := int(^uint(0) >> 1)
	for _, request := range chat.requestsSeen() {
		hasStub := false
		for _, msg := range request {
			if strings.HasPrefix(msg.Content, trimmedPrefix) {
				hasStub = true
				break
			}
		}
		if hasStub {
			trimmedRequests++
			if est := EstimateMessages(request); est < smallest {
				smallest = est
			}
		}
	}
	if trimmedRequests == 0 {
		t.Fatal("no trimmed request was sent to the model")
	}
	if smallest > ceiling {
		t.Fatalf("smallest trimmed request = %d tokens, want <= %d (%.0f%% of %d)",
			smallest, ceiling, defaultTrimTargetRatio*100, maxContext)
	}
}

// oldPruneToolResultMiddle 内联 pruneToolResultMiddle 在字素吸附之前的公式，
// 仅用于差分测试：当切点落在字素簇边界时，新实现必须与它逐字节相等。
func oldPruneToolResultMiddle(content string) string {
	runes := []rune(content)
	if len(runes) <= toolResultPruneThreshold {
		return content
	}
	if toolResultPruneHead+toolResultPruneTail >= len(runes) {
		return content
	}
	var b strings.Builder
	b.Grow(len(content) + len(toolResultPruneMarker))
	b.WriteString(string(runes[:toolResultPruneHead]))
	b.WriteString(toolResultPruneMarker)
	b.WriteString(string(runes[len(runes)-toolResultPruneTail:]))
	return b.String()
}

// randMixedRunes 生成恰好 n 个 rune 的随机混合文本，素材覆盖 ASCII/CJK/
// emoji/组合符/ZWJ/旗帜/畸形字节。畸形字节按 utf8 解码口径每个算一个 rune。
func randMixedRunes(rng *rand.Rand, n int) string {
	atoms := []string{
		"a", "Z", "9",
		"中", "界",
		"😀", "🚀",
		"e\u0301", "a\u0301",
		"👨‍👩‍👧",
		"🇨🇳", "🇺🇸",
		"\xFF", "\xC3",
	}
	var b strings.Builder
	for remaining := n; remaining > 0; {
		atom := atoms[rng.Intn(len(atoms))]
		r := utf8.RuneCountInString(atom)
		if r > remaining {
			atom = "a"
			r = 1
		}
		b.WriteString(atom)
		remaining -= r
	}
	return b.String()
}

// TestPruneByteIdenticalAtClusterBoundary 锁定"默认逐字节不变"：固定种子随机
// 文本在 head/tail 切点都落在字素簇边界（由两侧的安全单 rune 字素保证）时，
// pruneToolResultMiddle 必须与旧的 rune 公式逐字节相等。
func TestPruneByteIdenticalAtClusterBoundary(t *testing.T) {
	const iterations = 200
	rng := rand.New(rand.NewSource(20261121))
	for i := 0; i < iterations; i++ {
		mid := 50 + rng.Intn(200)
		// head 末尾与 middle 开头、middle 末尾与 tail 开头都是 'a'，
		// 两侧不粘合，因此 rune 4096 与 len-1024 都是字素簇边界。
		content := randMixedRunes(rng, toolResultPruneHead-1) + "a" + "a" +
			randMixedRunes(rng, mid) + "a" + "a" +
			randMixedRunes(rng, toolResultPruneTail-1)

		runes := []rune(content)
		encoded := string(runes)
		headByte := runeByteOffset(encoded, toolResultPruneHead)
		tailByte := runeByteOffset(encoded, len(runes)-toolResultPruneTail)
		if textutil.ClusterBoundaryBefore(encoded, headByte) != headByte ||
			textutil.ClusterBoundaryAfter(encoded, tailByte) != tailByte {
			t.Fatalf("iteration %d: fixture cut is not at a cluster boundary", i)
		}

		got := pruneToolResultMiddle(content)
		want := oldPruneToolResultMiddle(content)
		if got != want {
			t.Fatalf("iteration %d: default path changed bytes at a cluster boundary:\n got: %q\nwant: %q", i, got, want)
		}
	}
}

// contentWithClusterAtCuts 把一个多 rune 字素簇精确摆在 head 切点（rune 4096）
// 和 tail 切点（rune len-1024）上，使两个切点都落在簇中间。
func contentWithClusterAtCuts(cluster string) string {
	kc := utf8.RuneCountInString(cluster)
	// 中间段足够长，使总 rune 数超过 toolResultPruneThreshold，确保真正裁剪。
	const mid = toolResultPruneThreshold
	return strings.Repeat("a", toolResultPruneHead-1) + cluster +
		strings.Repeat("m", mid) + cluster +
		strings.Repeat("z", toolResultPruneTail+1-kc)
}

// TestPruneSnapsGraphemeBoundaries 验证多 rune 字素簇横跨切点时，head 回退、
// tail 前进，使两端都不残留悬空字素；无粘合理由时单 rune 字素不受影响。
func TestPruneSnapsGraphemeBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		cluster string
	}{
		{"ZWJ emoji", "👨‍👩‍👧"},
		{"regional indicator flag", "🇨🇳"},
		{"combining mark", "e\u0301"},
		{"hangul jamo", "각"},
		{"CRLF", "\r\n"},
		{"emoji skin tone", "👍🏽"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := contentWithClusterAtCuts(tc.cluster)
			kc := utf8.RuneCountInString(tc.cluster)
			pruned := pruneToolResultMiddle(content)

			if !utf8.ValidString(pruned) {
				t.Fatalf("pruned result is not valid UTF-8: %q", pruned)
			}
			if strings.ContainsRune(pruned, utf8.RuneError) {
				t.Fatalf("pruned result contains a replacement rune: %q", pruned)
			}
			if n := strings.Count(pruned, toolResultPruneMarker); n != 1 {
				t.Fatalf("marker appears %d times, want 1: %q", n, pruned)
			}

			idx := strings.Index(pruned, toolResultPruneMarker)
			head, tail := pruned[:idx], pruned[idx+len(toolResultPruneMarker):]
			wantHead := strings.Repeat("a", toolResultPruneHead-1)
			wantTail := strings.Repeat("z", toolResultPruneTail+1-kc)
			if head != wantHead {
				t.Fatalf("head = %q, want %d safe runes (no dangling grapheme)", head, toolResultPruneHead-1)
			}
			if tail != wantTail {
				t.Fatalf("tail = %q, want %d safe runes (no dangling grapheme)", tail, toolResultPruneTail+1-kc)
			}

			// 旧公式会把簇切成两半；新实现必须与之不同，证明吸附确实生效。
			if pruned == oldPruneToolResultMiddle(content) {
				t.Fatal("grapheme snapping did not change the mid-cluster cut")
			}
		})
	}
}
