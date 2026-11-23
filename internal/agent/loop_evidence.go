package agent

import (
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 循环提示的三级阈值。DetectLoop 的判定是 count > threshold，
// 因此传入 tier1-1 即可在 count 达到 tier1 时首次触发。
const (
	loopNudgeTier1 = 3
	loopNudgeTier2 = 5
	loopNudgeTier3 = 8
)

// loopNudgeTier 返回次数对应的等级（0 表示尚未达到提示阈值）。
func loopNudgeTier(count int) int {
	switch {
	case count >= loopNudgeTier3:
		return 3
	case count >= loopNudgeTier2:
		return 2
	case count >= loopNudgeTier1:
		return 1
	default:
		return 0
	}
}

// loopEvidenceMessage 生成带证据的升级提示：工具名、重复次数与
// 脱敏且截断的规范化参数，让模型知道具体在哪个调用上打转。
func loopEvidenceMessage(steps []ToolStep, sig string, count int) string {
	name := "未知工具"
	args := ""
	for _, step := range steps {
		if stepSignature(step) == sig {
			name = step.Name
			args = security.RedactToolOutput(step.Input)
			break
		}
	}
	args = truncateRunes(strings.ReplaceAll(args, "\n", " "), 120)
	return fmt.Sprintf("循环证据：工具 %q 已连续重复 %d 次，最近参数为 %s；请改用其他工具或显著不同的参数。", name, count, args)
}

// checkLoopDetection 在每个工具批次之后运行。
// 默认仍受既有 LoopDetection opt-in 门控（不新增开关）；开启后：
//   - 只看用户上次插话之后的工具步（watermark），纠正前的重复不再误报；
//   - 按 3/5/8 分级，首次命中注入基础提示，>=5 再追加带证据的提示；
//   - 提示按签名去重，同一情节只提示一次。
func (s *runState) checkLoopDetection() {
	if !LoopDetectionEnabled() {
		return
	}
	events := s.eventsSnapshot()
	if s.loopWatermark > 0 && s.loopWatermark <= len(events) {
		events = events[s.loopWatermark:]
	}
	steps := toolStepsFromEvents(events)
	if len(steps) == 0 {
		return
	}
	sig, count, detected := DetectLoop(steps, 0, loopNudgeTier1-1)
	if !detected {
		// 没有循环意味着上一段情节已结束；清空去重状态，使下一段能重新提示。
		s.loopGuidance.Reset()
		return
	}
	if !s.loopGuidance.ShouldInject(sig) {
		return
	}
	extras := []string{loopDetectionGuidance}
	if loopNudgeTier(count) >= 2 {
		extras = append(extras, loopEvidenceMessage(steps, sig, count))
	}
	s.injectSteeringWithNudge(extras...)
}
