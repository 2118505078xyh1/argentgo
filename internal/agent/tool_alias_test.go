package agent

import (
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

func TestToolAliasFallback(t *testing.T) {
	t.Setenv("ARGENT_TOOL_ALIAS", "1")
	available := []string{"bash", "read"}
	calls := []llm.ToolCall{
		toolCall("c1", "shell", `{"cmd":"ls"}`),
		toolCall("c2", "Bash", `{"cmd":"pwd"}`),
		toolCall("c3", "bash", `{"cmd":"echo"}`),
		toolCall("c4", "read", `{}`),
	}
	got, aliased := resolveToolAliases(calls, available)
	if got[0].Function.Name != "bash" || got[1].Function.Name != "bash" {
		t.Fatalf("别名未改写: %q %q", got[0].Function.Name, got[1].Function.Name)
	}
	if got[2].Function.Name != "bash" || got[3].Function.Name != "read" {
		t.Fatal("真实工具名被改动")
	}
	if len(aliased) != 2 || aliased["c1"] != "shell" || aliased["c2"] != "Bash" {
		t.Fatalf("别名映射错误: %#v", aliased)
	}
	// 入参不可变。
	if calls[0].Function.Name != "shell" {
		t.Fatal("入参被修改")
	}
}

func TestToolAliasNoteOnlyWhenAliased(t *testing.T) {
	t.Setenv("ARGENT_TOOL_ALIAS", "1")
	s := newRunState(Options{Model: "m", System: "s"}, "go", nil)
	// 未别名化的调用不追加提示。
	if got := s.appendAliasNote("none", "ok"); got != "ok" {
		t.Fatalf("非别名调用被追加提示: %q", got)
	}
	s.aliasTargets = map[string]string{"c1": "shell"}
	first := s.appendAliasNote("c1", "ok")
	if !strings.Contains(first, "shell") || !strings.Contains(first, "bash") {
		t.Fatalf("更正提示缺失: %q", first)
	}
	// 同一别名只提示一次。
	second := s.appendAliasNote("c1", "ok")
	if second != "ok" {
		t.Fatalf("一次性提示重复: %q", second)
	}
}

func TestToolAliasNoTargetRegistered(t *testing.T) {
	t.Setenv("ARGENT_TOOL_ALIAS", "1")
	calls := []llm.ToolCall{toolCall("c1", "shell", "{}")}
	got, aliased := resolveToolAliases(calls, []string{"shell"})
	if got[0].Function.Name != "shell" || len(aliased) != 0 {
		t.Fatal("目标未注册时不应改写")
	}
}
