package eval

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestEvaluateTaskAllStepsCompleted 覆盖全完成：无噪声的直接命中，以及夹杂噪声轮次后仍按顺序命中。
// 噪声条目不计入 StepsDone，但计入 TurnsUsed。
func TestEvaluateTaskAllStepsCompleted(t *testing.T) {
	steps := []TaskStep{
		{ExpectedTool: "read", ExpectedPath: "config.yaml"},
		{ExpectedTool: "write", ExpectedPath: "out.txt"},
	}

	for _, tc := range []struct {
		name       string
		transcript []string
		wantTurns  int
	}{
		{
			name:       "exact order",
			transcript: []string{"read config.yaml", "write out.txt"},
			wantTurns:  2,
		},
		{
			// 最后一条 "done" 不会被消费：命中全部步骤后立即停止。
			name:       "noise between steps",
			transcript: []string{"list /", "read config.yaml", "check status", "write out.txt", "done"},
			wantTurns:  4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateTask(LongTask{Name: "deploy", Steps: steps, MaxTurns: 0}, tc.transcript)
			want := TaskResult{
				TaskName:   "deploy",
				Completed:  true,
				StepsDone:  2,
				StepsTotal: 2,
				TurnsUsed:  tc.wantTurns,
				StopReason: TaskStopCompleted,
			}
			if got != want {
				t.Fatalf("EvaluateTask() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestEvaluateTaskPartialCompletion 覆盖部分完成：transcript 提前结束，剩余步骤未命中。
func TestEvaluateTaskPartialCompletion(t *testing.T) {
	task := LongTask{
		Name: "three steps",
		Steps: []TaskStep{
			{ExpectedTool: "read", ExpectedPath: "a.txt"},
			{ExpectedTool: "write", ExpectedPath: "b.txt"},
			{ExpectedTool: "delete", ExpectedPath: "c.txt"},
		},
	}
	transcript := []string{"read a.txt", "write b.txt"}

	got := EvaluateTask(task, transcript)
	want := TaskResult{
		TaskName:   "three steps",
		Completed:  false,
		StepsDone:  2,
		StepsTotal: 3,
		TurnsUsed:  2,
		StopReason: TaskStopTranscriptExhausted,
	}
	if got != want {
		t.Fatalf("EvaluateTask() = %+v, want %+v", got, want)
	}
}

// TestEvaluateTaskOrderMatters 锁定"按顺序出现"：乱序 transcript 只能命中第一步，
// 即使后续包含全部步骤所需的工具调用也不会回头补上。
func TestEvaluateTaskOrderMatters(t *testing.T) {
	task := LongTask{
		Name: "ordered",
		Steps: []TaskStep{
			{ExpectedTool: "read", ExpectedPath: "a.txt"},
			{ExpectedTool: "write", ExpectedPath: "b.txt"},
		},
	}

	got := EvaluateTask(task, []string{"write b.txt", "read a.txt"})
	if got.Completed || got.StepsDone != 1 || got.StopReason != TaskStopTranscriptExhausted {
		t.Fatalf("out-of-order transcript = %+v, want StepsDone=1 and transcript_exhausted", got)
	}

	got = EvaluateTask(task, []string{"read a.txt", "write b.txt"})
	if !got.Completed || got.StepsDone != 2 || got.StopReason != TaskStopCompleted {
		t.Fatalf("in-order transcript = %+v, want full completion", got)
	}
}

// TestEvaluateTaskEmpty 覆盖空输入：
//   - 有步骤但 transcript 为空 -> 不完成，transcript_exhausted；
//   - 无步骤 -> 按真空真值视为完成，no_steps，且不消费任何轮次。
func TestEvaluateTaskEmpty(t *testing.T) {
	withSteps := LongTask{
		Name:  "needs one step",
		Steps: []TaskStep{{ExpectedTool: "read"}},
	}
	for _, transcript := range [][]string{nil, {}} {
		got := EvaluateTask(withSteps, transcript)
		want := TaskResult{
			TaskName:   "needs one step",
			StepsDone:  0,
			StepsTotal: 1,
			TurnsUsed:  0,
			StopReason: TaskStopTranscriptExhausted,
		}
		if got != want {
			t.Fatalf("empty transcript = %+v, want %+v", got, want)
		}
	}

	got := EvaluateTask(LongTask{Name: "no steps"}, []string{"read a.txt", "write b.txt"})
	want := TaskResult{
		TaskName:   "no steps",
		Completed:  true,
		StepsDone:  0,
		StepsTotal: 0,
		TurnsUsed:  0,
		StopReason: TaskStopNoSteps,
	}
	if got != want {
		t.Fatalf("empty task = %+v, want %+v", got, want)
	}
}

// TestEvaluateTaskMaxTurns 覆盖轮次上限：额度用尽即停止，剩余条目不再消费。
// 负数/零表示无界；恰好在上限内完成应报告 completed 而非 max_turns。
func TestEvaluateTaskMaxTurns(t *testing.T) {
	steps := []TaskStep{
		{ExpectedTool: "read", ExpectedPath: "a.txt"},
		{ExpectedTool: "write", ExpectedPath: "b.txt"},
	}

	for _, tc := range []struct {
		name       string
		maxTurns   int
		transcript []string
		want       TaskResult
	}{
		{
			name:       "cut off before second step",
			maxTurns:   1,
			transcript: []string{"read a.txt", "write b.txt"},
			want: TaskResult{
				TaskName: "capped", StepsDone: 1, StepsTotal: 2, TurnsUsed: 1,
				StopReason: TaskStopMaxTurns,
			},
		},
		{
			name:       "noise consumes the budget",
			maxTurns:   2,
			transcript: []string{"noise", "read a.txt", "write b.txt"},
			want: TaskResult{
				TaskName: "capped", StepsDone: 1, StepsTotal: 2, TurnsUsed: 2,
				StopReason: TaskStopMaxTurns,
			},
		},
		{
			name:       "completion within budget wins",
			maxTurns:   2,
			transcript: []string{"read a.txt", "write b.txt", "extra"},
			want: TaskResult{
				TaskName: "capped", Completed: true, StepsDone: 2, StepsTotal: 2, TurnsUsed: 2,
				StopReason: TaskStopCompleted,
			},
		},
		{
			name:       "negative means unlimited",
			maxTurns:   -5,
			transcript: []string{"read a.txt", "write b.txt"},
			want: TaskResult{
				TaskName: "capped", Completed: true, StepsDone: 2, StepsTotal: 2, TurnsUsed: 2,
				StopReason: TaskStopCompleted,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateTask(LongTask{Name: "capped", Steps: steps, MaxTurns: tc.maxTurns}, tc.transcript)
			if got != tc.want {
				t.Fatalf("EvaluateTask() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestEvaluateTaskVerifyCallback 锁定 Verify 的调用语义：
//   - 静态筛子通过后才会调用 Verify，参数恰为原始 transcript 条目；
//   - Verify 返回 false 时步骤不推进，直到后续条目通过；
//   - 静态筛子失败时完全不调用 Verify。
func TestEvaluateTaskVerifyCallback(t *testing.T) {
	var seen []string
	step := TaskStep{
		ExpectedTool: "read",
		Verify: func(entry string) bool {
			seen = append(seen, entry)
			return strings.HasSuffix(entry, ".go")
		},
	}
	task := LongTask{Name: "verify", Steps: []TaskStep{step}}

	got := EvaluateTask(task, []string{"write notes.txt", "read notes.txt", "read main.go"})
	want := TaskResult{
		TaskName: "verify", Completed: true, StepsDone: 1, StepsTotal: 1,
		TurnsUsed: 3, StopReason: TaskStopCompleted,
	}
	if got != want {
		t.Fatalf("EvaluateTask() = %+v, want %+v", got, want)
	}
	if wantSeen := []string{"read notes.txt", "read main.go"}; !reflect.DeepEqual(seen, wantSeen) {
		t.Fatalf("Verify saw %q, want %q (write entry must not reach Verify)", seen, wantSeen)
	}
}

// TestEvaluateTaskCJK 覆盖中文与 emoji：子串匹配在完整 rune 边界上生效，
// 形近但不同的 CJK 文本不会误命中；匹配成功后输出字段仍是合法 UTF-8。
func TestEvaluateTaskCJK(t *testing.T) {
	const sample = "你好.go"
	if utf8.RuneCountInString(sample) >= len(sample) {
		t.Fatalf("test premise broken: sample is ASCII-sized")
	}

	task := LongTask{
		Name: "读取配置🙂",
		Steps: []TaskStep{
			{ExpectedTool: "读取", ExpectedPath: sample},
			{ExpectedTool: "写入", ExpectedPath: "结果.md"},
		},
	}

	// 形近字「号」与「好」不同，第一步应跳过该条目，由下一条正确命中。
	transcript := []string{"读取 你号.go", "读取 你好.go 🚀", "干扰🚀", "写入 结果.md"}
	got := EvaluateTask(task, transcript)
	want := TaskResult{
		TaskName: "读取配置🙂", Completed: true, StepsDone: 2, StepsTotal: 2,
		TurnsUsed: 4, StopReason: TaskStopCompleted,
	}
	if got != want {
		t.Fatalf("EvaluateTask() = %+v, want %+v", got, want)
	}
	if !utf8.ValidString(got.TaskName) || !utf8.ValidString(got.StopReason) {
		t.Fatalf("result contains invalid UTF-8: %+v", got)
	}
}

// TestEvaluateTaskDoesNotMutateInput 锁定纯函数语义：Steps 切片与 transcript 调用前后完全一致。
// func 值无法用 DeepEqual 比较，因此静态字段逐字段快照，Verify 则检查仍非 nil 且语义未变。
func TestEvaluateTaskDoesNotMutateInput(t *testing.T) {
	steps := []TaskStep{
		{ExpectedTool: "read", Verify: func(string) bool { return true }},
		{ExpectedTool: "write", ExpectedPath: "x.txt"},
	}
	type stepSnapshot struct{ tool, path string }
	snapshot := func(in []TaskStep) []stepSnapshot {
		out := make([]stepSnapshot, len(in))
		for i, s := range in {
			out[i] = stepSnapshot{s.ExpectedTool, s.ExpectedPath}
		}
		return out
	}
	stepsBefore := snapshot(steps)
	transcript := []string{"read a.txt", "noise", "write x.txt"}
	transcriptBefore := append([]string(nil), transcript...)

	EvaluateTask(LongTask{Name: "pure", Steps: steps, MaxTurns: 10}, transcript)

	if got := snapshot(steps); !reflect.DeepEqual(got, stepsBefore) {
		t.Fatalf("Steps mutated:\n got %+v\nwant %+v", got, stepsBefore)
	}
	if steps[0].Verify == nil || !steps[0].Verify("read a.txt") {
		t.Fatalf("Verify field was cleared or altered")
	}
	if !reflect.DeepEqual(transcript, transcriptBefore) {
		t.Fatalf("transcript mutated:\n got %+v\nwant %+v", transcript, transcriptBefore)
	}
}

// TestEvaluateTaskDeterministic 证明相同入参重复评估得到完全一致的结果。
func TestEvaluateTaskDeterministic(t *testing.T) {
	task := LongTask{
		Name:     "stable",
		Steps:    []TaskStep{{ExpectedTool: "read"}, {ExpectedTool: "write"}},
		MaxTurns: 3,
	}
	transcript := []string{"read a.txt", "write b.txt", "extra"}
	first := EvaluateTask(task, transcript)
	second := EvaluateTask(task, transcript)
	if first != second {
		t.Fatalf("not deterministic:\nfirst  %+v\nsecond %+v", first, second)
	}
}

// TestTaskCompletionRate 覆盖完成率：空输入为 0，避免 0/0 的 NaN；其余按 Completed 比例计算，
// 且不修改 results。
func TestTaskCompletionRate(t *testing.T) {
	completed := func(name string) TaskResult { return TaskResult{TaskName: name, Completed: true} }
	pending := func(name string) TaskResult { return TaskResult{TaskName: name, Completed: false} }

	for _, tc := range []struct {
		name    string
		results []TaskResult
		want    float64
	}{
		{"empty", nil, 0},
		{"all complete", []TaskResult{completed("a"), completed("b")}, 1},
		{"none complete", []TaskResult{pending("a"), pending("b")}, 0},
		{"half", []TaskResult{completed("a"), pending("b")}, 0.5},
		{"one of three", []TaskResult{completed("a"), pending("b"), pending("c")}, 1.0 / 3.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]TaskResult(nil), tc.results...)
			got := TaskCompletionRate(tc.results)
			if math.IsNaN(got) || math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("TaskCompletionRate() = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.results, before) {
				t.Fatalf("results mutated: %+v", tc.results)
			}
		})
	}
}
