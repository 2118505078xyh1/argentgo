package app

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// TestRunnerBackgroundQueuePersistsAcrossPrompts 守护 B 修复：后台结果队列在
// 同一会话的多个提示间持久（后台任务比提示活得久时结果可被后续提示抽取），
// 换会话时重建（防跨会话串味）。
func TestRunnerBackgroundQueuePersistsAcrossPrompts(t *testing.T) {
	var seen []*agent.BackgroundResults
	r := &runner{
		stdio: cli.IO{},
		res:   Resolved{Root: t.TempDir()},
		store: testStore(t, Resolved{Root: t.TempDir()}),
		execute: func(ctx context.Context, _ cli.IO, _ Resolved, _ *session.Session, _ string, _ func(string)) (agent.Result, error) {
			seen = append(seen, agent.BackgroundResultsFrom(ctx))
			return agent.Result{Text: "ok"}, nil
		},
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Prompt(context.Background(), "same session", nil); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
	}
	if len(seen) != 2 || seen[0] == nil || seen[1] == nil {
		t.Fatalf("两次提示都应拿到队列: %v", seen)
	}
	if seen[0] != seen[1] {
		t.Fatal("同会话跨提示应共享同一后台队列")
	}

	// 模拟换会话：重置绑定后应换一个新队列，不复用上一个会话的队列。
	r.bgSession = "a-different-session"
	if _, err := r.Prompt(context.Background(), "another session", nil); err != nil {
		t.Fatalf("换会话 prompt: %v", err)
	}
	if len(seen) != 3 || seen[2] == seen[1] {
		t.Fatal("换会话应重建后台队列，避免串味")
	}
}
