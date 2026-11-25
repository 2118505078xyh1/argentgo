package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/argentgo/internal/llm"
)

// Workspace 是 RunFunc 运行一个场景所需的一切。
type Workspace struct {
	// Dir 是种入的临时目录，必须是循环的 cwd。
	Dir    string
	Prompt string
	System string
	Model  string
	// Chatter 是该场景的脚本化 provider。直接与 provider 对话的 runner（而非接受 provider 的循环）使用它。
	// 直接与 provider 对话的 runner（而非接受 provider 的循环）使用它。
	Chatter llm.Chatter
}

// Trace 是一次运行报告自身的內容。
type Trace struct {
	// Tools 是按顺序运行过的工具名。
	Tools []string
	// Text 是最终助手答案。
	Text string
	// Usage 是模型报告的累计计费用量。
	Usage llm.Usage
	// Stopped 报告结束运行的不是模型：轮数上限、策略拦截、取消。
	// 轮数上限、策略拦截、取消。
	Stopped bool
}

// RunFunc 执行一个场景。harness 种入工作区并评分结果；调用方拥有循环，因此本包从不导入 agent。
// 结果；调用方拥有循环，因此本包从不导入 agent。
//
// 返回 context.Canceled 或 context.DeadlineExceeded 被视为正常取消而非失败。
// 取消而非失败。
type RunFunc func(ctx context.Context, scenario Scenario, workspace Workspace) (Trace, error)

// Options 配置一次套件运行。
type Options struct {
	// Verbose 为每个场景添加一行 TAP 诊断。
	Verbose bool
	// Live 在报告末尾追加完成率、token 与轮次汇总行，供真模型跑批使用。
	// 默认 false 时 TAP 输出逐字节不变；开启后仅在结尾新增指标行，
	// 不改任何场景行（结果里不含 provider 凭据，因此指标行也不会泄漏 API key）。
	Live bool
	// Out 接收类似 TAP 的报告。默认为 os.Stdout。
	Out io.Writer
	// Keep 保留临时目录，用于调试失败。
	Keep bool
	// ChatterFactory 为每个场景构造真实 provider。nil 时 runOne 沿用脚本化 provider
	// （默认路径逐字节不变）。非 nil 时每个场景调用一次，返回的 Chatter 被注入
	// Workspace.Chatter，并由计数包装器统计轮数，使 Expect.Turns 对真实 provider 同样有效。
	ChatterFactory func(Scenario) llm.Chatter
	// WorkspaceFactory 为场景创建隔离工作区。nil 时 runOne 沿用 os.MkdirTemp，
	// 默认路径逐字节不变。非 nil 时返回工作目录与清理函数；Keep 为真时不调用
	// 清理函数（保留现场供调试）。git 历史任务用它把工作区放进临时 worktree。
	WorkspaceFactory func(Scenario) (dir string, cleanup func(), err error)
}

// Result 是一个场景的评分结果。
//
// 所有字段都带 JSON 标签：Result 向量既是内存中的评分产物，也是 `eval --json`
// 落盘、再被 `eval compare` / `eval ab` 读回的测量载体。显式小写标签让外部
// 解析不再依赖 Go 字段名大小写不敏感匹配（R51 研究 #88）。
type Result struct {
	Index   int      `json:"index"`
	Name    string   `json:"name"`
	Passed  bool     `json:"passed"`
	Reason  string   `json:"reason"`
	Tools   []string `json:"tools"`
	Text    string   `json:"text"`
	Turns   int      `json:"turns"`
	Stopped bool     `json:"stopped"`
	// Usage 是该场景累计的计费 token；由 Trace.Usage 逐项拷贝而来，
	// 供 Live 报告的 token 汇总使用（JSON 标签便于外部持久化）。
	Usage llm.Usage `json:"usage,omitempty"`
	// Duration 是执行场景所花的挂钟时间。它是
	// Duration 是执行场景所花的挂钟时间。它是 AggregateScores 消费的诊断信号；从不影响评分。
	Duration time.Duration `json:"duration"`
}

// Report 汇总一次套件运行。Results 的标签使 Report 也可直接落盘为
// {"passed": n, "failed": n, "results": [...]}，而 CLI 只导出其中的 results。
type Report struct {
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Results []Result `json:"results"`
}

// lastReportMu 保护 lastReport。它只服务于带外的测量导出（CLI 的 --json 前缀），
// 不参与评分与 TAP 输出，因此默认路径逐字节不变。
var (
	lastReportMu sync.Mutex
	lastReport   Report
)

// ResetLastReport 清空最近一次报告。CLI 在 --json 跑批开始前调用，避免读回
// 上一次运行（或同进程内其它套件）的残留结果。
func ResetLastReport() {
	lastReportMu.Lock()
	lastReport = Report{}
	lastReportMu.Unlock()
}

// LastReport 返回最近一次 Run 生成的 Report 副本。
//
// RunSuite / RunSuiteLive 的接口只回传 (passed, failed, err)，CLI 无法从返回值
// 拿到 Result 向量；这个读取口让 `eval --json` 能在套件跑完后取回 Results 落盘，
// 从而闭合 compare/ab 的测量回路。返回值是浅拷贝的切片，调用方只读取即可。
// 若同一进程并发跑多个套件，语义是"最近完成的那个"，CLI 单跑批场景不受影响。
func LastReport() Report {
	lastReportMu.Lock()
	defer lastReportMu.Unlock()
	cp := lastReport
	cp.Results = append([]Result(nil), lastReport.Results...)
	return cp
}

// rememberReport 在 Run 收尾时记录本次报告，供 LastReport 读取。
func rememberReport(report Report) {
	lastReportMu.Lock()
	lastReport = report
	lastReportMu.Unlock()
}

// Run 执行每个场景并打印类似 TAP 的报告。
//
// 任一场景失败时返回非 nil 错误；报告仍然完整，因此调用方可以同时打印两者。
// 因此调用方可以同时打印两者。
func Run(ctx context.Context, scenarios []Scenario, run RunFunc, opts Options) (Report, error) {
	if run == nil {
		return Report{}, errors.New("eval: RunFunc is nil")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	report := Report{Results: make([]Result, 0, len(scenarios))}
	fmt.Fprintf(out, "1..%d\n", len(scenarios))
	for i, scenario := range scenarios {
		result := runOne(ctx, i+1, scenario, run, opts)
		report.Results = append(report.Results, result)
		if result.Passed {
			report.Passed++
			fmt.Fprintf(out, "ok %d - %s\n", result.Index, result.Name)
		} else {
			report.Failed++
			fmt.Fprintf(out, "not ok %d - %s: %s\n", result.Index, result.Name, oneLine(result.Reason))
		}
		if opts.Verbose {
			fmt.Fprintf(out, "#   tools=%v turns=%d stopped=%t text=%q\n",
				result.Tools, result.Turns, result.Stopped, clip(result.Text, 120))
		}
	}
	fmt.Fprintf(out, "# %d passed, %d failed\n", report.Passed, report.Failed)
	if opts.Live {
		// 指标行只在报告末尾追加；默认路径到此为止，输出逐字节不变。
		fmt.Fprint(out, liveMetricsReport(report.Results))
	}
	// 无论是否失败都记录完整报告：带外消费者（--json）需要在失败时也能拿到
	// 逐场景结果做配对诊断。这一步只写包内状态，不改变任何输出。
	rememberReport(report)
	if report.Failed > 0 {
		return report, fmt.Errorf("eval: %d of %d scenarios failed", report.Failed, len(scenarios))
	}
	return report, nil
}

// liveMetricsReport 渲染真模型跑批的指标行：完成率、token 汇总与轮次汇总，
// 后接逐场景的 token/耗时诊断行与总耗时。
//
// 它只读 results，不修改入参；行内容全部由数字、时长与固定标签组成，不含模型输出或
// provider 凭据。逐场景行用序号而非场景名，避免把可能含 key 的用户文本带进指标行。
// 空 results 的完成率由 ScenarioCompletionRate 守卫为 0，不会出现 NaN。
func liveMetricsReport(results []Result) string {
	var usage llm.Usage
	turns := 0
	var totalDuration time.Duration
	for _, result := range results {
		usage.PromptTokens += result.Usage.PromptTokens
		usage.CompletionTokens += result.Usage.CompletionTokens
		usage.CachedTokens += result.Usage.CachedTokens
		usage.CacheWriteTokens += result.Usage.CacheWriteTokens
		turns += result.Turns
		totalDuration += result.Duration
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# completion rate: %.3f\n", ScenarioCompletionRate(results))
	fmt.Fprintf(&b, "# tokens: prompt=%d completion=%d cached=%d cache_write=%d total=%d\n",
		usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, usage.CacheWriteTokens, usage.TotalTokens())
	fmt.Fprintf(&b, "# turns: %d\n", turns)
	// 以下为追加行：既有三行保持逐字节不变，默认（非 Live）路径完全不受影响。
	for i, result := range results {
		u := result.Usage
		fmt.Fprintf(&b, "# scenario %d: tokens prompt=%d completion=%d cached=%d cache_write=%d total=%d duration=%s\n",
			i+1, u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.CacheWriteTokens, u.TotalTokens(), result.Duration)
	}
	fmt.Fprintf(&b, "# total duration: %s\n", totalDuration)
	return b.String()
}

// scoredChatter 是 runOne 评分所需的 provider 能力：除了对话本身，还要能报告
// 调用次数与请求期望违例。Scripted 原生满足；注入的真实 provider 由 countingChatter 补齐。
type scoredChatter interface {
	llm.Chatter
	Calls() int
	Violations() []string
}

// countingChatter 包装任意 llm.Chatter，仅统计 Chat 的调用次数（仿 Scripted.Calls），
// 与 Violations 的空实现一起满足 scoredChatter。真实 provider 没有脚本化的
// expect_request_contains 概念，因此其违规列表恒为空。
type countingChatter struct {
	llm.Chatter
	mu    sync.Mutex
	calls int
}

var _ scoredChatter = (*countingChatter)(nil)

func (c *countingChatter) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.Chatter.Chat(ctx, model, messages, tools, onText)
}

func (c *countingChatter) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingChatter) Violations() []string { return nil }

// LiveChatterAdapter 把一个已构造好的 llm.Chatter 适配为 ChatterFactory：每个场景都复用同一实例。
// 若调用方需要为每个场景创建独立会话状态的 provider，可直接传入自定义的
// func(Scenario) llm.Chatter，无需经过此辅助。
func LiveChatterAdapter(ch llm.Chatter) func(Scenario) llm.Chatter {
	return func(Scenario) llm.Chatter { return ch }
}

// runOne 种入临时工作区，在其中运行场景并评分。
func runOne(ctx context.Context, index int, scenario Scenario, run RunFunc, opts Options) Result {
	result := Result{Index: index, Name: scenario.Name}

	var dir string
	var cleanup func()
	var err error
	if opts.WorkspaceFactory != nil {
		dir, cleanup, err = opts.WorkspaceFactory(scenario)
		// 工厂未提供清理函数时退回删除目录，避免遗留临时文件。
		if cleanup == nil && dir != "" {
			cleanup = func() { _ = os.RemoveAll(dir) }
		}
	} else {
		dir, err = os.MkdirTemp("", fmt.Sprintf("argent-eval-%02d-", index))
		cleanup = func() { _ = os.RemoveAll(dir) }
	}
	if err != nil {
		result.Reason = "cannot create the scratch directory: " + err.Error()
		return result
	}
	if !opts.Keep && cleanup != nil {
		defer cleanup()
	}
	if err := seed(dir, scenario.Files); err != nil {
		result.Reason = err.Error()
		return result
	}

	var provider scoredChatter = NewScripted(scenario.Provider, scenario.Repeat)
	if opts.ChatterFactory != nil {
		provider = &countingChatter{Chatter: opts.ChatterFactory(scenario)}
	}
	model := scenario.Model
	if model == "" {
		model = DefaultModel
	}
	workspace := Workspace{
		Dir:     dir,
		Prompt:  scenario.Prompt,
		System:  scenario.System,
		Model:   model,
		Chatter: provider,
	}

	started := time.Now()
	trace, runErr := callRun(ctx, run, scenario, workspace)
	result.Duration = time.Since(started)
	result.Tools = append([]string(nil), trace.Tools...)
	result.Text = trace.Text
	result.Stopped = trace.Stopped
	result.Usage = trace.Usage
	result.Turns = provider.Calls()

	observed := observation{calls: provider.Calls(), violations: provider.Violations()}
	switch {
	case runErr == nil:
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		// 被取消的运行是停止，而非 harness 失败。
		trace.Stopped = true
		result.Stopped = true
	default:
		result.Reason = "runner error: " + runErr.Error()
	}
	if result.Reason == "" {
		result.Reason = evaluate(scenario, trace, observed, dir)
	}
	result.Passed = result.Reason == ""
	return result
}

// callRun 把 panic 的 runner 变成失败的场景而非死掉的
// suite.
func callRun(ctx context.Context, run RunFunc, scenario Scenario, workspace Workspace) (trace Trace, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runner panic: %v", recovered)
		}
	}()
	return run(ctx, scenario, workspace)
}

// workspacePath 在临时工作区内解析 fixture 声明的路径。
//
// 场景文件路径文档记载为相对于工作区，harness 承诺其 I/O
// 场景文件路径文档记载为相对于工作区，harness 承诺其 I/O 保持在 runOne 之后移除的一次性目录内。".." 或绝对路径会逃逸
// runOne 之后移除的一次性目录内。".." 或绝对路径会逃逸
// （在 Windows 上 filepath.Join 会把 "../x" 变成 dir 的父级），写入或检查临时目录之外的文件并在 RemoveAll 之后泄漏。应拒绝
// 检查临时目录之外的文件并在 RemoveAll 之后泄漏。应拒绝
// 这些路径，而不是静默突破边界。
func workspacePath(dir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("fixture path is empty")
	}
	rel := filepath.FromSlash(name)
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("fixture path %q is absolute", name)
	}
	target := filepath.Join(dir, rel)
	inside, err := filepath.Rel(dir, target)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path %q escapes the workspace", name)
	}
	return target, nil
}

// seed 将场景的起始文件写入工作区。
func seed(dir string, files map[string]string) error {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		target, err := workspacePath(dir, path)
		if err != nil {
			return fmt.Errorf("seed %q: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("seed %q: %w", path, err)
		}
		if err := os.WriteFile(target, []byte(files[path]), 0o644); err != nil {
			return fmt.Errorf("seed %q: %w", path, err)
		}
	}
	return nil
}

// observation 是 harness 拥有的脚本化 provider 所见内容的视图。
type observation struct {
	calls      int
	violations []string
}

// evaluate 对一次运行评分，所有期望成立时返回 ""。
func evaluate(scenario Scenario, trace Trace, observed observation, dir string) string {
	if len(observed.violations) > 0 {
		return observed.violations[0]
	}
	expect := scenario.Expect
	if expect.ToolCalls != nil && !equalStrings(expect.ToolCalls, trace.Tools) {
		return fmt.Sprintf("expected tools %v, ran %v", expect.ToolCalls, nilOr(trace.Tools))
	}
	for _, needle := range expect.TextContains {
		if !strings.Contains(trace.Text, needle) {
			return fmt.Sprintf("the answer does not contain %q (got %q)", needle, clip(trace.Text, 200))
		}
	}
	for _, needle := range expect.TextNotContains {
		if strings.Contains(trace.Text, needle) {
			return fmt.Sprintf("the answer must not contain %q (got %q)", needle, clip(trace.Text, 200))
		}
	}
	if expect.Turns != nil && *expect.Turns != observed.calls {
		return fmt.Sprintf("expected %d turns, the model was called %d times", *expect.Turns, observed.calls)
	}
	if expect.Stopped != nil && *expect.Stopped != trace.Stopped {
		return fmt.Sprintf("expected stopped=%t, got %t", *expect.Stopped, trace.Stopped)
	}
	if expect.CachedTokens != nil && *expect.CachedTokens != trace.Usage.CachedTokens {
		return fmt.Sprintf("expected %d cached tokens, the model reported %d", *expect.CachedTokens, trace.Usage.CachedTokens)
	}
	// exec 检查是严格选择加入的（AllowExec）。它们在其他检查之后运行于一次性临时工作区；没有该标志时绝不启动任何命令，因此现有 fixture 保持旧行为。
	// 临时工作区在其他检查之后运行；没有该标志时
	// 绝不启动任何命令，因此现有 fixture 保持旧行为。
	if scenario.Check.AllowExec && scenario.Check.Exec != nil {
		check := scenario.Check.Exec
		res := RunExecCheck(dir, *check)
		if !GradeExecCheck(res, *check) {
			return execFailureReason(res, *check)
		}
	}
	return checkWorkspace(scenario.Check, dir)
}

// execFailureReason 将失败的 exec 检查渲染为 TAP 报告内容。
func execFailureReason(res ExecResult, c ExecCheck) string {
	switch {
	case res.TimedOut:
		return fmt.Sprintf("exec check timed out: %q %v", c.Command, c.Args)
	case res.ExitCode == -1 && res.Err != nil:
		return fmt.Sprintf("exec check could not start %q %v: %v", c.Command, c.Args, res.Err)
	case res.ExitCode != c.WantExitCode:
		return fmt.Sprintf("exec check %q %v exited %d, want %d (stderr: %q)",
			c.Command, c.Args, res.ExitCode, c.WantExitCode, clip(res.StderrTail, 160))
	default:
		return fmt.Sprintf("exec check stdout %q does not contain %q", clip(res.StdoutTail, 160), c.WantStdoutContains)
	}
}

// checkWorkspace 在运行后验证种入的工作区。
func checkWorkspace(check Check, dir string) string {
	for _, path := range check.FilesExist {
		target, err := workspacePath(dir, path)
		if err != nil {
			return err.Error()
		}
		info, err := os.Stat(target)
		if err != nil {
			return fmt.Sprintf("file %q does not exist after the run", path)
		}
		if info.IsDir() {
			return fmt.Sprintf("%q is a directory, not a file", path)
		}
	}
	paths := make([]string, 0, len(check.FileContains))
	for path := range check.FileContains {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		target, err := workspacePath(dir, path)
		if err != nil {
			return err.Error()
		}
		needle := check.FileContains[path]
		data, err := os.ReadFile(target)
		if err != nil {
			return fmt.Sprintf("cannot read %q after the run: %v", path, err)
		}
		if !strings.Contains(string(data), needle) {
			return fmt.Sprintf("file %q does not contain %q (got %q)", path, needle, clip(string(data), 200))
		}
	}
	// FileEquals 在 FileContains 之后检查：按字节精确比对，长度或结尾差一个字节都算失败。
	// 路径排序保证多文件同时失败时总报告同一个（稳定的）失败路径。
	equalPaths := make([]string, 0, len(check.FileEquals))
	for path := range check.FileEquals {
		equalPaths = append(equalPaths, path)
	}
	sort.Strings(equalPaths)
	for _, path := range equalPaths {
		target, err := workspacePath(dir, path)
		if err != nil {
			return err.Error()
		}
		want := check.FileEquals[path]
		data, err := os.ReadFile(target)
		if err != nil {
			return fmt.Sprintf("cannot read %q after the run: %v", path, err)
		}
		if !bytes.Equal(data, []byte(want)) {
			return fmt.Sprintf("file %q does not equal the expected bytes (got %q, want %q)",
				path, clip(string(data), 200), clip(want, 200))
		}
	}
	return ""
}

func equalStrings(expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if expected[i] != actual[i] {
			return false
		}
	}
	return true
}

func nilOr(tools []string) []string {
	if tools == nil {
		return []string{}
	}
	return tools
}

// oneLine 让失败原因保持在一条 TAP 行内。
func oneLine(reason string) string {
	return strings.Join(strings.Fields(reason), " ")
}

// clip 折叠空白并截断值以适合报告行。
//
// limit 计算字符（rune）数而非字节数：被裁剪的文本是模型
// limit 计算字符（rune）数而非字节数：被裁剪的文本是模型输出、种入的文件内容和 exec 输出，其中任何一个都可能包含 CJK 或
// emoji。按字节索引切片 Go 字符串过去会在切点留下半个 UTF-8 rune，使失败原因成为无效 UTF-8。少保留一个 rune
// rune 在切点，使失败原因成为无效 UTF-8。少保留一个 rune
// 并追加省略号，使结果仍至多为 limit 个 rune。
func clip(text string, limit int) string {
	joined := strings.Join(strings.Fields(text), " ")
	runes := []rune(joined)
	if len(runes) <= limit {
		return joined
	}
	if limit <= 0 {
		return ""
	}
	if limit == 1 {
		return string(runes[:1])
	}
	return string(runes[:limit-1]) + "…"
}
