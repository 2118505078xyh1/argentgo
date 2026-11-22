package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baselineFixture 返回一个隔离的 storage 目录，以及 accept 用的结果向量路径。
// 用 App.StorageDir 固定基线落盘位置，避免测试写到真实用户目录。
func baselineFixture(t *testing.T) (storage, resultsPath string) {
	t.Helper()
	storage = t.TempDir()
	resultsPath = filepath.Join(storage, "results.json")
	return storage, resultsPath
}

// writeBaselineResults 把通过序列写成 Result 向量 JSON 文件（带场景名）。
func writeBaselineResults(t *testing.T, path string, passed []bool) {
	t.Helper()
	results := make([]map[string]any, 0, len(passed))
	for i, p := range passed {
		results = append(results, map[string]any{
			"index":  i + 1,
			"name":   "s" + string(rune('a'+i)),
			"passed": p,
		})
	}
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// acceptBaseline 执行一次 `eval baseline accept`，失败即终止测试。
func acceptBaseline(t *testing.T, storage, resultsPath string, extra ...string) string {
	t.Helper()
	stdio, out, errOut := newTestIO("", nil)
	args := []string{"eval", "baseline", "accept", resultsPath}
	args = append(args, extra...)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, args); code != 0 {
		t.Fatalf("accept exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	return out.String()
}

// checkBaseline 执行一次 `eval baseline check`，返回退出码与 stdout。
func checkBaseline(t *testing.T, storage, candidatePath string) (int, string, string) {
	t.Helper()
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"eval", "baseline", "check", candidatePath})
	return code, out.String(), errOut.String()
}

// TestBaselineAcceptCheckStable 是主闭环：accept 固化基线后，用同一份向量
// check 应得到 stable，退出 0，且落盘路径为 <storage>/eval/baseline.json。
func TestBaselineAcceptCheckStable(t *testing.T) {
	storage, resultsPath := baselineFixture(t)
	writeBaselineResults(t, resultsPath, []bool{true, true, false, true})

	acceptOut := acceptBaseline(t, storage, resultsPath)
	wantPath := filepath.Join(storage, "eval", "baseline.json")
	if !strings.Contains(acceptOut, wantPath) {
		t.Fatalf("accept stdout 未报告落盘路径 %q: %s", wantPath, acceptOut)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("基线文件未落盘: %v", err)
	}

	code, out, errOut := checkBaseline(t, storage, resultsPath)
	if code != 0 {
		t.Fatalf("stable check exit = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"状态: stable", "完成率", "基线文件"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout 缺少 %q: %s", want, out)
		}
	}
}

// TestBaselineCheckRegressionReturnsNonZero 证明人为劣化（通过数下降）后
// CheckRegression 判为 regressed，stdout 输出 regression，且退出码非 0（CI 门禁）。
func TestBaselineCheckRegressionReturnsNonZero(t *testing.T) {
	storage, resultsPath := baselineFixture(t)
	writeBaselineResults(t, resultsPath, []bool{true, true, true, true}) // 基线 1.0
	acceptBaseline(t, storage, resultsPath)

	degradedPath := filepath.Join(storage, "degraded.json")
	writeBaselineResults(t, degradedPath, []bool{true, false, false, false}) // 0.25
	code, out, errOut := checkBaseline(t, storage, degradedPath)
	if code == 0 {
		t.Fatalf("regression 应退出非 0, stdout = %q", out)
	}
	for _, want := range []string{"状态: regression", "regressed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout 缺少 %q: %s", want, out)
		}
	}
	if errOut != "" {
		t.Fatalf("回归判定应走 stdout 而非 stderr, stderr = %q", errOut)
	}
}

// TestBaselineCheckImproved 证明候选完成率高于基线时输出 improved 且退出 0。
func TestBaselineCheckImproved(t *testing.T) {
	storage, resultsPath := baselineFixture(t)
	writeBaselineResults(t, resultsPath, []bool{true, false, false, false}) // 0.25
	acceptBaseline(t, storage, resultsPath)

	improvedPath := filepath.Join(storage, "improved.json")
	writeBaselineResults(t, improvedPath, []bool{true, true, true, true}) // 1.0
	code, out, errOut := checkBaseline(t, storage, improvedPath)
	if code != 0 {
		t.Fatalf("improved check exit = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "状态: improved") {
		t.Fatalf("stdout 缺少 improved 状态: %s", out)
	}
}

// TestBaselineAcceptNotePersistedAndCJK 验证 --note 备注（含中文与 <>&）
// 原样写入基线 JSON，且 --note=<text> 形式同样生效、不被 HTML 转义。
func TestBaselineAcceptNotePersistedAndCJK(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "separate", args: []string{"--note", "中文备注：已接受 <>& v1"}},
		{name: "equals", args: []string{"--note=中文备注：已接受 <>& v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage, resultsPath := baselineFixture(t)
			writeBaselineResults(t, resultsPath, []bool{true, false, true, true})
			acceptOut := acceptBaseline(t, storage, resultsPath, tc.args...)
			if !strings.Contains(acceptOut, "中文备注：已接受 <>& v1") {
				t.Fatalf("accept stdout 未回显备注: %s", acceptOut)
			}
			raw, err := os.ReadFile(filepath.Join(storage, "eval", "baseline.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "中文备注：已接受 <>& v1") {
				t.Fatalf("基线文件未原样保存中文备注: %s", raw)
			}
			if strings.Contains(string(raw), `\u`) {
				t.Fatalf("中文备注不应被转义为 \\uXXXX: %s", raw)
			}
		})
	}
}

// TestBaselineCheckMissingFileErrsWithHint 验证基线文件缺失时给出可操作的
// 错误提示（引导先 accept），且退出码非 0、stdout 为空。
func TestBaselineCheckMissingFileErrsWithHint(t *testing.T) {
	storage, candidatePath := baselineFixture(t)
	writeBaselineResults(t, candidatePath, []bool{true, false})
	code, out, errOut := checkBaseline(t, storage, candidatePath)
	if code == 0 {
		t.Fatalf("缺失基线应退出非 0, stdout = %q", out)
	}
	if out != "" {
		t.Fatalf("缺失基线时 stdout 应为空, got %q", out)
	}
	for _, want := range []string{"基线文件不存在", "eval baseline accept"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr 缺少 %q: %s", want, errOut)
		}
	}
}

// TestBaselineCheckInsufficientNoGate 验证单样本候选判为 insufficient，
// 不算回归、退出 0。这是 CheckRegression 的样本不足守卫状态。
func TestBaselineCheckInsufficientNoGate(t *testing.T) {
	storage, resultsPath := baselineFixture(t)
	writeBaselineResults(t, resultsPath, []bool{true, true, false, true})
	acceptBaseline(t, storage, resultsPath)

	singlePath := filepath.Join(storage, "single.json")
	writeBaselineResults(t, singlePath, []bool{false})
	code, out, errOut := checkBaseline(t, storage, singlePath)
	if code != 0 {
		t.Fatalf("样本不足不应触发门禁, exit = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "状态: insufficient") {
		t.Fatalf("stdout 缺少 insufficient 状态: %s", out)
	}
}

// TestBaselineUsageAndArgErrors 覆盖帮助输出与参数错误（缺文件、多余参数、
// 未知子命令、--note 缺值），保证带外前缀的用法提示稳定。
func TestBaselineUsageAndArgErrors(t *testing.T) {
	storage, resultsPath := baselineFixture(t)
	writeBaselineResults(t, resultsPath, []bool{true, false})

	// 帮助：三处 -h 都应退出 0 并打印用法。
	for _, args := range [][]string{
		{"eval", "baseline", "-h"},
		{"eval", "baseline", "accept", "-h"},
		{"eval", "baseline", "check", "-h"},
	} {
		stdio, out, errOut := newTestIO("", nil)
		if code := RunWith(App{IO: stdio, StorageDir: storage}, args); code != 0 {
			t.Errorf("%v: exit = %d, stderr = %q", args, code, errOut.String())
		}
		if !strings.Contains(out.String(), "usage: argentgo eval baseline") {
			t.Errorf("%v: stdout 缺少用法: %s", args, out.String())
		}
	}

	// 参数错误：均应退出非 0。
	for _, args := range [][]string{
		{"eval", "baseline"},
		{"eval", "baseline", "bogus"},
		{"eval", "baseline", "check"},
		{"eval", "baseline", "check", resultsPath, resultsPath},
		{"eval", "baseline", "accept"},
		{"eval", "baseline", "accept", "--note"},
		{"eval", "baseline", "accept", resultsPath, resultsPath},
	} {
		stdio, _, errOut := newTestIO("", nil)
		if code := RunWith(App{IO: stdio, StorageDir: storage}, args); code == 0 {
			t.Errorf("%v: exit = 0, want non-zero", args)
		}
		if errOut.Len() == 0 {
			t.Errorf("%v: stderr 为空", args)
		}
	}
}

// TestEvalUsageMentionsBaseline 确保主 eval 用法提示含 baseline 前缀说明。
func TestEvalUsageMentionsBaseline(t *testing.T) {
	var out strings.Builder
	evalUsage(&out)
	if !strings.Contains(out.String(), "eval baseline accept") || !strings.Contains(out.String(), "eval baseline check") {
		t.Fatalf("eval usage 未提及 baseline 子命令: %s", out.String())
	}
}

// TestBaselineAcceptIsOutOfBand 证明 accept/check 不需要注入 app.Eval，
// 与 compare/ab 一样是纯带外前缀；默认 eval 路径不受影响。
func TestBaselineAcceptIsOutOfBand(t *testing.T) {
	storage, resultsPath := baselineFixture(t)
	writeBaselineResults(t, resultsPath, []bool{true, true, false, true})
	// App.Eval 为 nil，accept 只依赖 storageDir 与文件路径。
	acceptBaseline(t, storage, resultsPath)
	code, out, errOut := checkBaseline(t, storage, resultsPath)
	if code != 0 || !strings.Contains(out, "状态: stable") {
		t.Fatalf("out-of-band check 失败: exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}
