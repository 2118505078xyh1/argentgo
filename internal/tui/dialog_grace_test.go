package tui

import (
	"context"
	"testing"
	"time"
)

// setDialogGrace 在单个测试内设置宽限期，并在测试结束时恢复原值，
// 避免各测试之间通过包变量互相干扰。
func setDialogGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := DialogKeyGracePeriod
	DialogKeyGracePeriod = d
	t.Cleanup(func() { DialogKeyGracePeriod = old })
}

// startGraceApproval 通过真实的 Approve 路径弹出对话框，返回审批结果通道。
// 时钟在启动协程前注入，宽限期依赖的可注入时钟由调用方通过 clock 推进。
func startGraceApproval(t *testing.T, m *Model, clock *time.Time) <-chan bool {
	t.Helper()
	m.now = func() time.Time { return *clock }
	done := make(chan bool, 1)
	go func() {
		ok, err := m.Approve(context.Background(), "bash", `{"command":"echo hi"}`)
		if err != nil {
			t.Errorf("Approve: %v", err)
		}
		done <- ok
	}()
	waitFor(t, "the approval modal", func() bool {
		_, _, ok := m.PendingApproval()
		return ok
	})
	return done
}

// assertNoAnswer 断言宽限期内按键没有把审批结果发出去。
func assertNoAnswer(t *testing.T, done <-chan bool) {
	t.Helper()
	select {
	case ok := <-done:
		t.Fatalf("对话框在宽限期内被回答了，答案=%v", ok)
	default:
	}
}

// assertAnswer 在超时前读取审批结果并比对期望值。
func assertAnswer(t *testing.T, done <-chan bool, want bool) {
	t.Helper()
	select {
	case ok := <-done:
		if ok != want {
			t.Fatalf("审批结果=%v，期望=%v", ok, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待审批结果超时")
	}
}

// TestDialogGraceZeroDisables 验证显式把宽限期置 0（紧急回退）时行为逐字节
// 不变：弹窗后立刻按 y 仍然马上批准，不吞键。产品默认值另由
// TestDialogGraceDefaultIs250ms 锁定。
func TestDialogGraceZeroDisables(t *testing.T) {
	setDialogGrace(t, 0)
	m := New(Config{})
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	done := startGraceApproval(t, m, &clock)

	if action := m.HandleKey(Key{Kind: KindRune, Rune: 'y'}); action != ActionNone {
		t.Fatalf("HandleKey 返回 %v，期望 ActionNone", action)
	}
	assertAnswer(t, done, true)
	if _, _, pending := m.PendingApproval(); pending {
		t.Fatal("宽限期关闭时 y 应立即结清对话框")
	}
}

// TestDialogGraceSwallowsKeysWithinWindow 验证宽限期内按键被吞、窗口结束后响应，
// 并检查边界：299ms 仍在窗口内被吞，恰好 300ms 起重新开始响应。
func TestDialogGraceSwallowsKeysWithinWindow(t *testing.T) {
	setDialogGrace(t, 300*time.Millisecond)
	m := New(Config{})
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := base
	done := startGraceApproval(t, m, &clock)

	// 弹窗瞬间按 y：尾随按键应被吞掉，不能批准。
	m.HandleKey(Key{Kind: KindRune, Rune: 'y'})
	assertNoAnswer(t, done)
	if _, _, pending := m.PendingApproval(); !pending {
		t.Fatal("宽限期内 y 不应结清对话框")
	}

	// 299ms 时按 n：仍在窗口内，应被吞掉。
	clock = base.Add(299 * time.Millisecond)
	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	assertNoAnswer(t, done)
	if _, _, pending := m.PendingApproval(); !pending {
		t.Fatal("299ms 仍在宽限期内，n 不应结清对话框")
	}

	// 恰好 300ms 时按 y：窗口结束，恢复正常审批。
	clock = base.Add(300 * time.Millisecond)
	m.HandleKey(Key{Kind: KindRune, Rune: 'y'})
	assertAnswer(t, done, true)
}

// TestDialogGraceCancelKeySwallowedThenDenies 验证取消键（Esc / Ctrl+C）
// 同样受宽限期保护：窗口内不能取消，窗口结束后正常拒绝。
func TestDialogGraceCancelKeySwallowedThenDenies(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  Key
	}{
		{"esc", Key{Kind: KindEsc}},
		{"ctrl_c", Key{Kind: KindCtrlC}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setDialogGrace(t, 300*time.Millisecond)
			m := New(Config{})
			base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			clock := base
			done := startGraceApproval(t, m, &clock)

			m.HandleKey(tc.key)
			assertNoAnswer(t, done)
			if _, _, pending := m.PendingApproval(); !pending {
				t.Fatal("宽限期内取消键不应结清对话框")
			}

			clock = base.Add(301 * time.Millisecond)
			m.HandleKey(tc.key)
			assertAnswer(t, done, false)
		})
	}
}

// TestDialogGraceCJKTrailingKeys 验证中文输入法场景下，弹窗瞬间连打的
// CJK 字符与 y 都被吞掉；窗口结束后 CJK 字符依旧不误判，n 才真正拒绝。
func TestDialogGraceCJKTrailingKeys(t *testing.T) {
	setDialogGrace(t, 300*time.Millisecond)
	m := New(Config{Locale: "zh"})
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := base
	done := startGraceApproval(t, m, &clock)

	m.HandleKey(Key{Kind: KindRune, Rune: '中'})
	m.HandleKey(Key{Kind: KindRune, Rune: '文'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'y'})
	assertNoAnswer(t, done)
	if _, _, pending := m.PendingApproval(); !pending {
		t.Fatal("宽限期内 CJK 尾随按键不应结清对话框")
	}

	clock = base.Add(301 * time.Millisecond)
	// 窗口结束后 CJK 字符既不是批准键也不是拒绝键，对话框保持原样。
	m.HandleKey(Key{Kind: KindRune, Rune: '中'})
	if _, _, pending := m.PendingApproval(); !pending {
		t.Fatal("CJK 字符不应被当作审批回答")
	}
	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	assertAnswer(t, done, false)
}

// TestDialogGraceRestampOnPromotion 验证排队请求被提升为当前对话框时
// 重新打戳，宽限期从新弹窗出现的时刻算起，而不是请求创建的时刻。
func TestDialogGraceRestampOnPromotion(t *testing.T) {
	setDialogGrace(t, 300*time.Millisecond)
	m := New(Config{})
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := base
	first := startGraceApproval(t, m, &clock)

	// 第二个请求进入队列，尚未显示。
	secondDone := make(chan bool, 1)
	go func() {
		ok, err := m.Approve(context.Background(), "bash", `{"command":"echo second"}`)
		if err != nil {
			t.Errorf("Approve: %v", err)
		}
		secondDone <- ok
	}()
	waitFor(t, "the queued approval", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.approvalQueue) == 1
	})

	// 在 t0+500ms 批准第一个请求，第二个请求此刻才被提升显示。
	clock = base.Add(500 * time.Millisecond)
	m.HandleKey(Key{Kind: KindRune, Rune: 'y'})
	assertAnswer(t, first, true)

	m.mu.Lock()
	promotedAt := m.pending.openedAt
	m.mu.Unlock()
	if !promotedAt.Equal(clock) {
		t.Fatalf("被提升对话框的 openedAt=%v，期望=%v", promotedAt, clock)
	}

	// 刚提升就按 y：仍处于新一轮宽限期，应被吞掉。
	m.HandleKey(Key{Kind: KindRune, Rune: 'y'})
	assertNoAnswer(t, secondDone)
	if _, _, pending := m.PendingApproval(); !pending {
		t.Fatal("提升后的宽限期内 y 不应结清对话框")
	}

	clock = base.Add(800 * time.Millisecond)
	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	assertAnswer(t, secondDone, false)
}
