package app

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// 本文件是"失败尝试隔离"（attempt 链）的端到端可达性守卫。
//
// R16 学习：attempt 链有三重隐藏前置，缺一不可——
//  1. opt-in attempt_isolation 开启（决定两个 wire 回调是否返回非 nil）；
//  2. 工作区存在 persona（configureSubagents 才会装配 opts.Subagent，回调才有挂载点）；
//  3. 模型在 task 工具参数里传了非空的 verify 字段（runTask 才会进入 runTaskVerified）。
//
// 另外，"配置了 verify_command"走的是完全不同的 verifyAfterBatch 路径，它只把
// opts.Verify 挂到循环上，与 attempt 链毫无关系。这里用真实 Execute 装配路径锁定：
// 仅满足第 1 项且配上 VerifyCommand，绝不会产生 KindAttempt 根记录或隔离通知，
// 防止未来有人把"验证命令"与"尝试链"误接在一起。

// attemptRegFromOptInEnv 复用与 Execute 完全相同的 env→注册表构造路径
// （ParseOptInEnv + 逐键 Set），使门控断言贴近生产而不是手工拼装注册表。
func attemptRegFromOptInEnv(env func(string) string) *OptInRegistry {
	reg := &OptInRegistry{}
	for key, on := range ParseOptInEnv(env) {
		reg.Set(key, on)
	}
	return reg
}

// executeAttemptReachability 走完整 Execute 装配：注入脚本化 chatter、无网络、无真实模型。
// 返回的会话用于检查是否落盘了 KindAttempt 根记录，事件用于检查隔离通知。
func executeAttemptReachability(t *testing.T, env func(string) string, root string, verifyCommand []string) (*session.Session, []agent.Event) {
	t.Helper()
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.TrustProject = true
	res.Approve = autoApprover{}
	// 脚本化 chatter 只回一句清单答案，不发任何工具调用：既没有 task 调用（第 3 项前置缺失），
	// 也没有变更型批次，因此 verify_command 的 verifyAfterBatch 同样不会真正运行。
	res.Chatter = &scriptedChatter{replies: []string{"done"}}
	res.Cfg.VerifyCommand = verifyCommand

	store := testStore(t, res)
	sess, err := store.NewSession("attempt-reachability")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var events []agent.Event
	ctx := withEventEmit(context.Background(), func(ev agent.Event) { events = append(events, ev) })
	if _, err := Execute(ctx, cli.IO{Env: env}, res, sess, "do work", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return sess, events
}

// TestVerifyCommandAloneDoesNotTriggerAttemptChain 是 R16 学习的核心端到端守卫：
// 即便 opt-in attempt_isolation 已开启、且显式配置了 VerifyCommand（激活的是另一条
// verifyAfterBatch 路径），只要工作区没有 persona、模型也没有在 task 参数里传 verify，
// attempt 链就完全不会被触发——没有 KindAttempt 根记录，也没有隔离通知。
func TestVerifyCommandAloneDoesNotTriggerAttemptChain(t *testing.T) {
	_, root, home := wiringEnv(t)
	// 刻意把 attempt_isolation 打开：这样"仅靠 opt-in 就触发 attempt 链"的误接线也会暴露。
	env := envFrom(map[string]string{
		"USERPROFILE": home,
		"HOME":        home,
		optInEnvVar:   string(OptInAttemptIsolation),
	})

	sess, events := executeAttemptReachability(t, env, root, []string{"go", "test", "./..."})

	attempts, err := sess.Attempts()
	if err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("attempt chain fired without persona/verify field: got %d KindAttempt records (%+v)", len(attempts), attempts)
	}
	if notices := isolationNotices(events); len(notices) != 0 {
		t.Fatalf("attempt isolation notice emitted without persona/verify field: %v", notices)
	}
}

// TestAttemptIsolationOffGateMirrorsExecute 是 attempt_wire_test.go 的端到端补充：
// 那里用手工构造的注册表验证门控，这里改用与 Execute 相同的 env→reg 路径。
// 关闭（含 env 缺失）时两个 wire 都返回 nil，打开时才返回非 nil 回调与根记录钩子。
func TestAttemptIsolationOffGateMirrorsExecute(t *testing.T) {
	sess, _ := attemptIsolationFixture(t)

	offReg := attemptRegFromOptInEnv(envFrom(nil))
	if got := wireVerifyFail(sess, offReg); got != nil {
		t.Fatal("wireVerifyFail = non-nil with attempt_isolation off, want nil")
	}
	if begin, end := wireAttemptRoot(sess, offReg); begin != nil || end != nil {
		t.Fatal("wireAttemptRoot returned non-nil hooks with attempt_isolation off, want (nil, nil)")
	}

	onEnv := envFrom(map[string]string{optInEnvVar: string(OptInAttemptIsolation)})
	onReg := attemptRegFromOptInEnv(onEnv)
	if got := wireVerifyFail(sess, onReg); got == nil {
		t.Fatal("wireVerifyFail = nil with attempt_isolation on, want non-nil")
	}
	if begin, end := wireAttemptRoot(sess, onReg); begin == nil || end == nil {
		t.Fatal("wireAttemptRoot returned nil hooks with attempt_isolation on, want both non-nil")
	}
}

// TestApproveDangerGateValues 锁定 ARGENT_APPROVE_DANGER 门控的精确语义：
// 默认开启；env 为 nil 或取值修剪后不等于 "0" 时开启，仅修剪后精确为 "0" 时关闭。
func TestApproveDangerGateValues(t *testing.T) {
	if !approveDangerEnabled(nil) {
		t.Fatal("approveDangerEnabled(nil) = false, want true (default on)")
	}
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"unset", "", true},
		{"zero", "0", false},
		{"padded zero", " 0 ", false},
		{"true word", "true", true},
		{"one", "1", true},
		{"padded one", " 1 ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := envFrom(map[string]string{"ARGENT_APPROVE_DANGER": tc.value})
			if got := approveDangerEnabled(env); got != tc.want {
				t.Fatalf("approveDangerEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestApproveDangerGateReachableOnlyThroughTUIApprover 说明门控的可达面：
// TUI 入口用 SetEnv 注入 stdio.Env 后可显式关闭门控；未装配的零值 Approver
// env 为 nil，门控默认开启。
// （范围说明：这里锁定的是 env 注入与门控开关本身，不构造 tui.Model，
// 因为 ApproveDanger 弹窗需要真实前端；弹窗调用见 tui.go 的 Approver.Ask。）
func TestApproveDangerGateReachableOnlyThroughTUIApprover(t *testing.T) {
	a := NewApprover()
	if !approveDangerEnabled(a.env) {
		t.Fatal("fresh Approver has danger gate off, want on by default")
	}
	a.SetEnv(envFrom(map[string]string{"ARGENT_APPROVE_DANGER": "0"}))
	if approveDangerEnabled(a.env) {
		t.Fatal("Approver.SetEnv(ARGENT_APPROVE_DANGER=0) did not turn the gate off")
	}
}

// TestHeadlessRunnerNeverBuildsTUIApprover 证明 --rpc 无界面路径不会构造承载
// ARGENT_APPROVE_DANGER 的 *Approver：runner.Prompt 装的是 *pendingApprover，
// 因此该门控在无界面路径根本不可达。复用 headless_approver_test.go 的注入接缝，
// 不读取真实 stdin、不发工具调用。
func TestHeadlessRunnerNeverBuildsTUIApprover(t *testing.T) {
	_, _, captured := approvalTestRunner(t, cli.Options{RPC: true}, strings.NewReader(""))
	approver := <-captured
	if approver == nil {
		t.Fatal("runner installed a nil approver")
	}
	if _, ok := approver.(*Approver); ok {
		t.Fatal("headless runner installed *app.Approver; ARGENT_APPROVE_DANGER gate would be reachable")
	}
	if _, ok := approver.(*pendingApprover); !ok {
		t.Fatalf("headless approver = %T, want *pendingApprover", approver)
	}
}
