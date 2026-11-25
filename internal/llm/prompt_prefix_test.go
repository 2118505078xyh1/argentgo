package llm_test

import (
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestStableSystemPromptAppendsPreservingPrefix 验证新增 node 按序追加，
// base 原样成为结果前缀，且公共前缀长度等于 base 的 rune 数。
func TestStableSystemPromptAppendsPreservingPrefix(t *testing.T) {
	base := "你是助手"
	nodes := []string{"规则一", "规则二", "规则三"}
	want := "你是助手\n规则一\n规则二\n规则三"

	got := llm.StableSystemPrompt(base, nodes)
	if got != want {
		t.Fatalf("StableSystemPrompt() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, base) {
		t.Fatalf("结果应保留 base 作为前缀，got %q", got)
	}
	if n := llm.PromptPrefixShared(base, got); n != len([]rune(base)) {
		t.Errorf("base 与结果的公共前缀 = %d, want %d", n, len([]rune(base)))
	}
}

// TestStableSystemPromptDedup 验证 base 已含的 node 与 nodes 中重复的 node 都只出现一次。
func TestStableSystemPromptDedup(t *testing.T) {
	base := "基础规则甲"
	nodes := []string{"基础规则甲", "新规则乙", "新规则乙"}
	want := "基础规则甲\n新规则乙"

	if got := llm.StableSystemPrompt(base, nodes); got != want {
		t.Fatalf("StableSystemPrompt() = %q, want %q", got, want)
	}
}

// TestStableSystemPromptDeletionPlaceholder 验证删除请求以 tombstone 占位表达，
// 既不移除 base 内容，也不改动前缀；随后新增的 node 仍正常追加在占位之后。
func TestStableSystemPromptDeletionPlaceholder(t *testing.T) {
	base := "基础\n旧规则"
	nodes := []string{llm.DeletionMark + "旧规则"}
	want := "基础\n旧规则\n[deleted:旧规则]"

	got := llm.StableSystemPrompt(base, nodes)
	if got != want {
		t.Fatalf("StableSystemPrompt() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, base) {
		t.Fatalf("删除不应改动 base 前缀，got %q", got)
	}

	// 删除 + 新增混合：占位先于新增，base 前缀保持。base 需含删除目标才会产生占位。
	base2 := "基础\n旧规则"
	nodes2 := []string{llm.DeletionMark + "旧规则", "新规则"}
	want2 := "基础\n旧规则\n[deleted:旧规则]\n新规则"
	if got2 := llm.StableSystemPrompt(base2, nodes2); got2 != want2 {
		t.Fatalf("删除+新增 = %q, want %q", got2, want2)
	}
}

// TestStableSystemPromptDeletionOfAppendedNode 验证同一批次里先追加再删除，
// 已追加内容仍留在前缀中，仅追加 tombstone。
func TestStableSystemPromptDeletionOfAppendedNode(t *testing.T) {
	got := llm.StableSystemPrompt("基础", []string{"规则A", llm.DeletionMark + "规则A"})
	want := "基础\n规则A\n[deleted:规则A]"
	if got != want {
		t.Fatalf("StableSystemPrompt() = %q, want %q", got, want)
	}
}

// TestStableSystemPromptDeletionNotPresent 验证目标不存在时删除请求是空操作。
func TestStableSystemPromptDeletionNotPresent(t *testing.T) {
	base := "基础"
	got := llm.StableSystemPrompt(base, []string{llm.DeletionMark + "不存在", llm.DeletionMark})
	if got != base {
		t.Fatalf("删除不存在的目标应为空操作，got %q, want %q", got, base)
	}
}

// TestStableSystemPromptCJK 验证 CJK 文本按整段追加，不切分码点。
func TestStableSystemPromptCJK(t *testing.T) {
	base := "你是一个中文助手"
	nodes := []string{"请回答中文", "保持礼貌"}
	want := "你是一个中文助手\n请回答中文\n保持礼貌"

	got := llm.StableSystemPrompt(base, nodes)
	if got != want {
		t.Fatalf("StableSystemPrompt() = %q, want %q", got, want)
	}
	for _, frag := range []string{"中文助手", "请回答中文", "保持礼貌"} {
		if !strings.Contains(got, frag) {
			t.Errorf("结果应完整包含 %q，got %q", frag, got)
		}
	}
}

// TestStableSystemPromptDoesNotMutateInputs 验证纯函数性：不修改入参 slice 与
// base 字符串，且相同输入得到相同结果（确定性）。
func TestStableSystemPromptDoesNotMutateInputs(t *testing.T) {
	base := "基础"
	nodes := []string{"规则一", llm.DeletionMark + "规则一"}
	snapshot := append([]string(nil), nodes...)

	first := llm.StableSystemPrompt(base, nodes)
	if !reflect.DeepEqual(nodes, snapshot) {
		t.Fatalf("入参 nodes 被修改：got %q, want %q", nodes, snapshot)
	}
	if base != "基础" {
		t.Fatalf("入参 base 被修改：got %q", base)
	}
	second := llm.StableSystemPrompt(base, nodes)
	if first != second {
		t.Fatalf("结果不确定：%q != %q", first, second)
	}
}

// TestStableSystemPromptEmptyInputs 验证空输入边界。
func TestStableSystemPromptEmptyInputs(t *testing.T) {
	if got := llm.StableSystemPrompt("base", nil); got != "base" {
		t.Errorf("nil nodes 应返回 base，got %q", got)
	}
	if got := llm.StableSystemPrompt("", []string{"规则"}); got != "\n规则" {
		t.Errorf("空 base 追加 node 得到 %q", got)
	}
}

// TestPromptPrefixShared 以 rune 为单位校验最长公共前缀，含 CJK 与字节差异场景。
func TestPromptPrefixShared(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"both empty", "", "", 0},
		{"one empty", "", "abc", 0},
		{"ascii prefix", "abc", "abcdef", 3},
		{"ascii reversed", "abcdef", "abc", 3},
		{"no overlap", "abc", "xyz", 0},
		{"identical", "完全相同", "完全相同", 4},
		{"cjk two runes", "你好世界", "你好中国", 2},
		{"cjk counts rune not byte", "你好", "你", 1},
		{"cjk divergence", "系统提示：你是一个助手", "系统提示：你是一个翻译", 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := llm.PromptPrefixShared(tt.a, tt.b); got != tt.want {
				t.Errorf("PromptPrefixShared(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
