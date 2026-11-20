package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
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

// trimFixture builds a conversation with an old tool result, a middle turn and
// the newest turn, all small except the tool bodies.
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
	// Trimming twice must not re-stub an existing stub.
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
		{"你好世界", 4},                 // 4 wide runes, one token each
		{"hello你好", 4},              // 5 narrow -> 2, plus 2 wide
		{strings.Repeat("界", 8), 8}, // no chars/4 under-estimate for CJK
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

// metaFixture builds a conversation with an early read of a.go, a successful
// later read of the same file and a final assistant turn.
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
	// Pairing: every tool result still follows its assistant turn.
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

	// Without a later assistant turn the model has not consumed the result.
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

// cacheGuardFixture builds a conversation whose old tool result (index 3) sits
// in front of a large tail, so its cached suffix is at least tailTokens wide.
// Keeping the tail configurable lets the tests exercise both the default guard
// (12000) and a raised guard without huge fixtures.
func cacheGuardFixture(tailTokens int) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "do work"},
		{Role: "assistant", Content: "old step", ToolCalls: []llm.ToolCall{toolCall("old", "read", `{}`)}},
		{Role: "tool", ToolCallID: "old", Content: strings.Repeat("x", 4000)},
		{Role: "assistant", Content: "recent step", ToolCalls: []llm.ToolCall{toolCall("recent", "read", `{}`)}},
		{Role: "tool", ToolCallID: "recent", Content: strings.Repeat("y", 2000)},
		{Role: "assistant", Content: strings.Repeat("z", tailTokens*charsPerToken)},
	}
}

// TestTrimBudgetGuardKeepsWarmPrefixToolResult verifies the budget loop refuses
// to stub a result whose cached suffix exceeds the default 12000-token guard.
func TestTrimBudgetGuardKeepsWarmPrefixToolResult(t *testing.T) {
	messages := cacheGuardFixture(12500) // suffix(3) ~= 13k > default 12000
	opts := TrimOptions{MaxTokens: 500, KeepTurns: 1, KeepToolTokens: 100}

	result := Trim(messages, opts)
	if got := result.Messages[3].Content; strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("budget trim rewrote a tool result inside the warm prefix: %q", got)
	}

	// Raising the guard must make the same fixture stub that result, proving the
	// guard — not a lack of budget pressure — kept it intact.
	opts.CacheProtectSuffixTokens = 100000
	unguarded := Trim(messages, opts)
	if got := unguarded.Messages[3].Content; !strings.HasPrefix(got, trimmedPrefix) {
		t.Fatalf("unguarded budget trim = %q, want a stub", got)
	}
}

// TestTrimCacheGuardConsistentAcrossPaths checks that under the guard the
// semantic (meta) path and the budget path both leave a warm-prefix result
// untouched, and that both rewrite it once the guard is lifted.
func TestTrimCacheGuardConsistentAcrossPaths(t *testing.T) {
	messages := cacheGuardFixture(12500)
	// Both read results share a supersede key; the newer one supersedes index 3.
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
	// Put a 4-byte emoji as the last retained head rune and a CJK ideograph as
	// the first retained tail rune. Byte-based slicing would split both.
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

// trimmedBytes is a deterministic fingerprint of a trimmed transcript. Trim
// only rewrites tool result bodies, so counting message fields catches any
// behaviour drift while staying O(messages) inside b.N.
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

// referenceReclaimBytes mirrors the original scanning reclaimByMeta queries
// verbatim. It is the benchmark's oracle: the precomputed tables must produce
// exactly the same transcript, not merely a faster one.
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

// BenchmarkReclaimByMetaWithLargeToolMeta checks both behaviour (against the
// original O(n^2) algorithm) and scaling of the precomputed suffix/assistant
// tables. Inputs are deterministic, so ns/op should grow roughly linearly with
// n; no wall-clock threshold is asserted, only b.N sub-benchmarks.
func BenchmarkReclaimByMetaWithLargeToolMeta(b *testing.B) {
	const protect = 1 << 30 // let every older result be reclaimed
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

// ratioFixture builds a conversation with n tool results, each carrying the
// same body. Every result except the newest is a trimming candidate; the cache
// guard is disabled by the caller so the ratio behaviour is isolated.
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

// oneTrimFreed is the estimated token saving from stubbing one body.
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

// TestTrimTargetRatioLeavesHeadroom verifies the P0 hysteresis: the trigger
// stays at MaxTokens but the loop trims below it, so the next turn has room to
// grow before trimming fires again and busts the prompt cache.
func TestTrimTargetRatioLeavesHeadroom(t *testing.T) {
	body := strings.Repeat("x", 2000)
	msgs := ratioFixture(3, body)
	freed := oneTrimFreed(body)
	if freed <= 0 {
		t.Fatalf("fixture does not free tokens when stubbed: %d", freed)
	}
	// max sits just above the level reached by one stub: the default stops
	// after one prune, while a 0.7 target has to reclaim a second result.
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

// TestTrimTargetRatioDoesNotOverTrim checks the loop stops as soon as the
// target is met, leaving further candidates verbatim.
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
	// The oldest candidate (c0, index 3) stays verbatim: trimming stopped once
	// the target was reached instead of walking every candidate.
	if got := res.Messages[3].Content; got != body {
		t.Fatalf("oldest tool result was over-trimmed: %q", got)
	}
}

// TestTrimTargetRatioZeroPreservesDefault locks in backward compatibility:
// the zero value and any ratio >= 1 must be indistinguishable from the old
// behaviour of trimming exactly to MaxTokens.
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

// TestTrimTargetRatioWithCJK exercises the ratio target with wide runes, which
// EstimateTokens counts individually rather than at charsPerToken.
func TestTrimTargetRatioWithCJK(t *testing.T) {
	body := strings.Repeat("界", 900) // 900 wide runes ~= 900 tokens
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
