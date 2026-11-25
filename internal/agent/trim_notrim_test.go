package agent

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// noTrimPreview 为失败信息生成一个定长 runes 预览，避免把整段大主体打进日志。
func noTrimPreview(s string) string {
	const max = 24
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// noTrimFixture 构建一段包含三个旧工具结果的对话：c3 因 KeepToolTokens 被保留为最近输出，
// c1 与 c2 是预算裁剪的两个候选。调用方通过 CacheProtectSuffixTokens 关闭缓存防护以隔离 no-trim 行为。
func noTrimFixture() []llm.Message {
	big := strings.Repeat("x", 4000)
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", ToolCalls: []llm.ToolCall{toolCall("c1", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "b", ToolCalls: []llm.ToolCall{toolCall("c2", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c2", Content: big},
		{Role: "assistant", Content: "c", ToolCalls: []llm.ToolCall{toolCall("c3", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c3", Content: big},
		{Role: "assistant", Content: "done"},
	}
}

func noTrimBudgetOpts(noTrim map[string]bool) TrimOptions {
	return TrimOptions{
		MaxTokens:                100,
		KeepTurns:                1,
		KeepToolTokens:           1,
		CacheProtectSuffixTokens: 1 << 30, // 关闭缓存防护，隔离 no-trim 行为
		NoTrimToolCallIDs:        noTrim,
	}
}

// TestTrimNoTrimToolCallIDsBudgetPath 验证预算裁剪路径：nil/空集合保持旧行为，
// 命中集合的结果及其中的恢复标记原样保留，未命中的候选仍被裁剪。
func TestTrimNoTrimToolCallIDsBudgetPath(t *testing.T) {
	bigContent := strings.Repeat("x", 4000)
	cases := []struct {
		name       string
		noTrim     map[string]bool
		wantC1Stub bool
		wantC2Stub bool
		wantPruned int
	}{
		{"nil keeps legacy behavior", nil, true, true, 2},
		{"empty map keeps legacy behavior", map[string]bool{}, true, true, 2},
		{"false entry does not protect", map[string]bool{"c1": false}, true, true, 2},
		{"protect c1", map[string]bool{"c1": true}, false, true, 1},
		{"protect c2", map[string]bool{"c2": true}, true, false, 1},
		{"protect both", map[string]bool{"c1": true, "c2": true}, false, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Trim(noTrimFixture(), noTrimBudgetOpts(tc.noTrim))

			c1Stub := strings.HasPrefix(res.Messages[3].Content, trimmedPrefix)
			c2Stub := strings.HasPrefix(res.Messages[5].Content, trimmedPrefix)
			if c1Stub != tc.wantC1Stub {
				t.Errorf("c1 stub = %v, want %v (content %q)", c1Stub, tc.wantC1Stub, noTrimPreview(res.Messages[3].Content))
			}
			if c2Stub != tc.wantC2Stub {
				t.Errorf("c2 stub = %v, want %v (content %q)", c2Stub, tc.wantC2Stub, noTrimPreview(res.Messages[5].Content))
			}
			if res.Pruned != tc.wantPruned {
				t.Errorf("pruned = %d, want %d", res.Pruned, tc.wantPruned)
			}
			// 被保护的结果内容必须逐字节不变（含其中的恢复标记）。
			if !tc.wantC1Stub && res.Messages[3].Content != bigContent {
				t.Error("protected c1 content changed")
			}
			if !tc.wantC2Stub && res.Messages[5].Content != bigContent {
				t.Error("protected c2 content changed")
			}
			// 最近工具输出 c3 始终逐字保留。
			if got := res.Messages[7].Content; got != bigContent {
				t.Errorf("most recent tool result changed to %q", got)
			}
		})
	}
}

func noTrimMetaOpts(noTrim map[string]bool) TrimOptions {
	return TrimOptions{
		ToolMeta: map[string]ToolMeta{
			"c1": {SupersedeKey: "read|a.go"},
			"c2": {SupersedeKey: "read|a.go"},
		},
		CacheProtectSuffixTokens: 1 << 30,
		NoTrimToolCallIDs:        noTrim,
	}
}

// TestTrimNoTrimToolCallIDsReclaimPath 验证元数据回收路径：nil 保持旧行为，
// 被保护的旧读取不会被 supersededStub 替换，未受保护时仍按旧行为回收。
func TestTrimNoTrimToolCallIDsReclaimPath(t *testing.T) {
	bigContent := strings.Repeat("x", 400)
	cases := []struct {
		name       string
		noTrim     map[string]bool
		wantC1Stub bool
		wantPruned int
	}{
		{"nil keeps legacy behavior", nil, true, 1},
		{"empty map keeps legacy behavior", map[string]bool{}, true, 1},
		{"protect old read", map[string]bool{"c1": true}, false, 0},
		{"protect newer read does not affect old", map[string]bool{"c2": true}, true, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Trim(metaFixture(), noTrimMetaOpts(tc.noTrim))

			gotStub := res.Messages[3].Content == supersededStub
			if gotStub != tc.wantC1Stub {
				t.Errorf("c1 superseded = %v, want %v (content %q)", gotStub, tc.wantC1Stub, noTrimPreview(res.Messages[3].Content))
			}
			if res.Pruned != tc.wantPruned {
				t.Errorf("pruned = %d, want %d", res.Pruned, tc.wantPruned)
			}
			if !tc.wantC1Stub && res.Messages[3].Content != bigContent {
				t.Error("protected result content changed")
			}
			// 较新的读取始终逐字保留。
			if got := res.Messages[5].Content; got != bigContent {
				t.Errorf("newest read changed to %q", got)
			}
		})
	}
}

// TestTrimNoTrimToolCallIDsCJK 验证非 ASCII（CJK）ToolCallID 能作为保护键命中，
// 且被保护结果中的 CJK 恢复标记逐字节保留、未被 UTF-8 截断。
func TestTrimNoTrimToolCallIDsCJK(t *testing.T) {
	const marker = "恢复标记：输出已落盘"
	body := strings.Repeat("界", 900) + marker + strings.Repeat("中", 900)
	big := strings.Repeat("x", 4000)
	msgs := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", ToolCalls: []llm.ToolCall{toolCall("调用一", "read", `{}`)}},
		{Role: "tool", ToolCallID: "调用一", Content: body},
		{Role: "assistant", Content: "b", ToolCalls: []llm.ToolCall{toolCall("调用二", "read", `{}`)}},
		{Role: "tool", ToolCallID: "调用二", Content: big},
		{Role: "assistant", Content: "c", ToolCalls: []llm.ToolCall{toolCall("调用三", "read", `{}`)}},
		{Role: "tool", ToolCallID: "调用三", Content: big},
		{Role: "assistant", Content: "done"},
	}

	res := Trim(msgs, noTrimBudgetOpts(map[string]bool{"调用一": true}))

	got := res.Messages[3].Content
	if got != body {
		t.Fatalf("CJK ToolCallID 未命中保护集，结果被改写；marker 保留 = %v", strings.Contains(got, marker))
	}
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatal("被保护内容出现非法 UTF-8")
	}
	if !strings.Contains(got, marker) {
		t.Fatal("恢复标记丢失")
	}
	if !strings.HasPrefix(res.Messages[5].Content, trimmedPrefix) {
		t.Fatalf("未受保护的结果仍应被裁剪，得到 %q", noTrimPreview(res.Messages[5].Content))
	}
}

// TestTrimNoTrimToolCallIDsDoesNotMutateInputs 验证 Trim 不修改传入的消息切片与保护集合本身。
func TestTrimNoTrimToolCallIDsDoesNotMutateInputs(t *testing.T) {
	msgs := noTrimFixture()
	msgsCopy := append([]llm.Message(nil), msgs...)
	noTrim := map[string]bool{"c1": true, "c2": false}
	noTrimCopy := make(map[string]bool, len(noTrim))
	for k, v := range noTrim {
		noTrimCopy[k] = v
	}

	_ = Trim(msgs, noTrimBudgetOpts(noTrim))

	if !reflect.DeepEqual(msgs, msgsCopy) {
		t.Fatal("Trim 修改了传入的消息切片")
	}
	if !reflect.DeepEqual(noTrim, noTrimCopy) {
		t.Fatalf("Trim 修改了传入的保护集合: %v", noTrim)
	}
}

// TestRunStateNoTrimToolCallIDsWiring 验证 Options 中的保护集合被写入 runState 构建的 TrimOptions。
func TestRunStateNoTrimToolCallIDsWiring(t *testing.T) {
	cases := []struct {
		name   string
		noTrim map[string]bool
	}{
		{"nil", nil},
		{"populated", map[string]bool{"调用一": true, "c2": false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := newRunState(Options{MaxContextTokens: 1000, NoTrimToolCallIDs: tc.noTrim}, "q", func(Event) {})
			if !reflect.DeepEqual(state.trim.NoTrimToolCallIDs, tc.noTrim) {
				t.Fatalf("state.trim.NoTrimToolCallIDs = %v, want %v", state.trim.NoTrimToolCallIDs, tc.noTrim)
			}
		})
	}
}
