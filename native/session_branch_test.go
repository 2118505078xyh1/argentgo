package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

// branchTree 是 session.tree 的一棵双分支树：root 有两个 Fork 子节点，
// 其中一条路径上挂活动叶子。单线树（无 Fork）不应渲染任何节点。
const branchTree = `{"branches":[
	{"id":"root","kind":"header","depth":0,"label":"根"},
	{"id":"b1","parent":"root","kind":"user","depth":1,"label":"分支甲","fork":true},
	{"id":"b2","parent":"root","kind":"user","depth":1,"label":"分支乙","fork":true},
	{"id":"leaf","parent":"b2","kind":"assistant","depth":2,"label":"当前","active":true}
]}`

// branchEvents 是 session.load 的返回：一条用户、一条工具、一条助手记录，
// 覆盖 data 为字符串与对象两种线上形态。
const branchEvents = `{"id":"sess-abc","events":[
	{"id":"e1","kind":"user","data":"问题甲"},
	{"id":"e2","kind":"tool","data":{"name":"bash","output":"ok","is_error":false}},
	{"id":"e3","kind":"assistant","data":{"content":"回答甲"}}
]}`

// treeForkA 是测试里要点击的分支节点标签，必须与 branchNodeLabel 的生成规则一致。
func treeForkA() string {
	return branchNodeLabel(SessionBranch{ID: "b1", Parent: "root", Kind: "user", Depth: 1, Label: "分支甲", Fork: true})
}

// withBranchRPCs 给 app 装上三种 RPC 接缝，使测试无需活 engine 进程。
func withBranchRPCs(app *chat, tree string, loads *[]string) {
	app.engine = &bridge{}
	app.sessionListRPC = func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"sessions":[{"id":"sess-abc","title":"真实会话A","updated":"2026-11-20T10:00:00Z"}]}`), nil
	}
	app.sessionTreeRPC = func(method string, params any) (json.RawMessage, error) {
		if method != "session.tree" {
			return nil, errors.New("unexpected method " + method)
		}
		return json.RawMessage(tree), nil
	}
	app.sessionLoadRPC = func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		*loads = append(*loads, method+" "+string(raw))
		return json.RawMessage(branchEvents), nil
	}
}

func TestBranchNodeClickLoadsBranch(t *testing.T) {
	var loads []string
	app := newTestChat()
	withBranchRPCs(app, branchTree, &loads)

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("真实会话A"); err != nil {
		t.Fatalf("click session row: %v (texts %q)", err, tt.Texts())
	}
	if !tt.HasText(treeForkA()) {
		t.Fatalf("fork node not rendered: %q", tt.Texts())
	}
	if err := tt.Click(treeForkA()); err != nil {
		t.Fatalf("click fork node: %v (texts %q)", err, tt.Texts())
	}

	if len(loads) != 2 {
		t.Fatalf("session.load calls = %v, want 2 (row then branch)", loads)
	}
	if want := `session.load {"id":"sess-abc"}`; loads[0] != want {
		t.Errorf("first load = %q, want %q", loads[0], want)
	}
	// json.Marshal 对 map 的键排序：branch 在 id 之前。
	if want := `session.load {"branch":"b1","id":"sess-abc"}`; loads[1] != want {
		t.Errorf("branch load = %q, want %q", loads[1], want)
	}
}

func TestBranchTranscriptMapsEventsToMessages(t *testing.T) {
	var loads []string
	app := newTestChat()
	withBranchRPCs(app, `{"branches":[]}`, &loads)

	app.openSessionBranch(session{id: "sess-abc", title: "真实会话A"}, "")

	if len(app.messages) != 3 {
		t.Fatalf("messages = %d, want 3: %+v", len(app.messages), app.messages)
	}
	want := []struct {
		kind   msgKind
		text   string
		name   string
		status string
	}{
		{msgUser, "问题甲", "", ""},
		{msgTool, "ok", "bash", "done"},
		{msgAssistant, "回答甲", "", ""},
	}
	for i, w := range want {
		m := app.messages[i]
		if m.kind != w.kind || m.text != w.text || m.name != w.name || m.status != w.status {
			t.Errorf("message[%d] = {kind:%d text:%q name:%q status:%q}, want {kind:%d text:%q name:%q status:%q}",
				i, m.kind, m.text, m.name, m.status, w.kind, w.text, w.name, w.status)
		}
	}
	// 映射后的消息继续走既有 app.message / ui.List 渲染。
	tt := ui.NewTester(app.view, 1180, 720)
	for _, text := range []string{"问题甲", "回答甲", "bash", "ok"} {
		if !tt.HasText(text) {
			t.Errorf("transcript missing %q: %q", text, tt.Texts())
		}
	}
}

func TestBranchLoadClearsStaleStreamState(t *testing.T) {
	var loads []string
	app := newTestChat()
	withBranchRPCs(app, `{"branches":[]}`, &loads)

	// 模拟上一轮遗留的流式回复、工具卡、审批和旧消息。
	app.streaming = true
	app.reply = &message{id: 90, kind: msgAssistant, text: "旧回复"}
	app.tool = &message{id: 91, kind: msgTool, name: "旧工具"}
	app.approval = &approval{id: "old", name: "旧审批"}
	app.tools = map[string]*message{"old": app.tool}
	app.messages = []*message{
		{id: 88, kind: msgUser, text: "旧问题"},
		app.reply,
		app.tool,
	}
	app.lastPrompt = "旧问题"

	app.openSessionBranch(session{id: "sess-abc", title: "真实会话A"}, "")

	if app.streaming || app.reply != nil || app.tool != nil || app.approval != nil || app.tools != nil {
		t.Errorf("stale state survived: streaming=%v reply=%v tool=%v approval=%v tools=%v",
			app.streaming, app.reply, app.tool, app.approval, app.tools)
	}
	if app.lastPrompt != "" {
		t.Errorf("lastPrompt = %q, want empty", app.lastPrompt)
	}
	if len(app.messages) != 3 {
		t.Fatalf("messages = %d, want 3 mapped rows", len(app.messages))
	}
	for _, m := range app.messages {
		if m.id == 88 || m.id == 90 || m.id == 91 {
			t.Errorf("stale message id %d mixed into transcript", m.id)
		}
	}
}

func TestSingleLineTreeFrameUnchanged(t *testing.T) {
	plain := newTestChat()
	plain.selectedSession = "sess-abc"

	linear := newTestChat()
	linear.selectedSession = "sess-abc"
	linear.branchSession = "sess-abc"
	linear.branches = []SessionBranch{
		{ID: "root", Kind: "header", Depth: 0, Label: "根"},
		{ID: "u", Parent: "root", Kind: "user", Depth: 1, Label: "单线问题"},
		{ID: "a", Parent: "u", Kind: "assistant", Depth: 2, Label: "单线回答", Active: true},
	}

	ttPlain := ui.NewTester(plain.view, 1180, 720)
	ttLinear := ui.NewTester(linear.view, 1180, 720)
	if !bytes.Equal(ttPlain.Image().Pix, ttLinear.Image().Pix) {
		t.Error("single-line tree changed the frame: branch nodes must render only when a fork exists")
	}
	if got, want := ttLinear.Texts(), ttPlain.Texts(); !equalStrings(got, want) {
		t.Errorf("single-line tree changed texts: got %q, want %q", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSessionTreeFailureKeepsOldBranches(t *testing.T) {
	var loads []string
	app := newTestChat()
	withBranchRPCs(app, branchTree, &loads)

	treeCalls := 0
	app.sessionTreeRPC = func(method string, params any) (json.RawMessage, error) {
		treeCalls++
		if treeCalls == 1 {
			return json.RawMessage(branchTree), nil
		}
		return nil, errors.New("api_key: sk-abcdefghijklmnopqrstuvwxyz")
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("真实会话A"); err != nil {
		t.Fatalf("click session row: %v", err)
	}
	if !tt.HasText(treeForkA()) {
		t.Fatalf("fork node missing before failure: %q", tt.Texts())
	}

	// 切换到另一个会话时 session.tree 失败：旧分支节点留在原会话行下。
	app.openSessionBranch(session{id: "sess-other", title: "另一个会话"}, "")
	if treeCalls != 2 {
		t.Fatalf("session.tree calls = %d, want 2", treeCalls)
	}
	if len(app.branches) != 4 || app.branches[1].ID != "b1" {
		t.Fatalf("tree failure discarded old branches: %+v", app.branches)
	}
	after := ui.NewTester(app.view, 1180, 720)
	if !after.HasText(treeForkA()) {
		t.Errorf("tree failure removed old branch node: %q", after.Texts())
	}
}

func TestDemoModeSkipsBranchRPCs(t *testing.T) {
	app := newTestChat() // engine == nil
	treeCalled, loadCalled := false, false
	app.sessionTreeRPC = func(string, any) (json.RawMessage, error) {
		treeCalled = true
		return nil, nil
	}
	app.sessionLoadRPC = func(string, any) (json.RawMessage, error) {
		loadCalled = true
		return nil, nil
	}

	tt := ui.NewTester(app.view, 1180, 720)
	if err := tt.Click("重构聊天界面"); err != nil {
		t.Fatalf("click demo session: %v", err)
	}
	if treeCalled || loadCalled {
		t.Errorf("demo mode sent RPCs: tree=%v load=%v", treeCalled, loadCalled)
	}
}
