package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func spec(name string) llm.ToolSpec {
	var s llm.ToolSpec
	s.Function.Name = name
	return s
}

func routingSpecNames(specs []llm.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Function.Name)
	}
	return out
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// TestResolveModelToolRouting 锁定 #49 的内置规则：openai-native/gpt/codex + act
// 模式关 editor、保 apply_patch；只收紧不新增；plan 模式与其它 provider 不变。
func TestResolveModelToolRouting(t *testing.T) {
	specs := []llm.ToolSpec{spec("read"), spec("apply_patch"), spec("editor"), spec("bash")}

	cases := []struct {
		name          string
		provider      string
		model         string
		mode          string
		wantEditor    bool
		wantPatch     bool
		wantUnchanged bool
	}{
		{"openai native act", "openai-native", "gpt-4o", "act", false, true, false},
		{"gpt model act", "custom", "GPT-4o-mini", "act", false, true, false},
		{"codex act", "codex", "codex-mini", "act", false, true, false},
		{"openai plan unchanged", "openai-native", "gpt-4o", "plan", true, true, true},
		{"anthropic unchanged", "anthropic", "claude-3", "act", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveModelToolRouting(specs, tc.provider, tc.model, tc.mode)
			names := routingSpecNames(got)
			if containsName(names, "editor") != tc.wantEditor {
				t.Fatalf("editor presence = %v, names = %v", containsName(names, "editor"), names)
			}
			if containsName(names, "apply_patch") != tc.wantPatch {
				t.Fatalf("apply_patch presence = %v, names = %v", containsName(names, "apply_patch"), names)
			}
			if len(got) > len(specs) {
				t.Fatalf("routing added tools: %v", names)
			}
			if tc.wantUnchanged && len(got) != len(specs) {
				t.Fatalf("unchanged case lost tools: %v", names)
			}
		})
	}
}

// TestToolRoutingDefaultUnchanged 锁定：没有规则命中且输入为空时逐字节不变；
// 不存在的 editor 不会因为规则而凭空出现（只收紧不新增）。
func TestToolRoutingDefaultUnchanged(t *testing.T) {
	if got := ResolveModelToolRouting(nil, "openai-native", "gpt-4o", "act"); got != nil {
		t.Fatalf("nil specs became %v", got)
	}
	specs := []llm.ToolSpec{spec("read"), spec("apply_patch")}
	got := ResolveModelToolRouting(specs, "openai-native", "gpt-4o", "act")
	if len(got) != len(specs) || got[0].Function.Name != "read" || got[1].Function.Name != "apply_patch" {
		t.Fatalf("unexpected routing: %v", routingSpecNames(got))
	}
}
