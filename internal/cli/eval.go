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

	// orchestration 是 eval 子命令的带外前缀：读取一份编排场景文档，交给注入的
	// OrchestrationRunner 用真实 agent 循环运行，输出 Total/Passed/Failed 与每条
	// 失败诊断。与 compare/ab 一样不进 flagSurface/parity，手写扫描并分派；
	// 默认 eval 路径完全不变。
	if len(args) > 0 && args[0] == "orchestration" {
		return runEvalOrchestration(app, ctx, args[1:])
	}

	// --live 与 --json 都是 eval 子命令的带外前缀：不进 flagSurface/parity，
	// 只在这里手写扫描。默认 eval 路径、compare 与 ab 路径互不影响。
	//   --live       决定把套件分派给 LiveEvalSuiteRunner 而非脚本化 runner；
	//   --json <path> 只决定"跑完后额外把 Result 向量落盘到 path"，不改分派目标。
	// 两者可同时出现且顺序任意；没有 --json 时一行文件都不会写，默认路径零变化。
	live := false
	jsonPath := ""
prefixLoop:
	for len(args) > 0 {
		switch args[0] {
		case "--live":
			live = true
			args = args[1:]
		case "--json":
			if len(args) < 2 {
				evalUsage(app.IO.Err)
				return 2
			}
			jsonPath = args[1]
			args = args[2:]
		default:
			// 剩下的第一个非前缀参数就是套件文件路径，交给下面的既有校验。
			break prefixLoop
		}
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
		if jsonPath != "" {
			eval.ResetLastReport()
		}
		passed, failed, err := liveSuite.RunSuiteLive(ctx, path, app.IO.Out)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: %s: %v\n", path, err)
			return 1
		}
		if code := exportEvalResults(app, jsonPath, eval.LastReport().Results); code != 0 {
			return code
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
		if jsonPath != "" {
			eval.ResetLastReport()
		}
		passed, failed, err := suite.RunSuite(ctx, args[0], app.IO.Out)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
			return 1
		}
		// RunSuite 只回传计数；Result 向量由 eval.Run 记录在包内，这里取回并落盘。
		if code := exportEvalResults(app, jsonPath, eval.LastReport().Results); code != 0 {
			return code
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
	// 同步构造一份 eval.Report：逐场景评分的产物本来就是 Result 向量，
	// 只是过去它从不离开 CLI。--json 前缀把它落盘，供 compare/ab 直接消费。
	results := make([]eval.Result, 0, len(scenarios))
	for i, scenario := range scenarios {
		result := eval.Result{Index: i + 1, Name: scenario.Prompt}
		report, err := runner.RunScenario(ctx, scenario.Prompt)
		if err != nil {
			result.Reason = fmt.Sprintf("runner error: %v", err)
			fmt.Fprintf(app.IO.Out, "FAIL %d %q: runner error: %v\n", i+1, scenario.Prompt, err)
			failures++
			results = append(results, result)
			continue
		}
		result.Tools = append([]string(nil), report.Tools...)
		result.Text = report.Text
		if reason := gradeScenario(scenario, report); reason != "" {
			result.Reason = reason
			fmt.Fprintf(app.IO.Out, "FAIL %d %q: %s\n", i+1, scenario.Prompt, reason)
			failures++
			results = append(results, result)
			continue
		}
		result.Passed = true
		fmt.Fprintf(app.IO.Out, "ok   %d %q\n", i+1, scenario.Prompt)
		results = append(results, result)
	}
	fmt.Fprintf(app.IO.Out, "%d/%d scenarios passed\n", len(scenarios)-failures, len(scenarios))
	if code := exportEvalResults(app, jsonPath, results); code != 0 {
		return code
	}
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
	fmt.Fprint(w, `usage: argentgo eval [--live] [--json <path>] <file.json>
       argentgo eval compare <baseline-results.json> <candidate-results.json>
       argentgo eval ab <baseline-results.json> <candidate-results.json> [fingerprints-a.json fingerprints-b.json]
       argentgo eval orchestration <orchestration.json>

The file holds [{"prompt": "...", "expect_tool": "...", "expect_text": "..."}],
or {"scenarios": [...]}. Exit code is non-zero when any scenario fails.
With --live, the suite is dispatched to a live-model runner instead of the
scripted grader; this prefix is out-of-band and not a global flag.
With --json <path>, the suite's Result vector is additionally written to <path>
as {"results": [...]}, ready to feed eval compare / eval ab without hand-writing
vectors. Without the prefix no file is written and the default output is unchanged.
It combines with --live in either order and is also out-of-band, not a global flag.
With compare, two Result vectors are compared with paired statistics; small
samples (n < 30) are reported as insufficient instead of claiming improvement.
With ab, two arms are paired by scenario fingerprint (name -> fingerprint maps,
usually produced by eval.ScenarioFingerprint) before the same paired statistics
run; omit both fingerprint files to pair by scenario name. Both prefixes are
out-of-band and not global flags.
With orchestration, a scripted multi-turn suite runs through the real agent loop
and the real task tool (routed fake model, no network/API key); Total/Passed/Failed
and one FAIL line per failed scenario are printed, and any failure exits non-zero.
This prefix is also out-of-band and not a global flag.
`)
}

// runEvalOrchestration 实现 `eval orchestration <file>`：读取编排场景文档，交给
// 注入的 OrchestrationRunner 用真实 agent 循环运行，输出 Total/Passed/Failed 与
// 每条失败诊断。任一场景失败或加载出错时退出非 0。它是带外前缀，默认 eval 路径
// 不依赖此接口。
func runEvalOrchestration(app App, ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		evalOrchestrationUsage(app.IO.Out)
		return 0
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		evalOrchestrationUsage(app.IO.Err)
		return 2
	}
	runner, ok := app.Eval.(OrchestrationRunner)
	if !ok {
		fmt.Fprintln(app.IO.Err, "error: eval runner does not support orchestration")
		return 1
	}
	report, err := runner.RunOrchestration(ctx, args[0])
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "Total: %d  Passed: %d  Failed: %d\n", report.Total, report.Passed, report.Failed)
	for _, failure := range report.Failures {
		fmt.Fprintf(app.IO.Out, "FAIL [%s] %s: %s\n", failure.ID, failure.Name, failure.Reason)
	}
	fmt.Fprintf(app.IO.Out, "%d/%d orchestration scenarios passed\n", report.Passed, report.Total)
	if report.Failed > 0 {
		return 1
	}
	return 0
}

func evalOrchestrationUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval orchestration <orchestration.json>

Runs a scripted multi-turn orchestration suite through the real agent loop and
the real task tool, using a routed fake model (no network, no API key). Prints
Total/Passed/Failed and one FAIL line per failed scenario; exit code is non-zero
when any scenario fails. This prefix is out-of-band and not a global flag.
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
// Result 各字段有显式小写 json 标签，手写向量只需 "passed" / "index" 等关键字段。
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

// writeResultVector 把一份 Results 落盘为 {"results": [...]}，产物可直接被
// loadResultVector 读回，也能喂给 `eval compare` / `eval ab`。
func writeResultVector(path string, results []eval.Result) error {
	if results == nil {
		results = []eval.Result{}
	}
	data, err := json.MarshalIndent(struct {
		Results []eval.Result `json:"results"`
	}{Results: results}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// exportEvalResults 只在 --json 前缀显式出现时落盘；没有前缀时它是空操作，
// 因此默认 eval 路径的文件系统与输出都零变化。写盘失败会让命令返回非零，
// 但不吞掉既有 TAP 输出。
func exportEvalResults(app App, jsonPath string, results []eval.Result) int {
	if jsonPath == "" {
		return 0
	}
	if err := writeResultVector(jsonPath, results); err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", jsonPath, err)
		return 1
	}
	return 0
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
