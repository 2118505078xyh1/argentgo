package agent

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// attemptEndCall 记录一次 EndAttempt 调用收到的参数。
type attemptEndCall struct {
	rootID  string
	verdict string
}

// attemptHookRun 汇总一次带尝试根钩子的验证运行的观察结果。
type attemptHookRun struct {
	reasons []string         // BeginAttempt 收到的 reason
	infos   []VerifyFailInfo // OnVerifyFail 观察到的旁路信息
	ends    []attemptEndCall // EndAttempt 收到的参数
	run     verifyRun
}

// runVerifyWithAttemptConfig 以完整的 SubagentConfig 驱动一次带验证的 task 调用，
// 在配置副本上包裹三个钩子以记录观察值，因此绝不修改调用方传入的配置。
// 未提供 NewChild 时注入按提示分流的伪子级，与 runVerifyWithHook 的行为一致。
func runVerifyWithAttemptConfig(retries int, verify func(string) (Result, error), cfg SubagentConfig) attemptHookRun {
	var reasons []string
	var infos []VerifyFailInfo
	var ends []attemptEndCall

	begin := cfg.BeginAttempt
	if begin != nil {
		cfg.BeginAttempt = func(reason string) (string, error) {
			reasons = append(reasons, reason)
			return begin(reason)
		}
	}
	end := cfg.EndAttempt
	if end != nil {
		cfg.EndAttempt = func(rootID, verdict string) error {
			ends = append(ends, attemptEndCall{rootID: rootID, verdict: verdict})
			return end(rootID, verdict)
		}
	}
	hook := cfg.OnVerifyFail
	cfg.OnVerifyFail = func(ctx context.Context, info VerifyFailInfo) error {
		infos = append(infos, info)
		if hook != nil {
			return hook(ctx, info)
		}
		return nil
	}
	if cfg.NewChild == nil {
		cfg.NewChild = func(Options) ChildRunner {
			return &scriptedVerifyChild{fn: func(prompt string) (Result, error) {
				if strings.Contains(prompt, "Verify the following result") {
					return verify(prompt)
				}
				return Result{Text: "draft answer"}, nil
			}}
		}
	}

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
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{},
		Emit:     events.emit,
		Record:   record,
		Subagent: &cfg,
	}}
	res, err := runner.Run(context.Background(), "delegate")
	mu.Lock()
	rowCopy := append([]string(nil), rows...)
	mu.Unlock()
	return attemptHookRun{
		reasons: reasons,
		infos:   infos,
		ends:    ends,
		run:     verifyRun{text: res.Text, events: append([]Event(nil), events.events...), rows: rowCopy, err: err},
	}
}

// assertVerifyRunByteIdentical 比较两个验证运行的文本、事件语义字段与记录行是否一致。
func assertVerifyRunByteIdentical(t *testing.T, a, b verifyRun) {
	t.Helper()
	if a.text != b.text {
		t.Errorf("text changed: %q vs %q", a.text, b.text)
	}
	if !reflect.DeepEqual(a.rows, b.rows) {
		t.Errorf("recorder rows changed: %v vs %v", a.rows, b.rows)
	}
	if len(a.events) != len(b.events) {
		t.Fatalf("event count = %d vs %d", len(a.events), len(b.events))
	}
	for i := range a.events {
		x, y := a.events[i], b.events[i]
		if x.Kind != y.Kind || x.Turn != y.Turn || x.Depth != y.Depth ||
			x.Text != y.Text || x.ToolCallID != y.ToolCallID || x.ToolName != y.ToolName ||
			x.Args != y.Args || x.Intent != y.Intent || x.Output != y.Output ||
			x.IsError != y.IsError || x.Err != y.Err || x.Reason != y.Reason {
			t.Errorf("event[%d] changed: %+v vs %+v", i, x, y)
		}
	}
}

// TestAttemptRootDefaultNilKeepsSyntheticID 锁定默认 nil 行为：未配置尝试根钩子时，
// VerifyFailInfo.AttemptID 仍是旧的合成串，且运行不产生任何额外事件或记录。
func TestAttemptRootDefaultNilKeepsSyntheticID(t *testing.T) {
	verify := failVerdict("FAIL: not yet")

	// 仅用 OnVerifyFail 观察 AttemptID，BeginAttempt/EndAttempt 保持 nil。
	var infos []VerifyFailInfo
	observed := runVerifyWithAttemptConfig(2, verify, SubagentConfig{
		OnVerifyFail: func(_ context.Context, info VerifyFailInfo) error {
			infos = append(infos, info)
			return nil
		},
	})
	if observed.run.err != nil {
		t.Fatalf("runner.Run: %v", observed.run.err)
	}
	re := regexp.MustCompile(`^verifyfail-\d+-\d+$`)
	if len(infos) != 2 {
		t.Fatalf("OnVerifyFail calls = %d, want 2", len(infos))
	}
	for _, info := range infos {
		if !re.MatchString(info.AttemptID) {
			t.Errorf("AttemptID = %q, want pattern %q", info.AttemptID, re.String())
		}
	}

	// 全 nil（助手包裹的 OnVerifyFail 也返回 nil）与既有基线逐字节一致。
	baseline := runVerifyWithHook(2, verify, nil)
	withNilHooks := runVerifyWithAttemptConfig(2, verify, SubagentConfig{})
	assertVerifyRunByteIdentical(t, baseline, withNilHooks.run)
	if len(withNilHooks.ends) != 0 || len(withNilHooks.reasons) != 0 {
		t.Errorf("nil hooks produced side effects: ends=%v reasons=%v", withNilHooks.ends, withNilHooks.reasons)
	}
}

// TestAttemptRootInjectedHooksDriveIDsAndVerdicts 验证注入钩子后：BeginAttempt 收到
// "task_verify" 且只调用一次；OnVerifyFail 的 AttemptID 等于真实 rootID；
// EndAttempt 在 PASS 时以 succeeded、在确定性 FAIL/验证错误时以 failed 调用一次。
func TestAttemptRootInjectedHooksDriveIDsAndVerdicts(t *testing.T) {
	const rootID = "root-verify-42"
	newCfg := func() SubagentConfig {
		return SubagentConfig{
			BeginAttempt: func(reason string) (string, error) { return rootID, nil },
			EndAttempt:   func(string, string) error { return nil },
		}
	}

	t.Run("pass_succeeds", func(t *testing.T) {
		got := runVerifyWithAttemptConfig(2, func(string) (Result, error) {
			return Result{Text: "PASS - all good"}, nil
		}, newCfg())
		if got.run.err != nil {
			t.Fatalf("runner.Run: %v", got.run.err)
		}
		if !reflect.DeepEqual(got.reasons, []string{"task_verify"}) {
			t.Errorf("BeginAttempt reasons = %v, want [task_verify]", got.reasons)
		}
		if len(got.infos) != 0 {
			t.Errorf("OnVerifyFail calls = %d, want 0 on PASS", len(got.infos))
		}
		if !reflect.DeepEqual(got.ends, []attemptEndCall{{rootID: rootID, verdict: "succeeded"}}) {
			t.Errorf("EndAttempt calls = %+v, want one succeeded for %q", got.ends, rootID)
		}
	})

	t.Run("exhausted_fail_marks_failed", func(t *testing.T) {
		got := runVerifyWithAttemptConfig(2, failVerdict("FAIL: missing detail"), newCfg())
		if got.run.err != nil {
			t.Fatalf("runner.Run: %v", got.run.err)
		}
		if !reflect.DeepEqual(got.reasons, []string{"task_verify"}) {
			t.Errorf("BeginAttempt reasons = %v, want [task_verify]", got.reasons)
		}
		if len(got.infos) != 2 {
			t.Fatalf("OnVerifyFail calls = %d, want 2", len(got.infos))
		}
		for _, info := range got.infos {
			if info.AttemptID != rootID {
				t.Errorf("info.AttemptID = %q, want real rootID %q", info.AttemptID, rootID)
			}
		}
		if !reflect.DeepEqual(got.ends, []attemptEndCall{{rootID: rootID, verdict: "failed"}}) {
			t.Errorf("EndAttempt calls = %+v, want one failed for %q", got.ends, rootID)
		}
	})

	t.Run("verify_error_marks_failed", func(t *testing.T) {
		got := runVerifyWithAttemptConfig(1, func(string) (Result, error) {
			return Result{}, errors.New("verifier exploded")
		}, newCfg())
		// 验证子级出错不会让 runner.Run 失败：错误作为工具结果回到模型循环。
		if !reflect.DeepEqual(got.ends, []attemptEndCall{{rootID: rootID, verdict: "failed"}}) {
			t.Errorf("EndAttempt calls = %+v, want one failed for %q", got.ends, rootID)
		}
		if len(got.infos) != 0 {
			t.Errorf("OnVerifyFail calls = %d, want 0 on verifier error", len(got.infos))
		}
		notice := false
		for _, event := range got.run.events {
			if event.Kind == EventNotice && strings.Contains(event.Text, "verification error") {
				notice = true
			}
		}
		if !notice {
			t.Error("no verification-error notice emitted")
		}
	})
}

// TestAttemptRootBeginErrorFallsBackToSynthetic 验证 BeginAttempt 报错时回落到合成串，
// 且不会调用 EndAttempt（空 rootID 不落结果），错误被吞掉不产生额外通知。
func TestAttemptRootBeginErrorFallsBackToSynthetic(t *testing.T) {
	got := runVerifyWithAttemptConfig(2, failVerdict("FAIL: weak"), SubagentConfig{
		BeginAttempt: func(string) (string, error) { return "", errors.New("begin exploded") },
		EndAttempt:   func(string, string) error { return nil },
	})

	if got.run.err != nil {
		t.Fatalf("runner.Run: %v", got.run.err)
	}
	if !reflect.DeepEqual(got.reasons, []string{"task_verify"}) {
		t.Errorf("BeginAttempt reasons = %v, want [task_verify]", got.reasons)
	}
	re := regexp.MustCompile(`^verifyfail-\d+-\d+$`)
	if len(got.infos) != 2 {
		t.Fatalf("OnVerifyFail calls = %d, want 2", len(got.infos))
	}
	for _, info := range got.infos {
		if !re.MatchString(info.AttemptID) {
			t.Errorf("AttemptID = %q, want synthetic pattern after BeginAttempt error", info.AttemptID)
		}
	}
	if len(got.ends) != 0 {
		t.Errorf("EndAttempt calls = %+v, want none when BeginAttempt failed", got.ends)
	}
	for _, event := range got.run.events {
		if strings.Contains(event.Text, "begin exploded") {
			t.Errorf("BeginAttempt error leaked into an event: %q", event.Text)
		}
	}
}

// TestAttemptRootPersonaPassthrough 验证 configForPersona 的合并链显式透传两个钩子：
// 具名角色分支以父级注入为准，空名/未知角色的回退分支靠整拷贝自动携带；
// 合并只写入副本，不修改调用方传入的配置。
func TestAttemptRootPersonaPassthrough(t *testing.T) {
	begin := func(string) (string, error) { return "root", nil }
	end := func(string, string) error { return nil }
	parent := &SubagentConfig{
		BeginAttempt: begin,
		EndAttempt:   end,
		ResolvePersona: func(string) (SubagentConfig, bool) {
			// 角色自带不同的钩子与系统提示，验证父级注入优先且不污染入参。
			return SubagentConfig{
				System:       "persona system",
				BeginAttempt: func(string) (string, error) { return "persona", nil },
				EndAttempt:   func(string, string) error { return nil },
			}, true
		},
	}
	tt := &taskTools{cfg: parent}

	merged := tt.configForPersona("expert")
	if merged.System != "persona system" {
		t.Fatalf("merged.System = %q, want the persona system (merge did not happen)", merged.System)
	}
	if merged.BeginAttempt == nil || merged.EndAttempt == nil {
		t.Fatal("attempt hooks were dropped from the persona merge chain")
	}
	if id, _ := merged.BeginAttempt("task_verify"); id != "root" {
		t.Errorf("merged BeginAttempt returned %q, want the parent hook's %q", id, "root")
	}

	// 入参未被合并过程修改。
	if parent.System != "" {
		t.Errorf("input config was mutated: System = %q, want empty", parent.System)
	}
	if parent.BeginAttempt == nil || parent.EndAttempt == nil {
		t.Error("input config lost its attempt hooks during merge")
	}

	// 无角色与未知角色分支靠 cfg := *t.cfg 整拷贝，同样保留钩子。
	for _, name := range []string{"", "unknown"} {
		fallback := tt.configForPersona(name)
		if fallback.BeginAttempt == nil || fallback.EndAttempt == nil {
			t.Errorf("fallback config for %q dropped the attempt hooks", name)
		}
	}
}
