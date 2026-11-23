package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoopDetectionEscalatingNudge(t *testing.T) {
	SetLoopDetection(true)
	t.Cleanup(func() { SetLoopDetection(true) })
	if loopNudgeTier(2) != 0 || loopNudgeTier(3) != 1 || loopNudgeTier(5) != 2 || loopNudgeTier(8) != 3 {
		t.Fatal("分级阈值错误")
	}

	s := newRunState(Options{Model: "m", System: "s"}, "go", nil)
	for i := 0; i < 5; i++ {
		s.events = append(s.events, Event{
			Kind: EventToolEnd, ToolCallID: fmt.Sprintf("c%d", i),
			ToolName: "probe", Args: `{"path":"secret-token=abc","b":2}`, Output: "same",
		})
	}
	s.checkLoopDetection()
	var texts []string
	for _, m := range s.messages {
		if m.Role == "user" {
			texts = append(texts, m.Content)
		}
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, loopDetectionGuidance) {
		t.Fatal("缺少基础循环提示")
	}
	if !strings.Contains(joined, "循环证据") || !strings.Contains(joined, "probe") || !strings.Contains(joined, "5 次") {
		t.Fatalf("缺少带证据的升级提示: %s", joined)
	}
	if strings.Contains(joined, "secret-token=abc") {
		t.Fatalf("参数未脱敏: %s", joined)
	}
}

func TestLoopDetectionResetsOnSteer(t *testing.T) {
	SetLoopDetection(true)
	t.Cleanup(func() { SetLoopDetection(true) })
	steer := make(chan string, 2)
	steer <- "换个方法"
	s := newRunState(Options{Model: "m", System: "s", Steer: steer}, "go", nil)
	for i := 0; i < 5; i++ {
		s.events = append(s.events, Event{
			Kind: EventToolEnd, ToolCallID: fmt.Sprintf("c%d", i),
			ToolName: "probe", Args: `{}`, Output: "same",
		})
	}
	before := len(s.events)
	if !s.injectSteering() {
		t.Fatal("steering 未被注入")
	}
	// 水位推进到插话时刻；之后到来的工具事件仍可见，之前的不再参与循环检测。
	if s.loopWatermark != before {
		t.Fatalf("水位 = %d, want %d", s.loopWatermark, before)
	}
	steps := toolStepsFromEvents(s.eventsSnapshot()[s.loopWatermark:])
	if len(steps) != 0 {
		t.Fatalf("插话前步骤未被切断: %d", len(steps))
	}
	// 新情节里只有一步，不构成循环。
	s.events = append(s.events, Event{Kind: EventToolEnd, ToolCallID: "new", ToolName: "probe", Args: `{}`, Output: "same"})
	s.checkLoopDetection()
	for _, m := range s.messages {
		if m.Role == "user" && m.Content == loopDetectionGuidance {
			t.Fatal("插话前的重复被调用后仍误报循环")
		}
	}
}
