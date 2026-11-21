package cli

import (
	"flag"
	"io"
	"sort"
)

// newFlags 在 fs 上注册完整的 flag 集合。
func newFlags(fs *flag.FlagSet, v *flagValues) {
	v.model = fs.String("m", "", "model id")
	v.baseURL = fs.String("base-url", "", "provider base URL")
	v.apiKey = fs.String("api-key", "", "provider API key")
	v.protocol = fs.String("protocol", "", "protocol: anthropic, openai or responses")
	v.provider = fs.String("provider", "", "provider template id (see `argent models`)")
	v.profile = fs.String("profile", "", "named config profile")
	v.yolo = fs.Bool("yolo", false, "auto-approve every tool call")
	v.yes = fs.Bool("yes", false, "auto-confirm prompts in non-interactive environments")
	// 默认值从 12 抬高到 100，与引擎 DefaultMaxTurns 对齐：这是抬高默认
	// 天花板而非剥夺控制，用户仍可用本 flag、ARGENT_MAX_TURNS、-c maxTurns、
	// 配置或提示词声明覆盖（后者在 app_execute.go 中叠加）。
	v.maxTurns = fs.Int("max-turns", 100, "maximum model turns")
	v.maxParallel = fs.Int("max-parallel", 0, "read-only tool workers per batch (0 uses the default, 1 disables)")
	v.mode = fs.String("mode", "", "output mode: text or json (default: text)")
	v.cwd = fs.String("cwd", "", "workspace root (default: current directory)")
	v.sandboxMode = fs.String("sandbox-mode", "", "read-only, workspace-write or danger-full-access")
	v.approval = fs.String("approval", "", "approval mode: ask, never or auto")
	v.session = fs.String("session", "", "resume the session with this id")
	v.resume = fs.Bool("resume", false, "resume the most recent session")
	v.undo = fs.Bool("undo", false, "restore the latest checkpoint of --session and exit")
	v.retry = fs.Bool("retry", false, "fork --session at its latest checkpoint before the prompt")
	v.title = fs.String("title", "", "override the generated session title")
	v.locale = fs.String("locale", "", "output locale")

	v.jsonLines = fs.Bool("json-lines", false, "one-shot strict JSONL on stdout")
	v.listProfiles = fs.Bool("list-profiles", false, "list config profiles and exit")
	v.listSessions = fs.Bool("list-sessions", false, "list stored sessions and exit")
	v.listModels = fs.Bool("list-models", false, "list provider templates and exit")
	v.noColor = fs.Bool("no-color", false, "disable ANSI colours")
	v.tokens = fs.Bool("tokens", false, "print the request payload breakdown and exit")
	v.trustProject = fs.Bool("trust-project", false, "trust project-local resources for this run")
	v.thinking = fs.String("thinking", "", "reasoning effort hint")
	v.noAutoCompact = fs.Bool("no-auto-compact", false, "disable automatic compaction for this run")
	v.noPromptCache = fs.Bool("no-prompt-cache", false, "disable provider prompt-cache markers for this run")
	v.theme = fs.String("theme", "", "UI theme name")
	v.plugins = &stringList{}
	fs.Var(v.plugins, "plugins", "load this plugin by name, repeatable; `all` loads every one")

	v.maxTokens = fs.Int("max-tokens", 0, "stop after this many billable tokens")
	v.maxCost = fs.Float64("max-cost", 0, "stop after this estimated dollar spend")
	v.maxSeconds = fs.Int("max-seconds", 0, "stop after this many wall-clock seconds")
	v.priceInput = fs.Float64("price-input", 0, "USD per million prompt tokens")
	v.priceOutput = fs.Float64("price-output", 0, "USD per million completion tokens")
	v.fallbackModel = fs.String("fallback-model", "", "model to retry once with when the primary fails")
	v.secondaryModel = fs.String("secondary-model", "", "model for delegated subagent tasks")
	v.addDirs = &stringList{}
	fs.Var(v.addDirs, "add-dir", "extra writable workspace directory, repeatable")
	v.outputSchema = fs.String("output-schema", "", "JSON Schema file path, or inline schema text starting with {")

	v.background = fs.Bool("background", false, "run in the background (not migrated)")
	v.worktree = &optionalString{}
	fs.Var(v.worktree, "worktree", "run in a git worktree (not migrated; bare flag names it automatically)")
	v.enableBrowser = fs.Bool("enable-browser", false, "enable the browser tool (not migrated)")
	v.enableDesktop = fs.Bool("enable-desktop", false, "enable desktop control (not migrated)")
	v.enableService = fs.Bool("enable-service", false, "enable the pty service tool (not migrated)")
	v.workflowMode = fs.String("workflow-mode", "", "dynamic workflow mode (not migrated)")
	v.workflowResume = fs.Bool("workflow-resume", false, "resume a workflow journal (not migrated)")
	v.workflowJournal = fs.String("workflow-journal", "", "workflow journal path (not migrated)")
	v.gui = fs.Bool("gui", false, "launch the desktop window (not migrated)")
	v.tui = fs.Bool("tui", false, "run the full-screen terminal UI")

	v.serve = fs.Bool("serve", false, "serve HTTP + SSE")
	v.rpc = fs.Bool("rpc", false, "serve newline-delimited JSON-RPC on stdio")
	v.acp = fs.Bool("acp", false, "serve ACP JSON-RPC on stdio")
	v.addr = fs.String("serve-address", "", "HTTP bind address (default 127.0.0.1)")
	v.port = fs.Int("serve-port", 7777, "HTTP bind port")
	v.token = fs.String("serve-token", "", "bearer token required by --serve")
	v.version = fs.Bool("version", false, "print version and exit")
	v.help = fs.Bool("help", false, "print this help")
	v.overrides = &Overrides{}
	fs.Var(v.overrides, "c", "config override key=value (repeatable)")

	// 别名共享同一存储，因此任一写法都是等价的。
	fs.StringVar(v.model, "model", "", "model id")
	fs.StringVar(v.cwd, "C", "", "workspace root")
	fs.StringVar(v.session, "session-id", "", "resume the session with this id")
	fs.BoolVar(v.resume, "continue", false, "resume the most recent session")
	fs.BoolVar(v.version, "V", false, "print version and exit")
	fs.BoolVar(v.help, "h", false, "print this help")
	fs.StringVar(v.token, "token", "", "alias for --serve-token")
	fs.StringVar(v.sandboxMode, "sandbox", "", "alias for --sandbox-mode")
	fs.BoolVar(v.resume, "continue-session", false, "alias for --resume")
	fs.Var(v.overrides, "config", "config override key=value (repeatable)")
}

// verifyFlagValues 保存选择加入的验证 flag 指针。这些 flag 故意注册在
// newFlags 之外：flagSurface（以及与之比较的锁定的 docs/PARITY.md 门禁）
// 枚举的是 newFlags，而验证是一个附加的、选择加入的功能，尚未属于那份
// 文档化的 flag 集合。parseArgs 直接调用 registerVerifyFlags。
type verifyFlagValues struct {
	cmd       *stringList
	timeoutMS *int
}

func registerVerifyFlags(fs *flag.FlagSet) verifyFlagValues {
	cmd := &stringList{}
	fs.Var(cmd, "verify-cmd", "opt-in post-edit verify argv (repeatable or space-separated): e.g. --verify-cmd go --verify-cmd test --verify-cmd ./...")
	timeoutMS := fs.Int("verify-timeout-ms", 30000, "verify command timeout in milliseconds (0 uses 30000)")
	return verifyFlagValues{cmd: cmd, timeoutMS: timeoutMS}
}

// privacyModeAny / privacyModeLocalOnly 镜像 config.PrivacyModeAny 和
// security.PrivacyLocalOnly。CLI 值在到达配置覆盖层之前会被规范化。
const (
	privacyModeAny       = "any"
	privacyModeLocalOnly = "local-only"
)

// privacyFlagValues 保存 --privacy-mode 指针。与 verifyFlagValues 一样，它
// 故意注册在 newFlags 之外：flagSurface（以及锁定的
// TestFlagSurfaceMatchesParityList 门禁）枚举的是 newFlags，而这个 flag 是
// 一个附加的、选择加入的安全姿态，不属于那份文档化的集合。parseArgs 直接
// 调用 registerPrivacyFlags，并把值以 "privacyMode" 推入配置覆盖层。
type privacyFlagValues struct {
	mode *string
}

func registerPrivacyFlags(fs *flag.FlagSet) privacyFlagValues {
	mode := fs.String("privacy-mode", "", "network posture: any (default) or local-only (blocks remote endpoints and web tools)")
	return privacyFlagValues{mode: mode}
}

// readinessFlagValues 保存 --serve-readiness 指针。和 verifyFlagValues、
// privacyFlagValues 一样，它故意注册在 newFlags 之外：flagSurface（以及锁定的
// TestFlagSurfaceMatchesParityList 门禁）枚举的是 newFlags，而就绪摘要是一个
// 附加的、选择加入的启动诊断，不属于那份文档化的 flag 集合。parseArgs 直接
// 调用 registerReadinessFlags。
type readinessFlagValues struct {
	serveReadiness *bool
}

func registerReadinessFlags(fs *flag.FlagSet) readinessFlagValues {
	serveReadiness := fs.Bool("serve-readiness", false, "opt-in: print a one-line startup readiness summary after the listening line")
	return readinessFlagValues{serveReadiness: serveReadiness}
}

// flagSurface 报告 CLI 接受的所有长 flag 名称，按序排列。它构建一个临时的
// flag 集合，使列表绝不会与解析器走样。
func flagSurface() []string {
	fs := flag.NewFlagSet("argent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	newFlags(fs, &flagValues{})
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		if len(f.Name) > 1 {
			names = append(names, f.Name)
		}
	})
	sort.Strings(names)
	return names
}
