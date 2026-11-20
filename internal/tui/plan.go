package tui

import (
	"encoding/json"
	"strconv"
	"strings"
)

// planFileName 是计划面板显示其结果的工具名。
const planToolName = "submit_plan"

// Plan 是在事件流上看到的最后一次 submit_plan 结果。步骤是
// 文档，而非队列：工具从不执行它们。
type Plan struct {
	Status string
	Steps  []string
	Result string
}

// parsePlanResult 读取 submit_plan 返回的人可读输出。当输出被
// 替换为简短错误字符串时，它还回退到该调用的 JSON 参数，
// 使使用奇怪工具包装器提交的计划仍能显示
// 其步骤。
func parsePlanResult(output, args string) Plan {
	var plan Plan
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "plan (status:") {
			plan.Status = between(line, "status: ", ",")
			continue
		}
		if step, ok := numberedStep(line); ok {
			plan.Steps = append(plan.Steps, step)
			continue
		}
		plan.Result = line
	}
	if len(plan.Steps) == 0 {
		plan.Status, plan.Steps = parsePlanArgs(args)
	}
	if plan.Status == "" {
		plan.Status = "draft"
	}
	return plan
}

func parsePlanArgs(args string) (string, []string) {
	var decoded struct {
		Status string   `json:"status"`
		Steps  []string `json:"steps"`
	}
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		return "", nil
	}
	steps := make([]string, 0, len(decoded.Steps))
	for _, step := range decoded.Steps {
		if step = strings.TrimSpace(step); step != "" {
			steps = append(steps, step)
		}
	}
	return strings.TrimSpace(decoded.Status), steps
}

func numberedStep(line string) (string, bool) {
	dot := strings.Index(line, ". ")
	if dot < 1 {
		return "", false
	}
	if _, err := strconv.Atoi(line[:dot]); err != nil {
		return "", false
	}
	step := strings.TrimSpace(line[dot+2:])
	return step, step != ""
}

func between(value, prefix, suffix string) string {
	start := strings.Index(value, prefix)
	if start < 0 {
		return ""
	}
	rest := value[start+len(prefix):]
	if end := strings.Index(rest, suffix); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func (m *Model) capturePlanLocked(event Event) {
	if !strings.EqualFold(event.ToolName, planToolName) {
		return
	}
	plan := parsePlanResult(event.Output, event.Args)
	m.plan = &plan
	m.planScroll = 0
	m.version++
}

// planLines 渲染计划面板。尚无计划时它是一行提示。
func (m *Model) planLines(width, height int) []Line {
	title := NewLine()
	title.Push(text(m.locale, "plan_title"), m.palette.accent())
	out := []Line{title}
	if m.plan == nil {
		hint := NewLine()
		hint.Push(Clip(text(m.locale, "no_plan"), maxInt(width, 1)), m.palette.dim())
		return append(out, hint)
	}
	status := NewLine()
	status.Push("· "+m.plan.Status, m.palette.muted())
	out = append(out, status)

	mark := "□ "
	if m.plan.Status == "done" || m.plan.Status == "completed" {
		mark = "✓ "
	}
	start := clampScroll(m.planScroll, len(m.plan.Steps))
	for i := start; i < len(m.plan.Steps) && len(out) < height-2; i++ {
		line := NewLine()
		line.Push(mark, m.palette.user())
		line.Push(strconv.Itoa(i+1)+". ", m.palette.muted())
		line.Push(Clip(m.plan.Steps[i], maxInt(width-8, 1)), Style{})
		out = append(out, line)
	}
	if m.plan.Result != "" && len(out) < height-1 {
		for i, segment := range Wrap(m.plan.Result, maxInt(width-2, 1)) {
			if len(out) >= height-1 {
				break
			}
			line := NewLine()
			label := "  "
			if i == 0 {
				label = "↳ "
			}
			line.Push(label, m.palette.dim())
			line.Push(segment, m.palette.dim())
			out = append(out, line)
		}
	}
	return out
}
