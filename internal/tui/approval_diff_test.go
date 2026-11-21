package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// approvalDiffFrameClock 冻结帧时钟，使同一模型在不同调用间产生逐字节一致的帧。
var approvalDiffFrameClock = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

func approvalDiffModel(locale string) *Model {
	m := New(Config{Locale: locale})
	m.started = approvalDiffFrameClock
	m.now = func() time.Time { return approvalDiffFrameClock }
	return m
}

func TestBuildApprovalDiffLineCounts(t *testing.T) {
	tests := []struct {
		name           string
		before, after  map[string]string
		files, add, rm int
	}{
		{
			name:   "modified file counts only edited lines",
			before: map[string]string{"foo.go": "a\nb\nc\n"},
			after:  map[string]string{"foo.go": "a\nx\nc\nd\n"},
			files:  1, add: 2, rm: 1,
		},
		{
			name:   "added file",
			before: map[string]string{},
			after:  map[string]string{"foo.go": "a\nb\n"},
			files:  1, add: 2, rm: 0,
		},
		{
			name:   "deleted file",
			before: map[string]string{"foo.go": "a\nb\n"},
			after:  map[string]string{},
			files:  1, add: 0, rm: 2,
		},
		{
			name:   "multiple files aggregate",
			before: map[string]string{"a.go": "x\n", "b.go": "p\nq\n"},
			after:  map[string]string{"a.go": "x\ny\n", "b.go": "p\n"},
			files:  2, add: 1, rm: 1,
		},
		{
			name:   "identical snapshots produce empty diff",
			before: map[string]string{"foo.go": "a\nb\n"},
			after:  map[string]string{"foo.go": "a\nb\n"},
			files:  0, add: 0, rm: 0,
		},
		{
			name:   "CRLF change is not counted as line edits",
			before: map[string]string{"foo.go": "a\r\nb\r\n"},
			after:  map[string]string{"foo.go": "a\nb\n"},
			files:  1, add: 0, rm: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildApprovalDiff(tc.before, tc.after)
			if got.FilesChanged != tc.files || got.LinesAdded != tc.add || got.LinesRemoved != tc.rm {
				t.Fatalf("BuildApprovalDiff = files %d, +%d/-%d; want files %d, +%d/-%d (%+v)",
					got.FilesChanged, got.LinesAdded, got.LinesRemoved, tc.files, tc.add, tc.rm, got)
			}
		})
	}
}

func TestBuildApprovalDiffCJK(t *testing.T) {
	before := map[string]string{"配置/模块.go": "甲\n乙\n"}
	after := map[string]string{"配置/模块.go": "甲\n丙\n丁\n"}
	got := BuildApprovalDiff(before, after)

	if !utf8.ValidString(got.Preview) {
		t.Fatalf("preview is not valid UTF-8: %q", got.Preview)
	}
	if !strings.Contains(got.Preview, "配置/模块.go") {
		t.Fatalf("CJK path missing from preview: %q", got.Preview)
	}
	if got.FilesChanged != 1 || got.LinesAdded != 2 || got.LinesRemoved != 1 {
		t.Fatalf("CJK counts = files %d, +%d/-%d; want 1, +2/-1", got.FilesChanged, got.LinesAdded, got.LinesRemoved)
	}

	// 超长 CJK 路径在字素边界被截断，不能产生非法 UTF-8 或半个字形。
	longPath := strings.Repeat("字", approvalDiffMaxPathCells+10) + ".go"
	truncated := BuildApprovalDiff(nil, map[string]string{longPath: "x\n"})
	if !utf8.ValidString(truncated.Preview) {
		t.Fatalf("truncated CJK preview is not valid UTF-8: %q", truncated.Preview)
	}
	if width := TextWidth(strings.Split(truncated.Preview, " in ")[1]); width > approvalDiffMaxPathCells {
		t.Fatalf("truncated path width = %d, want <= %d", width, approvalDiffMaxPathCells)
	}
}

func TestBuildApprovalDiffPreviewBounded(t *testing.T) {
	before := map[string]string{}
	after := make(map[string]string)
	for i := 0; i < 9; i++ {
		after["file"+string(rune('a'+i))+".go"] = "line\n"
	}
	got := BuildApprovalDiff(before, after)
	lines := strings.Split(got.Preview, "\n")
	if len(lines) != approvalDiffMaxPreviewLines {
		t.Fatalf("preview has %d lines, want %d: %q", len(lines), approvalDiffMaxPreviewLines, got.Preview)
	}
	if got.FilesChanged != 9 {
		t.Fatalf("FilesChanged = %d, want 9", got.FilesChanged)
	}
	if !strings.Contains(got.Preview, "还有 ") {
		t.Fatalf("overflow marker missing from bounded preview: %q", got.Preview)
	}
	// 恰好等于上限时不出现溢出提示。
	exact := BuildApprovalDiff(nil, map[string]string{
		"a": "x\n", "b": "x\n", "c": "x\n", "d": "x\n",
	})
	if n := len(strings.Split(exact.Preview, "\n")); n != approvalDiffMaxPreviewLines {
		t.Fatalf("exact-limit preview has %d lines, want %d", n, approvalDiffMaxPreviewLines)
	}
	if strings.Contains(exact.Preview, "还有 ") {
		t.Fatalf("exact-limit preview must not contain overflow marker: %q", exact.Preview)
	}
}

func TestBuildApprovalDiffDoesNotMutateInputs(t *testing.T) {
	before := map[string]string{"foo.go": "a\nb\n", "gone.go": "z\n"}
	after := map[string]string{"foo.go": "a\nx\n", "new.go": "q\n"}
	beforeCopy := make(map[string]string, len(before))
	for k, v := range before {
		beforeCopy[k] = v
	}
	afterCopy := make(map[string]string, len(after))
	for k, v := range after {
		afterCopy[k] = v
	}

	_ = BuildApprovalDiff(before, after)

	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("before map was modified: got %v, want %v", before, beforeCopy)
	}
	if !reflect.DeepEqual(after, afterCopy) {
		t.Fatalf("after map was modified: got %v, want %v", after, afterCopy)
	}
}

func TestBuildApprovalDiffStable(t *testing.T) {
	// 以两种插入顺序构造内容相同的 map：输出必须逐字节一致。
	first := map[string]string{"b.go": "1\n2\n", "a.go": "甲\n", "c.go": "x\n"}
	second := make(map[string]string)
	second["c.go"] = "x\n"
	second["a.go"] = "甲\n"
	second["b.go"] = "1\n2\n"

	want := BuildApprovalDiff(first, map[string]string{
		"b.go": "1\n3\n", "a.go": "甲\n乙\n", "c.go": "x\n",
	})
	for i := 0; i < 20; i++ {
		got := BuildApprovalDiff(second, map[string]string{
			"c.go": "x\n", "a.go": "甲\n乙\n", "b.go": "1\n3\n",
		})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unstable BuildApprovalDiff:\ngot  %+v\nwant %+v", got, want)
		}
	}
}

func TestRenderApprovalDiff(t *testing.T) {
	if got := RenderApprovalDiff(ApprovalDiff{}); got != "" {
		t.Fatalf("empty diff summary = %q, want empty string", got)
	}
	d := ApprovalDiff{LinesAdded: 3, LinesRemoved: 1, FilesChanged: 2, Preview: "+3/-1 in foo.go"}
	want := "diff：2 个文件，+3/-1 行"
	if got := RenderApprovalDiff(d); got != want {
		t.Fatalf("RenderApprovalDiff = %q, want %q", got, want)
	}
	if got := RenderApprovalDiff(d); got != RenderApprovalDiff(d) {
		t.Fatal("RenderApprovalDiff is not stable across calls")
	}
}

func TestApprovalDiffDefaultOffFrameBytesUnchanged(t *testing.T) {
	prev := tuiApprovalDiff.Swap(false)
	defer tuiApprovalDiff.Store(prev)

	before := map[string]string{"foo.go": "a\nb\nc\n"}
	after := map[string]string{"foo.go": "a\nx\nc\nd\n"}

	plain := approvalDiffModel("en")
	plain.pending = &approvalRequest{name: "edit", args: `{"path":"foo.go"}`}

	withData := approvalDiffModel("en")
	withData.pending = &approvalRequest{
		name: "edit", args: `{"path":"foo.go"}`, diff: BuildApprovalDiff(before, after),
	}

	baseline, got := frameBytes(plain, 90, 24), frameBytes(withData, 90, 24)
	if got != baseline {
		t.Fatalf("diff data changed frame bytes with switch off\nbaseline=%q\ngot=%q", baseline, got)
	}
	// 开关关闭时，即使有 diff 数据也不得出现摘要标记。
	if body := frameText(withData, 90, 24); strings.Contains(body, "diff：") {
		t.Fatalf("diff summary leaked while switch is off: %s", body)
	}
}

func TestApprovalDiffSwitchOnRendersDiffLine(t *testing.T) {
	prev := tuiApprovalDiff.Swap(true)
	defer tuiApprovalDiff.Store(prev)

	before := map[string]string{"foo.go": "a\nb\nc\n", "配置.go": "甲\n乙\n"}
	after := map[string]string{"foo.go": "a\nx\nc\nd\n", "配置.go": "甲\n丙\n"}
	diff := BuildApprovalDiff(before, after)

	m := approvalDiffModel("zh")
	m.pending = &approvalRequest{name: "edit", args: `{"path":"foo.go"}`, diff: diff}
	body := frameText(m, 90, 24)
	for _, want := range []string{RenderApprovalDiff(diff), "+2/-1 in foo.go", "配置.go"} {
		if !strings.Contains(body, want) {
			t.Fatalf("diff approval frame is missing %q:\n%s", want, body)
		}
	}
	buf := m.Frame(90, 24)
	for y := 0; y < buf.Height(); y++ {
		if !utf8.ValidString(buf.RowText(y)) {
			t.Fatalf("row %d is invalid UTF-8: %q", y, buf.RowText(y))
		}
	}
}

func TestConfigureApprovalDiffEnv(t *testing.T) {
	prev := tuiApprovalDiff.Load()
	defer tuiApprovalDiff.Store(prev)

	ConfigureApprovalDiff(nil)
	if approvalDiffVisible() {
		t.Fatal("nil env must leave the switch off")
	}
	ConfigureApprovalDiff(func(string) string { return "0" })
	if approvalDiffVisible() {
		t.Fatal(`ARGENT_APPROVAL_DIFF=0 must leave the switch off`)
	}
	ConfigureApprovalDiff(func(string) string { return " 1 " })
	if !approvalDiffVisible() {
		t.Fatal(`ARGENT_APPROVAL_DIFF=" 1 " must opt in`)
	}
	ConfigureApprovalDiff(func(key string) string {
		if key == "ARGENT_APPROVAL_DIFF" {
			return "1"
		}
		return ""
	})
	if !approvalDiffVisible() {
		t.Fatal("ARGENT_APPROVAL_DIFF=1 must opt in")
	}
}

// answerDiffApproval 在开关打开时通过 ApproveDiff 唤起弹窗并施加按键，
// 守卫 y/n/a 与危险预选拒绝在这些新增渲染下仍然工作。
func answerDiffApproval(t *testing.T, key Key, danger string) bool {
	t.Helper()
	m := approvalDiffModel("en")
	done := make(chan bool, 1)
	go func() {
		ok, err := m.ApproveDiff(context.Background(), "edit", `{"path":"foo.go"}`, danger,
			map[string]string{"foo.go": "a\n"}, map[string]string{"foo.go": "b\n"})
		if err != nil {
			t.Errorf("ApproveDiff: %v", err)
		}
		done <- ok
	}()
	waitFor(t, "the diff approval modal", func() bool {
		_, _, ok := m.PendingApproval()
		return ok
	})
	if danger != "" && !m.pending.defaultsToDeny() {
		t.Error("a danger request with diff must still default to Deny")
	}
	m.HandleKey(key)
	select {
	case ok := <-done:
		return ok
	case <-time.After(2 * time.Second):
		t.Fatal("the diff approval modal did not resolve")
		return false
	}
}

func TestApprovalDiffSwitchOnKeepsAnswerBehavior(t *testing.T) {
	prev := tuiApprovalDiff.Swap(true)
	defer tuiApprovalDiff.Store(prev)

	if !answerDiffApproval(t, Key{Kind: KindRune, Rune: 'y'}, "") {
		t.Fatal("y must allow a diff approval")
	}
	if answerDiffApproval(t, Key{Kind: KindRune, Rune: 'n'}, "") {
		t.Fatal("n must deny a diff approval")
	}
	if !answerDiffApproval(t, Key{Kind: KindRune, Rune: 'a'}, "") {
		t.Fatal("a must allow a diff approval for the session")
	}
	if answerDiffApproval(t, Key{Kind: KindEnter}, "destructive edit") {
		t.Fatal("Enter must deny a dangerous diff approval")
	}
}
