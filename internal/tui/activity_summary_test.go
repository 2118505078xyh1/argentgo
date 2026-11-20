package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/i18n"
)

func finishedTask(name string) Task {
	return Task{Name: name, Status: TaskOK}
}

func TestActivitySummaryCountsFinishedTasks(t *testing.T) {
	mixed := []Task{
		finishedTask("read"),
		finishedTask("bash"),
		finishedTask("edit"),
		finishedTask("bash"),
		finishedTask("grep"),
		finishedTask("todo"),
		finishedTask("bash"),
		finishedTask("multi_edit"),
	}
	reversed := make([]Task, 0, len(mixed))
	for i := len(mixed) - 1; i >= 0; i-- {
		reversed = append(reversed, mixed[i])
	}

	cases := []struct {
		name  string
		tasks []Task
		want  string
	}{
		{"mixed families in fixed order", mixed, "3 commands · 2 edits · 2 reads · 1 other"},
		{"insertion order does not matter", reversed, "3 commands · 2 edits · 2 reads · 1 other"},
		{"singular labels", []Task{finishedTask("bash"), finishedTask("edit"), finishedTask("read")}, "1 command · 1 edit · 1 read"},
		{"case and whitespace are normalised", []Task{finishedTask("  BaSh ")}, "1 command"},
		{"search and lsp tools count as reads", []Task{finishedTask("lsp_symbols"), finishedTask("web_fetch"), finishedTask("glob")}, "3 reads"},
		{"unknown tools count as other", []Task{finishedTask("brand_new_tool")}, "1 other"},
		{"no tasks", nil, ""},
		{"empty slice", []Task{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activitySummary(tc.tasks); got != tc.want {
				t.Errorf("activitySummary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestActivitySummaryOnlyCountsCompletedTasks(t *testing.T) {
	tasks := []Task{
		{Name: "bash", Status: TaskPending},
		{Name: "edit", Status: TaskRunning, Started: time.Now()},
		{Name: "read", Status: TaskError},
	}
	if got := activitySummary(tasks); got != "" {
		t.Errorf("unfinished and failed tasks produced %q, want empty", got)
	}

	tasks = append(tasks, finishedTask("bash"))
	if got, want := activitySummary(tasks), "1 command"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestActivitySummaryIsStableAcrossDetailChanges(t *testing.T) {
	tasks := []Task{
		finishedTask("bash"),
		{Name: "read", Status: TaskRunning, Args: `{"path":"a.go"}`},
	}
	before := activitySummary(tasks)
	tasks[1].Args = `{"path":"a/much/longer/path/that/keeps/changing.go"}`
	tasks[1].Duration = 5 * time.Second
	if got := activitySummary(tasks); got != before {
		t.Errorf("changing running-task details changed summary: %q -> %q", before, got)
	}

	tasks[1].Status = TaskOK
	if got, want := activitySummary(tasks), "1 command · 1 read"; got != want {
		t.Errorf("summary after completion = %q, want %q", got, want)
	}
}

func TestActivitySummaryAppearsInTasksRail(t *testing.T) {
	m := New(Config{Locale: "en"})
	m.tasks = []Task{
		finishedTask("bash"),
		finishedTask("edit"),
		{Name: "read", Status: TaskRunning},
	}
	lines := m.tasksLines(60, 10)
	if len(lines) == 0 {
		t.Fatal("tasksLines returned no lines")
	}
	var title strings.Builder
	for _, span := range lines[0].Spans {
		title.WriteString(span.Text)
	}
	if got := title.String(); !strings.Contains(got, "1 command · 1 edit") {
		t.Errorf("rail title = %q, want the activity recap", got)
	}
	if got := lines[0].Width(); got > 60 {
		t.Errorf("rail title width = %d, want at most 60", got)
	}
}

func TestActivitySummaryEnglishLabelsMatchCatalog(t *testing.T) {
	cases := []struct {
		n    int
		key  string
		one  string
		many string
	}{
		{1, "activity.command", "command", "commands"},
		{1, "activity.edit", "edit", "edits"},
		{1, "activity.read", "read", "reads"},
		{1, "activity.other", "other", "others"},
	}
	for _, tc := range cases {
		oneKey := tc.key + "_one"
		manyKey := tc.key + "_many"
		if got, want := i18n.T(i18n.EnUS, oneKey, 1), "1 "+tc.one; got != want {
			t.Errorf("%s en = %q, want %q", oneKey, got, want)
		}
		if got, want := i18n.T(i18n.EnUS, manyKey, 3), "3 "+tc.many; got != want {
			t.Errorf("%s en = %q, want %q", manyKey, got, want)
		}
		// activitySummary 发出的中性标记必须保持同步。
		if got, want := countLabel(1, tc.one, tc.many), i18n.T(i18n.EnUS, oneKey, 1); got != want {
			t.Errorf("neutral label %q drifted from catalog %q", got, want)
		}
		if got, want := countLabel(3, tc.one, tc.many), i18n.T(i18n.EnUS, manyKey, 3); got != want {
			t.Errorf("neutral label %q drifted from catalog %q", got, want)
		}
	}
}

func TestActivitySummaryI18nLabelsAreBilingual(t *testing.T) {
	cases := []struct {
		key string
		n   int
		en  string
		zh  string
	}{
		{"activity.command_many", 3, "3 commands", "3 条命令"},
		{"activity.edit_one", 1, "1 edit", "1 次编辑"},
		{"activity.read_one", 1, "1 read", "1 次读取"},
		{"activity.other_many", 2, "2 others", "2 项其他"},
	}
	for _, tc := range cases {
		if got := i18n.T(i18n.EnUS, tc.key, tc.n); got != tc.en {
			t.Errorf("%s en = %q, want %q", tc.key, got, tc.en)
		}
		if got := i18n.T(i18n.ZhCN, tc.key, tc.n); got != tc.zh {
			t.Errorf("%s zh = %q, want %q", tc.key, got, tc.zh)
		}
	}
	if got, want := i18n.T(i18n.ZhCN, "activity.separator"), " · "; got != want {
		t.Errorf("separator zh = %q, want %q", got, want)
	}
}

func TestActivitySummaryCJKWidth(t *testing.T) {
	command := i18n.T(i18n.ZhCN, "activity.command_many", 3)
	edit := i18n.T(i18n.ZhCN, "activity.edit_one", 1)
	whole := command + i18n.T(i18n.ZhCN, "activity.separator") + edit

	if got, want := TextWidth(command), 8; got != want {
		t.Errorf("TextWidth(%q) = %d, want %d", command, got, want)
	}
	if got, want := TextWidth(whole), 19; got != want {
		t.Errorf("TextWidth(%q) = %d, want %d", whole, got, want)
	}
	if got := TextWidth(command); got <= len([]rune(command)) {
		t.Errorf("CJK label width %d should exceed its %d runes", got, len([]rune(command)))
	}
	if got := Clip(whole, TextWidth(command)); got != command {
		t.Errorf("Clip by cell width = %q, want %q", got, command)
	}
}
