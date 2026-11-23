package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIdleCtrlCTwoPressConfirmation 锁定空闲 Ctrl+C 的二次确认窗口：
// 第一次只武装，窗口内第二次退出，超窗后重新武装，工作态始终直接取消。
func TestIdleCtrlCTwoPressConfirmation(t *testing.T) {
	base := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	clock := base
	m := New(Config{Locale: "zh"})
	m.now = func() time.Time { return clock }

	if a := m.HandleKey(Key{Kind: KindCtrlC}); a != ActionNone {
		t.Fatalf("first idle ctrl+c = %v, want ActionNone", a)
	}
	if m.Quit() {
		t.Fatal("first ctrl+c must not quit")
	}
	if !strings.Contains(frameText(m, 80, 24), "再按一次 Ctrl+C 退出") {
		t.Fatal("quit confirmation hint not painted")
	}

	// 超过窗口后第二次不退出，只重新武装。
	clock = base.Add(quitConfirmWindow + time.Millisecond)
	if a := m.HandleKey(Key{Kind: KindCtrlC}); a != ActionNone {
		t.Fatalf("ctrl+c after window = %v, want ActionNone", a)
	}
	if m.Quit() {
		t.Fatal("ctrl+c after window must not quit")
	}
	// 重新武装后的窗口内第二次退出。
	clock = clock.Add(time.Second)
	if a := m.HandleKey(Key{Kind: KindCtrlC}); a != ActionQuit {
		t.Fatalf("second ctrl+c within window = %v, want ActionQuit", a)
	}
	if !m.Quit() {
		t.Fatal("quit flag must be set")
	}

	// 工作态直接取消，不走确认。
	work := New(Config{})
	work.working = true
	if a := work.HandleKey(Key{Kind: KindCtrlC}); a != ActionAbort {
		t.Fatalf("working ctrl+c = %v, want ActionAbort", a)
	}
}

// TestEscClosesLayersOneAtATime 验证 Esc 严格按浮层 → 面板 → 补全逐层关闭。
func TestEscClosesLayersOneAtATime(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo([]CommandInfo{{Command: "help", Description: "show help"}})
	m.showTasks, m.showWorkspace, m.showPlan, m.showSessions, m.showCommands = true, true, true, true, true
	typeBytes(m, "/he")
	if !m.completionOverlay || m.completion == nil {
		t.Fatal("typing /he should open the completion overlay")
	}

	steps := []struct {
		name  string
		check func() bool
	}{
		{"浮层", func() bool { return m.completionOverlay }},
		{"命令面板", func() bool { return m.showCommands }},
		{"任务面板", func() bool { return m.showTasks }},
		{"工作区面板", func() bool { return m.showWorkspace }},
		{"计划面板", func() bool { return m.showPlan }},
		{"会话面板", func() bool { return m.showSessions }},
		{"补全", func() bool { return m.completion != nil }},
	}
	for i, step := range steps {
		m.HandleKey(Key{Kind: KindEsc})
		if step.check() {
			t.Fatalf("Esc #%d did not close %s", i+1, step.name)
		}
		// 尚未关闭的层必须仍然打开，证明一次只关一层。
		for j, later := range steps[i+1:] {
			if !later.check() {
				t.Fatalf("Esc #%d closed %s too early", i+1, steps[j+1].name)
			}
		}
	}
}

// TestCtrlLClearRequest 验证非会话栏 Ctrl+L 置清屏请求且只可消费一次，
// 会话栏场景仍聚焦过滤栏而不请求清屏。
func TestCtrlLClearRequest(t *testing.T) {
	m := New(Config{})
	m.HandleKey(Key{Kind: KindCtrlL})
	if !m.consumeClearRequest() {
		t.Fatal("ctrl+l outside session rail must request a clear")
	}
	if m.consumeClearRequest() {
		t.Fatal("clear request must be consumed exactly once")
	}

	m.showSessions = true
	m.HandleKey(Key{Kind: KindCtrlL})
	if !m.sessionFiltering {
		t.Fatal("ctrl+l in session rail must focus the filter")
	}
	if m.consumeClearRequest() {
		t.Fatal("ctrl+l in session rail must not clear the screen")
	}
}

// TestApprovalShowsQueueCountAndHonestGrant 锁定弹窗的队列计数与 a 授权文案。
func TestApprovalShowsQueueCountAndHonestGrant(t *testing.T) {
	for _, tc := range []struct {
		locale, wantQueue, wantGrant string
	}{
		{"zh", "还有 2 个待确认", "[a] 仅此调用不再询问"},
		{"en", "2 more pending", "[a] allow this call only"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			m := New(Config{Locale: tc.locale})
			results := make([]chan bool, 0, 3)
			for i := 0; i < 3; i++ {
				done := make(chan bool, 1)
				results = append(results, done)
				go func(reply chan bool, n int) {
					ok, _ := m.Approve(context.Background(), "bash", fmt.Sprintf(`{"command":"cmd-%d"}`, n))
					reply <- ok
				}(done, i)
			}
			waitFor(t, "three queued approvals", func() bool {
				_, _, ok := m.PendingApproval()
				if !ok {
					return false
				}
				// Approve 在 m.mu 下写 approvalQueue；轮询时必须同锁读，
				// 否则 -race 会抓到测试 goroutine 与审批 goroutine 的数据竞争。
				m.mu.Lock()
				defer m.mu.Unlock()
				return len(m.approvalQueue) == 2
			})
			body := strings.Join(approvalLineTexts(m.approvalLines(80)), "\n")
			if !strings.Contains(body, tc.wantQueue) {
				t.Fatalf("queue count missing: %q\n%s", tc.wantQueue, body)
			}
			if !strings.Contains(body, tc.wantGrant) {
				t.Fatalf("honest grant text missing: %q\n%s", tc.wantGrant, body)
			}
			// 收尾三个弹窗，避免协程泄漏。
			for range results {
				m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
			}
		})
	}
}

// approvalLineTexts 抽取弹窗每行的纯文本，便于断言文案。
func approvalLineTexts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line.Text()
	}
	return out
}

// TestEndTurnErrorStickyActionAndExpand 验证回合错误通知：带 /retry 动作、
// 标记 sticky 免过期、完整详情只在展开时绘制。
func TestEndTurnErrorStickyActionAndExpand(t *testing.T) {
	m := New(Config{Locale: "zh"})
	const secret = "sk-proj-abcdefghijklmnop"
	const tail = "ZZTAILMARK"
	detail := strings.Repeat("x", 260) + tail + " " + secret
	m.EndTurn(fmt.Errorf("%s", detail))

	entries := m.Transcript()
	last := entries[len(entries)-1]
	if !last.sticky {
		t.Fatal("turn error must be marked sticky")
	}
	if last.Detail == "" {
		t.Fatal("turn error must carry expandable detail")
	}
	if !strings.Contains(last.Text, "/retry") {
		t.Fatalf("turn error must suggest the retry action: %q", last.Text)
	}
	if strings.Contains(last.Text, secret) || !strings.Contains(last.Detail, "[redacted]") {
		t.Fatal("secret must be redacted in both summary and detail")
	}
	if strings.Contains(last.Text, tail) {
		t.Fatal("detail tail must not leak into the compact summary")
	}

	// 折叠态不显示详情；点击后展开显示，再点击收起。
	if strings.Contains(frameText(m, 100, 24), tail) {
		t.Fatal("collapsed detail must not be painted")
	}
	m.Frame(100, 24)
	if len(m.errorHits) != 1 {
		t.Fatalf("expected one error hit, got %+v", m.errorHits)
	}
	m.MouseDown(3, m.errorHits[0].top)
	if !m.errorExpandedAt(0) {
		t.Fatal("click did not expand the error detail")
	}
	if !strings.Contains(frameText(m, 100, 24), tail) {
		t.Fatal("expanded detail not painted")
	}
	m.Frame(100, 24)
	m.MouseDown(3, m.errorHits[0].top)
	if m.errorExpandedAt(0) {
		t.Fatal("second click did not collapse the error detail")
	}
}
