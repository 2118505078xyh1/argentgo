package tui

import (
	"strconv"
	"strings"
	"time"
)

// TaskStatus is the state of one tool call in the tasks rail.
type TaskStatus int

// Tool-call states, from waiting for approval to a finished result.
const (
	TaskPending TaskStatus = iota
	TaskRunning
	TaskOK
	TaskError
)

// maxTasks bounds the history so a long session cannot grow the rail forever.
const maxTasks = 100

// Status marks are single cells: a dotted circle for running avoids the
// per-frame spinner repaint that would make the side rail shimmer.
const (
	taskPendingMark = "·"
	taskRunningMark = "◌"
	taskOKMark      = "✓"
	taskErrorMark   = "✗"
)

// Task is one row of the tasks panel.
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

// activityFamily groups a tool name into one coarse bucket for the recap.
// Unrecognised tools fall into "other" so registering a new tool never
// reshuffles the buckets that already exist.
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

// countLabel renders "1 edit" / "3 edits". The English tokens are the neutral
// form of the i18n activity.* labels; keeping them here lets the pure counter
// stay locale-free while the catalog carries the localised variants.
func countLabel(n int, singular, plural string) string {
	word := plural
	if n == 1 {
		word = singular
	}
	return strconv.Itoa(n) + " " + word
}

// activitySummary returns a stable one-line recap of finished tool calls,
// e.g. "3 commands · 1 edit · 2 reads". Only TaskOK rows count: pending and
// running calls are still changing and failed calls are not work completed,
// so streaming details never make the line flicker or reshuffle. Categories
// are emitted in a fixed order, independent of task insertion order. An empty
// recap is "".
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

// tasksLines renders the tasks rail newest first. The caller clips the result
// to the rows assigned to this panel.
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
		label := summarizeArgs(task.Args)
		if label != "" {
			line.Push(" "+Clip(label, maxInt(width/2, 4)), m.palette.muted())
		}
		duration := m.taskDuration(task)
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
