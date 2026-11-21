package hooks

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVerdictOrdering(t *testing.T) {
	if !(VerdictNone < VerdictAllow && VerdictAllow < VerdictDeny) {
		t.Fatalf("verdict ordering must be none < allow < deny: none=%d allow=%d deny=%d",
			VerdictNone, VerdictAllow, VerdictDeny)
	}
}

func TestVerdictFromOutcomeExitCodes(t *testing.T) {
	tests := []struct {
		name string
		out  Outcome
		want Verdict
	}{
		{"exit2_no_stdout", Outcome{ExitCode: 2}, VerdictDeny},
		{"exit2_with_cjk_output", Outcome{ExitCode: 2, Stdout: "禁止执行：请检查权限"}, VerdictDeny},
		{"exit49_allow_json", Outcome{ExitCode: 49, Stdout: `{"permissionDecision":"allow"}`}, VerdictAllow},
		{"exit49_deny_json", Outcome{ExitCode: 49, Stdout: `{"permissionDecision":"deny"}`}, VerdictDeny},
		{"exit49_no_json", Outcome{ExitCode: 49, Stdout: "no decision here"}, VerdictNone},
		{"exit49_malformed_json", Outcome{ExitCode: 49, Stdout: `{"permissionDecision":`}, VerdictNone},
		{"exit0_no_json", Outcome{ExitCode: 0}, VerdictNone},
		{"exit1_no_json", Outcome{ExitCode: 1}, VerdictNone},
		{"exit3_no_json", Outcome{ExitCode: 3}, VerdictNone},
		{"exit_neg1_no_json", Outcome{ExitCode: -1}, VerdictNone},
		{"timeout_no_json", Outcome{ExitCode: -1, TimedOut: true}, VerdictNone},
		{"timeout_with_allow_json", Outcome{TimedOut: true, Stdout: `{"permissionDecision":"allow"}`}, VerdictNone},
		{"timeout_with_deny_json", Outcome{TimedOut: true, Stdout: `{"permissionDecision":"deny"}`}, VerdictNone},
		// 退出码 2 是绝对阻断信号：即便 stdout 声明 allow，deny 仍胜出。
		{"exit2_with_allow_json_deny_wins", Outcome{ExitCode: 2, Stdout: `{"permissionDecision":"allow"}`}, VerdictDeny},
		// 结构化决策码与 stdout 声明冲突时，deny 仍胜出。
		{"exit49_conflict_deny_wins", Outcome{ExitCode: 49, Stdout: `{"permissionDecision":"allow","hookSpecificOutput":{"permissionDecision":"deny"}}`}, VerdictDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerdictFromOutcome(tt.out); got != tt.want {
				t.Fatalf("VerdictFromOutcome(%+v) = %d, want %d", tt.out, got, tt.want)
			}
		})
	}
}

func TestVerdictFromOutcomeJSONVariants(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   Verdict
	}{
		{"top_level_allow", `{"permissionDecision":"allow"}`, VerdictAllow},
		{"top_level_deny", `{"permissionDecision":"deny"}`, VerdictDeny},
		{"nested_allow", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`, VerdictAllow},
		{"nested_deny", `{"hookSpecificOutput":{"permissionDecision":"deny"}}`, VerdictDeny},
		{"uppercase_allow", `{"permissionDecision":"ALLOW"}`, VerdictAllow},
		{"mixed_case_deny", `{"permissionDecision":" DeNy "}`, VerdictDeny},
		{"leading_log_then_json", "2026/01/01 hook starting\n{\"permissionDecision\":\"deny\"}", VerdictDeny},
		{"log_brace_then_json", "config {not json}\n{\"permissionDecision\":\"allow\"}", VerdictAllow},
		{"no_decision_field", `{"reason":"清晰的原因","hookEventName":"PreToolUse"}`, VerdictNone},
		{"empty_stdout", "", VerdictNone},
		{"malformed_object", `{"permissionDecision":}`, VerdictNone},
		{"truncated_json", `{"permissionDecision":"deny`, VerdictNone},
		{"wrong_type_number", `{"permissionDecision":1}`, VerdictNone},
		{"wrong_type_object", `{"permissionDecision":{}}`, VerdictNone},
		{"unknown_value_ask", `{"permissionDecision":"ask"}`, VerdictNone},
		{"cjk_value_unrecognized", `{"permissionDecision":"允许"}`, VerdictNone},
		// 多对象：deny 绝对优先，与出现顺序无关。
		{"two_objects_deny_first", `{"permissionDecision":"deny"}{"permissionDecision":"allow"}`, VerdictDeny},
		{"two_objects_allow_first", `{"permissionDecision":"allow"}{"permissionDecision":"deny"}`, VerdictDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Outcome{ExitCode: 0, Stdout: tt.stdout}
			if got := VerdictFromOutcome(out); got != tt.want {
				t.Fatalf("VerdictFromOutcome(stdout=%q) = %d, want %d", tt.stdout, got, tt.want)
			}
		})
	}
}

// CJK 消息不得按固定字节数切割：长中文原因字段应被完整当作字符串解码，
// 不 panic、不误判，且原始 Outcome 不被改动。
func TestVerdictFromOutcomeCJKSafe(t *testing.T) {
	reason := strings.Repeat("拒绝原因：工作区越界且需要审批", 500)
	stdout := `{"permissionDecision":"deny","permissionDecisionReason":"` + reason + `"}`
	out := Outcome{ExitCode: 0, Stdout: stdout}
	original := out

	if got := VerdictFromOutcome(out); got != VerdictDeny {
		t.Fatalf("CJK reason must not affect decision: got %d, want deny", got)
	}
	if out.Stdout != original.Stdout || out.ExitCode != original.ExitCode {
		t.Fatal("VerdictFromOutcome must not mutate its input")
	}
	if !utf8.ValidString(out.Stdout) {
		t.Fatal("input CJK message must remain valid UTF-8")
	}

	// 嵌入非法 UTF-8 字节也不得 panic；解码器会安全替换，裁决仍可得出。
	broken := `{"permissionDecision":"allow","reason":"` + string([]byte{0xff, 0xfe}) + `"}`
	if got := VerdictFromOutcome(Outcome{Stdout: broken}); got != VerdictAllow {
		t.Fatalf("invalid UTF-8 must not panic nor flip a valid decision: got %d", got)
	}
}

// 纯函数性质：相同输入多次调用结果一致，且不产生任何外部可见状态。
func TestVerdictFromOutcomeDeterministic(t *testing.T) {
	out := Outcome{ExitCode: 49, Stdout: `{"permissionDecision":"allow"}`}
	first := VerdictFromOutcome(out)
	for i := 0; i < 5; i++ {
		if got := VerdictFromOutcome(out); got != first {
			t.Fatalf("call %d returned %d, want %d", i, got, first)
		}
	}
}

func TestAggregateVerdictBasics(t *testing.T) {
	tests := []struct {
		name string
		in   []Verdict
		want Verdict
	}{
		{"nil", nil, VerdictNone},
		{"empty", []Verdict{}, VerdictNone},
		{"single_none", []Verdict{VerdictNone}, VerdictNone},
		{"single_allow", []Verdict{VerdictAllow}, VerdictAllow},
		{"single_deny", []Verdict{VerdictDeny}, VerdictDeny},
		{"allow_and_none", []Verdict{VerdictNone, VerdictAllow}, VerdictAllow},
		{"deny_and_allow", []Verdict{VerdictAllow, VerdictDeny}, VerdictDeny},
		{"deny_absolute", []Verdict{VerdictDeny, VerdictAllow, VerdictNone}, VerdictDeny},
		{"duplicates_allow", []Verdict{VerdictAllow, VerdictAllow, VerdictNone}, VerdictAllow},
		{"duplicates_deny", []Verdict{VerdictDeny, VerdictDeny, VerdictAllow}, VerdictDeny},
		{"many_none", []Verdict{VerdictNone, VerdictNone, VerdictNone}, VerdictNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AggregateVerdict(tt.in); got != tt.want {
				t.Fatalf("AggregateVerdict(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestAggregateVerdictOrderIndependent(t *testing.T) {
	// 穷举 {none, allow, deny} 的所有排列，结果必须一致（deny）。
	values := []Verdict{VerdictNone, VerdictAllow, VerdictDeny}
	permutations := [][]Verdict{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2},
		{1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
	for _, p := range permutations {
		in := []Verdict{values[p[0]], values[p[1]], values[p[2]]}
		if got := AggregateVerdict(in); got != VerdictDeny {
			t.Fatalf("permutation %v = %d, want deny", in, got)
		}
	}

	// 含重复的乱序输入同样幂等。
	in := []Verdict{VerdictAllow, VerdictNone, VerdictDeny, VerdictAllow, VerdictNone}
	want := AggregateVerdict(in)
	reversed := []Verdict{VerdictNone, VerdictAllow, VerdictDeny, VerdictNone, VerdictAllow}
	if got := AggregateVerdict(reversed); got != want {
		t.Fatalf("reversed aggregation = %d, want %d", got, want)
	}
	if want != VerdictDeny {
		t.Fatalf("expected deny, got %d", want)
	}
}

func TestAggregateVerdictIdempotent(t *testing.T) {
	// 重复聚合（含把结果再次并入）不得改变结果。
	base := []Verdict{VerdictNone, VerdictAllow, VerdictDeny}
	first := AggregateVerdict(base)
	for i := 0; i < 5; i++ {
		again := AggregateVerdict([]Verdict{first, first, VerdictAllow})
		if again != first {
			t.Fatalf("aggregation not idempotent: %d -> %d", first, again)
		}
	}

	// 分批聚合与一次性聚合一致（结合律）。
	all := []Verdict{VerdictDeny, VerdictNone, VerdictAllow, VerdictAllow, VerdictNone}
	split := higherVerdict(AggregateVerdict(all[:2]), AggregateVerdict(all[2:]))
	if split != AggregateVerdict(all) {
		t.Fatalf("batched aggregation = %d, whole = %d", split, AggregateVerdict(all))
	}
}
