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

// TestApprovalDiffConfigureEnv 验证 diff 默认开启，只有 =0 紧急回退关闭。
func TestApprovalDiffConfigureEnv(t *testing.T) {
	prev := nativeApprovalDiff.Load()
	defer nativeApprovalDiff.Store(prev)

	// 默认开启：nil、空值、任意非 "0" 值都保持显示。
	configureApprovalDiff(nil)
	if !approvalDiffVisible() {
		t.Fatal("nil env must leave the gate on by default")
	}
	configureApprovalDiff(func(string) string { return "" })
	if !approvalDiffVisible() {
		t.Fatal("empty value must leave the gate on")
	}
	configureApprovalDiff(func(string) string { return "1" })
	if !approvalDiffVisible() {
		t.Fatal("value 1 must leave the gate on")
	}
	configureApprovalDiff(func(string) string { return " 0 " })
	if approvalDiffVisible() {
		t.Fatal(`value " 0 " must disable the gate (emergency fallback)`)
	}
	configureApprovalDiff(func(key string) string {
		if key == "ARGENT_NATIVE_APPROVAL_DIFF" {
			return "0"
		}
		return ""
	})
	if approvalDiffVisible() {
		t.Fatal("ARGENT_NATIVE_APPROVAL_DIFF=0 must disable the gate")
	}
}

// TestApprovalDiffDefaultOn 验证未显式配置时审批卡默认渲染 diff。
func TestApprovalDiffDefaultOn(t *testing.T) {
	// 模拟 main() 未注入任何 env 时的默认状态。
	configureApprovalDiff(nil)
	if !approvalDiffVisible() {
		t.Fatal("diff must be visible by default")
	}
	texts := approvalTexts(t, "write", `{"path":"foo.go","content":"a\nb\n"}`)
	found := false
	for _, line := range texts {
		if strings.Contains(line, "diff：") || strings.Contains(line, "+2/-0 in") {
			found = true
		}
	}
	if !found {
		t.Fatalf("default frame has no diff rows: %q", texts)
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

// TestApprovalViewDiffRendersWhenOptedIn 验证摘要非空时展示摘要与预览。
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

// TestApprovalLineDiffEdit 验证 edit 能生成带上下文/删除/新增的逐行 diff 数据。
func TestApprovalLineDiffEdit(t *testing.T) {
	files := buildApprovalLineDiff("edit", `{"path":"foo.go","old":"a\nb\nc\n","new":"a\nx\nc\nd\n"}`)
	if len(files) != 1 || files[0].Path != "foo.go" {
		t.Fatalf("line diff files = %+v", files)
	}
	var adds, dels, ctx int
	for _, line := range files[0].Lines {
		switch line.Kind {
		case approvalLineAdd:
			adds++
		case approvalLineDel:
			dels++
		case approvalLineContext:
			ctx++
		}
	}
	if adds != 2 || dels != 1 || ctx < 2 {
		t.Fatalf("line kinds: +%d/-%d/context%d: %+v", adds, dels, ctx, files[0].Lines)
	}
	if got := renderApprovalLineDiff(files); !strings.Contains(got, "-b") || !strings.Contains(got, "+x") {
		t.Fatalf("rendered line diff missing changes: %q", got)
	}
}

// TestApprovalLineDiffWriteAndPatch 验证 write 与 apply_patch 的逐行 diff。
func TestApprovalLineDiffWriteAndPatch(t *testing.T) {
	write := buildApprovalLineDiff("write", `{"path":"new.go","content":"x\ny\n"}`)
	if len(write) != 1 || len(write[0].Lines) != 2 ||
		write[0].Lines[0].Kind != approvalLineAdd || write[0].Lines[0].Text != "x" {
		t.Fatalf("write line diff = %+v", write)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n a\n-b\n+c\n*** End Patch\n"
	args := `{"input":` + jsonArg(t, patch) + `}`
	files := buildApprovalLineDiff("apply_patch", args)
	if len(files) != 1 || files[0].Path != "f.go" {
		t.Fatalf("patch line diff files = %+v", files)
	}
	var hasDel, hasAdd bool
	for _, line := range files[0].Lines {
		if line.Kind == approvalLineDel && line.Text == "b" {
			hasDel = true
		}
		if line.Kind == approvalLineAdd && line.Text == "c" {
			hasAdd = true
		}
	}
	if !hasDel || !hasAdd {
		t.Fatalf("patch line diff missing -b/+c: %+v", files[0].Lines)
	}
}

// TestApprovalLineDiffBounded 验证逐行 diff 的行数与行长都受上界约束。
func TestApprovalLineDiffBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < approvalLineDiffMaxLines+10; i++ {
		b.WriteString("line\n")
	}
	args := `{"path":"big.go","content":` + jsonArg(t, b.String()) + `}`
	files := buildApprovalLineDiff("write", args)
	if len(files) != 1 || len(files[0].Lines) != approvalLineDiffMaxLines {
		t.Fatalf("bounded line count = %d, want %d", len(files[0].Lines), approvalLineDiffMaxLines)
	}
	if !strings.Contains(files[0].Lines[len(files[0].Lines)-1].Text, "省略") {
		t.Fatalf("bounded diff missing overflow marker: %+v", files[0].Lines[len(files[0].Lines)-1])
	}

	long := strings.Repeat("字", approvalLineDiffMaxRunes+10)
	files = buildApprovalLineDiff("write", `{"path":"long.go","content":`+jsonArg(t, long)+`}`)
	if got := []rune(files[0].Lines[0].Text); len(got) != approvalLineDiffMaxRunes {
		t.Fatalf("line has %d runes, want %d", len(got), approvalLineDiffMaxRunes)
	}
}

// TestApprovalLineDiffNonEditToolsEmpty 验证非编辑工具不生成逐行 diff 数据。
func TestApprovalLineDiffNonEditToolsEmpty(t *testing.T) {
	for _, name := range []string{"bash", "read", "grep", "unknown", ""} {
		if got := buildApprovalLineDiff(name, `{"path":"foo.go","content":"a\n"}`); len(got) != 0 {
			t.Errorf("%q returned line diff: %+v", name, got)
		}
	}
}
