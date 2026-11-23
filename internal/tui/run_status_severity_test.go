package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// startRunWithAction 启动一个由管道驱动的 Run，等待 OnReady，
// 并返回模型、写入端和结束信号。调用方负责 cancel ctx 并等待 done。
func startRunWithAction(t *testing.T, undo TurnAction) (*Model, *io.PipeWriter, <-chan error, context.CancelFunc) {
	t.Helper()
	reader, writer := io.Pipe()
	var out bytes.Buffer
	ready := make(chan *Model, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())

	opts := Options{
		Locale: "en",
		IO:     &IO{In: reader, Out: &out},
		Undo:   undo,
		OnReady: func(m *Model) {
			ready <- m
		},
	}
	go func() { done <- Run(ctx, opts) }()

	var model *Model
	select {
	case model = <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("Run never became ready")
	}
	return model, writer, done, cancel
}

// statusStyleAt 返回状态行（最后一行）首格的绘制样式。
// 状态消息的左侧 span 从 x=0 开始绘制，因此首格即消息样式。
func statusStyleAt(m *Model, width, height int) Style {
	for _, span := range m.statusRuleLocked(width).Spans {
		if strings.Contains(span.Text, "undo failed") {
			return span.Style
		}
	}
	return Style{}
}

// TestRunActionFailureUsesErrorSeverity 验证 run.go 动作失败路径
// 把错误以 SeverityError 发布，且状态行以红色 failure 样式渲染。
func TestRunActionFailureUsesErrorSeverity(t *testing.T) {
	model, writer, done, cancel := startRunWithAction(t, func(context.Context, string) error {
		return errors.New("undo failed")
	})
	defer func() {
		cancel()
		writer.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	}()

	// Alt+U 触发 ActionUndo，进而调用上面返回错误的 Undo 钩子。
	if _, err := writer.Write([]byte{0x1b, 'u'}); err != nil {
		t.Fatalf("write alt+u: %v", err)
	}

	waitFor(t, "the error notice", func() bool {
		n, ok := model.notices.Peek()
		return ok && n.Severity == SeverityError && strings.Contains(n.Text, "undo failed")
	})

	n, ok := model.notices.Peek()
	if !ok || n.Severity != SeverityError {
		t.Fatalf("notice = %+v (ok=%v), want SeverityError", n, ok)
	}
	if got := model.Frame(100, 24).RowText(model.layoutLocked(100, 24).usageY); !strings.Contains(got, "undo failed") {
		t.Fatalf("error text missing from status line: %q", got)
	}
	// failure() = Style{Fg: ColorRed, Bold: true}（非 --no-color 时）。
	style := statusStyleAt(model, 100, 24)
	if style.Fg != ColorRed || !style.Bold {
		t.Fatalf("status style = %+v, want red bold (failure)", style)
	}
}

// TestRunActionSuccessStaysInfo 验证动作成功仍以 SeverityInfo 发布，
// 且状态行不是红色（成功/信息不要被染红）。
func TestRunActionSuccessStaysInfo(t *testing.T) {
	model, writer, done, cancel := startRunWithAction(t, func(context.Context, string) error {
		return nil
	})
	defer func() {
		cancel()
		writer.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	}()

	if _, err := writer.Write([]byte{0x1b, 'u'}); err != nil {
		t.Fatalf("write alt+u: %v", err)
	}

	waitFor(t, "the success notice", func() bool {
		n, ok := model.notices.Peek()
		return ok && n.Severity == SeverityInfo && strings.Contains(n.Text, "undo finished")
	})

	n, ok := model.notices.Peek()
	if !ok || n.Severity != SeverityInfo {
		t.Fatalf("notice = %+v (ok=%v), want SeverityInfo", n, ok)
	}
	style := statusStyleAt(model, 100, 24)
	if style.Fg == ColorRed {
		t.Fatalf("success status must not be red: %+v", style)
	}
}

// TestDefaultFrameHasNoErrorStatus 验证没有错误时默认帧不含错误
// 通知，状态行也不是红色：本次改动不应改变无错误时的画面。
func TestDefaultFrameHasNoErrorStatus(t *testing.T) {
	m := New(Config{Locale: "en"})
	if n, ok := m.notices.Peek(); ok {
		t.Fatalf("fresh model has a notice: %+v", n)
	}
	style := statusStyleAt(m, 100, 24)
	if style.Fg == ColorRed {
		t.Fatalf("default status line must not be red: %+v", style)
	}
}
