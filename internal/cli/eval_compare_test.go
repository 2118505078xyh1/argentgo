package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeResultVectorFile 把通过/失败序列写成 Result 向量 JSON 文件。
func writeResultVectorFile(t *testing.T, passed []bool) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("[")
	for i, p := range passed {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "{\"index\":%d,\"passed\":%t}", i+1, p)
	}
	b.WriteString("]")
	path := filepath.Join(t.TempDir(), fmt.Sprintf("results-%d.json", len(passed)))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunEvalCompareSingleFlipMarksInsufficient 证明 eval compare 是带外前缀：
// 即使 App.Eval 为 nil（没有注入任何 runner）也能工作，且 6 场景单翻转输出
// "样本不足 / 不显著"，不给出提升结论。
func TestRunEvalCompareSingleFlipMarksInsufficient(t *testing.T) {
	a := writeResultVectorFile(t, []bool{false, true, true, true, true, true})
	b := writeResultVectorFile(t, []bool{true, true, true, true, true, true})
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"eval", "compare", a, b}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"配对诊断报告", "样本不足", "n=6 < 30", "不显著"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"verdict: 显著提升", "verdict: 显著正向"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("single flip must not claim improvement, got %q:\n%s", forbidden, got)
		}
	}
}

// TestRunEvalCompareSignificant 用 30 对向量验证显著提升结论经 CLI 正常输出。
func TestRunEvalCompareSignificant(t *testing.T) {
	base := make([]bool, 30)
	cand := make([]bool, 30)
	for i := range base {
		base[i] = i >= 10
		cand[i] = true
	}
	a := writeResultVectorFile(t, base)
	b := writeResultVectorFile(t, cand)
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"eval", "compare", a, b}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "verdict: 显著提升：候选完成率更高") {
		t.Fatalf("stdout missing significant verdict:\n%s", out.String())
	}
}

// TestRunEvalCompareErrors 覆盖参数个数、文件缺失与向量长度不一致。
func TestRunEvalCompareErrors(t *testing.T) {
	a := writeResultVectorFile(t, []bool{true, false})
	b := writeResultVectorFile(t, []bool{true, true, false})
	cases := []struct {
		name string
		args []string
	}{
		{name: "missing paths", args: []string{"eval", "compare"}},
		{name: "one path", args: []string{"eval", "compare", a}},
		{name: "too many paths", args: []string{"eval", "compare", a, a, a}},
		{name: "missing file", args: []string{"eval", "compare", filepath.Join(t.TempDir(), "nope.json"), a}},
		{name: "unequal vectors", args: []string{"eval", "compare", a, b}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdio, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: stdio}, tc.args)
			if code == 0 {
				t.Fatalf("exit = 0, want non-zero; stdout = %q", out.String())
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr is empty")
			}
		})
	}
}

// TestRunEvalCompareHelp 验证 `eval compare -h` 不需要文件，打印 compare 用法。
func TestRunEvalCompareHelp(t *testing.T) {
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"eval", "compare", "-h"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "usage: argentgo eval compare") {
		t.Fatalf("stdout missing compare usage:\n%s", out.String())
	}
}

// TestEvalUsageMentionsCompare 确保主 eval 用法提示包含 compare 前缀。
func TestEvalUsageMentionsCompare(t *testing.T) {
	var out strings.Builder
	evalUsage(&out)
	if !strings.Contains(out.String(), "eval compare") {
		t.Fatalf("eval usage does not mention compare:\n%s", out.String())
	}
}
