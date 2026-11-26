package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestNavGitBranchFromHEAD 覆盖只读 .git/HEAD 解析：正常分支、detached HEAD
// 与缺文件三种情况。它不调用任何 CLI，也不新增 RPC。
func TestNavGitBranchFromHEAD(t *testing.T) {
	tests := []struct {
		name string
		head string
		miss bool
		want string
	}{
		{name: "feature branch", head: "ref: refs/heads/kernel-refactor\n", want: "kernel-refactor"},
		{name: "main", head: "ref: refs/heads/main\n", want: "main"},
		{name: "slash branch", head: "ref: refs/heads/feat/nav\n", want: "feat/nav"},
		{name: "detached", head: "a1b2c3d4e5f6\n", want: ""},
		{name: "empty ref", head: "ref: refs/heads/\n", want: ""},
		{name: "missing", miss: true, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			read := func(string) ([]byte, error) {
				if tc.miss {
					return nil, os.ErrNotExist
				}
				return []byte(tc.head), nil
			}
			if got := navGitBranch(t.TempDir(), read); got != tc.want {
				t.Fatalf("navGitBranch = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParsePlanTodos 只接受带 todos 数组的 write 参数，read/畸形输入不覆盖。
func TestParsePlanTodos(t *testing.T) {
	items := parsePlanTodos(`{"op":"write","todos":[{"id":"1","text":" first ","status":"in_progress"},{"text":""},{"text":"second"}]}`)
	if len(items) != 2 || items[0].text != "first" || items[0].status != "in_progress" || items[1].text != "second" {
		t.Fatalf("parsePlanTodos = %+v", items)
	}
	for _, args := range []string{``, `{}`, `{"op":"read"}`, `{"todos":null}`, `not json`} {
		if got := parsePlanTodos(args); got != nil {
			t.Errorf("parsePlanTodos(%q) = %+v, want nil", args, got)
		}
	}
}

// TestApplyEngineEventUpdatesLivePanels 验证 usage 与 todo 工具事件经真实
// 事件桥接路径更新会话状态，不触碰 transcript 消息数。
func TestApplyEngineEventUpdatesLivePanels(t *testing.T) {
	app := &chat{}
	app.applyEngineEvent("usage", json.RawMessage(`{"Kind":"usage","TotalUsage":{"prompt_tokens":500,"completion_tokens":75,"cached_tokens":100}}`))
	if app.usage.input != 500 || app.usage.output != 75 || app.usage.cached != 100 || app.usage.calls != 1 {
		t.Fatalf("usage = %+v", app.usage)
	}
	app.applyEngineEvent("usage", json.RawMessage(`{"Kind":"usage","TotalUsage":{"prompt_tokens":800,"completion_tokens":120}}`))
	if app.usage.input != 800 || app.usage.calls != 2 {
		t.Fatalf("cumulative usage not replaced: %+v", app.usage)
	}
	// 新一轮的累计值回落时请求数归零，避免跨轮口径不一致。
	app.applyEngineEvent("usage", json.RawMessage(`{"Kind":"usage","TotalUsage":{"prompt_tokens":30,"completion_tokens":5}}`))
	if app.usage.input != 30 || app.usage.calls != 1 {
		t.Fatalf("new run did not reset call count: %+v", app.usage)
	}

	app.applyEngineEvent("tool_end", json.RawMessage(`{"ToolCallID":"c1","ToolName":"todo","Args":"{\"todos\":[{\"text\":\"写测试\",\"status\":\"pending\"}]}","Output":"[ ] 1 写测试"}`))
	if len(app.planTodos) != 1 || app.planTodos[0].text != "写测试" {
		t.Fatalf("planTodos = %+v", app.planTodos)
	}
	if len(app.messages) != 1 {
		t.Fatalf("tool card count = %d, want 1", len(app.messages))
	}
	// todo 的 read 调用不带 todos，不应清空已保存的计划。
	app.applyEngineEvent("tool_end", json.RawMessage(`{"ToolCallID":"c2","ToolName":"todo","Args":"{\"op\":\"read\"}","Output":"task list is empty"}`))
	if len(app.planTodos) != 1 {
		t.Fatalf("read call cleared plan: %+v", app.planTodos)
	}
}

// TestLoadTranscriptExtractsPlan 验证 session.load 返回的 todo 工具记录
// 在加载会话时刷新计划状态（仅 native 解码 RPC 数据）。
func TestLoadTranscriptExtractsPlan(t *testing.T) {
	app := &chat{}
	app.loadTranscript([]sessionEvent{
		{Kind: "tool", Data: json.RawMessage(`{"name":"todo","arguments":"{\"todos\":[{\"text\":\"旧计划\",\"status\":\"completed\"}]}"}`)},
		{Kind: "tool", Data: json.RawMessage(`{"name":"todo","arguments":"{\"todos\":[{\"text\":\"新计划\",\"status\":\"in_progress\"}]}"}`)},
		{Kind: "tool", Data: json.RawMessage(`{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}`)},
	})
	if len(app.planTodos) != 1 || app.planTodos[0].text != "新计划" {
		t.Fatalf("planTodos = %+v, want last todo write", app.planTodos)
	}
}
