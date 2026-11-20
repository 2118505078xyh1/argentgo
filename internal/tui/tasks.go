package tui

import (
	"strconv"
	"strings"
	"time"
)

// TaskStatus 是任务栏中某次工具调用的状态。
type TaskStatus int

// 工具调用状态，从等待审批到已完成结果。
const (
	TaskPending TaskStatus = iota
	TaskRunning
	TaskOK
	TaskError
)

// maxTasks 限制历史，使长会话不会让侧边栏无限增长。
const maxTasks = 100

// 状态标记是单个单元格：对运行中的调用使用点状圆，避免
// 每帧 spinner 重绘使侧边栏闪烁。
const (
	taskPendingMark = "·"
	taskRunningMark = "◌"
	taskOKMark      = "✓"
	taskErrorMark   = "✗"
)

// Task 是任务面板的一行。
type Task struct {
	ID       string
	Name     string
	Args     string
	Status   TaskStatus
	Started  time.Time
	Duration time.Duration
}

func taskMark(status TaskStatus) string {
	switch status {
	case TaskPending:
		return taskPendingMark
	case TaskRunning:
		return taskRunningMark
	case TaskError:
		return taskErrorMark
	default:
		return taskOKMark
	}
}

func (m *Model) taskDuration(task Task) string {
	if task.Status == TaskRunning && !task.Started.IsZero() {
		return formatDuration(m.now().Sub(task.Started))
	}
	return formatDuration(task.Duration)
}

// taskArgsLabel 为侧边栏渲染任务的参数。调用仍活动时，
// 其 JSON 可能在字段中间被截断，因此 formatPartialArgs 显示
// 到目前为止恢复的每个字段。当结果为空 —— 没有参数、
// 非对象或无法解析的文本 —— 时，经典的单值摘要
// 保持该行有信息量，而不是留下空白标签。
func taskArgsLabel(args string) string {
	if label := formatPartialArgs(args); label != "" {
		return label
	}
	return summarizeArgs(args)
}

// taskLiveElapsed 选择运行中或待处理栏行显示的经过时间。
// 它镜像 toolPendingLine 的自适应时钟：显示整回合时间，
// 直到工具自身的经过时间超过 toolElapsedThreshold，然后工具时间
// 接管，使单个缓慢调用凸显。待处理任务尚未开始，因此
// 显示回合时钟；缺失/零时钟降级为 0ms。
func (m *Model) taskLiveElapsed(task Task) time.Duration {
	now := m.now()
	turn := time.Duration(0)
	if !m.started.IsZero() && now.After(m.started) {
		turn = now.Sub(m.started)
	}
	tool := time.Duration(0)
	if !task.Started.IsZero() && now.After(task.Started) {
		tool = now.Sub(task.Started)
	}
	return adaptiveElapsed(tool, turn, toolElapsedThreshold)
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0ms"
	}
	if d < time.Second {
		ms := d.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		return strconv.FormatInt(ms, 10) + "ms"
	}
	if d < time.Minute {
		tenths := d.Milliseconds() / 100
		return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10) + "s"
	}
	return strconv.Itoa(int(d.Minutes())) + "m"
}

// activityFamily 把工具名归入一个粗粒度类别以用于回顾。
// 未识别的工具归入 "other"，因此注册新工具永不
// 重排已有的类别。
func activityFamily(name string) string {
	tool := strings.ToLower(strings.TrimSpace(name))
	switch {
	case tool == "bash" || tool == "powershell" || tool == "shell" || tool == "exec":
		return "command"
	case tool == "edit" || tool == "multi_edit" || tool == "write" ||
		tool == "patch" || tool == "apply_patch" || tool == "lsp_rename":
		return "edit"
	case tool == "read" || tool == "ls" || tool == "grep" || tool == "find" ||
		tool == "glob" || tool == "web_fetch" || tool == "web_search" ||
		tool == "repo_map" || tool == "git_status" || tool == "current_time" ||
		tool == "memory_recall" || strings.HasPrefix(tool, "lsp_"):
		return "read"
	default:
		return "other"
	}
}

// countLabel 渲染 "1 edit" / "3 edits"。这些英文标记是
// i18n activity.* 标签的中性形式；把它们保留在这里使纯计数器
// 无需区域设置，而目录承载本地化的变体。
func countLabel(n int, singular, plural string) string {
	word := plural
	if n == 1 {
		word = singular
	}
	return strconv.Itoa(n) + " " + word
}

// activitySummary 返回已完成工具调用的稳定单行回顾，
// 例如 "3 commands · 1 edit · 2 reads"。只有 TaskOK 行计入：待处理和
// 运行中的调用仍在变化，失败的调用不是已完成的工作，
// 因此流式细节绝不会使该行闪烁或重排。类别
// 按固定顺序输出，与任务插入顺序无关。为空时
// 回顾为 ""。
func activitySummary(tasks []Task) string {
	var commands, edits, reads, other int
	for _, task := range tasks {
		if task.Status != TaskOK {
			continue
		}
		switch activityFamily(task.Name) {
		case "command":
			commands++
		case "edit":
			edits++
		case "read":
			reads++
		default:
			other++
		}
	}
	parts := make([]string, 0, 4)
	if commands > 0 {
		parts = append(parts, countLabel(commands, "command", "commands"))
	}
	if edits > 0 {
		parts = append(parts, countLabel(edits, "edit", "edits"))
	}
	if reads > 0 {
		parts = append(parts, countLabel(reads, "read", "reads"))
	}
	if other > 0 {
		parts = append(parts, countLabel(other, "other", "others"))
	}
	return strings.Join(parts, " · ")
}

// tasksLines 以最新优先渲染任务栏。调用方把结果裁剪
// 到分配给该面板的行。
func (m *Model) tasksLines(width, height int) []Line {
	title := NewLine()
	title.Push(text(m.locale, "tasks_title"), m.palette.accent())
	pending := 0
	for _, task := range m.tasks {
		if task.Status == TaskPending || task.Status == TaskRunning {
			pending++
		}
	}
	if pending > 0 {
		title.Push(" "+strconv.Itoa(pending), m.palette.muted())
	}
	if summary := activitySummary(m.tasks); summary != "" {
		if room := maxInt(width-title.Width(), 0); room > 0 {
			title.Push(Clip(" · "+summary, room), m.palette.muted())
		}
	}
	out := []Line{title, plainPanelRule(width)}

	start := clampScroll(m.tasksScroll, len(m.tasks))
	for i := len(m.tasks) - 1 - start; i >= 0 && len(out) < height; i-- {
		task := m.tasks[i]
		line := NewLine()
		style := m.palette.dim()
		switch task.Status {
		case TaskOK:
			style = m.palette.accent()
		case TaskError:
			style = m.palette.warning()
		case TaskRunning:
			style = m.palette.user()
		}
		line.Push(taskMark(task.Status)+" ", style)
		line.Push(Clip(task.Name, maxInt(width-10, 1)), Style{Bold: task.Status == TaskRunning})
		// 活动行流式输出其参数并使用自适应时钟，与
		// toolPendingLine 用于记录行的原语相同。已完成的行
		// 保持稳定摘要和最终时长，因此永不闪烁。
		label := ""
		duration := ""
		switch task.Status {
		case TaskPending, TaskRunning:
			label = taskArgsLabel(task.Args)
			duration = formatDuration(m.taskLiveElapsed(task))
		default:
			label = summarizeArgs(task.Args)
			duration = m.taskDuration(task)
		}
		if label != "" {
			line.Push(" "+Clip(label, maxInt(width/2, 4)), m.palette.muted())
		}
		pad := width - TextWidth(taskMark(task.Status)+" "+task.Name) - TextWidth(label) - TextWidth(duration) - 4
		if pad > 0 {
			line.Push(strings.Repeat(" ", pad), Style{})
		}
		line.Push(duration, m.palette.muted())
		out = append(out, line)
	}
	if len(out) == 2 && height > 2 {
		hint := NewLine()
		hint.Push(Clip(text(m.locale, "no_tasks"), width), m.palette.dim())
		out = append(out, hint)
	}
	return out
}
