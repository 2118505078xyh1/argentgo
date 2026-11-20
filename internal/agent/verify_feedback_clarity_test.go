package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestVerifyFeedbackClarityWithSourceWindow 覆盖 aider 审计修复：面向模型的 FAIL 现在以修复指令开头，
// 并在锚定源码窗口之前紧接着解释 █ 标记。
// （接上行）
func TestVerifyFeedbackClarityWithSourceWindow(t *testing.T) {
	root := writeAnchorFixture(t)
	out := "compiling\nf.go:7: api_key=sk_live_abcdefghijklmnop\nsome failure detail\n"
	got := BuildVerifyFeedback("go test", out, VerifyLoopState{Root: root, Max: 3})

	if !strings.HasPrefix(got, verifyFixInstruction+"\n") {
		t.Fatalf("feedback = %q, want the fix instruction first", got)
	}
	for _, want := range []string{
		"[verify: GO TEST] FAIL",
		verifyAnchorExplanation,
		"f.go:7:\n",
		"█7|",
		"Reflections left: 3. Do not re-run the same check unchanged.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("feedback = %q, want it to contain %q", got, want)
		}
	}

	// 行顺序：指令 → 结论 → 日志尾部 → █ 说明 → 窗口 → 提示。
	ordered := []string{
		verifyFixInstruction,
		"[verify: GO TEST] FAIL",
		"some failure detail",
		verifyAnchorExplanation,
		"f.go:7:\n",
		"█7|",
		"Reflections left:",
	}
	previous := -1
	for _, part := range ordered {
		at := strings.Index(got, part)
		if at <= previous {
			t.Fatalf("feedback = %q, want %q after the previous part", got, part)
		}
		previous = at
	}
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
}

// TestVerifyFeedbackClarityNoWindowOmitsExplanation 检查没有源码窗口的 FAIL 保留修复指令，
// 但不带 █ 说明。
func TestVerifyFeedbackClarityNoWindowOmitsExplanation(t *testing.T) {
	out := "FAIL: assertion x"

	plain := BuildVerifyFeedback("go test", out, VerifyLoopState{Max: 3})
	if !strings.HasPrefix(plain, verifyFixInstruction+"\n[verify: GO TEST] FAIL\n") {
		t.Fatalf("feedback = %q, want fix instruction then verdict", plain)
	}
	if strings.Contains(plain, verifyAnchorExplanation) || strings.Contains(plain, "█") {
		t.Fatalf("feedback without a window gained a █ explanation: %q", plain)
	}

	// 非空 root 但没有可用的 file:line 引用时也必须如此。
	root := writeAnchorFixture(t)
	noAnchor := VerificationFeedbackAt("test", false, out, 4096, root)
	if !strings.HasPrefix(noAnchor, verifyFixInstruction+"\n[verify: TEST] FAIL\n") {
		t.Fatalf("no-anchor feedback = %q, want fix instruction then verdict", noAnchor)
	}
	if strings.Contains(noAnchor, verifyAnchorExplanation) || strings.Contains(noAnchor, "█") {
		t.Fatalf("no-anchor feedback gained a █ explanation: %q", noAnchor)
	}
}

// TestVerifyFeedbackClarityCJKErrorText 检查非 ASCII 诊断在增强模板和字节限制下保持完整。
// （接上行）
func TestVerifyFeedbackClarityCJKErrorText(t *testing.T) {
	const out = "编译失败：未定义的标识符 foo"

	got := BuildVerifyFeedback("build", out, VerifyLoopState{Max: 3})
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, verifyFixInstruction+"\n[verify: BUILD] FAIL\n") {
		t.Fatalf("feedback = %q, want fix instruction then verdict", got)
	}
	if !strings.Contains(got, out) {
		t.Fatalf("feedback = %q, want the CJK error kept", got)
	}
	if strings.Contains(got, verifyAnchorExplanation) {
		t.Fatalf("plain CJK failure gained a █ explanation: %q", got)
	}
}

// TestVerifyFeedbackClarityCJKSourceWindow 检查源码行本身包含 CJK 文本的 █ 标记窗口。
// （接上行）
func TestVerifyFeedbackClarityCJKSourceWindow(t *testing.T) {
	root := t.TempDir()
	src := "package p\n// 中文注释\nfunc f() {\n\tboom() // 失败\n}\n"
	if err := os.WriteFile(filepath.Join(root, "c.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got := BuildVerifyFeedback("go test", "c.go:4: undefined: boom", VerifyLoopState{Root: root, Max: 3})
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, verifyAnchorExplanation) || !strings.Contains(got, "█4|") {
		t.Fatalf("feedback = %q, want the █ explanation and marked CJK line", got)
	}
	if !strings.Contains(got, "失败") {
		t.Fatalf("feedback = %q, want the CJK source text kept", got)
	}
}

// TestVerifyFeedbackClarityEmptyAndPassStayVerdictOnly 守护既有规则：没有可用输出的 FAIL（以及所有 PASS）只有结论。
// （接上行）
func TestVerifyFeedbackClarityEmptyAndPassStayVerdictOnly(t *testing.T) {
	root := writeAnchorFixture(t)
	for _, rc := range []struct {
		name string
		root string
	}{{"no root", ""}, {"with root", root}} {
		t.Run(rc.name+" empty", func(t *testing.T) {
			got := BuildVerifyFeedback("go test", "  \n\t ", VerifyLoopState{Root: rc.root, Max: 3})
			want := "[verify: GO TEST] FAIL\nReflections left: 3. Do not re-run the same check unchanged."
			if got != want {
				t.Fatalf("feedback = %q, want %q", got, want)
			}
			if strings.Contains(got, verifyFixInstruction) || strings.Contains(got, verifyAnchorExplanation) {
				t.Fatalf("empty feedback gained clarity boilerplate: %q", got)
			}
		})
	}

	pass := VerificationFeedbackAt("lint", true, "f.go:7 noisy pass", 4096, root)
	if want := "[verify: LINT] PASS"; pass != want {
		t.Fatalf("PASS feedback = %q, want %q", pass, want)
	}
	if strings.Contains(pass, verifyFixInstruction) || strings.Contains(pass, "█") {
		t.Fatalf("PASS feedback gained clarity boilerplate: %q", pass)
	}
}

// TestVerifyFeedbackClarityBytesAreStable 检查重复调用在有无源码窗口时都渲染出相同的字节。
// （接上行）
func TestVerifyFeedbackClarityBytesAreStable(t *testing.T) {
	root := writeAnchorFixture(t)
	const out = "编译失败 at f.go:7\nsecond line"
	state := VerifyLoopState{Root: root, Max: 3}

	first := BuildVerifyFeedback("go test", out, state)
	for i := 0; i < 8; i++ {
		if got := BuildVerifyFeedback("go test", out, state); got != first {
			t.Fatalf("call %d = %q, want %q", i, got, first)
		}
	}

	atFirst := VerificationFeedbackAt("test", false, out, 4096, root)
	for i := 0; i < 8; i++ {
		if got := VerificationFeedbackAt("test", false, out, 4096, root); got != atFirst {
			t.Fatalf("VerificationFeedbackAt call %d = %q, want %q", i, got, atFirst)
		}
	}
}
