package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestParseEngineApproval 覆盖审批通知线上格式的本地解码器，
// 使 native/ 永不导入 internal/agent。
func TestParseEngineApproval(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantKind   string
		wantTool   string
		wantID     string
		wantDigest string
		wantGrant  bool
		wantOK     bool
	}{
		{
			name:       "request",
			text:       `approval:request {"v":1,"tool":"bash","digest":"deadbeef","id":"call_1"}`,
			wantKind:   "request",
			wantTool:   "bash",
			wantID:     "call_1",
			wantDigest: "deadbeef",
			wantOK:     true,
		},
		{
			name:      "resolved granted",
			text:      `approval:resolved {"v":1,"tool":"write","id":"call_2","granted":true}`,
			wantKind:  "resolved",
			wantTool:  "write",
			wantID:    "call_2",
			wantGrant: true,
			wantOK:    true,
		},
		{
			name:     "resolved denied",
			text:     `approval:resolved {"v":1,"tool":"write","id":"call_3","granted":false}`,
			wantKind: "resolved",
			wantTool: "write",
			wantID:   "call_3",
			wantOK:   true,
		},
		{name: "unrelated notice", text: "disk almost full"},
		{name: "unknown prefix", text: `approval:pending {"v":1,"tool":"x","id":"y"}`},
		{name: "malformed json", text: `approval:request {"v":1,`},
		{name: "wrong version", text: `approval:request {"v":2,"tool":"x","id":"y"}`},
		{name: "missing id", text: `approval:request {"v":1,"tool":"x"}`},
		{name: "missing granted", text: `approval:resolved {"v":1,"tool":"x","id":"y"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, tool, id, digest, granted, ok := parseEngineApproval(tc.text)
			if ok != tc.wantOK || kind != tc.wantKind || tool != tc.wantTool ||
				id != tc.wantID || digest != tc.wantDigest || granted != tc.wantGrant {
				t.Fatalf("parseEngineApproval(%q) = (%q,%q,%q,%q,%v,%v); want (%q,%q,%q,%q,%v,%v)",
					tc.text, kind, tool, id, digest, granted, ok,
					tc.wantKind, tc.wantTool, tc.wantID, tc.wantDigest, tc.wantGrant, tc.wantOK)
			}
		})
	}
}

// TestReadLoopDispatchesEvent 证明 readLoop 把 "event" 通知转发为
// (kind, 原始事件) 而不导入 engine 的事件类型。
func TestReadLoopDispatchesEvent(t *testing.T) {
	line := `{"jsonrpc":"2.0","method":"event","params":{"kind":"tool_start","event":{"Kind":"tool_start","ToolCallID":"c1","ToolName":"bash","Args":"ls"}}}` + "\n"
	var gotKind string
	var gotRaw json.RawMessage
	b := &bridge{
		stdout:  bufio.NewReader(strings.NewReader(line)),
		pending: make(map[int]chan rpcReply),
		onEvent: func(kind string, raw json.RawMessage) { gotKind, gotRaw = kind, raw },
	}
	b.readLoop() // returns at EOF

	if gotKind != "tool_start" {
		t.Fatalf("kind = %q, want tool_start", gotKind)
	}
	var ev struct {
		ToolCallID string `json:"ToolCallID"`
		ToolName   string `json:"ToolName"`
	}
	if err := json.Unmarshal(gotRaw, &ev); err != nil {
		t.Fatalf("raw event is not JSON: %v (%s)", err, gotRaw)
	}
	if ev.ToolCallID != "c1" || ev.ToolName != "bash" {
		t.Fatalf("event = %+v, want c1/bash", ev)
	}
}

// TestReadLoopStillAnswersReplies 守护在 switch 中加入新 event case 后
// TestParseEngineApproval 覆盖审批通知线上格式的本地解码器，使 native/ 永不导入 internal/agent。TestReadLoopDispatchesEvent 证明 readLoop 把 "event" 通知转发为 (kind, 原始事件) 而不导入 engine 的事件类型。TestReadLoopStillAnswersReplies 守护在 switch 中加入新 event case 后原有的响应路径。
func TestReadLoopStillAnswersReplies(t *testing.T) {
	line := `{"jsonrpc":"2.0","id":7,"result":{"text":"ok"}}` + "\n"
	b := &bridge{
		stdout:  bufio.NewReader(strings.NewReader(line)),
		pending: make(map[int]chan rpcReply),
	}
	ch := make(chan rpcReply, 1)
	b.pending[7] = ch
	b.readLoop()
	reply := <-ch
	if reply.err != "" || string(reply.result) != `{"text":"ok"}` {
		t.Fatalf("reply = %+v", reply)
	}
}

type captureWriteCloser struct{ bytes.Buffer }

func (*captureWriteCloser) Close() error { return nil }

// TestSubscribeEventsCallsRPC 检查 SubscribeEvents 发送订阅
// 请求并从回复返回 seq，无新依赖。
func TestSubscribeEventsCallsRPC(t *testing.T) {
	in := &captureWriteCloser{}
	// 保持 stdout 流打开直到测试结束，使 readLoop 不会遇到
	// EOF（并把 bridge 标记为关闭）在 SubscribeEvents 注册其
	// 待处理回复之前。strings.Reader 立即到达 EOF 并与
	// SubscribeEvents 内的异步调用竞争，在更快/更慢的 CI 机器上失败。
	pr, pw := io.Pipe()
	defer pw.Close()
	reply := `{"jsonrpc":"2.0","id":1,"result":{"seq":42}}` + "\n"
	go func() {
		_, _ = pw.Write([]byte(reply))
	}()
	b := &bridge{
		stdin:   in,
		stdout:  bufio.NewReader(pr),
		pending: make(map[int]chan rpcReply),
	}
	go b.readLoop()

	seq, err := b.SubscribeEvents(context.Background())
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}
	if seq != 42 {
		t.Fatalf("seq = %d, want 42", seq)
	}
	var sent map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(in.Bytes()), &sent); err != nil {
		t.Fatalf("request is not JSON: %v (%s)", err, in.String())
	}
	if sent["method"] != "event/subscribe" {
		t.Fatalf("method = %v, want event/subscribe", sent["method"])
	}
	params, _ := sent["params"].(map[string]any)
	if params["after"] != float64(0) {
		t.Fatalf("params = %v, want after=0", sent["params"])
	}
}

// TestApplyEngineEventUpsertsTools 检查 tool_start/tool_end 对每个工具调用 id 复用
// 一行，且出错的调用显示为失败。
func TestApplyEngineEventUpsertsTools(t *testing.T) {
	app := &chat{}
	app.applyEngineEvent("tool_start", json.RawMessage(`{"ToolCallID":"c1","ToolName":"bash","Args":"ls -la"}`))
	if len(app.messages) != 1 || len(app.tools) != 1 {
		t.Fatalf("after start: %d messages, %d tools", len(app.messages), len(app.tools))
	}
	first := app.tools["c1"]
	if first.name != "bash" || first.status != "running" || first.text != "ls -la" {
		t.Fatalf("start card = %+v", first)
	}

	app.applyEngineEvent("tool_end", json.RawMessage(`{"ToolCallID":"c1","ToolName":"bash","Output":"done","IsError":false}`))
	if len(app.messages) != 1 {
		t.Fatalf("tool_end added a second row: %d", len(app.messages))
	}
	if first.status != "done" {
		t.Fatalf("status = %q, want done", first.status)
	}

	app.applyEngineEvent("tool_end", json.RawMessage(`{"ToolCallID":"c2","ToolName":"write","Output":"boom","IsError":true}`))
	second := app.tools["c2"]
	if second == nil || second.status != "failed" {
		t.Fatalf("error card = %+v, want failed", second)
	}
	if len(app.messages) != 2 {
		t.Fatalf("distinct ids should create two rows, got %d", len(app.messages))
	}
}

// TestApplyEngineEventApprovalFlow 检查请求通知升起真实
// 卡片，且匹配的已解决通知把它替换为结果行。
func TestApplyEngineEventApprovalFlow(t *testing.T) {
	app := &chat{}
	app.applyEngineEvent("notice", json.RawMessage(`{"Text":"approval:request {\"v\":1,\"tool\":\"bash\",\"digest\":\"abc123\",\"id\":\"c1\"}"}`))
	if app.approval == nil || app.approval.id != "c1" || app.approval.name != "bash" || app.approval.args != "abc123" {
		t.Fatalf("request card = %+v", app.approval)
	}

	app.applyEngineEvent("notice", json.RawMessage(`{"Text":"approval:resolved {\"v\":1,\"tool\":\"bash\",\"id\":\"c1\",\"granted\":false}"}`))
	if app.approval != nil {
		t.Fatalf("resolved notice left the card open: %+v", app.approval)
	}
	if len(app.messages) != 1 {
		t.Fatalf("resolved notice should append one result row, got %d", len(app.messages))
	}
	if row := app.messages[0]; row.status != "failed" || !strings.Contains(row.text, "bash") {
		t.Fatalf("result row = %+v, want failed bash", row)
	}
}

// TestApplyEngineEventUpsertsTools 检查 tool_start/tool_end 对每个工具调用 id 复用一行，且出错的调用显示为失败。TestApplyEngineEventApprovalFlow 检查请求通知升起真实卡片，且匹配的已解决通知把它替换为结果行。TestClipToolTextNeverSplitsRune 防止在详细参数被截断时向工具卡片发出无效 UTF-8。
// 工具卡片，当详细参数被截断时。
func TestClipToolTextNeverSplitsRune(t *testing.T) {
	s := strings.Repeat("工具", 60)
	got := clipToolText(s, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("clipped text is not valid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 11 { // 10 runes + ellipsis
		t.Fatalf("rune count = %d, want 11", utf8.RuneCountInString(got))
	}
}
