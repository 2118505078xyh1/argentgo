package agent

import (
	"strings"
	"testing"
)

type savedProgress struct {
	turn      int
	text      string
	toolCalls []string
}

func TestProgressCaptureThrottlesAndIsRuneSafe(t *testing.T) {
	var saved []savedProgress
	sink := ProgressSinkFunc(func(turn int, text string, calls []string) {
		saved = append(saved, savedProgress{turn: turn, text: text, toolCalls: append([]string(nil), calls...)})
	})
	c := newProgressCapture(sink, 3)

	// 不到阈值不落盘。
	c.Add("hello")
	if len(saved) != 0 {
		t.Fatalf("未到阈值不应落盘: %v", saved)
	}
	// 跨过阈值后落盘一次，文本是累积值。
	c.Add(strings.Repeat("x", progressSaveEveryBytes))
	if len(saved) != 1 || saved[0].turn != 3 || !strings.HasPrefix(saved[0].text, "hello") {
		t.Fatalf("阈值落盘异常: %+v", saved)
	}
	// 工具调用也会触发一次落盘，并带上前次累积的文本。
	c.AddToolCall("bash")
	if len(saved) != 2 || len(saved[1].toolCalls) != 1 || saved[1].toolCalls[0] != "bash" {
		t.Fatalf("工具调用落盘异常: %+v", saved)
	}
}

func TestProgressCaptureRuneSafeAndBounded(t *testing.T) {
	var last string
	sink := ProgressSinkFunc(func(_ int, text string, _ []string) { last = text })
	c := newProgressCapture(sink, 1)
	// 单次超长增量：按 rune 安全截断到上限，CJK 不被切半。
	c.Add(strings.Repeat("界", progressByteLimit))
	if len(last) > progressByteLimit {
		t.Fatalf("文本超过上限: %d", len(last))
	}
	// 上限内应恰好是整数个 3 字节 CJK rune。
	if len(last)%3 != 0 {
		t.Fatalf("CJK 被切半: len=%d", len(last))
	}
}

func TestProgressCaptureNilSinkNoop(t *testing.T) {
	c := newProgressCapture(nil, 1)
	c.Add(strings.Repeat("x", 2*progressSaveEveryBytes))
	c.AddToolCall("edit")
	c.SetTurn(9)
	// 无 panic 即为通过（nil sink 全程空操作）。
}
