package app

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// TestExecuteProgressRecoveryNotice 守护读侧接线：纯进度崩溃（有 progress 记录、
// 无更晚的 assistant）在恢复路径产生一条用户可见公告，内容含未完成输出；
// 该文本绝不进入模型历史（由 session.Progress 的不可见契约 + 我们的公告通道保证）。
func TestExecuteProgressRecoveryNotice(t *testing.T) {
	env, root, _ := wiringEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.Approve = autoApprover{}
	res.Chatter = &scriptedChatter{replies: []string{"done"}}
	store := testStore(t, res)
	sess, err := store.NewSession("progress-recovery")
	if err != nil {
		t.Fatal(err)
	}
	// 干净历史 + 一条进行中的进度（Turn=2，尚无第 2 轮 assistant）。
	if err := sess.Append(session.KindUser, "question"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindAssistant, "answer"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendProgress(session.Progress{Turn: 2, AssistantText: "半截输出", ToolCalls: []string{"bash"}}); err != nil {
		t.Fatal(err)
	}

	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	var texts []string
	if _, err := Execute(ctx, cli.IO{Env: env}, res, sess, "continue", func(s string) { texts = append(texts, s) }); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !textsContain(texts, "半截输出") {
		t.Fatalf("onText 未收到进度公告: %v", texts)
	}
	if !noticesContain(emitted, "半截输出") {
		t.Fatalf("EventNotice 未发布进度公告: %v", emitted)
	}
	// 进度文本不得进入模型可见历史。
	for _, m := range sess.Messages() {
		if strings.Contains(m.Content, "半截输出") {
			t.Fatalf("进度文本泄漏进模型历史: %+v", m)
		}
	}
}

// TestExecuteProgressRecoveryStaleIgnored 守护两道过期检查：进度之后已提交
// assistant（轮次已完成）时，RecoverProgress 返回 false，不产生公告。
func TestExecuteProgressRecoveryStaleIgnored(t *testing.T) {
	env, root, _ := wiringEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.Approve = autoApprover{}
	res.Chatter = &scriptedChatter{replies: []string{"done"}}
	store := testStore(t, res)
	sess, err := store.NewSession("progress-stale")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "q"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendProgress(session.Progress{Turn: 1, AssistantText: "旧进度"}); err != nil {
		t.Fatal(err)
	}
	// 进度之后出现 assistant：该轮次已完成，进度过期。
	if err := sess.Append(session.KindAssistant, "已完成"); err != nil {
		t.Fatal(err)
	}

	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	if _, err := Execute(ctx, cli.IO{Env: env}, res, sess, "next", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if noticesContain(emitted, "旧进度") {
		t.Fatalf("过期进度不应产生公告: %v", emitted)
	}
}

// textsContain / noticesContain 做子串匹配（进度公告是拼接后的完整句）。
func textsContain(texts []string, sub string) bool {
	for _, got := range texts {
		if strings.Contains(got, sub) {
			return true
		}
	}
	return false
}

func noticesContain(events []agent.Event, sub string) bool {
	for _, e := range events {
		if e.Kind == agent.EventNotice && strings.Contains(e.Text, sub) {
			return true
		}
	}
	return false
}
