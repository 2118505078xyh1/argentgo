package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestVerifyLoopStateDefaultsToThreeReflections(t *testing.T) {
	s := VerifyLoopState{}
	for i := 0; i < DefaultMaxVerifyReflections; i++ {
		if !s.CanReflect() {
			t.Fatalf("CanReflect() at reflection %d = false, want true", i)
		}
		if !s.Record() {
			t.Fatalf("Record() at reflection %d = false, want true", i)
		}
	}
	if got := s.Reflections; got != DefaultMaxVerifyReflections {
		t.Fatalf("Reflections = %d, want %d", got, DefaultMaxVerifyReflections)
	}
	if s.CanReflect() {
		t.Fatal("CanReflect() at cap = true, want false")
	}
	if s.Record() {
		t.Fatal("Record() past the cap = true, want false")
	}
	if got := s.Reflections; got != DefaultMaxVerifyReflections {
		t.Fatalf("Reflections after blocked Record() = %d, want %d", got, DefaultMaxVerifyReflections)
	}
}

func TestVerifyLoopStateHonorsExplicitMax(t *testing.T) {
	s := VerifyLoopState{Max: 1}
	if !s.CanReflect() || !s.Record() {
		t.Fatal("first reflection should be available with Max=1")
	}
	if s.CanReflect() {
		t.Fatal("CanReflect() with Max=1 after one reflection = true, want false")
	}
	if s.Record() || s.Reflections != 1 {
		t.Fatalf("Record() past Max=1 changed state to %+v", s)
	}
}

func TestVerifyLoopStateMaxClampsRemainingAtZero(t *testing.T) {
	s := VerifyLoopState{Reflections: 99, Max: 3}
	if got := s.remaining(); got != 0 {
		t.Fatalf("remaining() = %d, want 0", got)
	}
}

func TestBuildVerifyFeedbackContainsFeedbackCountAndHint(t *testing.T) {
	state := VerifyLoopState{Reflections: 1, Max: 3}
	got := BuildVerifyFeedback("go test", "step 1 ok\nFAIL: assertion x", state)

	if want := "[verify: GO TEST] FAIL\n"; !strings.HasPrefix(got, want) {
		t.Fatalf("feedback = %q, want prefix %q", got, want)
	}
	if !strings.Contains(got, "FAIL: assertion x") {
		t.Fatalf("feedback = %q, want the failure output", got)
	}
	if want := "Reflections left: 2. Do not re-run the same check unchanged."; !strings.Contains(got, want) {
		t.Fatalf("feedback = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "Do not re-run the same check unchanged.") {
		t.Fatalf("feedback = %q, want the anti-retry line last", got)
	}
}

func TestBuildVerifyFeedbackIsBoundedAndUTF8Safe(t *testing.T) {
	out := strings.Repeat("q", defaultVerifyMaxBytes+512) + "界界" + "FAIL_TAIL"
	got := BuildVerifyFeedback("test", out, VerifyLoopState{Max: 3})
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("feedback lines = %d, want 3 (verdict, output, hint): %q", len(lines), got)
	}
	if len(lines[1]) > defaultVerifyMaxBytes {
		t.Fatalf("output section = %d bytes, want <= %d", len(lines[1]), defaultVerifyMaxBytes)
	}
	if !strings.HasPrefix(lines[1], verifyTruncatedMarker) {
		t.Fatalf("output = %q, want truncation marker", lines[1])
	}
	if !strings.Contains(lines[1], "FAIL_TAIL") {
		t.Fatalf("output = %q, want the tail kept", lines[1])
	}
}

func TestBuildVerifyFeedbackRedactsPlantedSecret(t *testing.T) {
	out := "api_key=sk_live_abcdefghijklmnop failed at step 3"
	got := BuildVerifyFeedback("test", out, VerifyLoopState{Max: 3})
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("feedback leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("feedback = %q, want a [redacted] marker", got)
	}
}

func TestIsMutatingToolMirrorsDefaultMutatingSet(t *testing.T) {
	for _, name := range []string{
		"write", "edit", "multi_edit", "apply_patch", "patch",
		"bash", "shell", "exec", "run", "delete", "remove", "move", "copy", "mkdir",
	} {
		if !IsMutatingTool(name) {
			t.Errorf("IsMutatingTool(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"read", "ls", "grep", "glob", "list", "view"} {
		if IsMutatingTool(name) {
			t.Errorf("IsMutatingTool(%q) = true, want false", name)
		}
	}
}

// TestLoopVerifyFailFeedsBackThenPasses drives a mini-loop with a fake Verify:
// the first mutating batch fails verification, the failure arrives as the next
// user turn, the second mutating batch passes, and the run finishes normally.
func TestLoopVerifyFailFeedsBackThenPasses(t *testing.T) {
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		switch n {
		case 1:
			return toolReply(toolCall("c1", "write", `{"i":"create"}`)), nil
		case 2:
			return toolReply(toolCall("c2", "edit", `{"i":"fix"}`)), nil
		default:
			return textReply("fixed and verified"), nil
		}
	}}
	verifyCalls := 0
	opts := Options{
		Model:    "test",
		Chatter:  chat,
		Tools:    &fakeTools{},
		MaxTurns: 10,
		MutationPaths: func(string, string) []string {
			return []string{"main.go"}
		},
		Verify: func(_ context.Context, paths []string) (string, bool, string, bool) {
			verifyCalls++
			if verifyCalls == 1 {
				if len(paths) != 1 || paths[0] != "main.go" {
					t.Errorf("Verify paths = %v, want [main.go]", paths)
				}
				return "go test", false, "FAIL: assertion x", true
			}
			return "go test", true, "", true
		},
	}

	res, err := newRunState(opts, "go", nil).loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("Stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if verifyCalls != 2 {
		t.Fatalf("Verify called %d times, want 2", verifyCalls)
	}
	seen := chat.requestsSeen()
	if len(seen) < 2 {
		t.Fatalf("provider saw %d requests, want >= 2", len(seen))
	}
	feedbackSeen := false
	for _, m := range seen[1] {
		if m.Role == "user" && strings.Contains(m.Content, "[verify: GO TEST] FAIL") &&
			strings.Contains(m.Content, "Reflections left: 2") {
			feedbackSeen = true
		}
	}
	if !feedbackSeen {
		t.Fatalf("second request lacks the injected verify feedback: %+v", seen[1])
	}
}

// TestLoopStopsAfterVerifyReflectionCap checks the hard cap: with a permanently
// failing check the loop injects exactly MaxVerifyReflections feedback turns,
// then the next failure ends the run with the distinct stop reason.
func TestLoopStopsAfterVerifyReflectionCap(t *testing.T) {
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		call := toolCall(fmt.Sprintf("c%d", n), "write", fmt.Sprintf(`{"i":"attempt %d"}`, n))
		return toolReply(call), nil
	}}
	verifyCalls := 0
	opts := Options{
		Model:    "test",
		Chatter:  chat,
		Tools:    &fakeTools{},
		MaxTurns: 20,
		Verify: func(context.Context, []string) (string, bool, string, bool) {
			verifyCalls++
			return "go test", false, "FAIL: still broken", true
		},
	}
	s := newRunState(opts, "go", nil)
	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	if res.Stopped != StopVerifyReflections {
		t.Fatalf("Stopped = %q, want %q", res.Stopped, StopVerifyReflections)
	}
	if want := DefaultMaxVerifyReflections + 1; verifyCalls != want {
		t.Fatalf("Verify called %d times, want %d", verifyCalls, want)
	}
	feedbacks := 0
	for _, m := range res.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "[verify: GO TEST] FAIL") {
			feedbacks++
		}
	}
	if feedbacks != DefaultMaxVerifyReflections {
		t.Fatalf("injected %d feedback turns, want %d", feedbacks, DefaultMaxVerifyReflections)
	}
	stopped := false
	for _, e := range s.eventsSnapshot() {
		if e.Kind == EventStop && e.Reason == StopVerifyReflections {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("no EventStop with reason %q was emitted", StopVerifyReflections)
	}
}

// TestLoopVerifyUnrunnableOrReadOnlyBatchLeavesStateUntouched guards the
// default-off contract: no feedback is injected when the checker reports ok=false,
// and read-only batches never call Verify at all.
func TestLoopVerifyUnrunnableOrReadOnlyBatchLeavesStateUntouched(t *testing.T) {
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		switch n {
		case 1:
			return toolReply(toolCall("c1", "read", `{"i":"inspect"}`)), nil
		case 2:
			return toolReply(toolCall("c2", "write", `{"i":"create"}`)), nil
		default:
			return textReply("done"), nil
		}
	}}
	verifyCalls := 0
	opts := Options{
		Model:    "test",
		Chatter:  chat,
		Tools:    &fakeTools{},
		MaxTurns: 10,
		Verify: func(context.Context, []string) (string, bool, string, bool) {
			verifyCalls++
			return "go test", false, "command not found", false
		},
	}
	s := newRunState(opts, "go", nil)
	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("Stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if verifyCalls != 1 {
		t.Fatalf("Verify called %d times, want 1 (read-only batch skipped)", verifyCalls)
	}
	if s.verifyState.Reflections != 0 {
		t.Fatalf("Reflections = %d, want 0 after an unrunnable check", s.verifyState.Reflections)
	}
	for _, m := range res.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "[verify:") {
			t.Fatalf("unexpected verify feedback in transcript: %q", m.Content)
		}
	}
}
