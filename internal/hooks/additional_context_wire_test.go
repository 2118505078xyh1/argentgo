package hooks

import (
	"context"
	"strings"
	"testing"
)

// additionalContextRunner 返回一个总是产出给定 stdout 的注入式 runner，
// 用于在 Outcome 产出点验证 AdditionalContext 字段被填充。
func additionalContextRunner(stdout string) CommandRunner {
	return func(_ context.Context, command Command, _ Payload) Outcome {
		return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0, Stdout: stdout}
	}
}

// runOutcomeWithStdout 驱动一次 Run 并返回唯一的 Outcome，覆盖真实路径的产出收口。
func runOutcomeWithStdout(t *testing.T, stdout string) Outcome {
	t.Helper()
	config := &Config{Commands: []Command{{Event: PostToolUse, Command: "ctx-hook"}}}
	outcomes, err := (&Dispatcher{Runner: additionalContextRunner(stdout)}).Run(
		context.Background(), config, Payload{Event: string(PostToolUse)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(outcomes))
	}
	return outcomes[0]
}

// TestOutcomeAdditionalContextFilled 确认 hook stdout 中的 additionalContext 在 Outcome 产出时被提取填字段，
// 顶层与 hookSpecificOutput 两种写法都支持，CJK 文本原样保留。
func TestOutcomeAdditionalContextFilled(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{"top-level", `{"additionalContext":"请只改 loop.go"}`, "请只改 loop.go"},
		{"nested", `{"hookSpecificOutput":{"additionalContext":"记得跑 go vet"}}`, "记得跑 go vet"},
		{"with log prefix", `starting...{"additionalContext":"上下文"}`, "上下文"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runOutcomeWithStdout(t, tt.stdout).AdditionalContext
			if got != tt.want {
				t.Fatalf("AdditionalContext = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOutcomeAdditionalContextNoFieldByteIdentical 确认没有 additionalContext 时字段保持零值，
// 且 stdout 原样保留——默认绝大多数 hook 走这条路径，行为完全不变。
func TestOutcomeAdditionalContextNoFieldByteIdentical(t *testing.T) {
	stdout := "hook printed something without json"
	out := runOutcomeWithStdout(t, stdout)
	if out.AdditionalContext != "" {
		t.Fatalf("AdditionalContext = %q, want empty", out.AdditionalContext)
	}
	if out.Stdout != stdout {
		t.Fatalf("Stdout was mutated: %q, want %q", out.Stdout, stdout)
	}
}

// TestOutcomeAdditionalContextTimeoutEmpty 确认超时的 hook 即使 stdout 携带字段也不填充，
// 与 AdditionalContextFromOutcome 的安全约定一致（超时stdout不可信）。
func TestOutcomeAdditionalContextTimeoutEmpty(t *testing.T) {
	config := &Config{Commands: []Command{{Event: PostToolUse, Command: "ctx-hook"}}}
	outcomes, err := (&Dispatcher{Runner: func(_ context.Context, command Command, _ Payload) Outcome {
		return Outcome{Event: command.Event, Command: command.Command, TimedOut: true,
			Stdout: `{"additionalContext":"不可信"}`}
	}}).Run(context.Background(), config, Payload{Event: string(PostToolUse)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].AdditionalContext != "" {
		t.Fatalf("timed-out outcome must not carry context: %+v", outcomes)
	}
}

// TestOutcomeAdditionalContextBounded 确认单条字段被 hooks 侧的字节上限钳制且 CJK 不被切成半个 rune。
func TestOutcomeAdditionalContextBounded(t *testing.T) {
	huge := strings.Repeat("中", maxAdditionalContextBytes) // 3 字节/rune，远超上限
	out := runOutcomeWithStdout(t, `{"additionalContext":"`+huge+`"}`)
	got := out.AdditionalContext
	if len(got) > maxAdditionalContextBytes {
		t.Fatalf("AdditionalContext too long: %d > %d", len(got), maxAdditionalContextBytes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped context must end with ellipsis: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatal("clipped context must not contain replacement rune")
	}
}

// TestDispatchCarriesOutcomeAdditionalContext 确认通过 Dispatch 聚合路径产出的 Outcome 同样带字段，
// 使 app 层的 hook 桥接能直接读取（字段填充在 guardedRunCommand 统一收口处）。
func TestDispatchCarriesOutcomeAdditionalContext(t *testing.T) {
	config := &Config{Commands: []Command{{Event: PostToolUse, Command: "ctx-hook"}}}
	outcomes, err := (&Dispatcher{Runner: additionalContextRunner(`{"additionalContext":"聚合上下文"}`)}).Run(
		context.Background(), config, Payload{Event: string(PostToolUse)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].AdditionalContext != "聚合上下文" {
		t.Fatalf("outcomes = %+v, want one with AdditionalContext", outcomes)
	}
}
