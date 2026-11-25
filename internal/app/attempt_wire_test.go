package app

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/session"
)

// attemptIsolationFixture 构造一棵真实 JSONL 会话树：用户轮次之后是一次尝试，
// 尝试里只有一条 is_error 的工具记录。这样的尝试满足 DiscardFailedAttempt 的
// 全部前置条件（有失败证据、分叉点在活动路径上），因此接线成功时确实会分叉。
func attemptIsolationFixture(t *testing.T) (*session.Session, string) {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	sess, err := store.NewSession("失败尝试隔离")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := sess.Append(session.KindUser, "继续任务"); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	attempt, err := sess.AppendAttempt(session.NewAttempt("initial", nil))
	if err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if err := sess.Append(session.KindTool, map[string]any{
		"name": "bash", "output": "boom", "is_error": true,
	}); err != nil {
		t.Fatalf("Append(tool): %v", err)
	}
	return sess, attempt.ID
}

// attemptIsolationReg 返回已显式开启失败尝试隔离的注册表。
func attemptIsolationReg() *OptInRegistry {
	reg := new(OptInRegistry)
	reg.Set(OptInAttemptIsolation, true)
	return reg
}

// isolationNotices 返回事件列表中以隔离前缀开头的通知正文。
func isolationNotices(events []agent.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Kind == agent.EventNotice && strings.HasPrefix(ev.Text, attemptIsolationNoticePrefix) {
			out = append(out, ev.Text)
		}
	}
	return out
}

// TestAttemptIsolationKeyIsRegistered 证明开关键已进入统一注册表：
// 既出现在全量清单里，也能从 ARGENT_OPT_INS 的解析结果中被识别。
func TestAttemptIsolationKeyIsRegistered(t *testing.T) {
	if _, ok := DefaultOptIns()[OptInAttemptIsolation]; !ok {
		t.Fatal("DefaultOptIns() does not contain OptInAttemptIsolation")
	}
	parsed := ParseOptInEnv(func(string) string { return "attempt_isolation" })
	if !parsed[OptInAttemptIsolation] {
		t.Fatalf("ParseOptInEnv(attempt_isolation) = %v, want key enabled", parsed)
	}
}

// TestAttemptIsolationOnStates 覆盖注册表的各种状态：nil、零值、其他键开启、
// 显式开启、显式关闭与 Reset，确认只有显式开启才返回 true。
func TestAttemptIsolationOnStates(t *testing.T) {
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInContextPressure, true)
	turnedOff := new(OptInRegistry)
	turnedOff.Set(OptInAttemptIsolation, true)
	turnedOff.Set(OptInAttemptIsolation, false)
	reset := attemptIsolationReg()
	reset.Reset()

	tests := []struct {
		name string
		reg  *OptInRegistry
		want bool
	}{
		{"nil registry is off", nil, false},
		{"zero registry is off", new(OptInRegistry), false},
		{"other key on is off", otherOnly, false},
		{"explicitly on", attemptIsolationReg(), true},
		{"explicitly off", turnedOff, false},
		{"reset is off", reset, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attemptIsolationOn(tt.reg); got != tt.want {
				t.Fatalf("attemptIsolationOn() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWireVerifyFailDisabled 证明关闭时不装配：nil 注册表、零值注册表、其他键开启
// 以及 nil 会话都返回 nil 回调，调用方据此保持原有路径。
func TestWireVerifyFailDisabled(t *testing.T) {
	sess, _ := attemptIsolationFixture(t)
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInUntrustedResult, true)

	tests := []struct {
		name string
		sess *session.Session
		reg  *OptInRegistry
	}{
		{"nil registry", sess, nil},
		{"zero registry", sess, new(OptInRegistry)},
		{"other key on", sess, otherOnly},
		{"nil session with enabled registry", nil, attemptIsolationReg()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wireVerifyFail(tt.sess, tt.reg); got != nil {
				t.Fatal("wireVerifyFail() = non-nil, want nil when disabled")
			}
		})
	}
}

// TestWireVerifyFailEnabledIsolatesAttempt 证明开启时回调分叉失败尝试并发出携带
// 旁支 id 的 EventNotice，同时不返回错误（不改变任何循环决策）。
func TestWireVerifyFailEnabledIsolatesAttempt(t *testing.T) {
	sess, attemptID := attemptIsolationFixture(t)
	cb := wireVerifyFail(sess, attemptIsolationReg())
	if cb == nil {
		t.Fatal("wireVerifyFail() = nil, want non-nil when enabled")
	}

	var events []agent.Event
	ctx := withEventEmit(context.Background(), func(ev agent.Event) { events = append(events, ev) })
	if err := cb(ctx, agent.VerifyFailInfo{AttemptID: attemptID}); err != nil {
		t.Fatalf("callback returned error: %v", err)
	}

	notices := isolationNotices(events)
	if len(notices) != 1 {
		t.Fatalf("got %d isolation notices, want 1: %v", len(notices), events)
	}

	// 旁支 id 出现在通知正文中，且该旁支确实落盘并带有重试血缘。
	metas, err := session.List(sess.Dir())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("sessions in store = %d, want 2 (source + fork)", len(metas))
	}
	forkFound := false
	for _, meta := range metas {
		if meta.ID == sess.ID() || !strings.Contains(notices[0], meta.ID) {
			continue
		}
		forkFound = true
		fork, err := session.OpenSession(sess.Dir(), meta.ID)
		if err != nil {
			t.Fatalf("OpenSession(fork): %v", err)
		}
		attempts, err := fork.Attempts()
		if err != nil {
			t.Fatalf("fork.Attempts: %v", err)
		}
		if len(attempts) != 1 || attempts[0].Reason != session.AttemptRetried || attempts[0].SourceRecord != attemptID {
			t.Fatalf("fork attempts = %+v, want one retried attempt sourced from %q", attempts, attemptID)
		}
	}
	if !forkFound {
		t.Fatalf("notice %q does not mention any fork session id", notices[0])
	}
}

// TestWireVerifyFailErrorOnlyNotices 证明失败只转成 notice：不可丢弃的 attemptID
// 让 DiscardFailedAttempt 返回哨兵错误，但回调不 panic、不返回错误、不发第二次分叉。
func TestWireVerifyFailErrorOnlyNotices(t *testing.T) {
	sess, _ := attemptIsolationFixture(t)
	cb := wireVerifyFail(sess, attemptIsolationReg())
	if cb == nil {
		t.Fatal("wireVerifyFail() = nil, want non-nil when enabled")
	}

	var events []agent.Event
	ctx := withEventEmit(context.Background(), func(ev agent.Event) { events = append(events, ev) })
	if err := cb(ctx, agent.VerifyFailInfo{AttemptID: "does-not-exist"}); err != nil {
		t.Fatalf("callback returned error on discard failure: %v", err)
	}

	notices := isolationNotices(events)
	if len(notices) != 1 || !strings.Contains(notices[0], "discard failed") {
		t.Fatalf("got notices %v, want one discard-failure notice", notices)
	}
	metas, err := session.List(sess.Dir())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("sessions in store = %d, want 1 (no fork on error)", len(metas))
	}
}

// TestWireVerifyFailEmptyAttemptID 证明空的 AttemptID 被安全忽略：只记一条 notice，
// 不调用 DiscardFailedAttempt，也不改变返回错误。
func TestWireVerifyFailEmptyAttemptID(t *testing.T) {
	sess, _ := attemptIsolationFixture(t)
	cb := wireVerifyFail(sess, attemptIsolationReg())

	var events []agent.Event
	ctx := withEventEmit(context.Background(), func(ev agent.Event) { events = append(events, ev) })
	if err := cb(ctx, agent.VerifyFailInfo{}); err != nil {
		t.Fatalf("callback returned error on empty attempt id: %v", err)
	}
	notices := isolationNotices(events)
	if len(notices) != 1 || !strings.Contains(notices[0], "empty attempt id") {
		t.Fatalf("got notices %v, want one empty-id notice", notices)
	}
	metas, err := session.List(sess.Dir())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("sessions in store = %d, want 1 (no fork on empty id)", len(metas))
	}
}
