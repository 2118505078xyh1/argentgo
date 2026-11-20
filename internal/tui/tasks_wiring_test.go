package tui

import (
	"strings"
	"testing"
	"time"
)

// railText 把一次栏渲染扁平化为可搜索的文本。
func railText(lines []Line) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line.Text())
		b.WriteString("\n")
	}
	return b.String()
}

func TestTasksRailStreamsPartialArgs(t *testing.T) {
	m := New(Config{Locale: "en"})
	fixed := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	m.started = fixed.Add(-3 * time.Second)
	// 运行中的行（字符串中间被截断）和待处理的行（没有闭合
	// 花括号）都必须显示目前恢复的字段，而不是旧的单值。
	m.tasks = []Task{
		{
			Name: "read", Status: TaskRunning, Started: fixed.Add(-500 * time.Millisecond),
			Args: `{"path":"a.go","command":"ls -l`,
		},
		{
			Name: "grep", Status: TaskPending,
			Args: `{"pattern":"foo","path":"src`,
		},
	}

	got := railText(m.tasksLines(80, 10))
	if !strings.Contains(got, "path=a.go, command=ls -l") {
		t.Errorf("running row lost streaming args preview: %q", got)
	}
	if !strings.Contains(got, "pattern=foo, path=src") {
		t.Errorf("pending row lost streaming args preview: %q", got)
	}
}

func TestTasksRailAdaptiveElapsed(t *testing.T) {
	m := New(Config{Locale: "en"})
	fixed := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	m.started = fixed.Add(-3 * time.Second)
	m.tasks = []Task{{
		Name: "read", Status: TaskRunning, Started: fixed.Add(-500 * time.Millisecond),
		Args: `{"path":"a.go"}`,
	}}

	// 低于阈值时，整回合时钟（3s）比
	// 工具自身的 0.5s 更有信息量。
	if got := railText(m.tasksLines(80, 10)); !strings.Contains(got, "3.0s") {
		t.Fatalf("below threshold should show turn elapsed: %q", got)
	}

	// 一旦工具自身的经过时间超过阈值，它就接管（3.1s），
	// 且较大的回合时钟（5.6s）不得再显示。
	advanced := fixed.Add(2600 * time.Millisecond)
	m.now = func() time.Time { return advanced }
	got := railText(m.tasksLines(80, 10))
	if !strings.Contains(got, "3.1s") {
		t.Errorf("above threshold should show tool elapsed: %q", got)
	}
	if strings.Contains(got, "5.6s") {
		t.Errorf("turn elapsed leaked past the threshold: %q", got)
	}
}

func TestTasksRailArgsFallback(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string // "" means no argument label at all
	}{
		{"empty", "", ""},
		{"empty_object", `{}`, ""},
		{"non_object", "not json", "not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Config{Locale: "en"})
			m.tasks = []Task{{Name: "read", Status: TaskRunning, Args: tc.args}}
			got := railText(m.tasksLines(80, 10))
			if tc.want == "" {
				// 没有流式字段也没有经典摘要：该行只携带
				// 名称和时长，绝不带多余的 "()"。
				if strings.Contains(got, "()") {
					t.Errorf("unexpected empty argument label: %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("classic summary fallback missing %q: %q", tc.want, got)
			}
		})
	}
}

func TestTasksRailFinishedRowsKeepClassicSummary(t *testing.T) {
	m := New(Config{Locale: "en"})
	m.tasks = []Task{{
		Name: "read", Status: TaskOK, Args: `{"path":"a.go","command":"ls"}`,
		Duration: 900 * time.Millisecond,
	}}
	got := railText(m.tasksLines(80, 10))
	if !strings.Contains(got, "a.go") {
		t.Errorf("finished row lost its classic summary: %q", got)
	}
	if strings.Contains(got, "command=ls") {
		t.Errorf("finished row should not stream fields: %q", got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("finished row should not animate: %q", got)
	}
}

func TestTasksRailCancelledUsesSharedRenderer(t *testing.T) {
	p, err := paletteFor(ThemeDark)
	if err != nil {
		t.Fatalf("paletteFor: %v", err)
	}
	// 活动栏行现在与 formatPartialArgs/adaptiveElapsed 共享
	// toolPendingLine。验证该共享渲染器的已取消分支
	// 仍显示取消字形/标签且永不动画化。
	cancelled := toolPendingLine("read", `{"path":"a.go","command":"ls -l`,
		time.Second, 0, 80, true, false, true, "zh", p)
	got := cancelled.Text()
	if !strings.Contains(got, toolCanceledMark) || !strings.Contains(got, "已取消") {
		t.Fatalf("cancelled row wrong: %q", got)
	}
	if strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("cancelled row must not animate: %q", got)
	}
}
