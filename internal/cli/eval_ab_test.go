package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNamedResultVectorFile 把带名字的通过/失败序列写成 Result 向量 JSON 文件。
func writeNamedResultVectorFile(t *testing.T, names []string, passed []bool) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("[")
	for i := range names {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "{\"index\":%d,\"name\":%q,\"passed\":%t}", i+1, names[i], passed[i])
	}
	b.WriteString("]")
	path := filepath.Join(t.TempDir(), fmt.Sprintf("ab-results-%d.json", len(names)))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFingerprintFile 把「场景名 → 指纹」映射写成 JSON 文件。
func writeFingerprintFile(t *testing.T, m map[string]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("{")
	first := true
	for name, fp := range m {
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, "%q:%q", name, fp)
	}
	b.WriteString("}")
	path := filepath.Join(t.TempDir(), fmt.Sprintf("fp-%d.json", len(m)))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunEvalABDispatchWithoutFingerprints 证明 ab 是带外前缀：不注入 app.Eval、
// 不提供指纹文件时按场景名配对，也能输出 verdict 与配对明细。
func TestRunEvalABDispatchWithoutFingerprints(t *testing.T) {
	names := []string{"s0", "s1", "s2", "s3", "s4", "s5"}
	passedA := []bool{false, true, true, true, true, true}
	passedB := []bool{true, true, true, true, true, true}
	a := writeNamedResultVectorFile(t, names, passedA)
	b := writeNamedResultVectorFile(t, names, passedB)
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"eval", "ab", a, b}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"配对诊断报告", "verdict:", "样本不足", "配对明细", "配对数 n=6", "A=\"s0\"(失败) ↔ B=\"s0\"(通过)"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
}

// TestRunEvalABWithFingerprints 证明给了两份指纹文件后按指纹配对：
// 两臂名字与顺序都不同，仍能正确对应并输出两臂场景名。
func TestRunEvalABWithFingerprints(t *testing.T) {
	baseA := writeNamedResultVectorFile(t,
		[]string{"甲", "乙", "丙"}, []bool{false, true, false})
	baseB := writeNamedResultVectorFile(t,
		[]string{"丙B", "甲B", "乙B"}, []bool{true, true, true})
	fpA := writeFingerprintFile(t, map[string]string{"甲": "f1", "乙": "f2", "丙": "f3"})
	fpB := writeFingerprintFile(t, map[string]string{"甲B": "f1", "乙B": "f2", "丙B": "f3"})

	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"eval", "ab", baseA, baseB, fpA, fpB}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "配对数 n=3") {
		t.Fatalf("stdout missing pairing count:\n%s", got)
	}
	for _, want := range []string{`A="甲"(失败) ↔ B="甲B"(通过)`, `A="乙"(通过) ↔ B="乙B"(通过)`, `A="丙"(失败) ↔ B="丙B"(通过)`} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "指纹缺失") || strings.Contains(got, "无对应指纹") {
		t.Errorf("unexpected unmatched scenarios:\n%s", got)
	}
}

// TestRunEvalABErrors 覆盖参数个数、结果文件缺失与指纹文件格式错误。
func TestRunEvalABErrors(t *testing.T) {
	a := writeNamedResultVectorFile(t, []string{"s0"}, []bool{true})
	badFP := filepath.Join(t.TempDir(), "bad-fp.json")
	if err := os.WriteFile(badFP, []byte(`["not", "an", "object"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{name: "no paths", args: []string{"eval", "ab"}},
		{name: "one path", args: []string{"eval", "ab", a}},
		{name: "three paths", args: []string{"eval", "ab", a, a, a}},
		{name: "one fingerprint path", args: []string{"eval", "ab", a, a, a}},
		{name: "missing result file", args: []string{"eval", "ab", filepath.Join(t.TempDir(), "nope.json"), a}},
		{name: "bad fingerprint file", args: []string{"eval", "ab", a, a, badFP, badFP}},
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

// TestRunEvalABHelp 验证 `eval ab -h` 不需要文件，打印 ab 用法。
func TestRunEvalABHelp(t *testing.T) {
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"eval", "ab", "-h"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "usage: argentgo eval ab") {
		t.Fatalf("stdout missing ab usage:\n%s", out.String())
	}
}

// TestEvalUsageMentionsAB 确保主 eval 用法提示包含 ab 前缀。
func TestEvalUsageMentionsAB(t *testing.T) {
	var out strings.Builder
	evalUsage(&out)
	if !strings.Contains(out.String(), "eval ab") {
		t.Fatalf("eval usage does not mention ab:\n%s", out.String())
	}
}
