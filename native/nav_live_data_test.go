package main

import (
	"encoding/json"
	"os"
	"testing"
	"unicode"

	"github.com/egoist/mygo/ui"
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

// TestNavUsagePanelShowsRealData 验证 usage 事件数据到达后面板显示真实数字，
// 并去掉"即将推出"角标；没有数据时保持诚实占位。
func TestNavUsagePanelShowsRealData(t *testing.T) {
	app := newTestChat()
	app.nav = "用量"
	empty := ui.NewTester(app.view, 1180, 720)
	if !empty.HasText("运行后可见：尚未收到用量事件") || !empty.HasText("即将推出") {
		t.Fatalf("empty usage panel is not honest: %q", empty.Texts())
	}

	app.usage = navUsage{input: 1200, output: 340, cached: 200, calls: 3}
	full := ui.NewTester(app.view, 1180, 720)
	for _, want := range []string{"本轮输入 token：1200", "本轮输出 token：340", "本轮请求数：3"} {
		if !full.HasText(want) {
			t.Fatalf("usage panel missing %q: %q", want, full.Texts())
		}
	}
	if full.HasText("即将推出") || full.HasText("运行后可见：尚未收到用量事件") {
		t.Fatalf("live usage panel still shows placeholder: %q", full.Texts())
	}
}

// TestNavGitPanelShowsRealBranch 验证从工作区读取的分支名进入面板。
func TestNavGitPanelShowsRealBranch(t *testing.T) {
	app := newTestChat()
	app.nav, app.gitBranch = "Git", "kernel-refactor"
	tt := ui.NewTester(app.view, 1180, 720)
	if !tt.HasText("当前分支：kernel-refactor") {
		t.Fatalf("Git panel missing branch: %q", tt.Texts())
	}
	if tt.HasText("即将推出") {
		t.Fatalf("live Git panel still badged: %q", tt.Texts())
	}
}

// TestNavPlanPanelShowsTodoList 验证会话里的 todo 结构化参数变成可见计划。
func TestNavPlanPanelShowsTodoList(t *testing.T) {
	app := newTestChat()
	app.nav = "计划"
	app.planTodos = []navTodoItem{
		{text: "实现导航面板", status: "completed"},
		{text: "接入用量事件", status: "in_progress"},
		{text: "补充测试", status: "pending"},
	}
	tt := ui.NewTester(app.view, 1180, 720)
	for _, want := range []string{"已完成 · 实现导航面板", "进行中 · 接入用量事件", "待办 · 补充测试"} {
		if !tt.HasText(want) {
			t.Fatalf("plan panel missing %q: %q", want, tt.Texts())
		}
	}
	if tt.HasText("即将推出") {
		t.Fatalf("live plan panel still badged: %q", tt.Texts())
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
// 事件桥接路径更新导航面板状态，不触碰 transcript 消息数。
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
// 在加载会话时刷新"计划"面板（仅 native 解码 RPC 数据）。
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

// TestNavBadgeTracksLiveData 验证静态判定不变，动态角标随数据就绪消失。
func TestNavBadgeTracksLiveData(t *testing.T) {
	app := newTestChat()
	for _, name := range []string{"用量", "Git", "计划"} {
		if navItemBadge(name) != "即将推出" {
			t.Fatalf("static badge for %q changed", name)
		}
		if app.navPanelBadge(name) != "即将推出" {
			t.Fatalf("live badge for %q should start as coming soon", name)
		}
	}
	app.usage.calls, app.gitBranch = 1, "main"
	app.planTodos = []navTodoItem{{text: "x"}}
	for _, name := range []string{"用量", "Git", "计划"} {
		if app.navPanelBadge(name) != "" {
			t.Errorf("live badge for %q should be empty, got %q", name, app.navPanelBadge(name))
		}
	}
}

// TestEnUSNavPanelsHaveNoChinese 覆盖 en-US 下所有导航面板（含真实数据行），
// 防止新增文案漏进 nativeEnglish 表。
func TestEnUSNavPanelsHaveNoChinese(t *testing.T) {
	withLocale(t, map[string]string{"ARGENT_LOCALE": "en-US"})
	app := newTestChat()
	app.configInfo.workspace = t.TempDir() // 无文档文件 → 确定性回退行
	app.usage = navUsage{input: 10, output: 2, calls: 1}
	app.gitBranch = "main"
	app.planTodos = []navTodoItem{{text: "ship", status: "in_progress"}}

	for _, name := range navItems {
		if name == "聊天" {
			continue
		}
		app.nav = name
		tt := ui.NewTester(app.view, 1180, 720)
		for _, text := range tt.Texts() {
			for _, r := range text {
				if unicode.Is(unicode.Han, r) {
					t.Errorf("%s 面板 en-US 出现汉字：%q（全部文本：%q）", name, text, tt.Texts())
					break
				}
			}
		}
	}
}
