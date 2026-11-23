package tui

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// usageGateTokens 是只在头部/状态栏用量与成本行出现的文本片段。
// 门控默认关闭时，它们必须全部缺席，使帧字节与接线前逐字节一致。
var usageGateTokens = []string{"↑", "cached", "CH ", "tok/s", "$", "ctx"}

// usageGateEvent 给出一组确定的用量数据，足以点亮 ↑/↓、cached、CH 等片段。
func usageGateEvent() Event {
	return Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100, CachedTokens: 150}
}

// usageGateModel 冻结 now 与 started，消除时钟漂移对逐字节比较的影响。
// 返回的指针可用于推进时钟，以触发相邻 Usage 事件的速率（tok/s）。
func usageGateModel(theme string) (*Model, *time.Time) {
	now := usageFrameClock
	m := New(Config{Theme: theme})
	m.started = now
	m.now = func() time.Time { return now }
	return m, &now
}

// assertNoGateUsage 断言头部与状态行都没有任何用量/成本片段。
func assertNoGateUsage(t *testing.T, m *Model) {
	t.Helper()
	buf := m.Frame(110, 24)
	status := buf.RowText(buf.Height() - 1)
	for _, tok := range usageGateTokens {
		if strings.Contains(status, tok) {
			t.Errorf("status leaked usage token %q: %q", tok, status)
		}
	}
}

// TestUsageGateDefaultByteExact 守护 opt-in 的核心约束：SetUsageVisible 从未
// 调用（零值 false）时，即使收到 Usage 事件与成本数据，整帧字节仍与未接线前一致。
func TestUsageGateDefaultByteExact(t *testing.T) {
	m, _ := usageGateModel(ThemeDark)
	base := frameBytes(m, 110, 24)

	m.HandleEvent(usageGateEvent())
	m.SetContextInfo(1000, "$0.01")

	if got := frameBytes(m, 110, 24); got != base {
		t.Fatalf("default gate changed the frame bytes\nbefore=%q\nafter=%q", base, got)
	}
	assertNoGateUsage(t, m)
}

// TestUsageGateExplicitFalseMatchesDefault 显式传 false 与从未调用等价。
func TestUsageGateExplicitFalseMatchesDefault(t *testing.T) {
	m, _ := usageGateModel(ThemeLight)
	m.SetUsageVisible(false)
	m.HandleEvent(usageGateEvent())
	m.SetContextInfo(1000, "$0.01")
	assertNoGateUsage(t, m)
}

// TestUsageGateEnabledRendersUsage 开启门控后，用量行（含速率、成本、ctx）
// 必须在头部/状态行出现。速率需要相邻两条 Usage 事件，第二条推进时钟。
func TestUsageGateEnabledRendersUsage(t *testing.T) {
	m, now := usageGateModel(ThemeDark)
	m.SetUsageVisible(true)
	m.HandleEvent(usageGateEvent())
	*now = now.Add(2 * time.Second)
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 190, CachedTokens: 150})
	m.SetContextInfo(1000, "$0.01")

	buf := m.Frame(110, 24)
	status := buf.RowText(buf.Height() - 1)
	for _, tok := range usageGateTokens {
		if !strings.Contains(status, tok) {
			t.Errorf("gate on but token %q is missing: %q", tok, status)
		}
	}
}

// TestUsageGateOffKeepsNonUsageInfo 门关闭时，clock 与主题等非用量信息照常显示。
func TestUsageGateOffKeepsNonUsageInfo(t *testing.T) {
	m, _ := usageGateModel(ThemeLight)
	m.HandleEvent(usageGateEvent())
	m.SetContextInfo(1000, "$0.01")

	buf := m.Frame(110, 24)
	status := buf.RowText(buf.Height() - 1)
	if want := clock(0); !strings.Contains(status, want) {
		t.Errorf("status clock %q missing: %q", want, status)
	}
	if want := text("en", "theme") + " " + ThemeLight; !strings.Contains(status, want) {
		t.Errorf("status theme %q missing: %q", want, status)
	}
}

// TestUsageGateToggleOffRestoresBytes 门控来回切换后，关闭状态必须恢复原始字节。
func TestUsageGateToggleOffRestoresBytes(t *testing.T) {
	m, _ := usageGateModel(ThemeDark)
	base := frameBytes(m, 110, 24)

	m.HandleEvent(usageGateEvent())
	m.SetContextInfo(1000, "$0.01")
	m.SetUsageVisible(true)
	if got := frameBytes(m, 110, 24); got == base {
		t.Fatal("enabling the gate must render the usage rows")
	}

	m.SetUsageVisible(false)
	if got := frameBytes(m, 110, 24); got != base {
		t.Fatalf("disabling the gate must restore the original bytes\nbefore=%q\nafter=%q", base, got)
	}
}

// TestSetUsageVisibleConcurrent 并发切换门控不应 panic（由 Model.mu 保护）。
func TestSetUsageVisibleConcurrent(t *testing.T) {
	m := New(Config{})
	const workers, iterations = 8, 200
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				m.SetUsageVisible((seed+j)%2 == 0)
			}
		}(i)
	}
	wg.Wait()
	_ = m.Frame(80, 24)
}
