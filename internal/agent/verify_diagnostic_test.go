package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestHasDiagnosticContent covers the aider-P2 classifier: a FAIL whose
// feedback is only boilerplate must not be mistaken for actionable output.
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

// TestHasDiagnosticContentOnBuildVerifyFeedback exercises the classifier on the
// exact string that verifyAfterBatch hands to injectSteering.
func TestHasDiagnosticContentOnBuildVerifyFeedback(t *testing.T) {
	state := VerifyLoopState{Reflections: 1, Max: 3}
	if got := HasDiagnosticContent(BuildVerifyFeedback("go test", "   \n\t ", state)); got {
		t.Fatal("empty-output feedback was classified as diagnostic")
	}
	if got := HasDiagnosticContent(BuildVerifyFeedback("go test", "FAIL: assertion x", state)); !got {
		t.Fatal("feedback with a real error line was classified as non-diagnostic")
	}
}

// TestVerifyReflectionHintStringUnchanged guards the const refactor: the hint
// appended to every FAIL must stay byte-for-byte identical to the original.
func TestVerifyReflectionHintStringUnchanged(t *testing.T) {
	if got, want := fmt.Sprintf(verifyReflectionHint, 2), "Reflections left: 2. Do not re-run the same check unchanged."; got != want {
		t.Fatalf("verifyReflectionHint = %q, want %q", got, want)
	}
}

// TestVerifyAfterBatchRecordsLastOutcome checks the additive observational
// field without touching the reflection flow.
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
