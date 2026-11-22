package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// jsonArg 把字符串编码成 JSON 字符串字面量，便于构造工具参数的 JSON 对象。
func jsonArg(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestApprovalDiffWriteSummary 验证 write 参数能提取文件数与增删计数。
func TestApprovalDiffWriteSummary(t *testing.T) {
	s := buildApprovalSummary("write", `{"path":"foo.go","content":"a\nb\nc\n"}`)
	if s.FilesChanged != 1 || s.LinesAdded != 3 || s.LinesRemoved != 0 {
		t.Fatalf("write summary = files %d, +%d/-%d; want 1, +3/-0 (%+v)",
			s.FilesChanged, s.LinesAdded, s.LinesRemoved, s)
	}
	want := "diff：1 个文件，+3/-0 行"
	if got := renderApprovalSummary(s); got != want {
		t.Fatalf("renderApprovalSummary = %q, want %q", got, want)
	}
	if !strings.Contains(s.Preview, "+3/-0 in foo.go") {
		t.Fatalf("write preview missing counts/path: %q", s.Preview)
	}
}

// TestApprovalDiffEditShapes 覆盖 edit/multi_edit/apply_patch 的参数形状。
func TestApprovalDiffEditShapes(t *testing.T) {
	tests := []struct {
		name, tool, args string
		files, add, rm   int
		wantPreview      string
	}{
		{
			name: "edit with old/new", tool: "edit",
			args:  `{"path":"foo.go","old":"a\nb\nc\n","new":"a\nx\nc\nd\n"}`,
			files: 1, add: 2, rm: 1, wantPreview: "+2/-1 in foo.go",
		},
		{
			name: "edit with aliases", tool: "edit",
			args:  `{"path":"foo.go","old_text":"a\nb\n","new_text":"a\nx\n"}`,
			files: 1, add: 1, rm: 1, wantPreview: "+1/-1 in foo.go",
		},
		{
			name: "multi_edit aggregates one file", tool: "multi_edit",
			args:  `{"path":"foo.go","edits":[{"old":"a\n","new":"a\nb\n"},{"old":"c\n","new":"d\n"}]}`,
			files: 1, add: 2, rm: 1, wantPreview: "+2/-1 in foo.go",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := buildApprovalSummary(tc.tool, tc.args)
			if s.FilesChanged != tc.files || s.LinesAdded != tc.add || s.LinesRemoved != tc.rm {
				t.Fatalf("summary = files %d, +%d/-%d; want %d, +%d/-%d (%+v)",
					s.FilesChanged, s.LinesAdded, s.LinesRemoved, tc.files, tc.add, tc.rm, s)
			}
			if !strings.Contains(s.Preview, tc.wantPreview) {
				t.Fatalf("preview = %q, want it to contain %q", s.Preview, tc.wantPreview)
			}
		})
	}

	// apply_patch：从文件指令取路径，从 hunk 的 +/- 行取增删。
	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n a\n-b\n+c\n+d\n*** End Patch\n"
	args := `{"input":` + jsonArg(t, patch) + `}`
	s := buildApprovalSummary("apply_patch", args)
	if s.FilesChanged != 1 || s.LinesAdded != 2 || s.LinesRemoved != 1 {
		t.Fatalf("patch summary = files %d, +%d/-%d; want 1, +2/-1 (%+v)",
			s.FilesChanged, s.LinesAdded, s.LinesRemoved, s)
	}
	if !strings.Contains(s.Preview, "+2/-1 in f.go") {
		t.Fatalf("patch preview = %q", s.Preview)
	}
}

// TestApprovalDiffPreviewBounded 验证预览行数有上界，超出时以省略提示收尾。
func TestApprovalDiffPreviewBounded(t *testing.T) {
	makePatch := func(n int) string {
		var b strings.Builder
		b.WriteString("*** Begin Patch\n")
		for i := 0; i < n; i++ {
			b.WriteString("*** Add File: file")
			b.WriteByte(byte('a' + i))
			b.WriteString(".go\n+x\n")
		}
		b.WriteString("*** End Patch\n")
		return `{"input":` + jsonArg(t, b.String()) + `}`
	}

	many := buildApprovalSummary("apply_patch", makePatch(9))
	if lines := strings.Split(many.Preview, "\n"); len(lines) != approvalDiffMaxPreviewLines {
		t.Fatalf("preview has %d lines, want %d: %q", len(lines), approvalDiffMaxPreviewLines, many.Preview)
	}
	if many.FilesChanged != 9 {
		t.Fatalf("FilesChanged = %d, want 9", many.FilesChanged)
	}
	if !strings.Contains(many.Preview, "还有 ") {
		t.Fatalf("overflow marker missing from bounded preview: %q", many.Preview)
	}

	// 恰好等于上限时不出现溢出提示。
	exact := buildApprovalSummary("apply_patch", makePatch(approvalDiffMaxPreviewLines))
	if lines := strings.Split(exact.Preview, "\n"); len(lines) != approvalDiffMaxPreviewLines {
		t.Fatalf("exact-limit preview has %d lines, want %d", len(lines), approvalDiffMaxPreviewLines)
	}
	if strings.Contains(exact.Preview, "还有 ") {
		t.Fatalf("exact-limit preview must not contain overflow marker: %q", exact.Preview)
	}
}

// TestApprovalDiffCJKPathByRune 验证超长 CJK 路径按 rune 截断，不切断字形。
func TestApprovalDiffCJKPathByRune(t *testing.T) {
	long := strings.Repeat("字", approvalDiffMaxPathRunes+10) + ".go"
	s := buildApprovalSummary("write", `{"path":`+jsonArg(t, long)+`,"content":"x\n"}`)
	if !utf8.ValidString(s.Preview) {
		t.Fatalf("preview is not valid UTF-8: %q", s.Preview)
	}
	parts := strings.Split(s.Preview, " in ")
	if len(parts) != 2 {
		t.Fatalf("unexpected preview shape: %q", s.Preview)
	}
	got := []rune(parts[1])
	if len(got) != approvalDiffMaxPathRunes {
		t.Fatalf("truncated path has %d runes, want %d: %q", len(got), approvalDiffMaxPathRunes, parts[1])
	}
	if want := strings.Repeat("字", approvalDiffMaxPathRunes); string(got) != want {
		t.Fatalf("truncated path = %q, want %q", string(got), want)
	}

	// 短 CJK 路径完整保留。
	short := buildApprovalSummary("write", `{"path":"配置/模块.go","content":"甲\n乙\n"}`)
	if !strings.Contains(short.Preview, "配置/模块.go") {
		t.Fatalf("short CJK path missing from preview: %q", short.Preview)
	}
}

// TestApprovalDiffNonEditToolsEmpty 验证非编辑类工具返回空摘要。
func TestApprovalDiffNonEditToolsEmpty(t *testing.T) {
	args := `{"path":"foo.go","content":"a\n"}`
	for _, name := range []string{"bash", "read", "grep", "unknown", ""} {
		if s := buildApprovalSummary(name, args); !s.empty() {
			t.Errorf("%q returned non-empty summary: %+v", name, s)
		}
	}
}

// TestApprovalDiffConfigureEnv 验证门控只认 ARGENT_NATIVE_APPROVAL_DIFF=1。
func TestApprovalDiffConfigureEnv(t *testing.T) {
	prev := nativeApprovalDiff.Load()
	defer nativeApprovalDiff.Store(prev)

	configureApprovalDiff(nil)
	if approvalDiffVisible() {
		t.Fatal("nil env must leave the gate off")
	}
	configureApprovalDiff(func(string) string { return "0" })
	if approvalDiffVisible() {
		t.Fatal("value 0 must leave the gate off")
	}
	configureApprovalDiff(func(string) string { return " 1 " })
	if !approvalDiffVisible() {
		t.Fatal(`value " 1 " must opt in`)
	}
	configureApprovalDiff(func(key string) string {
		if key == "ARGENT_NATIVE_APPROVAL_DIFF" {
			return "1"
		}
		return ""
	})
	if !approvalDiffVisible() {
		t.Fatal("ARGENT_NATIVE_APPROVAL_DIFF=1 must opt in")
	}
}

// approvalTexts 渲染一张审批卡并返回该帧的文本元素列表。
// 使用 newTestChat 的无窗口范式，不调用 mygo.App.Run，可在无 GPU 环境运行。
func approvalTexts(t *testing.T, name, args string) []string {
	t.Helper()
	app := newTestChat()
	app.approval = &approval{name: name, args: args}
	tt := ui.NewTester(app.view, 1180, 720)
	return tt.Texts()
}

// TestApprovalDiffGateKeepsElements 验证门控关闭或摘要为空时，
// 审批卡的元素数与文案都与接线前逐字节一致。
func TestApprovalDiffGateKeepsElements(t *testing.T) {
	prev := nativeApprovalDiff.Swap(false)
	defer nativeApprovalDiff.Store(prev)

	diffArgs := `{"path":"foo.go","content":"a\nb\n"}` // 可提取摘要
	emptyArgs := "{}"                                  // 摘要为空

	// 门控关闭：无论能否提取摘要，元素数都与接线前一致，且不含 diff 标记。
	offDiff := approvalTexts(t, "write", diffArgs)
	offEmpty := approvalTexts(t, "write", emptyArgs)
	if len(offDiff) != len(offEmpty) {
		t.Fatalf("gated-off element count changed: %d vs %d", len(offDiff), len(offEmpty))
	}
	for _, line := range offDiff {
		if strings.Contains(line, "diff：") || strings.Contains(line, "+2/-0 in") {
			t.Fatalf("diff leaked while gate is off: %q", offDiff)
		}
	}

	// 摘要为空：即便门控打开，帧也逐字节不变。
	nativeApprovalDiff.Store(true)
	onEmpty := approvalTexts(t, "write", emptyArgs)
	if !reflect.DeepEqual(onEmpty, offEmpty) {
		t.Fatalf("empty summary changed frame while gate is on\nbaseline=%q\ngot      =%q", offEmpty, onEmpty)
	}

	// 只有 opt-in 且摘要非空才多渲染摘要与预览两行。
	onDiff := approvalTexts(t, "write", diffArgs)
	if len(onDiff) != len(offDiff)+2 {
		t.Fatalf("opted-in element count = %d, want %d (%d + 2): %q", len(onDiff), len(offDiff)+2, len(offDiff), onDiff)
	}
}

// TestApprovalViewDiffRendersWhenOptedIn 验证 opt-in 且摘要非空时展示摘要与预览。
func TestApprovalViewDiffRendersWhenOptedIn(t *testing.T) {
	prev := nativeApprovalDiff.Swap(true)
	defer nativeApprovalDiff.Store(prev)

	app := newTestChat()
	app.approval = &approval{name: "write", args: `{"path":"foo.go","content":"a\nb\n"}`}
	tt := ui.NewTester(app.view, 1180, 720)
	for _, want := range []string{"diff：1 个文件，+2/-0 行", "+2/-0 in foo.go"} {
		if !tt.HasText(want) {
			t.Fatalf("diff approval frame is missing %q: %q", want, tt.Texts())
		}
	}
}
