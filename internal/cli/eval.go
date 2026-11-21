package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/eval"
)

// evalScenario 是一个脚本化的 eval 用例。
type evalScenario struct {
	Prompt     string `json:"prompt"`
	ExpectTool string `json:"expect_tool"`
	ExpectText string `json:"expect_text"`
}

// runEval 实现 `eval <file>`：把每个场景交给注入的 EvalRunner 运行，
// 并对工具使用和回答文本评分。
func runEval(app App, ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		evalUsage(app.IO.Out)
		return 0
	}

	// compare 是 eval 子命令的带外前缀：读取两份 Result 向量文件（基线、候选），
	// 复用 eval 的配对统计原语输出中文 verdict。与 --live 一样不进 flagSurface/parity，
	// 手写扫描并分派；默认 eval 路径与 compare 路径互不影响。
	if len(args) > 0 && args[0] == "compare" {
		return runEvalCompare(app, args[1:])
	}

	// ab 是 eval 子命令的另一个带外前缀：读取两臂 Result 向量与可选的指纹映射，
	// 调用纯函数 eval.RunVariantAB 按场景指纹配对并输出中文 verdict。与 compare
	// 一样不进 flagSurface/parity，手写扫描并分派，不依赖 app.Eval。
	if len(args) > 0 && args[0] == "ab" {
		return runEvalAB(app, args[1:])
	}

	// --live 是 eval 子命令的带外前缀：它不进 flagSurface/parity，只在
	// 这里手写扫描并分派给 LiveEvalSuiteRunner。默认 eval 路径完全不变。
	live := false
	if len(args) > 0 && args[0] == "--live" {
		live = true
		args = args[1:]
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		evalUsage(app.IO.Err)
		return 2
	}
	path := args[0]
	if app.Eval == nil {
		fmt.Fprintln(app.IO.Err, "error: eval runner not configured")
		return 1
	}

	if live {
		liveSuite, ok := app.Eval.(LiveEvalSuiteRunner)
		if !ok {
			fmt.Fprintln(app.IO.Err, "error: eval runner does not support --live (live model runs)")
			return 1
		}
		passed, failed, err := liveSuite.RunSuiteLive(ctx, path, app.IO.Out)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: %s: %v\n", path, err)
			return 1
		}
		if failed > 0 {
			return 1
		}
		_ = passed
		return 0
	}

	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if suite, ok := app.Eval.(EvalSuiteRunner); ok {
		passed, failed, err := suite.RunSuite(ctx, args[0], app.IO.Out)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
			return 1
		}
		if failed > 0 {
			return 1
		}
		_ = passed
		return 0
	}
	scenarios, err := parseEvalFile(data)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
		return 1
	}
	if len(scenarios) == 0 {
		fmt.Fprintf(app.IO.Out, "no scenarios in %s\n", args[0])
		return 0
	}

	failures := 0
	runner, ok := app.Eval.(EvalRunner)
	if !ok {
		fmt.Fprintln(app.IO.Err, "error: eval runner does not support single scenarios")
		return 1
	}
	for i, scenario := range scenarios {
		report, err := runner.RunScenario(ctx, scenario.Prompt)
		if err != nil {
			fmt.Fprintf(app.IO.Out, "FAIL %d %q: runner error: %v\n", i+1, scenario.Prompt, err)
			failures++
			continue
		}
		if reason := gradeScenario(scenario, report); reason != "" {
			fmt.Fprintf(app.IO.Out, "FAIL %d %q: %s\n", i+1, scenario.Prompt, reason)
			failures++
			continue
		}
		fmt.Fprintf(app.IO.Out, "ok   %d %q\n", i+1, scenario.Prompt)
	}
	fmt.Fprintf(app.IO.Out, "%d/%d scenarios passed\n", len(scenarios)-failures, len(scenarios))
	if failures > 0 {
		return 1
	}
	return 0
}

func gradeScenario(scenario evalScenario, report EvalReport) string {
	if scenario.ExpectText != "" && !strings.Contains(report.Text, scenario.ExpectText) {
		return fmt.Sprintf("text does not contain %q (got %q)", scenario.ExpectText, clipText(report.Text, 80))
	}
	if scenario.ExpectTool != "" && !hasTool(report.Tools, scenario.ExpectTool) {
		return fmt.Sprintf("tool %q was not called (called: %s)", scenario.ExpectTool, joinOrNone(report.Tools))
	}
	return ""
}

func hasTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func joinOrNone(tools []string) string {
	if len(tools) == 0 {
		return "none"
	}
	return strings.Join(tools, ", ")
}

func clipText(text string, limit int) string {
	fields := strings.Fields(text)
	joined := strings.Join(fields, " ")
	if len(joined) <= limit {
		return joined
	}
	return joined[:limit-1] + "…"
}

func parseEvalFile(data []byte) ([]evalScenario, error) {
	var scenarios []evalScenario
	if err := json.Unmarshal(data, &scenarios); err == nil {
		return scenarios, nil
	}
	var document struct {
		Scenarios []evalScenario `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if document.Scenarios == nil {
		return nil, fmt.Errorf("expected a JSON array of scenarios or {\"scenarios\": [...]}")
	}
	return document.Scenarios, nil
}

func evalUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval [--live] <file.json>
       argentgo eval compare <baseline-results.json> <candidate-results.json>
       argentgo eval ab <baseline-results.json> <candidate-results.json> [fingerprints-a.json fingerprints-b.json]

The file holds [{"prompt": "...", "expect_tool": "...", "expect_text": "..."}],
or {"scenarios": [...]}. Exit code is non-zero when any scenario fails.
With --live, the suite is dispatched to a live-model runner instead of the
scripted grader; this prefix is out-of-band and not a global flag.
With compare, two Result vectors are compared with paired statistics; small
samples (n < 30) are reported as insufficient instead of claiming improvement.
With ab, two arms are paired by scenario fingerprint (name -> fingerprint maps,
usually produced by eval.ScenarioFingerprint) before the same paired statistics
run; omit both fingerprint files to pair by scenario name. Both prefixes are
out-of-band and not global flags.
`)
}

// runEvalCompare 实现 `eval compare <a> <b>`：读取两份 Result 向量并调用纯函数
// eval.CompareResults 输出中文 verdict。它是带外前缀，不依赖 app.Eval。
func runEvalCompare(app App, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		evalCompareUsage(app.IO.Out)
		return 0
	}
	if len(args) != 2 {
		evalCompareUsage(app.IO.Err)
		return 2
	}
	baseline, err := loadResultVector(args[0])
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
		return 1
	}
	candidate, err := loadResultVector(args[1])
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[1], err)
		return 1
	}
	verdict, err := eval.CompareResults(baseline, candidate)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprint(app.IO.Out, verdict)
	return 0
}

// loadResultVector 从 JSON 文件读取一份 Result 向量，接受裸数组或 {"results": [...]}。
// Result 无 json 标签的字段按 Go 字段名大小写不敏感匹配，因此只需 "passed" 等关键字段。
func loadResultVector(path string) ([]eval.Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var results []eval.Result
	if err := json.Unmarshal(data, &results); err == nil && results != nil {
		return results, nil
	}
	var document struct {
		Results []eval.Result `json:"results"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if document.Results == nil {
		return nil, fmt.Errorf("expected a JSON array of results or {\"results\": [...]}")
	}
	return document.Results, nil
}

func evalCompareUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval compare <baseline-results.json> <candidate-results.json>

Compares two Result vectors (same scenarios, baseline vs candidate) with paired
statistics. Small samples (n < 30) are reported as 样本不足/不显著 instead of a
promotion conclusion. Each file is a JSON array of results or {"results": [...]}.
`)
}

// runEvalAB 实现 `eval ab <a> <b> [fpA fpB]`：读取两臂 Result 向量与可选的
// 「场景名 → 指纹」映射，调用纯函数 eval.RunVariantAB 按指纹配对后出中文 verdict。
// 它是带外前缀，不依赖 app.Eval。指纹文件缺省时两臂退化为按场景名配对。
func runEvalAB(app App, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		evalABUsage(app.IO.Out)
		return 0
	}
	if len(args) != 2 && len(args) != 4 {
		evalABUsage(app.IO.Err)
		return 2
	}
	baseline, err := loadResultVector(args[0])
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
		return 1
	}
	candidate, err := loadResultVector(args[1])
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[1], err)
		return 1
	}
	var fingerprintsA, fingerprintsB map[string]string
	if len(args) == 4 {
		fingerprintsA, err = loadFingerprintMap(args[2])
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[2], err)
			return 1
		}
		fingerprintsB, err = loadFingerprintMap(args[3])
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[3], err)
			return 1
		}
	}
	report := eval.RunVariantAB(baseline, candidate, fingerprintsA, fingerprintsB)
	fmt.Fprint(app.IO.Out, report.Text)
	return 0
}

// loadFingerprintMap 从 JSON 对象读取「场景名 → 指纹」映射，
// 也接受 {"fingerprints": {...}} 包装形式。
func loadFingerprintMap(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err == nil && m != nil {
		return m, nil
	}
	var document struct {
		Fingerprints map[string]string `json:"fingerprints"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if document.Fingerprints == nil {
		return nil, fmt.Errorf("expected a JSON object of name-to-fingerprint or {\"fingerprints\": {...}}")
	}
	return document.Fingerprints, nil
}

func evalABUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval ab <baseline-results.json> <candidate-results.json> [fingerprints-a.json fingerprints-b.json]

Pairs two arms by scenario fingerprint, then runs paired statistics (same as
eval compare) on the aligned results. Each fingerprint file is a JSON object
mapping scenario name to fingerprint, e.g. {"读取笔记": "<sha256 hex>"}; omit
both files to pair by scenario name. Small samples (n < 30) are reported as
样本不足/不显著 instead of a promotion conclusion. This prefix is out-of-band
and not a global flag.
`)
}
