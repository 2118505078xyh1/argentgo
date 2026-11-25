package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
)

type goalFakeChatter struct {
	reply string
	calls int
}

func (g *goalFakeChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	g.calls++
	return llm.Message{Role: "assistant", Content: g.reply}, nil
}

// TestParseGoalPrompt 锁定 /goal 解析：--max 解析与默认上限 10。
func TestParseGoalPrompt(t *testing.T) {
	objective, max, ok := ParseGoalPrompt("/goal fix the flaky test")
	if !ok || objective != "fix the flaky test" || max != agent.DefaultGoalMaxIterations {
		t.Fatalf("plain = %q / %d / %v", objective, max, ok)
	}
	objective, max, ok = ParseGoalPrompt("/goal --max 3 ship the feature")
	if !ok || objective != "ship the feature" || max != 3 {
		t.Fatalf("--max = %q / %d / %v", objective, max, ok)
	}
	objective, max, ok = ParseGoalPrompt("/goal --max=7 do work")
	if !ok || objective != "do work" || max != 7 {
		t.Fatalf("--max= = %q / %d / %v", objective, max, ok)
	}
	if _, _, ok := ParseGoalPrompt("just a normal prompt"); ok {
		t.Fatal("non-goal prompt must not be claimed")
	}
}

// TestGoalJudge 锁定判官 JSON 解析（含围栏包裹）与缺失值传回。
func TestGoalJudge(t *testing.T) {
	chat := &goalFakeChatter{reply: "```json\n{\"complete\": false, \"missing\": [\"add tests\", \"fix docs\"]}\n```"}
	verdict, err := GoalJudge(context.Background(), chat, "secondary", "ship it", "partial answer", nil)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if verdict.Complete || len(verdict.Missing) != 2 || verdict.Missing[0] != "add tests" {
		t.Fatalf("verdict = %+v", verdict)
	}
	if chat.calls != 1 {
		t.Fatalf("judge calls = %d", chat.calls)
	}
	if _, err := GoalJudge(context.Background(), nil, "", "x", "y", nil); err == nil {
		t.Fatal("missing model must error")
	}
}

// TestRunGoalLoopEmitsGoalStatus 锁定 goal_status 事件：默认上限生效，状态最终为 capped。
func TestRunGoalLoopEmitsGoalStatus(t *testing.T) {
	chat := &goalFakeChatter{reply: `{"complete": false, "missing": ["one more"]}`}
	runner := &agent.Runner{Opts: agent.Options{
		Model:   "main",
		Chatter: &goalFakeChatter{reply: "work done"},
		Tools:   nopAgentTools{},
	}}
	var statuses []agent.GoalStatus
	emit := func(e agent.Event) {
		if e.Kind == goalStatusEventKind {
			var s agent.GoalStatus
			if json.Unmarshal([]byte(e.Text), &s) == nil {
				statuses = append(statuses, s)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := RunGoalLoop(ctx, runner, chat, "secondary", "finish", 2, emit, nil); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("no goal_status events")
	}
	last := statuses[len(statuses)-1]
	if last.State != agent.GoalStateCapped || last.Iteration != 2 || last.MaxIterations != 2 {
		t.Fatalf("last status = %+v", last)
	}
	if !strings.Contains(statuses[len(statuses)-1].Objective, "finish") {
		t.Fatalf("objective not carried: %+v", last)
	}
}

// nopAgentTools 是测试用的空 ToolRunner。
type nopAgentTools struct{}

func (nopAgentTools) Specs() []llm.ToolSpec { return nil }
func (nopAgentTools) Run(context.Context, string, string) (string, error) {
	return "", nil
}
