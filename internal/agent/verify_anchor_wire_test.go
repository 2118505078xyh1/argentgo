package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeAnchorFixture 写一个小源文件，其第 7 行带有一个形似凭据的机密。
// 它返回工作区根目录。
func writeAnchorFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := strings.Join([]string{
		"package sample",
		"",
		"func check() int {",
		"    a := 1",
		"    b := 2",
		"    c := 3",
		"    api_key=sk_live_abcdefghijklmnop", // 有问题的第 7 行
		"    return a + b + c",
		"}",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root
}

// outputPortion 返回 FAIL 反馈中有界的诊断部分：修复指令行和结论行之后的所有内容。
// （接上行）
func outputPortion(feedback string) (string, bool) {
	_, afterFix, found := strings.Cut(feedback, "\n")
	if !found {
		return "", false
	}
	_, rest, found := strings.Cut(afterFix, "\n")
	return rest, found
}

func TestVerificationFeedbackAtAppendsAnchoredSourceWindow(t *testing.T) {
	root := writeAnchorFixture(t)
	const secret = "sk_live_abcdefghijklmnop"
	out := "compiling\nf.go:7: api_key=" + secret + "\nsome failure detail\n"

	got := VerificationFeedbackAt("lint", false, out, 4096, root)

	if !strings.HasPrefix(got, verifyFixInstruction+"\n[verify: LINT] FAIL\n") {
		t.Fatalf("feedback = %q, want fix instruction then FAIL verdict header", got)
	}
	if !strings.Contains(got, "f.go:7:") {
		t.Fatalf("feedback = %q, want anchored file:line header", got)
	}
	if !strings.Contains(got, "█7|") {
		t.Fatalf("feedback = %q, want █-marked offending line", got)
	}
	if !strings.Contains(got, verifyAnchorExplanation+"\n") {
		t.Fatalf("feedback = %q, want the █ explanation line", got)
	}
	// 说明应紧接在源码窗口之前，而不是日志尾部之前（绝不能出现在没有源码窗口的 FAIL 上）。
	// （接上行）
	if expAt, anchorAt := strings.Index(got, verifyAnchorExplanation), strings.Index(got, "f.go:7:\n"); expAt < 0 || anchorAt < 0 || expAt > anchorAt {
		t.Fatalf("feedback = %q, want the █ explanation before the source window", got)
	}
	if !strings.Contains(got, "some failure detail") {
		t.Fatalf("feedback = %q, want the redacted log tail", got)
	}
	// 源码窗口必须跟在日志尾部之后，而非之前。
	tailAt := strings.Index(got, "some failure detail")
	anchorAt := strings.Index(got, "█7|")
	if tailAt < 0 || anchorAt < 0 || tailAt > anchorAt {
		t.Fatalf("feedback = %q, want source window appended after the tail", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	// 脱敏同时覆盖回显的输出和源码行本身。
	if strings.Contains(got, secret) {
		t.Fatalf("feedback leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("feedback = %q, want a [redacted] marker", got)
	}
}

func TestVerificationFeedbackAtPassIsUnchanged(t *testing.T) {
	root := writeAnchorFixture(t)
	const out = "f.go:7 noisy pass output"
	got := VerificationFeedbackAt("lint", true, out, 4096, root)
	want := VerificationFeedback("lint", true, out, 4096)
	if got != want {
		t.Fatalf("PASS feedback = %q, want unchanged %q", got, want)
	}
	if strings.Contains(got, "█") {
		t.Fatalf("PASS feedback must not contain a source window: %q", got)
	}
}

func TestVerificationFeedbackAtNoAnchorAddsFixInstructionOnly(t *testing.T) {
	root := writeAnchorFixture(t)
	out := "totally plain failure, no file reference here"
	got := VerificationFeedbackAt("test", false, out, 4096, root)
	want := verifyFixInstruction + "\n[verify: TEST] FAIL\n" + out
	if got != want {
		t.Fatalf("no-anchor feedback = %q, want %q", got, want)
	}
	if strings.Contains(got, verifyAnchorExplanation) || strings.Contains(got, "█") {
		t.Fatalf("no-anchor feedback gained a █ explanation: %q", got)
	}
}

func TestVerificationFeedbackAtEmptyRootAddsFixInstructionOnly(t *testing.T) {
	root := writeAnchorFixture(t)
	out := "f.go:7: failure detail"
	want := verifyFixInstruction + "\n[verify: TEST] FAIL\n" + out
	for _, emptyRoot := range []string{"", "   \t "} {
		if got := VerificationFeedbackAt("test", false, out, 4096, emptyRoot); got != want {
			t.Fatalf("empty-root %q feedback = %q, want %q", emptyRoot, got, want)
		}
	}
	// 健全性检查：非空 root 会进行锚定，证明该样本可用。
	if got := VerificationFeedbackAt("test", false, out, 4096, root); !strings.Contains(got, "█") {
		t.Fatalf("non-empty root did not anchor the valid fixture: %q", got)
	}
}

func TestVerificationFeedbackAtOutOfRootAnchorAddsFixInstructionOnly(t *testing.T) {
	writeAnchorFixture(t)
	out := "../outside.go:3: failure detail"
	root := t.TempDir()
	got := VerificationFeedbackAt("test", false, out, 4096, root)
	want := verifyFixInstruction + "\n[verify: TEST] FAIL\n" + out
	if got != want {
		t.Fatalf("out-of-root feedback = %q, want %q", got, want)
	}
	if strings.Contains(got, verifyAnchorExplanation) {
		t.Fatalf("out-of-root feedback gained a █ explanation: %q", got)
	}
}

func TestVerificationFeedbackStaysUnchangedWhileAtAddsFixInstruction(t *testing.T) {
	out := "f.go:7: failure\napi_key=sk_live_abcdefghijklmnop"
	for _, maxBytes := range []int{0, 64, 4096} {
		plain := VerificationFeedback("lint", false, out, maxBytes)
		at := VerificationFeedbackAt("lint", false, out, maxBytes, "")
		if want := verifyFixInstruction + "\n" + plain; at != want {
			t.Fatalf("maxBytes=%d: VerificationFeedbackAt = %q, want %q", maxBytes, at, want)
		}
	}
}

func TestVerificationFeedbackAtRespectsMaxBytes(t *testing.T) {
	root := writeAnchorFixture(t)
	out := strings.Repeat("q", 400) + "\nf.go:7: api_key=sk_live_abcdefghijklmnop\n" + strings.Repeat("z", 400)
	const maxBytes = 200

	got := VerificationFeedbackAt("test", false, out, maxBytes, root)
	portion, found := outputPortion(got)
	if !found {
		t.Fatalf("feedback has no output section: %q", got)
	}
	if len(portion) > maxBytes {
		t.Fatalf("output portion = %d bytes, want <= %d: %q", len(portion), maxBytes, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "█7|") {
		t.Fatalf("feedback = %q, want the █-marked window to survive the bound", got)
	}
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("feedback leaked the secret: %q", got)
	}
}
