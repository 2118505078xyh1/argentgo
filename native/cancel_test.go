package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsCancelError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"direct", context.Canceled, true},
		{"wrapped", fmt.Errorf("prompt failed: %w", context.Canceled), true},
		{"legacy rpc message", errors.New("context canceled"), true},
		{"stop reason message", errors.New("prompt cancelled"), true},
		{"real failure", errors.New("provider returned 500"), false},
		{"deadline is a failure", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCancelError(tc.err); got != tc.want {
				t.Fatalf("isCancelError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestFinishTextKeepsStreamedOnCancellation guards the native symptom: an
// aborted reply must keep the text the user already saw stream in, while a real
// failure keeps the engine's own result.
func TestFinishTextKeepsStreamedOnCancellation(t *testing.T) {
	cases := []struct {
		name     string
		full     string
		err      error
		streamed string
		want     string
	}{
		{"success full answer", "answer", nil, "answer", "answer"},
		{"success without stream", "answer", nil, "", "answer"},
		{"normalised cancelled result", "partial", nil, "partial", "partial"},
		{"empty result keeps stream", "", nil, "partial", "partial"},
		{"legacy engine error keeps stream", "", errors.New("context canceled"), "partial", "partial"},
		{"real failure keeps engine result", "", errors.New("boom"), "partial", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := finishText(tc.full, tc.err, tc.streamed); got != tc.want {
				t.Fatalf("finishText(%q, %v, %q) = %q, want %q",
					tc.full, tc.err, tc.streamed, got, tc.want)
			}
		})
	}
}

// TestCompleteFromPromptShowsEngineError locks the native symptom: a genuine
// engine failure must surface the error text and the "引擎错误" status. Before
// the fix, sendReal set both and then called finish(full), which overwrote them
// with the engine's empty result and "已完成" — failures looked like successes.
func TestCompleteFromPromptShowsEngineError(t *testing.T) {
	cases := []struct {
		name       string
		streamed   string
		full       string
		err        error
		wantText   string
		wantStatus string
	}{
		{"success keeps full answer", "partial", "answer", nil, "answer", "已完成"},
		{"success without stream uses full", "", "answer", nil, "answer", "已完成"},
		{"cancellation keeps streamed", "partial", "", context.Canceled, "partial", "已完成"},
		{"cancelled with full keeps streamed", "partial", "partial", context.Canceled, "partial", "已完成"},
		{"engine error shows banner", "partial", "", errors.New("provider returned 500"), "⚠ 引擎错误: provider returned 500", "引擎错误"},
		{"engine error ignores empty full", "", "", errors.New("provider returned 500"), "⚠ 引擎错误: provider returned 500", "引擎错误"},
		{"engine error is redacted and one line", "", "", errors.New("sse reset api_key: sk-proj-abcdefghijklmnop\nsecond line"), "⚠ 引擎错误: sse reset api_key: [redacted] second line", "引擎错误"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := &message{id: 1, kind: msgAssistant}
			// A non-nil engine keeps finish() from injecting the demo approval.
			app := &chat{reply: reply, streaming: true, engine: &bridge{}}
			app.messages = []*message{reply}

			app.completeFromPrompt(tc.streamed, tc.full, tc.err)

			if reply.text != tc.wantText {
				t.Fatalf("reply text = %q, want %q", reply.text, tc.wantText)
			}
			if !reply.done {
				t.Fatal("reply should be marked done")
			}
			if app.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", app.status, tc.wantStatus)
			}
			if app.streaming {
				t.Fatal("streaming should be false after completion")
			}
			if app.reply != nil {
				t.Fatal("current reply pointer should be cleared")
			}
		})
	}
}
