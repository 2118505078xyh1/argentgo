package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// stepReply 是脚本化 prompter 对某条提示的一次应答。
type stepReply struct {
	out string
	err error
}

// scriptedPrompter 按提示文本返回预设应答；同一文本的应答被依次消费，
// 最后一条在耗尽后重复。它同时记录提示顺序供断言。
type scriptedPrompter struct {
	mu      sync.Mutex
	seq     map[string][]stepReply
	idx     map[string]int
	prompts []string
}

func (s *scriptedPrompter) prompt(_ context.Context, text string) (string, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, text)
	replies := s.seq[text]
	i := s.idx[text]
	s.idx[text] = i + 1
	s.mu.Unlock()
	if len(replies) == 0 {
		return text, nil
	}
	if i >= len(replies) {
		i = len(replies) - 1
	}
	return replies[i].out, replies[i].err
}

func newScriptedPrompter(seq map[string][]stepReply) *scriptedPrompter {
	return &scriptedPrompter{seq: seq, idx: make(map[string]int)}
}

// wantStep 描述期望的步骤结果。
type wantStep struct {
	name   string
	status string
	out    string
}

// TestRunGuardedPreconditionsAndFallback 用表驱动覆盖前置条件成立/不成立、
// 失败跳过、失败回滚以及多步骤混合场景。
func TestRunGuardedPreconditionsAndFallback(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name        string
		def         string
		params      map[string]string
		script      map[string][]stepReply
		wantStatus  string
		wantSteps   []wantStep
		wantPrompts []string
	}{
		{
			name: "前置条件成立时步骤执行",
			def: `{"steps":[
				{"name":"a","prompt":"a"},
				{"name":"b","prompt":"b","guard":{"preconditions":[
					{"key":"steps.a.output","op":"equals","value":"READY"}
				]}}
			]}`,
			script: map[string][]stepReply{
				"a": {{out: "READY"}},
				"b": {{out: "B"}},
			},
			wantStatus: StatusOK,
			wantSteps: []wantStep{
				{name: "a", status: StatusOK, out: "READY"},
				{name: "b", status: StatusOK, out: "B"},
			},
			wantPrompts: []string{"a", "b"},
		},
		{
			name: "前置条件不成立默认跳过并继续",
			def: `{"steps":[
				{"name":"a","prompt":"a"},
				{"name":"b","prompt":"b","guard":{"preconditions":[
					{"key":"steps.a.output","op":"equals","value":"READY"}
				]}},
				{"name":"c","prompt":"c"}
			]}`,
			script: map[string][]stepReply{
				"a": {{out: "NOPE"}},
				"c": {{out: "C"}},
			},
			wantStatus: StatusOK,
			wantSteps: []wantStep{
				{name: "a", status: StatusOK, out: "NOPE"},
				{name: "b", status: StatusSkipped},
				{name: "c", status: StatusOK, out: "C"},
			},
			wantPrompts: []string{"a", "c"},
		},
		{
			name: "前置条件不成立时停止工作流",
			def: `{"steps":[
				{"name":"a","prompt":"a"},
				{"name":"b","prompt":"b","guard":{"on_unmet":"stop","preconditions":[
					{"key":"steps.a.output","op":"equals","value":"READY"}
				]}},
				{"name":"c","prompt":"c"}
			]}`,
			script: map[string][]stepReply{
				"a": {{out: "NOPE"}},
			},
			wantStatus: StatusFailed,
			wantSteps: []wantStep{
				{name: "a", status: StatusOK, out: "NOPE"},
				{name: "b", status: StatusSkipped},
				{name: "c", status: StatusSkipped},
			},
			wantPrompts: []string{"a"},
		},
		{
			name: "工作流参数前置条件成立",
			def: `{"parameters":[{"key":"mode","input_type":"string","requirement":"optional"}],
				"steps":[{"name":"gated","prompt":"gated {{mode}}","guard":{"preconditions":[
					{"key":"mode","op":"equals","value":"fast"}
				]}}]}`,
			params:      map[string]string{"mode": "fast"},
			script:      map[string][]stepReply{"gated fast": {{out: "G"}}},
			wantStatus:  StatusOK,
			wantSteps:   []wantStep{{name: "gated", status: StatusOK, out: "G"}},
			wantPrompts: []string{"gated fast"},
		},
		{
			name: "工作流参数前置条件不成立",
			def: `{"parameters":[{"key":"mode","input_type":"string","requirement":"optional"}],
				"steps":[{"name":"gated","prompt":"gated {{mode}}","guard":{"preconditions":[
					{"key":"mode","op":"equals","value":"fast"}
				]}}]}`,
			params:      map[string]string{"mode": "slow"},
			wantStatus:  StatusOK,
			wantSteps:   []wantStep{{name: "gated", status: StatusSkipped}},
			wantPrompts: []string{},
		},
		{
			name: "失败回退跳过使运行继续",
			def: `{"steps":[
				{"name":"a","prompt":"a","guard":{"fallback":"skip"}},
				{"name":"b","prompt":"b"}
			]}`,
			script: map[string][]stepReply{
				"a": {{err: boom}},
				"b": {{out: "B"}},
			},
			wantStatus: StatusOK,
			wantSteps: []wantStep{
				{name: "a", status: StatusSkipped},
				{name: "b", status: StatusOK, out: "B"},
			},
			wantPrompts: []string{"a", "b"},
		},
		{
			name: "失败回退回滚到检查点后成功",
			def: `{"steps":[
				{"name":"a","prompt":"a"},
				{"name":"b","prompt":"b","guard":{"fallback":"rollback","rollback_to":"a"}}
			]}`,
			script: map[string][]stepReply{
				"a": {{out: "stale"}, {out: "fresh"}},
				"b": {{err: boom}, {out: "done"}},
			},
			wantStatus: StatusOK,
			wantSteps: []wantStep{
				{name: "a", status: StatusOK, out: "fresh"},
				{name: "b", status: StatusOK, out: "done"},
			},
			wantPrompts: []string{"a", "b", "a", "b"},
		},
		{
			name: "失败回退回滚后仍失败",
			def: `{"steps":[
				{"name":"a","prompt":"a"},
				{"name":"b","prompt":"b","guard":{"fallback":"rollback","rollback_to":"a"}},
				{"name":"c","prompt":"c"}
			]}`,
			script: map[string][]stepReply{
				"a": {{out: "x"}},
				"b": {{err: boom}},
			},
			wantStatus: StatusFailed,
			wantSteps: []wantStep{
				{name: "a", status: StatusOK, out: "x"},
				{name: "b", status: StatusFailed},
				{name: "c", status: StatusSkipped},
			},
			wantPrompts: []string{"a", "b", "a", "b"},
		},
		{
			name: "多步骤混合守卫",
			def: `{"steps":[
				{"name":"s1","prompt":"s1"},
				{"name":"s2","prompt":"s2","guard":{"preconditions":[
					{"key":"steps.s1.output","op":"contains","value":"go"}
				]}},
				{"name":"s3","prompt":"s3","guard":{"fallback":"skip"}},
				{"name":"s4","prompt":"s4"}
			]}`,
			script: map[string][]stepReply{
				"s1": {{out: "go now"}},
				"s2": {{out: "two"}},
				"s3": {{err: boom}},
				"s4": {{out: "four"}},
			},
			wantStatus: StatusOK,
			wantSteps: []wantStep{
				{name: "s1", status: StatusOK, out: "go now"},
				{name: "s2", status: StatusOK, out: "two"},
				{name: "s3", status: StatusSkipped},
				{name: "s4", status: StatusOK, out: "four"},
			},
			wantPrompts: []string{"s1", "s2", "s3", "s4"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := mustDefinition(t, tc.def)
			prompter := newScriptedPrompter(tc.script)
			exec := &Executor{
				Prompter: prompterFunc(prompter.prompt),
				Backoff:  time.Millisecond,
			}
			run, err := exec.RunGuarded(context.Background(), def, "", tc.params)
			if err != nil {
				t.Fatalf("RunGuarded: %v", err)
			}
			if run.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; steps = %+v", run.Status, tc.wantStatus, run.Steps)
			}
			if len(run.Steps) != len(tc.wantSteps) {
				t.Fatalf("steps = %+v, want %d entries", run.Steps, len(tc.wantSteps))
			}
			for i, want := range tc.wantSteps {
				got := run.Steps[i]
				if got.Name != want.name || got.Status != want.status {
					t.Errorf("step[%d] = {%s %s}, want {%s %s}", i, got.Name, got.Status, want.name, want.status)
				}
				if want.out != "" && got.Output != want.out {
					t.Errorf("step[%d] output = %q, want %q", i, got.Output, want.out)
				}
			}
			if len(prompter.prompts) != len(tc.wantPrompts) {
				t.Fatalf("prompts = %q, want %q", prompter.prompts, tc.wantPrompts)
			}
			for i := range tc.wantPrompts {
				if prompter.prompts[i] != tc.wantPrompts[i] {
					t.Errorf("prompt[%d] = %q, want %q", i, prompter.prompts[i], tc.wantPrompts[i])
				}
			}
		})
	}
}

// TestPreconditionMatches 覆盖各比较操作与缺失键的行为。
func TestPreconditionMatches(t *testing.T) {
	outputs := map[string]string{"a": "hello world"}
	params := map[string]string{"mode": "fast"}
	cases := []struct {
		name string
		p    Precondition
		want bool
	}{
		{"equals 命中", Precondition{Key: "steps.a.output", Op: CondEquals, Value: "hello world"}, true},
		{"equals 未命中", Precondition{Key: "steps.a.output", Op: CondEquals, Value: "other"}, false},
		{"默认操作是 equals", Precondition{Key: "mode", Value: "fast"}, true},
		{"not_equals 命中", Precondition{Key: "mode", Op: CondNotEquals, Value: "slow"}, true},
		{"contains 命中", Precondition{Key: "steps.a.output", Op: CondContains, Value: "world"}, true},
		{"not_contains 未命中", Precondition{Key: "steps.a.output", Op: CondNotContains, Value: "world"}, false},
		{"exists 命中", Precondition{Key: "mode", Op: CondExists}, true},
		{"absent 命中", Precondition{Key: "missing", Op: CondAbsent}, true},
		{"缺失键对 equals 不成立", Precondition{Key: "steps.missing.output", Op: CondEquals, Value: ""}, false},
		{"input 可用", Precondition{Key: "input", Op: CondEquals, Value: "IN"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.matches("IN", outputs, params); got != tc.want {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRunIgnoresGuardByDefault 确认默认 Run 路径不读取 Guard 字段。
func TestRunIgnoresGuardByDefault(t *testing.T) {
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","guard":{
		"on_unmet":"skip","preconditions":[{"key":"missing","op":"equals","value":"x"}]
	}}]}`)
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(string) (string, error) { return "A", nil })}

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() || len(run.Steps) != 1 || run.Steps[0].Status != StatusOK {
		t.Fatalf("default path must ignore guards: run = %+v", run)
	}
	if rec.promptCount() != 1 || rec.prompts[0] != "a" {
		t.Fatalf("prompts = %q, want the step to run once", rec.prompts)
	}
}

// TestRunGuardedRejectsInvalidGuard 覆盖守卫声明的校验错误。
func TestRunGuardedRejectsInvalidGuard(t *testing.T) {
	cases := []struct {
		name    string
		def     string
		wantErr string
	}{
		{
			name:    "未知操作",
			def:     `{"steps":[{"name":"a","prompt":"a","guard":{"preconditions":[{"key":"x","op":"like","value":"y"}]}}]}`,
			wantErr: "unknown op",
		},
		{
			name:    "空键",
			def:     `{"steps":[{"name":"a","prompt":"a","guard":{"preconditions":[{"op":"equals","value":"y"}]}}]}`,
			wantErr: "has no key",
		},
		{
			name:    "未知 on_unmet",
			def:     `{"steps":[{"name":"a","prompt":"a","guard":{"on_unmet":"pause"}}]}`,
			wantErr: "unknown on_unmet",
		},
		{
			name:    "未知 fallback",
			def:     `{"steps":[{"name":"a","prompt":"a","guard":{"fallback":"rewind"}}]}`,
			wantErr: "unknown fallback",
		},
		{
			name:    "回滚缺少 rollback_to",
			def:     `{"steps":[{"name":"a","prompt":"a","guard":{"fallback":"rollback"}}]}`,
			wantErr: "needs rollback_to",
		},
		{
			name:    "回滚指向未定义步骤",
			def:     `{"steps":[{"name":"a","prompt":"a","guard":{"fallback":"rollback","rollback_to":"ghost"}}]}`,
			wantErr: "undefined step",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := mustDefinition(t, tc.def)
			exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) { return "", nil })}
			_, err := exec.RunGuarded(context.Background(), def, "", nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
