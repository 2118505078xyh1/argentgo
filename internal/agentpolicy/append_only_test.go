package agentpolicy

import (
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// msg 是测试用的消息构造函数，减少表驱动用例的噪音。
func msg(role, content string) llm.Message {
	return llm.Message{Role: role, Content: content}
}

// cloneMessages 返回消息切片的深拷贝，用于验证输入未被修改。
func cloneMessages(in []llm.Message) []llm.Message {
	out := make([]llm.Message, len(in))
	copy(out, in)
	for i := range out {
		if in[i].ToolCalls != nil {
			out[i].ToolCalls = append([]llm.ToolCall(nil), in[i].ToolCalls...)
		}
	}
	return out
}

// TestStablePrefixViewStrictExtension 用表驱动锁定严格前缀扩展的判定：
// 只有“旧视图完整保留且至少新增一条消息”才算追加，改写、重排、回退都不算。
func TestStablePrefixViewStrictExtension(t *testing.T) {
	frozen := "frozen-system"
	sys := msg("system", frozen)
	userA := msg("user", "a")
	assistant := msg("assistant", "ok")

	cases := []struct {
		name   string
		old    []llm.Message
		next   []llm.Message
		noPrev bool // true 表示首次观测，prevDigest 传空串
		want   bool
	}{
		{
			name: "append one message",
			old:  []llm.Message{sys, userA},
			next: []llm.Message{sys, userA, assistant},
			want: true,
		},
		{
			name: "append many messages",
			old:  []llm.Message{sys},
			next: []llm.Message{sys, userA, assistant, msg("user", "more")},
			want: true,
		},
		{
			name:   "first observation establishes baseline",
			old:    nil,
			next:   []llm.Message{sys, userA},
			noPrev: true,
			want:   true,
		},
		{
			name: "empty old view is a valid prefix",
			old:  nil,
			next: []llm.Message{sys, userA},
			want: true,
		},
		{
			name: "identical view is not a strict extension",
			old:  []llm.Message{sys, userA},
			next: []llm.Message{sys, userA},
			want: false,
		},
		{
			name: "modified message is not an extension",
			old:  []llm.Message{sys, userA},
			next: []llm.Message{sys, msg("user", "b")},
			want: false,
		},
		{
			name: "reordered prefix is not an extension",
			old:  []llm.Message{sys, userA, assistant},
			next: []llm.Message{sys, assistant, userA, msg("user", "new")},
			want: false,
		},
		{
			name: "insertion before the tail is not an extension",
			old:  []llm.Message{sys, userA},
			next: []llm.Message{sys, assistant, userA},
			want: false,
		},
		{
			name: "shortened view cannot extend longer old view",
			old:  []llm.Message{sys, userA, assistant},
			next: []llm.Message{sys, userA},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := ""
			if !tc.noPrev {
				prev = DigestOf(tc.old)
			}
			view, ok, digest := StablePrefixView(tc.next, frozen, prev)
			if ok != tc.want {
				t.Fatalf("isAppendOnly = %v, want %v", ok, tc.want)
			}
			if !reflect.DeepEqual(view, tc.next) {
				t.Fatalf("view 不是输入的只读引用：got %#v, want %#v", view, tc.next)
			}
			if want := DigestOf(tc.next); digest != want {
				t.Fatalf("digest = %q, want DigestOf(next) = %q", digest, want)
			}
		})
	}
}

// TestStablePrefixViewDetectsFrozenSystemChange 验证首条 system 被改写时判为非追加，
// 即便消息数量增加了也一样；同时返回当前视图的摘要。
func TestStablePrefixViewDetectsFrozenSystemChange(t *testing.T) {
	frozen := "frozen-system"
	old := []llm.Message{msg("system", frozen), msg("user", "a")}
	tampered := []llm.Message{msg("system", "tampered"), msg("user", "a"), msg("user", "b")}

	view, ok, digest := StablePrefixView(tampered, frozen, DigestOf(old))
	if ok {
		t.Fatal("首条 system 内容被改写，却判为严格前缀扩展")
	}
	if !reflect.DeepEqual(view, tampered) {
		t.Fatalf("view 不是输入的只读引用：got %#v, want %#v", view, tampered)
	}
	if want := DigestOf(tampered); digest != want {
		t.Fatalf("digest = %q, want %q", digest, want)
	}
}

// TestStablePrefixViewDoesNotMutateInputs 锁定“纯视图”保证：消息切片（含嵌套工具调用）、
// frozenSystem 与 prevDigest 在调用前后逐字节不变。
func TestStablePrefixViewDoesNotMutateInputs(t *testing.T) {
	frozen := "frozen-system"
	prev := "some-prev-digest"
	before := []llm.Message{
		msg("system", frozen),
		msg("user", "a"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"p":"x"}`}}}},
	}
	snapshot := cloneMessages(before)
	frozenBefore := frozen
	prevBefore := prev

	_, _, _ = StablePrefixView(before, frozen, prev)

	if !reflect.DeepEqual(before, snapshot) {
		t.Fatalf("输入消息被修改：got %#v, want %#v", before, snapshot)
	}
	if frozen != frozenBefore {
		t.Fatalf("frozenSystem 被修改：got %q, want %q", frozen, frozenBefore)
	}
	if prev != prevBefore {
		t.Fatalf("prevDigest 被修改：got %q, want %q", prev, prevBefore)
	}
}

// TestDigestOfDeterministic 验证摘要的确定性、对内容/顺序/数量变化的敏感性，
// 以及空切片与 nil 等价。
func TestDigestOfDeterministic(t *testing.T) {
	base := []llm.Message{msg("system", "s"), msg("user", "你好，世界")}

	if got, want := DigestOf(base), DigestOf(cloneMessages(base)); got != want {
		t.Fatalf("相同输入的摘要不一致：%q != %q", got, want)
	}
	if got, want := DigestOf(nil), DigestOf([]llm.Message{}); got != want {
		t.Fatalf("nil 与空切片的摘要不一致：%q != %q", got, want)
	}

	cases := []struct {
		name    string
		mutated []llm.Message
	}{
		{"content changed", []llm.Message{msg("system", "s"), msg("user", "你好，宇宙")}},
		{"order changed", []llm.Message{msg("user", "你好，世界"), msg("system", "s")}},
		{"count changed", []llm.Message{msg("system", "s"), msg("user", "你好，世界"), msg("user", "extra")}},
	}
	want := DigestOf(base)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DigestOf(tc.mutated); got == want {
				t.Fatalf("输入变化后摘要未改变：%q", got)
			}
		})
	}
}

// TestStablePrefixViewEmptyMessages 覆盖空视图：空摘要可作为基线并被任意非空视图扩展，
// 但空视图本身不能扩展非空的旧视图。
func TestStablePrefixViewEmptyMessages(t *testing.T) {
	emptyDigest := DigestOf(nil)

	view, ok, digest := StablePrefixView(nil, "", "")
	if len(view) != 0 {
		t.Fatalf("空输入的 view 应为空，got %#v", view)
	}
	if !ok {
		t.Fatal("首次观测的空视图应建立基线并视为可追加")
	}
	if digest != emptyDigest {
		t.Fatalf("digest = %q, want %q", digest, emptyDigest)
	}

	_, ok, _ = StablePrefixView([]llm.Message{msg("user", "x")}, "", emptyDigest)
	if !ok {
		t.Fatal("空视图应是任意非空视图的前缀")
	}

	_, ok, _ = StablePrefixView(nil, "", DigestOf([]llm.Message{msg("user", "x")}))
	if ok {
		t.Fatal("空视图不能扩展非空旧视图")
	}
}

// TestStablePrefixViewCJK 用中文与 emoji 内容覆盖多字节字符：摘要确定，
// 且中文消息的严格扩展判定不受 UTF-8 字节影响。
func TestStablePrefixViewCJK(t *testing.T) {
	frozen := "你是一个助手 🤖"
	old := []llm.Message{
		msg("system", frozen),
		msg("user", "请分析这段中文文本。"),
		msg("assistant", "好的，收到。"),
	}
	next := append(cloneMessages(old), msg("user", "再补充一点 🚀"))

	prevDigest := DigestOf(old)
	_, ok, digest := StablePrefixView(next, frozen, prevDigest)
	if !ok {
		t.Fatal("CJK 消息的严格前缀扩展未被识别")
	}
	if got := DigestOf(next); digest != got {
		t.Fatalf("digest = %q, want %q", digest, got)
	}
	if got := DigestOf(old); got != prevDigest {
		t.Fatalf("相同 CJK 输入的摘要不稳定：%q != %q", got, prevDigest)
	}
}
