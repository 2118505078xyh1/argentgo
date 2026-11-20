package hooks

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStopHookVerdict(t *testing.T) {
	tests := []struct {
		name             string
		exitCode         int
		message          string
		wantBlock        bool
		wantContinuation string
	}{
		{"exit2_with_cjk_message", 2, "请继续修复失败的测试", true, "请继续修复失败的测试"},
		{"exit2_message_trimmed", 2, "  go on  ", true, "go on"},
		{"exit2_empty_message", 2, "", false, ""},
		{"exit2_whitespace_message", 2, " \t\n ", false, ""},
		{"exit0_with_message", 0, "请继续", false, ""},
		{"exit1_with_message", 1, "请继续", false, ""},
		{"exit3_with_message", 3, "请继续", false, ""},
		{"negative_exit_with_message", -1, "请继续", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, continuation := stopHookVerdict(tt.exitCode, tt.message)
			if block != tt.wantBlock || continuation != tt.wantContinuation {
				t.Fatalf("stopHookVerdict(%d, %q) = (%v, %q), want (%v, %q)",
					tt.exitCode, tt.message, block, continuation, tt.wantBlock, tt.wantContinuation)
			}
		})
	}
}

func TestStopHookVerdictCJKSafe(t *testing.T) {
	message := "请继续完成剩余的重构工作"
	block, continuation := stopHookVerdict(2, message)
	if !block || continuation != message {
		t.Fatalf("CJK message must survive verbatim: block=%v continuation=%q", block, continuation)
	}
}

func TestStopHookVerdictBoundsLongMessage(t *testing.T) {
	// 每个词 8 字节，重复后远超上限，必然触发截断。
	message := strings.Repeat("继续测试", 2000)
	block, continuation := stopHookVerdict(2, message)
	if !block {
		t.Fatal("exit 2 with a long message must block")
	}
	if len(continuation) > maxContinuationBytes {
		t.Fatalf("continuation not bounded: %d > %d", len(continuation), maxContinuationBytes)
	}
	if !utf8.ValidString(continuation) {
		t.Fatalf("continuation cut a UTF-8 rune: %q", continuation)
	}
	if !strings.HasSuffix(continuation, "…") {
		t.Fatalf("truncated continuation must end with an ellipsis: %q", continuation)
	}
	if !strings.HasPrefix(message, strings.TrimSuffix(continuation, "…")) {
		t.Fatalf("continuation must be a rune-aligned prefix of the message: %q", continuation)
	}
}

// stopRunner 返回一个总是产出给定退出码与输出的注入式 runner。
func stopRunner(exitCode int, stdout string) CommandRunner {
	return func(ctx context.Context, command Command, payload Payload) Outcome {
		return Outcome{Event: command.Event, Command: command.Command, ExitCode: exitCode, Stdout: stdout}
	}
}

func TestStopBlockingIsOptIn(t *testing.T) {
	config := &Config{Commands: []Command{{Event: Stop, Command: "stop-hook"}}}
	payload := Payload{Event: string(Stop)}

	// 默认关闭：Stop 仅作事后通知，退出码 2 只产生警告，不阻断也不带续跑提示。
	result, err := (&Dispatcher{Runner: stopRunner(2, "请继续")}).Dispatch(context.Background(), config, payload)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked || result.Continuation != "" {
		t.Fatalf("default Stop must not block: %+v", result)
	}
	if !strings.Contains(result.Message, "exit 2") {
		t.Fatalf("default Stop must still warn: %q", result.Message)
	}

	// 显式开启：退出码 2 带消息应阻断，并暴露续跑提示。
	result, err = (&Dispatcher{StopBlocks: true, Runner: stopRunner(2, "请继续")}).Dispatch(context.Background(), config, payload)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("opted-in Stop must block: %+v", result)
	}
	if result.Continuation != "请继续" {
		t.Fatalf("continuation lost: %q", result.Continuation)
	}
}

func TestStopOptInRequiresNonEmptyMessage(t *testing.T) {
	config := &Config{Commands: []Command{{Event: Stop, Command: "stop-hook"}}}
	tests := []struct {
		name     string
		exitCode int
		stdout   string
	}{
		{"empty_message", 2, ""},
		{"whitespace_message", 2, " \n "},
		{"exit0", 0, "请继续"},
		{"exit1", 1, "请继续"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Dispatcher{StopBlocks: true, Runner: stopRunner(tt.exitCode, tt.stdout)}
			result, err := d.Dispatch(context.Background(), config, Payload{Event: string(Stop)})
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if result.Blocked || result.Continuation != "" {
				t.Fatalf("must not block: %+v", result)
			}
		})
	}
}

func TestStopOptInDoesNotAffectOtherEvents(t *testing.T) {
	config := &Config{Commands: []Command{{Event: PostToolUse, Command: "post"}}}
	d := &Dispatcher{StopBlocks: true, Runner: stopRunner(2, "请继续")}
	result, err := d.Dispatch(context.Background(), config, Payload{Event: string(PostToolUse)})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked || result.Continuation != "" {
		t.Fatalf("StopBlocks must only affect Stop events: %+v", result)
	}
}

func TestSummarizeCarriesStopContinuation(t *testing.T) {
	result := Summarize([]Outcome{
		{Event: Stop, Command: "a", ExitCode: 2, Blocked: true, Continuation: "请继续", Stdout: "请继续"},
	})
	if !result.Blocked || result.Continuation != "请继续" {
		t.Fatalf("continuation not aggregated into result: %+v", result)
	}
}
