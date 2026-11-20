package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestHasDiagnosticContent 覆盖 aider-P2 分类器：反馈只有样板的 FAIL 不得被误认为可操作的输出。
// （接上行）
func TestHasDiagnosticContent(t *testing.T) {
	const hint = "Reflections left: 2. Do not re-run the same check unchanged."
	tests := []struct {
		name     string
		feedback string
		want     bool
	}{
		{"empty feedback", "", false},
		{"whitespace only", "  \n\t ", false},
		{"fail header only", "[verify: GO TEST] FAIL", false},
		{"pass header only", "[verify: LINT] PASS", false},
		{"header plus hint only", "[verify: GO TEST] FAIL\n" + hint, false},
		{"fix instruction only", verifyFixInstruction, false},
		{"anchor explanation only", verifyAnchorExplanation, false},
		{"full boilerplate only", verifyFixInstruction + "\n[verify: GO TEST] FAIL\n" + verifyAnchorExplanation + "\n" + hint, false},
		{"fix instruction plus real error", verifyFixInstruction + "\n[verify: GO TEST] FAIL\nmain.go:42: undefined: foo", true},
		{"header with file line", "[verify: GO TEST] FAIL\nmain.go:42: undefined: foo", true},
		{"raw file line", "main.go:42: undefined: foo", true},
		{"cjk error text", "[verify: BUILD] FAIL\n编译失败：未定义的标识符 foo", true},
		{"raw cjk error text", "编译失败：未定义的标识符 foo", true},
		{"redacted placeholder only", "[verify: TEST] FAIL\n[redacted]", false},
		{"upper redacted placeholder only", "[verify: TEST] FAIL\n[REDACTED]", false},
		{"email placeholder only", "[verify: TEST] FAIL\n[email]", false},
		{"placeholder mix only", "[verify: TEST] FAIL\n[redacted] [email] [REDACTED]", false},
		{"placeholder beside real text", "[verify: TEST] FAIL\napi_key=[redacted] failed at step 3", true},
		{"anchored source window", "[verify: TEST] FAIL\n  █ main.go:9 -> return bad", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasDiagnosticContent(tc.feedback); got != tc.want {
				t.Fatalf("HasDiagnosticContent(%q) = %v, want %v", tc.feedback, got, tc.want)
			}
		})
	}
}

// TestHasDiagnosticContentOnBuildVerifyFeedback 在 verifyAfterBatch 交给 injectSteering 的确切字符串上演练分类器。
// （接上行）
func TestHasDiagnosticContentOnBuildVerifyFeedback(t *testing.T) {
	state := VerifyLoopState{Reflections: 1, Max: 3}
	if got := HasDiagnosticContent(BuildVerifyFeedback("go test", "   \n\t ", state)); got {
		t.Fatal("empty-output feedback was classified as diagnostic")
	}
	if got := HasDiagnosticContent(BuildVerifyFeedback("test", "[redacted]", state)); got {
		t.Fatal("fix instruction plus redaction placeholder was classified as diagnostic")
	}
	if got := HasDiagnosticContent(BuildVerifyFeedback("go test", "FAIL: assertion x", state)); !got {
		t.Fatal("feedback with a real error line was classified as non-diagnostic")
	}
}

// TestVerifyReflectionHintStringUnchanged 守护常量重构：附加到每个 FAIL 的提示必须与原始内容逐字节一致。
// （接上行）
func TestVerifyReflectionHintStringUnchanged(t *testing.T) {
	if got, want := fmt.Sprintf(verifyReflectionHint, 2), "Reflections left: 2. Do not re-run the same check unchanged."; got != want {
		t.Fatalf("verifyReflectionHint = %q, want %q", got, want)
	}
}

// TestVerifyAfterBatchRecordsLastOutcome 检查附加的观察性字段，而不触碰反思流程。
// （接上行）
func TestVerifyAfterBatchRecordsLastOutcome(t *testing.T) {
	tests := []struct {
		name   string
		verify func(context.Context, []string) (string, bool, string, bool)
		want   string
	}{
		{"pass", func(context.Context, []string) (string, bool, string, bool) {
			return "go test", true, "", true
		}, "PASS"},
		{"fail", func(context.Context, []string) (string, bool, string, bool) {
			return "go test", false, "FAIL: assertion x", true
		}, "FAIL"},
		{"skip", func(context.Context, []string) (string, bool, string, bool) {
			return "go test", false, "command not found", false
		}, "SKIP"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n == 1 {
					return toolReply(toolCall("c1", "write", `{"i":"create"}`)), nil
				}
				return textReply("done"), nil
			}}
			opts := Options{
				Model:    "test",
				Chatter:  chat,
				Tools:    &fakeTools{},
				MaxTurns: 6,
				Verify:   tc.verify,
			}
			s := newRunState(opts, "go", nil)
			if _, err := s.loop(context.Background()); err != nil {
				t.Fatalf("loop() error: %v", err)
			}
			if got := s.verifyState.LastOutcome; got != tc.want {
				t.Fatalf("LastOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}
