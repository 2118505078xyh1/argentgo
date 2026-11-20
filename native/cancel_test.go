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

// TestFinishTextKeepsStreamedOnCancellation 守护原生症状：
// 被中止的回复必须保留用户已经看到的流式文本，而真实
// 失败保留 engine 自己的结果。
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

// TestCompleteFromPromptShowsEngineError 锁定原生症状：真实的
// engine failure must surface the error text and the "引擎错误" status. Before
// 在修复之前，sendReal 同时设置两者然后调用 finish(full)，覆盖了它们。
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
