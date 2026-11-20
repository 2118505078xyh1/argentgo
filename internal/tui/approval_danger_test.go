package tui

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// startDangerApproval 通过真实的 ApproveDanger 路径唤起弹窗，
// 并返回调用方作答的通道。空的 danger 是
// 所有现有调用方使用的普通请求形态。
func startDangerApproval(t *testing.T, m *Model, danger string) <-chan bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		ok, err := m.ApproveDanger(context.Background(), "bash", `{"command":"sudo rm -rf /"}`, danger)
		if err != nil {
			t.Errorf("ApproveDanger: %v", err)
		}
		done <- ok
	}()
	waitFor(t, "the danger approval modal", func() bool {
		_, _, ok := m.PendingApproval()
		return ok
	})
	return done
}

// TestDangerRequestDefaultsToDeny 固定了 P1 修复：被标记的命令把 Deny
// 标记为安全默认值，而含义模糊的确认键（Enter）会拒绝而不是
// 批准它。
func TestDangerRequestDefaultsToDeny(t *testing.T) {
	m := New(Config{})
	done := startDangerApproval(t, m, "destructive shell command: sudo rm -rf /")

	if !m.pending.defaultsToDeny() {
		t.Fatal("a danger request must default to Deny")
	}
	if action := m.HandleKey(Key{Kind: KindEnter}); action != ActionNone {
		t.Fatalf("Enter returned %v, want ActionNone", action)
	}
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Enter must not approve a dangerous command")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enter did not resolve the danger modal")
	}
}

// TestNonDangerRequestKeepsHistoricalModalAndAllowDefault 证明该附加
// 更改对所有现有调用方无影响：defaultsToDeny 为 false，没有 danger
// 文本出现在框中，提示行逐字节保持历史原样，
// 且 Enter 仍让弹窗保持未回答。
func TestNonDangerRequestKeepsHistoricalModalAndAllowDefault(t *testing.T) {
	m := New(Config{})
	done := startDangerApproval(t, m, "")

	if m.pending.defaultsToDeny() {
		t.Fatal("an ordinary request must not default to Deny")
	}

	const inner = 52 // width 60 => boxWidth 56 => inner 52
	lines := m.approvalLines(60)
	if len(lines) != 7 {
		t.Fatalf("ordinary modal has %d lines, want 7:\n%s", len(lines), modalText(lines))
	}
	for i, line := range lines {
		for _, marker := range []string{"danger", "default:", "⚠", "危险", "默认"} {
			if strings.Contains(line.Text(), marker) {
				t.Fatalf("ordinary modal line %d contains danger marker %q:\n%s", i, marker, modalText(lines))
			}
		}
	}
	wantBlank := "│ " + strings.Repeat(" ", inner) + " │"
	if got := lines[3].Text(); got != wantBlank {
		t.Fatalf("blank row = %q, want %q", got, wantBlank)
	}
	wantHints := []string{
		"│ " + Pad("[y] allow   [n] deny", inner) + " │",
		"│ " + Pad("[a] allow for session", inner) + " │",
	}
	for i, want := range wantHints {
		if got := lines[4+i].Text(); got != want {
			t.Fatalf("hint row %d = %q, want %q", i, got, want)
		}
	}

	m.HandleKey(Key{Kind: KindEnter})
	if _, _, pending := m.PendingApproval(); !pending {
		t.Fatal("Enter must not resolve an ordinary modal")
	}
	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	select {
	case ok := <-done:
		if ok {
			t.Fatal("n must deny")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("n did not resolve the modal")
	}
}

// TestDangerReasonIsShown 验证原因被渲染在框内、
// 位于其自己的样式行上，且默认拒绝的提示是可见的。
func TestDangerReasonIsShown(t *testing.T) {
	m := New(Config{})
	done := startDangerApproval(t, m, "mutating shell command: sudo rm -rf /")

	lines := m.approvalLines(60)
	body := modalText(lines)
	for _, want := range []string{"danger", "mutating shell command: sudo rm -rf /", "default: deny"} {
		if !strings.Contains(body, want) {
			t.Fatalf("danger modal is missing %q:\n%s", want, body)
		}
	}
	if len(lines) <= 7 {
		t.Fatalf("danger modal must add rows, got %d lines:\n%s", len(lines), body)
	}

	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	select {
	case ok := <-done:
		if ok {
			t.Fatal("n must deny a danger request too")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("n did not resolve the modal")
	}
}

// TestCJKDangerReasonRendersIntact 守护 CJK 审计：中文原因必须
// 在 Wrap/Pad 后保持有效 UTF-8 和完整字形，并与 zh-CN 的
// "default: deny" 提示同时呈现。
func TestCJKDangerReasonRendersIntact(t *testing.T) {
	m := New(Config{Locale: "zh"})
	done := startDangerApproval(t, m, "sudo rm -rf / 会递归删除根目录")

	lines := m.approvalLines(60)
	body := modalText(lines)
	for _, want := range []string{"危险", "sudo rm -rf /", "会递归删除根目录", "默认：拒绝（回车即拒绝）"} {
		if !strings.Contains(body, want) {
			t.Fatalf("zh danger modal is missing %q:\n%s", want, body)
		}
	}
	for i, line := range lines {
		if !utf8.ValidString(line.Text()) {
			t.Fatalf("zh danger modal line %d is invalid UTF-8: %q", i, line.Text())
		}
		if width := line.Width(); width > 56 {
			t.Fatalf("zh danger modal line %d is %d cells, want <= 56: %q", i, width, line.Text())
		}
	}

	m.HandleKey(Key{Kind: KindEnter})
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Enter must not approve a dangerous command in zh-CN either")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enter did not resolve the zh danger modal")
	}
}

func modalText(lines []Line) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, strings.TrimRight(line.Text(), " "))
	}
	return strings.Join(out, "\n")
}
