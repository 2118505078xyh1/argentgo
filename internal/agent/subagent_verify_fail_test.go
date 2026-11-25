package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// scriptedVerifyChild 是按提示分流的伪子运行器：验证提示走 verify 脚本，其余提示返回固定草稿。
type scriptedVerifyChild struct {
	fn func(prompt string) (Result, error)
}

func (c *scriptedVerifyChild) Run(_ context.Context, prompt string) (Result, error) {
	return c.fn(prompt)
}

// verifyRun 是通过完整 Runner 驱动一次带验证的 task 调用的结果快照。
type verifyRun struct {
	text   string
	events []Event
	rows   []string
	err    error
}

// runVerifyWithHook 用 SubagentConfig.NewChild 注入按提示分流的伪子级，
// 让验证子级按 verify 脚本返回 PASS/FAIL/错误，并收集事件与 Recorder 记录。
func runVerifyWithHook(retries int, verify func(string) (Result, error), hook func(context.Context, VerifyFailInfo) error) verifyRun {
	events := &eventSink{}
	var mu sync.Mutex
	var rows []string
	record := func(kind string, _ any) error {
		mu.Lock()
		defer mu.Unlock()
		rows = append(rows, kind)
		return nil
	}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("t1", TaskToolName, verifyTaskArgs("do", "do work", "be perfect", retries))), nil
		}
		return textReply("root got: " + lastToolContent(messages)), nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   &fakeTools{},
		Emit:    events.emit,
		Record:  record,
		Subagent: &SubagentConfig{
			OnVerifyFail: hook,
			NewChild: func(Options) ChildRunner {
				return &scriptedVerifyChild{fn: func(prompt string) (Result, error) {
					if strings.Contains(prompt, "Verify the following result") {
						return verify(prompt)
					}
					return Result{Text: "draft answer"}, nil
				}}
			},
		},
	}}
	res, err := runner.Run(context.Background(), "delegate")
	mu.Lock()
	rowCopy := append([]string(nil), rows...)
	mu.Unlock()
	return verifyRun{text: res.Text, events: append([]Event(nil), events.events...), rows: rowCopy, err: err}
}

// failVerdict 返回一个 FAIL 验证脚本。
func failVerdict(verdict string) func(string) (Result, error) {
	return func(string) (Result, error) { return Result{Text: verdict}, nil }
}

// TestOnVerifyFailInvokeConditions 锁定回调的触发条件：PASS 不调用、仍有重试余量的 FAIL 调用、
// 验证子级出错不调用、重试耗尽时的最后一次 FAIL 不调用。同时断言 info 内容准确。
func TestOnVerifyFailInvokeConditions(t *testing.T) {
	cases := []struct {
		name         string
		retries      int
		verify       func(string) (Result, error)
		wantCalls    int
		wantTextHas  string
		wantAttempts []int
		wantLabel    string
		wantRetries  int
		wantVerdict  string
	}{
		{
			name:        "pass_skips_hook",
			retries:     2,
			verify:      func(string) (Result, error) { return Result{Text: "PASS - all good"}, nil },
			wantCalls:   0,
			wantTextHas: "draft answer",
		},
		{
			name:         "fail_with_retries_invokes_hook",
			retries:      2,
			verify:       failVerdict("FAIL: missing detail"),
			wantCalls:    2,
			wantTextHas:  "draft answer",
			wantAttempts: []int{1, 2},
			wantLabel:    "do",
			wantRetries:  2,
			wantVerdict:  "FAIL: missing detail",
		},
		{
			name:      "verify_error_skips_hook",
			retries:   2,
			verify:    func(string) (Result, error) { return Result{}, errors.New("verifier exploded") },
			wantCalls: 0,
		},
		{
			name:        "exhausted_retries_skip_hook",
			retries:     0,
			verify:      failVerdict("FAIL"),
			wantCalls:   0,
			wantTextHas: "draft answer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var infos []VerifyFailInfo
			hook := func(_ context.Context, info VerifyFailInfo) error {
				infos = append(infos, info)
				return nil
			}
			got := runVerifyWithHook(tc.retries, tc.verify, hook)
			if got.err != nil {
				t.Fatalf("runner.Run: %v", got.err)
			}
			if len(infos) != tc.wantCalls {
				t.Fatalf("OnVerifyFail calls = %d, want %d; infos = %+v", len(infos), tc.wantCalls, infos)
			}
			if tc.wantTextHas != "" && !strings.Contains(got.text, tc.wantTextHas) {
				t.Fatalf("text = %q, want it to contain %q", got.text, tc.wantTextHas)
			}
			if len(tc.wantAttempts) > 0 {
				var attempts []int
				for _, info := range infos {
					attempts = append(attempts, info.Attempt)
					if info.Label != tc.wantLabel {
						t.Errorf("info.Label = %q, want %q", info.Label, tc.wantLabel)
					}
					if info.Retries != tc.wantRetries {
						t.Errorf("info.Retries = %d, want %d", info.Retries, tc.wantRetries)
					}
					if info.Verdict != tc.wantVerdict {
						t.Errorf("info.Verdict = %q, want %q", info.Verdict, tc.wantVerdict)
					}
					if info.AttemptID == "" {
						t.Error("info.AttemptID is empty")
					}
				}
				if !reflect.DeepEqual(attempts, tc.wantAttempts) {
					t.Errorf("attempts = %v, want %v", attempts, tc.wantAttempts)
				}
				if infos[0].AttemptID == infos[len(infos)-1].AttemptID && len(infos) > 1 {
					t.Error("different attempts must carry different AttemptID")
				}
			}
		})
	}
}

// TestOnVerifyFailDefaultNilIsByteIdentical 验证默认 nil 与注入一个返回 nil 的空钩子时，
// 事件（种类与正文）和 Recorder 记录逐条相同；钩子自身不产生任何可观察副作用。
func TestOnVerifyFailDefaultNilIsByteIdentical(t *testing.T) {
	verify := failVerdict("FAIL: not yet")
	baseline := runVerifyWithHook(2, verify, nil)
	withHook := runVerifyWithHook(2, verify, func(context.Context, VerifyFailInfo) error { return nil })

	if baseline.text != withHook.text {
		t.Errorf("text changed: %q vs %q", baseline.text, withHook.text)
	}
	if !reflect.DeepEqual(baseline.rows, withHook.rows) {
		t.Errorf("recorder rows changed: %v vs %v", baseline.rows, withHook.rows)
	}
	if len(baseline.events) != len(withHook.events) {
		t.Fatalf("event count = %d vs %d", len(baseline.events), len(withHook.events))
	}
	for i := range baseline.events {
		// 忽略耗时等运行期抖动，只比较语义字段。
		a, b := baseline.events[i], withHook.events[i]
		if a.Kind != b.Kind || a.Turn != b.Turn || a.Depth != b.Depth ||
			a.Text != b.Text || a.ToolCallID != b.ToolCallID || a.ToolName != b.ToolName ||
			a.Args != b.Args || a.Intent != b.Intent || a.Output != b.Output ||
			a.IsError != b.IsError || a.Err != b.Err || a.Reason != b.Reason {
			t.Errorf("event[%d] changed: %+v vs %+v", i, a, b)
		}
	}
}

// TestOnVerifyFailHookErrorAddsNoticeOnly 验证钩子返回错误时只多出一条父级通知，
// 不改变返回文本与 Recorder 记录，也不改变 FAIL 通知本身的数量。
func TestOnVerifyFailHookErrorAddsNoticeOnly(t *testing.T) {
	verify := failVerdict("FAIL: weak")
	baseline := runVerifyWithHook(1, verify, nil)
	withErr := runVerifyWithHook(1, verify, func(context.Context, VerifyFailInfo) error {
		return errors.New("fork failed")
	})

	if baseline.text != withErr.text {
		t.Errorf("text changed: %q vs %q", baseline.text, withErr.text)
	}
	if !reflect.DeepEqual(baseline.rows, withErr.rows) {
		t.Errorf("recorder rows changed: %v vs %v", baseline.rows, withErr.rows)
	}
	hookNotices := 0
	for _, event := range withErr.events {
		if event.Kind == EventNotice && strings.Contains(event.Text, "fork failed") {
			hookNotices++
		}
	}
	if hookNotices != 1 {
		t.Fatalf("hook-error notices = %d, want 1; events = %+v", hookNotices, withErr.events)
	}
	failNotices := 0
	for _, event := range withErr.events {
		if event.Kind == EventNotice && strings.Contains(event.Text, "verification FAIL") {
			failNotices++
		}
	}
	if failNotices != 2 {
		t.Fatalf("FAIL notices = %d, want 2 (retries=1 means two attempts)", failNotices)
	}
}

// TestOnVerifyFailPersonaPassthrough 验证 configForPersona 的合并链显式透传 OnVerifyFail：
// 角色配置自带的空钩子不会覆盖父级注入的钩子，且透传后的钩子仍能被调用。
func TestOnVerifyFailPersonaPassthrough(t *testing.T) {
	called := 0
	hook := func(context.Context, VerifyFailInfo) error {
		called++
		return nil
	}
	tt := &taskTools{cfg: &SubagentConfig{
		OnVerifyFail: hook,
		ResolvePersona: func(string) (SubagentConfig, bool) {
			// 角色自带一个不同的钩子，且刻意不带 Background/NewChild 等父级字段。
			return SubagentConfig{System: "persona system"}, true
		},
	}}

	merged := tt.configForPersona("expert")
	if merged.OnVerifyFail == nil {
		t.Fatal("OnVerifyFail was dropped from the persona merge chain")
	}
	if err := merged.OnVerifyFail(context.Background(), VerifyFailInfo{Label: "x"}); err != nil {
		t.Fatalf("calling merged hook: %v", err)
	}
	if called != 1 {
		t.Fatalf("merged hook calls = %d, want 1", called)
	}

	// 未知角色/空名字的回退路径同样要保留钩子。
	for _, name := range []string{"", "unknown"} {
		fallback := tt.configForPersona(name)
		if fallback.OnVerifyFail == nil {
			t.Errorf("fallback config for %q dropped OnVerifyFail", name)
		}
	}
}
