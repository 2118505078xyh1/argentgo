// Package cli 是 argentgo 二进制文件中可测试的命令层：flag
// 解析、子命令分发，以及每种传输方式的入口点。
//
// agent 循环是注入进来的。集成方通过设置 DefaultRunner
// （或 App.NewRunner）来接入 internal/agent，这样在迁移的其余部分落地期间，
// 本包就不依赖于 tools/session/security 等包。
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/server"
)

// Version 是 --version、/v1/health 和 ACP initialize 所报告的版本号。
const Version = "0.8.1"

// IO 承载进程的各种流，使命令可以在不依赖 os.Stdin 的情况下进行测试。
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	Env func(string) string
	// HasTerminal 报告是否连接了交互式终端。OSIO
	// 接入真实的平台检测；测试注入一个桩来模拟 TTY 和
	// 无 TTY 的环境。nil 值表示"无终端"，因此仅限终端的
	// 命令会在做任何工作之前就退出。
	HasTerminal func() bool
	// StdinIsCharDevice 报告进程 stdin 是否为字符设备（终端/控制台）。
	// OSIO 用 os.Stdin.Stat() 的 ModeCharDevice 实现（零依赖）；测试注入桩，
	// 无需真实控制台即可检验首次运行引导。nil 表示"非字符设备"，因此
	// 用 newTestIO 构建的既有测试不会意外触发引导。
	StdinIsCharDevice func() bool
}

// OSIO 返回真实的进程流。
func OSIO() IO {
	return IO{
		In:                os.Stdin,
		Out:               os.Stdout,
		Err:               os.Stderr,
		Env:               os.Getenv,
		HasTerminal:       defaultHasTerminal,
		StdinIsCharDevice: defaultStdinIsCharDevice,
	}
}

// firstRunMarkerName 是存储目录中的首次引导标记文件名。文件存在即视为已引导，
// 不再重复打印；删除它可以让引导重新出现。
const firstRunMarkerName = ".onboarded"

// 首次运行的四行引导文案。每行都以独特中文前缀开头，便于与 usage 中的
// login/doctor/tui 子命令名区分；整串写入，UTF-8 字节不会被切开。
// 末行只提示可运行 init，不自动进入向导——引导不能拦路。
const (
	firstRunLoginLine  = "argentgo: 首次运行请先登录：argentgo login openai（也可在配置中设置 apiKey）"
	firstRunTUILine    = "argentgo: 交互界面请用 argentgo tui 或 argentgo --tui；桌面 GUI 为 argentgo-native"
	firstRunDoctorLine = "argentgo: 运行 argentgo doctor 可自检配置、存储与提供方"
	firstRunInitLine   = "argentgo: 运行 argentgo init 可交互式初始化（选提供方、配凭据、生成配置并自检）"
)

// defaultStdinIsCharDevice 用 os.Stdin.Stat() 判定 stdin 是否为字符设备
// （终端/控制台），仅依赖标准库。stat 失败时按"非字符设备"处理，因此
// 重定向或管道输入不会触发首次引导。
func defaultStdinIsCharDevice() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// maybeFirstRunGuide 在"无参数 + cli_hints opt-in + stdin 为字符设备 +
// 无配置/凭据 + 无 onboarded 标记"时向 IO.Err 打印一次四行引导，并在
// storage 目录写入标记。任一项不满足都立即返回：开关默认关闭时连 stat
// 和读文件都不做，因此未 opt-in 的既有行为逐字节不变。
func (a App) maybeFirstRunGuide() {
	if !cliHintsEnabled(a.IO.Env) {
		return
	}
	if a.IO.StdinIsCharDevice == nil || !a.IO.StdinIsCharDevice() {
		return
	}
	if a.firstRunConfigured() {
		return
	}
	marker := filepath.Join(a.storageDir(), firstRunMarkerName)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	for _, line := range [...]string{firstRunLoginLine, firstRunTUILine, firstRunDoctorLine, firstRunInitLine} {
		fmt.Fprintln(a.IO.Err, line)
	}
	// 标记只写一次；写失败时静默忽略——引导本身已输出，且不应阻塞主流程。
	_ = os.MkdirAll(a.storageDir(), 0o700)
	_ = os.WriteFile(marker, []byte("argentgo first-run guide shown\n"), 0o600)
}

// firstRunConfigured 报告是否已有可用配置或凭据，用于避免向老用户重复引导。
// 顶层或 provider 级 apiKey、全局 ARGENT_API_KEY、auth store 中的任意 token
// 都算"已配置"。配置与 token 只读，解析失败按"未配置"处理；打开 auth store
// 可能创建空目录，但这只发生在 opt-in 已开启的引导探测路径上。
func (a App) firstRunConfigured() bool {
	if text := strings.TrimSpace(a.env("ARGENT_API_KEY")); text != "" {
		return true
	}
	cfg, err := loadConfigFile(a.configPath())
	if err == nil {
		for _, key := range []string{"apiKey", "api_key", "api-key"} {
			if text, _ := cfg[key].(string); strings.TrimSpace(text) != "" {
				return true
			}
		}
		providers, _ := cfg["providers"].(map[string]any)
		for _, raw := range providers {
			entry, _ := raw.(map[string]any)
			for _, key := range []string{"apiKey", "api_key", "api-key"} {
				if text, _ := entry[key].(string); strings.TrimSpace(text) != "" {
					return true
				}
			}
		}
	}
	if store, err := authStore(a); err == nil {
		if tokens, err := store.List(); err == nil && len(tokens) > 0 {
			return true
		}
	}
	return false
}

// Runner 是每种传输方式都驱动的 agent 切片。
type Runner = server.Runner

// RunnerFactory 根据解析后的选项构建一个 runner。
type RunnerFactory func(Options) (Runner, error)

// DefaultRunner 是进程级的 runner 工厂。在集成方把 internal/agent
// 接入本包之前，它保持为 nil。
var DefaultRunner RunnerFactory

// SessionStore 是 `sessions` 以及 RPC/ACP 所使用的本地会话视图
// 传输方式。
type SessionStore interface {
	server.SessionStore
	Delete(id string) error
}

// EvalReport 是单个 eval 场景的评分依据。
type EvalReport struct {
	Text  string
	Tools []string
}

// EvalSuiteRunner 执行整个脚本化的套件文件。RunEval 在可用时
// 优先使用它；较窄的 EvalRunner 保留用于简单的注入假对象。
type EvalSuiteRunner interface {
	RunSuite(ctx context.Context, path string, out io.Writer) (passed, failed int, err error)
}

// LiveEvalSuiteRunner 用真实模型执行整个脚本化的 eval 套件。它是 eval
// 子命令 `--live` 前缀的带外分派目标：该前缀不进 flagSurface/parity，
// 默认 eval 路径不依赖此接口，由 runEval 在运行时按需类型断言。
type LiveEvalSuiteRunner interface {
	RunSuiteLive(ctx context.Context, path string, out io.Writer) (passed, failed int, err error)
}

// EvalRunner 执行单个 eval 场景。
type EvalRunner interface {
	RunScenario(ctx context.Context, prompt string) (EvalReport, error)
}

// OrchestrationFailure 是一条编排场景失败诊断，字段对应 internal/eval 的同名类型。
type OrchestrationFailure struct {
	ID     string
	Name   string
	Reason string
}

// OrchestrationReport 汇总一次编排套件运行。
type OrchestrationReport struct {
	Total    int
	Passed   int
	Failed   int
	Failures []OrchestrationFailure
}

// OrchestrationRunner 执行一份编排场景文档。它是 eval 子命令 `orchestration`
// 前缀的带外分派目标：该前缀不进 flagSurface/parity，默认 eval 路径不依赖此
// 接口，由 runEval 在运行时按需对 app.Eval 做类型断言。实现由 internal/app 注入。
type OrchestrationRunner interface {
	RunOrchestration(ctx context.Context, path string) (OrchestrationReport, error)
}

// HistoryEvalSpec 描述一次 git 历史评测的输入：仓库路径、任务上限与起始 revision。
type HistoryEvalSpec struct {
	Repo     string
	MaxTasks int
	Since    string
	// Scripted 用一轮脚本化回复代替真实模型，使 git→worktree→评分流程
	// 无需 API key 即可离线验证（任务通常不通过，正是评分机制的证明）。
	Scripted bool
	// Verify 在跑模型前先离线自检，跳过无判别力的任务，避免浪费 token。
	Verify bool
}

// HistoryEvalSuiteRunner 用真实模型执行从 git 历史抽取的任务套件。
// 它是 eval 子命令 `history` 前缀的带外分派目标，默认 eval 路径不依赖此接口。
type HistoryEvalSuiteRunner interface {
	RunHistory(ctx context.Context, spec HistoryEvalSpec, out io.Writer) (passed, failed int, err error)
}

// TUIFunc 针对一组解析后的选项运行全屏终端 UI。
type TUIFunc func(ctx context.Context, opts Options) error

// ProfileLister 报告某个工作区可见的 profile 名称以及当前选中的那个。
type ProfileLister func(opts Options) (names []string, active string, err error)

// TokenReporter 渲染 --tokens 打印的请求负载明细。
type TokenReporter func(opts Options) (string, error)

// ContextReporter 渲染 `argentgo context` 的上下文占用分解报告。
// 实现位于 internal/app，负责采集四类 token 估算并调用本包的纯函数原语。
type ContextReporter func(opts Options) (string, error)

// App 汇集了命令层所注入的各项依赖。
type App struct {
	IO        IO
	Version   string
	NewRunner RunnerFactory
	Sessions  SessionStore
	// Eval 接受 EvalRunner、EvalSuiteRunner、LiveEvalSuiteRunner 或
	// OrchestrationRunner；runEval 在运行时按带外前缀与可用接口选择分派目标。
	Eval          any
	TUI           TUIFunc
	Profiles      ProfileLister
	TokenReport   TokenReporter
	ContextReport ContextReporter
	// ConfigInfoProvider 为 --serve/--rpc/--acp 的 config.info 方法提供真实配置；
	// 由 internal/app 注入。nil 时该方法返回干净错误，既有 RPC 行为不变。
	ConfigInfoProvider server.ConfigInfoProvider
	// StatusProvider 为 --serve 的 GET /v1/status 与 --rpc 的 status 方法提供
	// 状态字节。由 internal/app 注入；nil 时回退到 ConfigInfoProvider 上同时
	// 实现 server.StatusProvider 的实例（app 的默认适配器即是），二者皆 nil
	// 时不注入，/v1/status 仍 404、RPC status 返回干净错误，wire 完全不变。
	StatusProvider server.StatusProvider
	// CompletionProvider 为 --serve/--rpc 的 spelling.autocomplete 方法提供
	// 预测补全；由 internal/app 注入。nil 时该方法返回干净错误。
	CompletionProvider server.CompletionProvider
	ConfigPath         string
	StorageDir         string
	// UpdateOps 为测试覆盖更新器的接缝；为 nil 时调用 internal/update。
	UpdateOps *UpdateOps
}

// Run 执行 CLI 并返回进程退出码。
func Run(args []string, stdio IO) int {
	return RunWith(App{IO: stdio}, args)
}

// RunWith 使用显式的 App 执行 CLI，这是测试所用的形式。
func RunWith(app App, args []string) int {
	app = app.withDefaults()
	ctx, stop := commandContext()
	defer stop()

	if len(args) > 0 {
		if handler, ok := subcommands[args[0]]; ok {
			return handler(app, ctx, args[1:])
		}
	} else {
		// 首次运行引导：仅在 cli_hints opt-in 且各项条件齐备时写 stderr，
		// 默认关闭时零副作用，保持原有 usage/stdin 行为逐字节不变。
		app.maybeFirstRunGuide()
		if app.hasTerminal() && app.env("ARGENT_NO_TUI") != "1" {
			// 无参数且连接交互终端时默认进入 TUI；非 TTY（CI/重定向）落到
			// runMain 保持 usage/stdin 行为逐字节不变，也不会挂起。
			return runTUI(app, ctx, nil)
		}
	}
	return app.runMain(ctx, args)
}

var subcommands = map[string]func(App, context.Context, []string) int{
	"sessions":   runSessions,
	"replay":     runReplay,
	"usage":      runUsage,
	"context":    runContext,
	"doctor":     runDoctor,
	"init":       runInit,
	"login":      runLogin,
	"logout":     runLogout,
	"auth":       runAuth,
	"models":     runModels,
	"mcp":        runMCP,
	"opt-ins":    runOptIns,
	"skills":     runSkills,
	"commands":   runCommands,
	"hooks":      runHooks,
	"plugin":     runPlugin,
	"policy":     runPolicy,
	"audit":      runAudit,
	"update":     runUpdate,
	"completion": runCompletion,
	"tui":        runTUI,
	"bug":        runBug,
	"eval":       runEval,
	"config":     runConfig,
	"workflow":   runWorkflow,
	"telemetry":  runTelemetry,
	"attach":     runAttach,
	"logs":       runLogs,
	"jobs":       runJobs,
	"follow":     runFollow,
	"stop":       runStop,
	"board":      runBoard,
	"help":       runHelp,
	"version":    runVersion,
}

// subcommandOrder 与分发映射保持一致。它存在的原因是让 completion 不必在
// 包初始化时引用该映射（那会通过 runCompletion 形成导入循环）；
// parity_test 断言两者始终相同。
var subcommandOrder = []string{
	"sessions", "replay", "usage", "context", "doctor", "init", "login", "logout", "auth",
	"models", "mcp", "opt-ins", "skills", "commands", "hooks", "plugin", "policy", "audit",
	"update", "completion", "tui", "bug", "eval", "config",
	"workflow", "telemetry", "attach", "logs", "jobs", "follow", "stop", "board",
	"help", "version",
}

// subcommandNames 列出所有被分发的命令，按序排列。completion 读取它，
// 而 parity 测试将其与分发映射交叉核对，因此两者不会走样。
func subcommandNames() []string {
	names := append([]string(nil), subcommandOrder...)
	sort.Strings(names)
	return names
}

func runHelp(app App, _ context.Context, _ []string) int {
	writeUsage(app.IO.Out)
	return 0
}

func runVersion(app App, _ context.Context, args []string) int {
	verbose := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			versionUsage(app.IO.Out)
			return 0
		case arg == "-v" || arg == "--verbose" || arg == "--long":
			verbose = true
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: version: unexpected argument %q\n", arg)
			versionUsage(app.IO.Err)
			return 2
		}
	}
	if verbose {
		writeVersionInfo(app.IO.Out, app.Version)
		return 0
	}
	fmt.Fprintf(app.IO.Out, "argentgo %s\n", app.Version)
	return 0
}

// writeVersionInfo 打印详细的构建报告。它故意不读取任何配置、也不打开网络连接，
// 因此输出是即时且确定的；协议列表直接取自 llm 常量，而不是字符串的副本，
// 所以它不会与实际发布的内容走样。
func writeVersionInfo(w io.Writer, version string) {
	protocols := []string{
		llm.ProtocolAnthropicMessages,
		llm.ProtocolChatCompletions,
		llm.ProtocolResponses,
	}
	fmt.Fprintf(w, "argentgo %s\n", version)
	fmt.Fprintf(w, "go: %s\n", runtime.Version())
	fmt.Fprintf(w, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(w, "protocols: %s\n", strings.Join(protocols, ", "))
}

func versionUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo version [-v|--verbose]

Print the version, or with --verbose the Go runtime, target OS/architecture
and supported wire protocols. The -V/--version flag keeps the one-line form.
`)
}

// usageError 标记那些在 flag 包打印完自身诊断信息后仍需要一条消息的校验失败。
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func (a App) runMain(ctx context.Context, args []string) int {
	opts, positional, err := parseArgs(args, a.env, a.IO.Err)
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(a.IO.Err, "argentgo:", ue.msg)
			writeUsage(a.IO.Err)
		}
		return 2
	}
	if opts.Help {
		writeUsage(a.IO.Out)
		return 0
	}
	if opts.Version {
		fmt.Fprintf(a.IO.Out, "argentgo %s\n", a.Version)
		return 0
	}
	if opts.ListProfiles {
		return a.listProfiles(*opts)
	}
	if opts.ListModels {
		return runModels(a, ctx, nil)
	}
	if opts.ListSessions {
		return sessionsList(a, a.sessionStore())
	}
	if opts.Tokens {
		return a.printTokens(*opts)
	}
	if reason := unsupportedFlag(opts); reason != "" {
		fmt.Fprintln(a.IO.Err, "argentgo:", reason)
		return 1
	}
	// 仅 opt-in 安装：无 flag 时完全不碰全局 sink，保留集成方可能预装的收集器。
	// save/restore 精确恢复原值（而非 nil 清除），避免测试间污染。
	if opts.DebugTrace {
		prev := llm.SetTraceSink(newStderrTraceSink(a.IO.Err))
		defer llm.SetTraceSink(prev)
	}
	if opts.TUI {
		return a.runTUI(ctx, opts)
	}
	a.warnPartial(opts)

	root := opts.Cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error: resolve workspace:", err)
		return 1
	}
	opts.Cwd = absRoot

	if code, handled := a.applySessionFlags(opts); handled {
		return code
	}

	switch {
	case opts.Serve:
		if strings.TrimSpace(opts.ServeToken) == "" {
			fmt.Fprintln(a.IO.Err, "error: --serve requires a bearer token (--serve-token or ARGENT_SERVE_TOKEN)")
			return 1
		}
		runner, err := a.newRunner(opts)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1
		}
		return a.serve(ctx, opts, runner)
	case opts.RPC:
		runner, err := a.newRunner(opts)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1
		}
		return a.rpc(ctx, opts, runner)
	case opts.ACP:
		runner, err := a.newRunner(opts)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1
		}
		return a.acp(ctx, opts, runner)
	}

	prompt := strings.TrimSpace(strings.Join(positional, " "))
	if prompt == "" {
		data, err := io.ReadAll(a.IO.In)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error: read stdin:", err)
			return 1
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		writeUsage(a.IO.Err)
		return 2
	}

	runner, err := a.newRunner(opts)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return a.runPrompt(ctx, opts, runner, prompt)
}

// statusProvider 解析本轮应注入的状态提供者：显式设置的 StatusProvider 优先，
// 否则复用同时实现两个接口的 ConfigInfoProvider（app 的默认适配器）。
// 两者都为 nil 时返回 nil，保持 --serve/--rpc 的默认 wire 不变。
func (a App) statusProvider() server.StatusProvider {
	if a.StatusProvider != nil {
		return a.StatusProvider
	}
	if p, ok := a.ConfigInfoProvider.(server.StatusProvider); ok {
		return p
	}
	return nil
}

func (a App) serve(ctx context.Context, opts *Options, runner Runner) int {
	cfg := server.Config{
		Addr:               opts.ServeAddr,
		Token:              opts.ServeToken,
		Version:            a.Version,
		Runner:             runner,
		Sessions:           a.Sessions,
		ConfigInfoProvider: a.ConfigInfoProvider,
		StatusProvider:     a.statusProvider(),
		CompletionProvider: a.CompletionProvider,
	}
	srv, ln, err := server.Listen(cfg)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(a.IO.Err, "argentgo listening on http://%s (bearer token required)\n", ln.Addr())
	// opt-in 就绪摘要：默认关闭时既不探测也不输出，stderr 仅保留上面一行。
	writeServeReadiness(a.IO.Err, ctx, opts.ServeReadiness, func(ctx context.Context) []readinessCheck {
		return a.appReadinessProbes(opts).checks(ctx)
	})
	if err := server.ServeListener(ctx, srv, ln); err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return 0
}

func (a App) rpc(ctx context.Context, opts *Options, runner Runner) int {
	cfg := server.Config{
		Version:            a.Version,
		Runner:             runner,
		Sessions:           a.Sessions,
		ConfigInfoProvider: a.ConfigInfoProvider,
		StatusProvider:     a.statusProvider(),
		CompletionProvider: a.CompletionProvider,
	}
	if err := server.RunRPC(ctx, a.IO.In, a.IO.Out, cfg); err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return 0
}

func (a App) acp(ctx context.Context, opts *Options, runner Runner) int {
	cfg := server.Config{Version: a.Version, Runner: runner, Sessions: a.Sessions, ConfigInfoProvider: a.ConfigInfoProvider, CompletionProvider: a.CompletionProvider}
	if err := server.RunACP(ctx, a.IO.In, a.IO.Out, cfg); err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return 0
}

// runPrompt 向 IO 流式输出一轮对话并返回退出码。
func (a App) runPrompt(ctx context.Context, opts *Options, runner Runner, prompt string) int {
	jsonMode := opts.Mode == ModeJSON
	streamed := false
	onText := func(chunk string) {
		if chunk == "" {
			return
		}
		streamed = true
		if jsonMode {
			a.emit(map[string]any{"type": "text", "text": chunk})
			return
		}
		fmt.Fprint(a.IO.Out, chunk)
	}

	result, err := runner.Prompt(ctx, prompt, onText)
	cancelled := isCancelled(ctx, err)
	if jsonMode {
		event := map[string]any{"type": "result", "text": result, "error": errText(err)}
		if cancelled {
			// 用户主动中止是正常停止。保留事件结构，但不要
			// 把它包装成失败请求供脚本解析。
			event["error"] = ""
			event["stopReason"] = "cancelled"
		}
		a.emit(event)
	}
	if err != nil {
		if cancelled {
			// 没有任何失败，也没有重试动作：安静退出，与其他
			// 传输方式的中止结果保持一致。
			return 0
		}
		fmt.Fprintln(a.IO.Err, "error:", err)
		writeActionableErrorEnv(a.IO.Err, err, a.IO.Env, opts.Locale)
		// 接线已实现但从未被调用的友好建议：认证失败附凭证状态、审批/非交互
		// 失败附 --yes 提示。默认开启，ARGENT_CLI_HINTS=0 可紧急回退。
		a.appendRuntimeAdvice(a.IO.Err, err, opts.Provider, a.hasTerminal())
		return 1
	}
	if !jsonMode {
		if !streamed {
			fmt.Fprint(a.IO.Out, result)
		}
		fmt.Fprintln(a.IO.Out)
	}
	return 0
}

// writeActionableError 把规范化后的提供方解释及其下一步追加到简短的
// "error: ..." 行之后。它是有意增量式的：对于解析 stderr 的调用方来说
// 原始行保持不变，而用户能获得一个具体的恢复动作。对 nil 错误，
// 分类是空操作，并且永远不会改变成功路径。
func writeActionableError(w io.Writer, err error) {
	if err == nil {
		return
	}
	actionable := llm.ClassifyAPIError(err)
	if actionable == nil {
		return
	}
	fmt.Fprintln(w, actionable.Message)
	fmt.Fprintln(w, actionable.NextStep)
}

func (a App) emit(event map[string]any) {
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintln(a.IO.Out, string(line))
}

func (a App) newRunner(opts *Options) (Runner, error) {
	factory := a.NewRunner
	if factory == nil {
		factory = DefaultRunner
	}
	if factory == nil {
		return nil, errors.New("no runner configured: set cli.DefaultRunner (integrator wires internal/agent)")
	}
	return factory(*opts)
}

func (a App) withDefaults() App {
	if a.IO.In == nil {
		a.IO.In = os.Stdin
	}
	if a.IO.Out == nil {
		a.IO.Out = os.Stdout
	}
	if a.IO.Err == nil {
		a.IO.Err = os.Stderr
	}
	if a.IO.Env == nil {
		a.IO.Env = os.Getenv
	}
	if a.Version == "" {
		a.Version = Version
	}
	return a
}

func (a App) env(key string) string {
	if a.IO.Env == nil {
		return ""
	}
	return a.IO.Env(key)
}

func (a App) storageDir() string {
	if a.StorageDir != "" {
		return a.StorageDir
	}
	// 与 app.DefaultStorage / config.storageDirFromEnv 共用同一优先级。
	if dir := firstNonEmpty(a.env("ARGENT_STORAGE"), a.env("ARGENT_STORAGE_DIR"), a.env("ARGENT_HOME")); dir != "" {
		return dir
	}
	home := firstNonEmpty(a.env("USERPROFILE"), a.env("HOME"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return ".argentgo"
		}
	}
	return filepath.Join(home, ".argentgo")
}

func (a App) configPath() string {
	if a.ConfigPath != "" {
		return a.ConfigPath
	}
	return filepath.Join(a.storageDir(), "config.json")
}

func (a App) sessionDir() string {
	return filepath.Join(a.storageDir(), "sessions")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isCancelled 沿用 server 各传输方式的规则：Ctrl-C 或 abort 会使运行
// 正常结束。超时和其他所有错误都保留失败路径，这样
// writeActionableError 仍有机会对它们作出解释。
func isCancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled
}

// commandContext 在 Ctrl-C 时被取消。
func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
