package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// scriptedTTSRChatter 按脚本对每次请求推送增量。第 n 次请求用第 n 段脚本。
type scriptedTTSRChatter struct {
	turns   [][]string // 每次请求的增量序列
	calls   int
	lastMsg []llm.Message
}

func (c *scriptedTTSRChatter) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.lastMsg = messages
	i := c.calls
	c.calls++
	if i >= len(c.turns) {
		return llm.Message{Role: "assistant", Content: ""}, nil
	}
	var b strings.Builder
	for _, d := range c.turns[i] {
		b.WriteString(d)
		if onText != nil {
			onText(d)
		}
	}
	return llm.Message{Role: "assistant", Content: b.String()}, nil
}

func ttsrRules(t *testing.T, action TTSRAction) []CompiledTTSRRule {
	t.Helper()
	rules, err := CompileTTSRRules([]TTSRRule{
		{Name: "stop", Scope: TTSRScopeText, Pattern: `STOP`, Action: action, Reminder: "do not stop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func TestChatWithTTSRNoRulesPassthrough(t *testing.T) {
	chat := &scriptedTTSRChatter{turns: [][]string{{"hello ", "world"}}}
	s := &runState{opts: Options{Chatter: chat}, ttsr: newTTSRWatcherForRun(nil)}
	var got []string
	reply, err := s.chatWithTTSR(context.Background(), func(d string) { got = append(got, d) })
	if err != nil || reply.Content != "hello world" {
		t.Fatalf("reply=%q err=%v", reply.Content, err)
	}
	if strings.Join(got, "") != "hello world" {
		t.Fatalf("deltas=%q", got)
	}
	if chat.calls != 1 {
		t.Fatalf("calls=%d want 1", chat.calls)
	}
}

func TestChatWithTTSRInjectEmitsNoticeOnly(t *testing.T) {
	chat := &scriptedTTSRChatter{turns: [][]string{{"before ", "STOP", " after"}}}
	s := &runState{opts: Options{Chatter: chat}, ttsr: newTTSRWatcherForRun(ttsrRules(t, TTSRInject))}
	var notices []string
	s.emitFn = func(e Event) {
		if e.Kind == EventNotice {
			notices = append(notices, e.Text)
		}
	}
	var got []string
	reply, err := s.chatWithTTSR(context.Background(), func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "before STOP after" || strings.Join(got, "") != "before STOP after" {
		t.Fatalf("content altered: reply=%q deltas=%q", reply.Content, got)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "[ttsr]") {
		t.Fatalf("no ttsr notice: %q", notices)
	}
	if chat.calls != 1 {
		t.Fatalf("calls=%d want 1", chat.calls)
	}
}

func TestChatWithTTSRRetryAbortInjectAndResume(t *testing.T) {
	// 首段包含 STOP 触发 Retry；重发返回"prefix + 后缀"，第一份请求后历史里应多一条
	// 用户提醒，UI 只收到前缀 + 精确后缀（不重复）。
	chat := &scriptedTTSRChatter{turns: [][]string{
		{"partial ", "STOP"},                  // 首段被中止
		{"partial ", "STOP", " resumed tail"}, // 重发：含相同前缀
	}}
	s := &runState{opts: Options{Chatter: chat}, ttsr: newTTSRWatcherForRun(ttsrRules(t, TTSRRetry))}
	var notices []string
	s.emitFn = func(e Event) {
		if e.Kind == EventNotice {
			notices = append(notices, e.Text)
		}
	}
	var got []string
	reply, err := s.chatWithTTSR(context.Background(), func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatalf("resume err=%v", err)
	}
	if !strings.Contains(reply.Content, "resumed tail") {
		t.Fatalf("resume content=%q", reply.Content)
	}
	if chat.calls != 2 {
		t.Fatalf("calls=%d want 2 (one retry)", chat.calls)
	}
	// 历史里应追加一条用户提醒
	found := false
	for _, m := range chat.lastMsg {
		if m.Role == "user" && strings.Contains(m.Content, "do not stop") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no reminder user message in last request: %+v", chat.lastMsg)
	}
	// UI 增量：首段 "partial STOP" + 后缀 " resumed tail"，不应出现两次 "partial STOP"
	joined := strings.Join(got, "")
	if strings.Count(joined, "partial STOP") != 1 {
		t.Fatalf("prefix duplicated in deltas: %q", joined)
	}
	if !strings.Contains(joined, "resumed tail") {
		t.Fatalf("suffix missing: %q", joined)
	}
	if len(notices) == 0 {
		t.Fatalf("no retry notice")
	}
}

func TestChatWithTTSRRetryBudgetOnce(t *testing.T) {
	// 两段都含 STOP；即使重发再次命中，也只重发一次（共 2 次请求）。
	chat := &scriptedTTSRChatter{turns: [][]string{{"STOP"}, {"STOP"}}}
	s := &runState{opts: Options{Chatter: chat}, ttsr: newTTSRWatcherForRun(ttsrRules(t, TTSRRetry))}
	_, err := s.chatWithTTSR(context.Background(), nil)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if chat.calls != 2 {
		t.Fatalf("calls=%d want 2 (budget caps retry)", chat.calls)
	}
}

func TestNewTTSRWatcherForRunEmpty(t *testing.T) {
	if w := newTTSRWatcherForRun(nil); w != nil {
		t.Fatalf("empty rules should give nil watcher, got %+v", w)
	}
}
