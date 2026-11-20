package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestUndoRemovesWholeTurnKeepsEarlierTurns 检查回合边界：undo 必须
// 把最后一条用户消息连同其回复和工具卡片一起删除，保留
// 之前的回合，并为接下来的 retry 保留提示文本。
func TestUndoRemovesWholeTurnKeepsEarlierTurns(t *testing.T) {
	app := newTestChat()
	app.send("第一轮问题")
	app.send("第二轮问题")
	if len(app.messages) != 6 {
		t.Fatalf("setup left %d messages, want 6", len(app.messages))
	}
	if app.lastPrompt != "第二轮问题" {
		t.Fatalf("lastPrompt = %q", app.lastPrompt)
	}

	app.undo()
	if len(app.messages) != 3 {
		t.Fatalf("undo left %d messages, want 3 (first turn only)", len(app.messages))
	}
	if app.lastPrompt != "第二轮问题" {
		t.Fatalf("undo should keep the prompt for retry, got %q", app.lastPrompt)
	}
	var firstTurn bool
	for _, m := range app.messages {
		if m.text == "第二轮问题" {
			t.Fatal("undone turn still present")
		}
		if m.kind == msgUser && m.text == "第一轮问题" {
			firstTurn = true
		}
	}
	if !firstTurn {
		t.Fatal("undo dropped the earlier turn")
	}

	app.undo()
	if len(app.messages) != 0 || app.status != "已撤销上一轮" {
		t.Fatalf("second undo: messages=%d status=%q", len(app.messages), app.status)
	}
}

// autoReplyStdin 回答 bridge.call 写入的任何 JSON-RPC 请求，使测试
// 无需活 engine 即可驱动 stop()/abort()。回复在
type autoReplyStdin struct{ b *bridge }

func (s *autoReplyStdin) Write(p []byte) (int, error) {
	var m rpcMessage
	if json.Unmarshal(p, &m) == nil && len(m.ID) > 0 {
		var id int
		if json.Unmarshal(m.ID, &id) == nil {
			go func() {
				s.b.mu.Lock()
				ch := s.b.pending[id]
				s.b.mu.Unlock()
				if ch != nil {
					ch <- rpcReply{result: json.RawMessage(`{}`)}
				}
			}()
		}
	}
	return len(p), nil
}
func (*autoReplyStdin) Close() error { return nil }

// TestUndoWhileStreamingRetiresLateCompletion 锁定如下回归：
// 被中止/撤销轮的迟到结果（尤其是通过错误路由）
// finishEngineError) overwrote "已撤销上一轮" with "引擎错误"/"已完成" and
// 重新挂上一个陈旧的回复指针。
func TestUndoWhileStreamingRetiresLateCompletion(t *testing.T) {
	b := &bridge{pending: make(map[int]chan rpcReply)}
	b.stdin = &autoReplyStdin{b: b}
	reply := &message{id: 1, kind: msgAssistant, text: "partial"}
	app := &chat{engine: b, reply: reply, streaming: true}
	app.messages = []*message{{id: 0, kind: msgUser, text: "问题"}, reply}
	gen := b.setDeltaHandler(func(string) {})

	app.undo()

	if app.streaming || app.reply != nil || app.approval != nil {
		t.Fatalf("undo left streaming=%v reply=%v approval=%v", app.streaming, app.reply, app.approval)
	}
	if len(app.messages) != 0 {
		t.Fatalf("undo left %d messages, want 0", len(app.messages))
	}
	if app.status != "已撤销上一轮" {
		t.Fatalf("undo status = %q", app.status)
	}
	if b.isCurrentDeltaGen(gen) {
		t.Fatal("undo did not retire the streaming generation")
	}

	// 旧轮的错误迟到到达；它必须被完全忽略。
	app.completeRealReply(b, gen, reply, "late full", errors.New("api_key: sk-proj-abcdefghijklmnop boom"))
	if app.status != "已撤销上一轮" || len(app.messages) != 0 || app.reply != nil {
		t.Fatalf("late completion clobbered undo state: status=%q messages=%d reply=%v",
			app.status, len(app.messages), app.reply)
	}
	if strings.Contains(reply.text, "引擎错误") {
		t.Fatalf("late error leaked into the undone reply: %q", reply.text)
	}
}

// TestRetryReplaysPromptAfterUndo 检查 retry 恢复用户键入的确切提示
// 并重建完整的三行回合。
func TestRetryReplaysPromptAfterUndo(t *testing.T) {
	app := newTestChat()
	app.send("重试这个问题")
	app.undo()
	if len(app.messages) != 0 {
		t.Fatalf("undo left %d messages", len(app.messages))
	}

	app.retry()
	if len(app.messages) != 3 {
		t.Fatalf("retry left %d messages, want 3", len(app.messages))
	}
	if got := app.messages[0]; got.kind != msgUser || got.text != "重试这个问题" {
		t.Fatalf("replayed row = %+v", got)
	}
	if app.lastPrompt != "重试这个问题" {
		t.Fatalf("lastPrompt = %q", app.lastPrompt)
	}
}

// TestRetryNoopWhenStreamingOrEmpty 守护 retry 的两个空操作边界。
func TestRetryNoopWhenStreamingOrEmpty(t *testing.T) {
	app := newTestChat()
	app.retry()
	if len(app.messages) != 0 || app.status != "没有可重试的消息" {
		t.Fatalf("empty retry: messages=%d status=%q", len(app.messages), app.status)
	}

	app.lastPrompt = "x"
	app.streaming = true
	app.retry()
	if len(app.messages) != 0 || app.status != "正在生成，无法重试" {
		t.Fatalf("streaming retry: messages=%d status=%q", len(app.messages), app.status)
	}
}

// （工具卡片）存在：没有可移除的回合。
func TestUndoWithNoUserMessageIsNoop(t *testing.T) {
	app := &chat{}
	app.applyEngineEvent("tool_start", json.RawMessage(`{"ToolCallID":"c1","ToolName":"bash","Args":"ls"}`))
	app.undo()
	if len(app.messages) != 1 || app.status != "没有可撤销的消息" {
		t.Fatalf("messages=%d status=%q", len(app.messages), app.status)
	}
}

// TestCompleteRealReplyRoutesEngineErrorThroughRedaction 验证实际的
// sendReal 完成路径（而非仅辅助函数）把 provider 错误呈现为
// 带显式状态的脱敏、单行、rune 有界通知。
func TestCompleteRealReplyRoutesEngineErrorThroughRedaction(t *testing.T) {
	b := &bridge{pending: make(map[int]chan rpcReply)}
	reply := &message{id: 1, kind: msgAssistant}
	app := &chat{engine: b, reply: reply, streaming: true}
	app.messages = []*message{reply}
	gen := b.setDeltaHandler(func(string) {})

	detail := "api_key: sk-proj-abcdefghijklmnop\n" + strings.Repeat("行", maxEngineErrorDetail)
	app.completeRealReply(b, gen, reply, "", errors.New(detail))

	if reply.text != engineErrorNotice(errors.New(detail)) {
		t.Fatalf("reply text = %q, want the redacted notice", reply.text)
	}
	if app.status != "引擎错误" {
		t.Fatalf("status = %q", app.status)
	}
	if strings.Contains(reply.text, "sk-proj-abcdefghijklmnop") {
		t.Fatalf("credential leaked: %q", reply.text)
	}
	if strings.ContainsAny(reply.text, "\r\n\t") {
		t.Fatalf("notice is not single-line: %q", reply.text)
	}
	if got := utf8.RuneCountInString(reply.text); got != utf8.RuneCountInString(engineErrorPrefix)+maxEngineErrorDetail {
		t.Fatalf("notice rune count = %d, want %d", got, utf8.RuneCountInString(engineErrorPrefix)+maxEngineErrorDetail)
	}
}
