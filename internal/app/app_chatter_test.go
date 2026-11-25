package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// fallbackScriptChatter 是一个伪 Chatter，记录它被要求使用的模型，
// 并由脚本驱动每次调用。不使用网络或真实模型。
type fallbackScriptChatter struct {
	calls  []string
	script func(call int, model string, onText func(string)) (llm.Message, error)
}

func (c *fallbackScriptChatter) Chat(_ context.Context, model string, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.calls = append(c.calls, model)
	return c.script(len(c.calls), model, onText)
}

func emit(text string, onText func(string)) {
	for _, chunk := range strings.SplitAfter(text, "-") {
		if chunk != "" {
			onText(chunk)
		}
	}
}

// TestFallbackChatterDropsPartialPrimaryText 锁定 bug #1：主模型
// 流式输出半个答案后失败，其文本绝不能泄漏进
// 回退模型的答案。
func TestFallbackChatterDropsPartialPrimaryText(t *testing.T) {
	primary := "PARTIAL-FROM-PRIMARY-"
	backup := "FULL-ANSWER-FROM-FALLBACK"
	inner := &fallbackScriptChatter{script: func(call int, model string, onText func(string)) (llm.Message, error) {
		switch call {
		case 1:
			if model != "primary" {
				t.Fatalf("first model = %q, want primary", model)
			}
			emit(primary, onText)
			return llm.Message{}, errors.New("HTTP 429: too many requests")
		case 2:
			if model != "backup" {
				t.Fatalf("second model = %q, want backup", model)
			}
			emit(backup, onText)
			return llm.Message{Role: "assistant", Content: backup}, nil
		default:
			t.Fatalf("unexpected call %d", call)
			return llm.Message{}, nil
		}
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	var shown strings.Builder
	reply, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
		shown.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Content != backup {
		t.Fatalf("reply = %q, want %q", reply.Content, backup)
	}
	if got := shown.String(); got != backup {
		t.Fatalf("streamed text = %q, want exactly %q (partial primary text leaked)", got, backup)
	}
}

// TestFallbackChatterReplaysSuccessfulPrimaryText 保持成功路径：当
// 主模型成功时，其增量会被转发，且不会发出回退请求，
// 即使配置了回退模型也是如此。
func TestFallbackChatterReplaysSuccessfulPrimaryText(t *testing.T) {
	answer := "ANSWER-FROM-PRIMARY"
	inner := &fallbackScriptChatter{script: func(call int, _ string, onText func(string)) (llm.Message, error) {
		if call != 1 {
			t.Fatalf("unexpected fallback call %d", call)
		}
		emit(answer, onText)
		return llm.Message{Role: "assistant", Content: answer}, nil
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	var shown strings.Builder
	if _, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
		shown.WriteString(delta)
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := shown.String(); got != answer {
		t.Fatalf("streamed text = %q, want %q", got, answer)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("requests = %d, want 1 (successful primary must not retry)", len(inner.calls))
	}
}

// TestFallbackChatterRetriesOnlyRetryableErrors 锁定 bug #2：确定性的
// 提供方错误会立即返回，而瞬时失败仍会
// 切换到回退模型。
func TestFallbackChatterRetriesOnlyRetryableErrors(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{"rate limit", errors.New("HTTP 429: too many requests"), 2},
		{"server error", errors.New("provider returned 503: service unavailable"), 2},
		{"timeout prose", errors.New("upstream is 5xx right now"), 2},
		{"dial timeout", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, 2},
		{"unauthorized", errors.New("provider returned 401 Unauthorized: invalid key"), 1},
		{"validation", errors.New("400 invalid request: unknown role"), 1},
		{"content filter", errors.New("provider returned 400 Bad Request: content_filter"), 1},
		{"hard quota 402", errors.New("provider returned 402 Payment Required: quota exceeded"), 1},
		{"hard quota 429", errors.New("provider returned 429: quota_exceeded for this account"), 1},
		{"forbidden", errors.New("403 forbidden: permission denied"), 1},
		{"unknown application error", errors.New("record not found"), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fallbackScriptChatter{script: func(_ int, model string, onText func(string)) (llm.Message, error) {
				if model == "backup" {
					onText("backup answer")
					return llm.Message{Role: "assistant", Content: "backup answer"}, nil
				}
				return llm.Message{}, tt.err
			}}
			chatter := &fallbackChatter{primary: inner, fallback: "backup"}

			var shown strings.Builder
			reply, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
				shown.WriteString(delta)
			})
			if tt.wantCalls == 1 {
				if err == nil || !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want the original error", err)
				}
				if shown.Len() != 0 {
					t.Fatalf("streamed text = %q, want empty", shown.String())
				}
			} else {
				if err != nil {
					t.Fatalf("Chat: %v", err)
				}
				if reply.Content != "backup answer" {
					t.Fatalf("reply = %q, want the backup answer", reply.Content)
				}
			}
			if len(inner.calls) != tt.wantCalls {
				t.Fatalf("requests = %d, want %d", len(inner.calls), tt.wantCalls)
			}
		})
	}
}

// TestFallbackChatterDoesNotRetryAfterCancellation 保持取消
// 保证：被取消的 context 会以单次请求停止运行。
func TestFallbackChatterDoesNotRetryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := &fallbackScriptChatter{script: func(_ int, _ string, _ func(string)) (llm.Message, error) {
		cancel()
		return llm.Message{}, errors.New("HTTP 429: too many requests")
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	// 提供方的错误被原样返回；关键在于被取消的
	// context 会停止运行，而不发出第二次请求。
	if _, err := chatter.Chat(ctx, "primary", nil, nil, nil); err == nil {
		t.Fatal("cancelled run returned nil error")
	}
	if len(inner.calls) != 1 {
		t.Fatalf("requests = %d, want 1 (cancellation must not retry)", len(inner.calls))
	}
}

// TestFallbackChatterDropsPartialFallbackTextOnFailure 覆盖两个模型都失败的情形：
// 任何一个模型的部分文本都不能到达操作人员。
func TestFallbackChatterDropsPartialFallbackTextOnFailure(t *testing.T) {
	inner := &fallbackScriptChatter{script: func(call int, _ string, onText func(string)) (llm.Message, error) {
		if call == 1 {
			onText("HALF-FROM-PRIMARY-")
			return llm.Message{}, errors.New("HTTP 503: service unavailable")
		}
		onText("HALF-FROM-BACKUP-")
		return llm.Message{}, errors.New("HTTP 502: bad gateway")
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	var shown strings.Builder
	if _, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
		shown.WriteString(delta)
	}); err == nil {
		t.Fatal("both models failed but Chat returned nil error")
	}
	if got := shown.String(); got != "" {
		t.Fatalf("streamed text = %q, want empty (failed attempts must not leak)", got)
	}
	if len(inner.calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(inner.calls))
	}
}
