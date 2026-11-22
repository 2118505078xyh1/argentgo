package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	// quality 是 eval 子命令的带外前缀：读取一份 model-quality.json（脚本化长任务
	// 夹具），用纯函数核对步骤顺序、汇总完成率与测量质量，全程不联网、不需要模型。
	// 与 compare/ab/orchestration 一样手写分派，默认 eval 路径完全不变。
	if len(args) > 0 && args[0] == "quality" {
		return runEvalQuality(app, args[1:])
	}

	// baseline 是 eval 子命令的带外前缀：把已接受的评分结果固化为一份可落盘的
	// 基线 JSON，并用候选结果做三态回归门。与 compare/ab/orchestration 一样
	// 不进 flagSurface/parity，手写扫描并分派；默认 eval 路径完全不变。
	if len(args) > 0 && args[0] == "baseline" {
		return runEvalBaseline(app, args[1:])
	}

	// history 是 eval 子命令的带外前缀：从 git 历史抽取任务，在隔离 worktree
	// 中用真实模型执行并评分。与 compare/ab/orchestration 一样手写分派，
	// 默认 eval 路径完全不变。
	if len(args) > 0 && args[0] == "history" {
		return runEvalHistory(app, ctx, args[1:])
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
       argentgo eval quality <model-quality.json>
       argentgo eval baseline accept <result.json> [--note <text>]
       argentgo eval baseline check <result.json>
       argentgo eval history <repo> [--max-tasks <n>] [--since <ref>]

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
With quality, a model-quality.json of scripted long tasks is graded offline
(no network/API key): step order is checked, task completion rate and measurement
quality (sample count / significance / blind-spot warnings) are printed.
This prefix is out-of-band and not a global flag.
With baseline accept, the Result vector is frozen as an accepted baseline JSON at
<storage>/eval/baseline.json (--note attaches an optional free-text remark).
With baseline check, a candidate vector is graded against that baseline and the
tri-state result (stable / improved / regression) is printed; only regression
exits non-zero, so it can be used as a CI gate. Both are out-of-band prefixes.
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

// runEvalQuality 实现 `eval quality <model-quality.json>`：加载脚本化长任务夹具，
// 用纯函数核对步骤顺序并汇总完成率与测量质量。全程不联网、不需要模型 runner，
// 因此即使没有任何 provider 配置也能运行，输出稳定可比对。
func runEvalQuality(app App, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		evalQualityUsage(app.IO.Out)
		return 0
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		evalQualityUsage(app.IO.Err)
		return 2
	}
	doc, err := eval.LoadModelQualityFile(args[0])
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
		return 1
	}
	report := eval.RunModelQuality(doc)
	fmt.Fprintf(app.IO.Out, "%s: %d/%d tasks completed (completion_rate=%.3f)\n",
		report.Name, report.Completed, report.Tasks, report.CompletionRate)
	q := report.Quality
	fmt.Fprintf(app.IO.Out, "samples: %d  reason: %s  significant: %t\n",
		q.SampleCount, q.IncompleteReason, q.Significant)
	for _, warning := range q.Warnings {
		fmt.Fprintf(app.IO.Out, "warning: %s\n", warning)
	}
	// 离线夹具不要求真实模型通过率达标；质量报告本身是输出，不因样本不足失败。
	return 0
}

func evalQualityUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval quality <model-quality.json>

Grades a model-quality.json of scripted long tasks offline: each task's expected
steps must appear in order in its transcript. Prints task completion rate and
measurement quality (sample count / significance / blind-spot warnings). No
network or API key is needed. This prefix is out-of-band and not a global flag.
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

// ---- eval baseline 子命令：复用 internal/eval 的已接受基线纯函数 ----
//
// 与 compare/ab/orchestration 一样，baseline 是 eval 的带外前缀：不进
// flagSurface/parity，也不进 subcommandOrder/completion，手写扫描并分派，
// 默认 eval 路径零变化。基线落盘路径由 storageDir 派生，accept/check 共用，
// 因此 check 只需候选结果文件一个参数即可定位基线。

// baselineStateEpsilon 是区分「稳定」与「提升」的浮点容差，避免序列化/舍入噪声
// 把持平误报为提升。注意真正的回归阈值由 eval.CheckRegression 掌管，这里只做
// 已接受状态下的数据展示细分，不参与回归判定。
const baselineStateEpsilon = 1e-9

// baselinePath 返回已接受基线的唯一落盘路径：<storage>/eval/baseline.json。
func baselinePath(app App) string {
	return filepath.Join(app.storageDir(), "eval", "baseline.json")
}

// runEvalBaseline 分派 `eval baseline accept|check`。
func runEvalBaseline(app App, args []string) int {
	if len(args) == 0 {
		evalBaselineUsage(app.IO.Err)
		return 2
	}
	switch args[0] {
	case "accept":
		return runEvalBaselineAccept(app, args[1:])
	case "check":
		return runEvalBaselineCheck(app, args[1:])
	case "-h", "--help", "help":
		evalBaselineUsage(app.IO.Out)
		return 0
	default:
		fmt.Fprintf(app.IO.Err, "error: 未知的 baseline 子命令 %q（accept 或 check）\n", args[0])
		evalBaselineUsage(app.IO.Err)
		return 2
	}
}

// runEvalBaselineAccept 实现 `eval baseline accept <result.json> [--note X]`：
// 读取结果向量，调用 eval.BaselineFromResults + BaselineToJSON 固化为基线，
// 写入 <storage>/eval/baseline.json（父目录不存在则创建）。
func runEvalBaselineAccept(app App, args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			evalBaselineAcceptUsage(app.IO.Out)
			return 0
		}
	}
	path, note, err := parseBaselineAcceptArgs(args)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %v\n", err)
		evalBaselineAcceptUsage(app.IO.Err)
		return 2
	}
	results, err := loadResultVector(path)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", path, err)
		return 1
	}
	// 固化与序列化直接复用 accepted_baseline.go 的纯函数，不改其逻辑。
	baseline := eval.BaselineFromResults(results, note)
	data, err := eval.BaselineToJSON(baseline)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: 编码基线失败: %v\n", err)
		return 1
	}
	dst := baselinePath(app)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fmt.Fprintf(app.IO.Err, "error: 创建基线目录失败: %v\n", err)
		return 1
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", dst, err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "已接受基线写入: %s\n", dst)
	fmt.Fprintf(app.IO.Out, "场景数: %d  完成率: %.4f  指纹: %s\n", len(results), baseline.Score, shortBaselineFingerprint(baseline.Fingerprint))
	if note == "" {
		fmt.Fprintln(app.IO.Out, "备注: （无）")
	} else {
		fmt.Fprintf(app.IO.Out, "备注: %s\n", note)
	}
	return 0
}

// parseBaselineAcceptArgs 解析 accept 参数：恰好一个结果向量路径，可选
// --note <text> 或 --note=<text>。备注原样返回（允许中文，不裁剪）。
func parseBaselineAcceptArgs(args []string) (string, string, error) {
	var paths []string
	note := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--note":
			if i+1 >= len(args) {
				return "", "", errors.New("--note 缺少备注文本")
			}
			note = args[i+1]
			i++
		case strings.HasPrefix(arg, "--note="):
			note = strings.TrimPrefix(arg, "--note=")
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("未知选项 %q", arg)
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) != 1 {
		return "", "", fmt.Errorf("需要恰好一个结果向量路径，得到 %d 个", len(paths))
	}
	return paths[0], note, nil
}

// runEvalBaselineCheck 实现 `eval baseline check <result.json>`：读取候选结果
// 向量与 <storage>/eval/baseline.json，调用 eval.CheckRegression 做三态判定并
// 输出到 stdout；仅回归时退出码非 0，供 CI 作门禁。
func runEvalBaselineCheck(app App, args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			evalBaselineCheckUsage(app.IO.Out)
			return 0
		}
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		evalBaselineCheckUsage(app.IO.Err)
		return 2
	}
	candidate, err := loadResultVector(args[0])
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
		return 1
	}
	path := baselinePath(app)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(app.IO.Err, "error: 基线文件不存在: %s；请先运行 `argentgo eval baseline accept <result.json>`\n", path)
		} else {
			fmt.Fprintf(app.IO.Err, "error: 读取基线失败: %s: %v\n", path, err)
		}
		return 1
	}
	baseline, err := eval.BaselineFromJSON(data)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: 基线文件无效: %s: %v\n", path, err)
		return 1
	}
	verdict := eval.CheckRegression(baseline, candidate)
	rate := baselineCompletionRate(candidate)
	state := baselineState(verdict, rate, baseline.Score)
	fmt.Fprintf(app.IO.Out, "基线文件: %s\n", path)
	fmt.Fprintf(app.IO.Out, "基线分数: %.4f  候选完成率: %.4f  样本数: %d\n", baseline.Score, rate, len(candidate))
	fmt.Fprintf(app.IO.Out, "状态: %s\n", state)
	fmt.Fprintf(app.IO.Out, "判定: %s\n", baselineVerdictText(verdict))
	if verdict == eval.RegressionRegressed {
		return 1
	}
	return 0
}

// baselineCompletionRate 计算候选结果向量的完成率，空向量按 0 处理。
// 它只用于 check 的展示细分；真正的回归阈值由 eval.CheckRegression 掌管。
func baselineCompletionRate(results []eval.Result) float64 {
	if len(results) == 0 {
		return 0
	}
	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	return float64(passed) / float64(len(results))
}

// baselineState 把 CheckRegression 的三态判定与完成率映射为面向用户的
// stable/improved/regression 三态。样本不足作为额外守卫状态单列，不算回归。
func baselineState(verdict eval.RegressionVerdict, rate, score float64) string {
	switch verdict {
	case eval.RegressionRegressed:
		return "regression"
	case eval.RegressionInsufficient:
		return "insufficient"
	default:
		if rate > score+baselineStateEpsilon {
			return "improved"
		}
		return "stable"
	}
}

// baselineVerdictText 把 CheckRegression 的枚举翻译成中文判定说明。
func baselineVerdictText(verdict eval.RegressionVerdict) string {
	switch verdict {
	case eval.RegressionRegressed:
		return "regressed（候选完成率低于基线，发生退化）"
	case eval.RegressionInsufficient:
		return "insufficient（样本不足，无法给出可靠回归结论）"
	default:
		return "accepted（候选完成率不低于基线，无退化）"
	}
}

// shortBaselineFingerprint 把 64 位指纹截短为可读前缀。
func shortBaselineFingerprint(fp string) string {
	if len(fp) > 16 {
		return fp[:16] + "…"
	}
	return fp
}

func evalBaselineUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval baseline accept <result.json> [--note <text>]
       argentgo eval baseline check <result.json>

Freezes a scored Result vector as an accepted baseline at
<storage>/eval/baseline.json, then grades a candidate vector against it with a
tri-state regression gate (stable / improved / regression). Only regression
exits non-zero, so check can be used as a CI gate. Both are out-of-band
prefixes and not global flags.
`)
}

func evalBaselineAcceptUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval baseline accept <result.json> [--note <text>]

Reads a Result vector (array or {"results": [...]}), freezes it as an accepted
baseline and writes it to <storage>/eval/baseline.json. --note attaches an
optional free-text remark (UTF-8, Chinese allowed); --note=<text> also works.
`)
}

func evalBaselineCheckUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval baseline check <result.json>

Reads a candidate Result vector and the accepted baseline at
<storage>/eval/baseline.json, then prints the tri-state verdict to stdout:
stable / improved / regression (insufficient when the sample is too small).
Exit code is non-zero only for regression. Run eval baseline accept first if
the baseline file is missing.
`)
}
