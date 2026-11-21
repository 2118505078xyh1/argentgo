package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// parseArgs 解析 flag 集合，返回解析后的选项以及位置参数形式的 prompt 词。
// flag 可以出现在位置参数之前，也可以穿插在位置参数之间。
func parseArgs(args []string, env func(string) string, stderr io.Writer) (*Options, []string, error) {
	before, after := splitTerminator(args)

	fs := flag.NewFlagSet("argent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { writeUsage(stderr) }
	values := &flagValues{}
	newFlags(fs, values)
	// 选择加入的验证 flag 在这里解析，而不是在 newFlags 中，这样锁定的 parity
	// flag 列表保持不变；它们仍然通过下面的覆盖层流入配置。
	verify := registerVerifyFlags(fs)
	// --privacy-mode 采用与验证 flag 相同的带外注册方式：它绝不能改变
	// flagSurface()，锁定的 parity 门禁会把它与精确列表比较。它作为覆盖值
	// 到达 app/config。
	privacy := registerPrivacyFlags(fs)
	// --serve-readiness 同样带外注册：它绝不改变 flagSurface()，因此锁定的
	// parity flag 列表保持不变。值直接落到 Options.ServeReadiness。
	readiness := registerReadinessFlags(fs)
	// --debug-trace 同样带外注册：绝不改变 flagSurface()，锁定的 parity
	// flag 列表保持不变；值直接落到 Options.DebugTrace。
	trace := registerTraceFlags(fs)

	// 在 flag 集合建立之后再运行 help 预扫描：它必须区分真正的 help 开关
	// 和传给另一个 flag 的、看起来像 '-h' 的值。
	if hasHelpFlag(fs, before) {
		return &Options{Help: true}, nil, nil
	}

	positional, err := parseInterleaved(fs, before)
	if err != nil {
		return nil, nil, err
	}
	// flag 区域中单独一个 "-" 是约定的从 stdin 读取 prompt 的写法
	// （"argent [flags] -"），而不是字面的 prompt 词。把它去掉，
	// 使 runMain 的空 prompt 分支改为消费 IO.In，而不是把 "-" 交给 runner。
	// "--" 之后的词保持字面含义，因此那里末尾的 "-" 仍是真正的 prompt。
	if len(positional) == 1 && positional[0] == "-" && len(after) == 0 {
		positional = nil
	}
	positional = append(positional, after...)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	overrides := *values.overrides
	// 验证 flag 通过配置到达 app.Execute：argv 被写成 JSON 数组，使
	// Overrides.Nested() 把它解码为 []any，再由 config 解码为 Config.VerifyCommand。
	// 每个 --verify-cmd 可以包含多个以空格分隔的词，也可以重复该 flag，
	// 因此两种形式都可用。
	if len(verify.cmd.values) > 0 {
		var argv []string
		for _, word := range verify.cmd.values {
			argv = append(argv, strings.Fields(word)...)
		}
		if len(argv) > 0 {
			if encoded, err := json.Marshal(argv); err == nil {
				_ = overrides.Set("verifyCommand=" + string(encoded))
			}
		}
	}
	if set["verify-timeout-ms"] {
		if *verify.timeoutMS < 0 {
			return nil, nil, &usageError{msg: "--verify-timeout-ms must be >= 0"}
		}
		_ = overrides.Set("verifyTimeoutMS=" + strconv.Itoa(*verify.timeoutMS))
	}
	// --privacy-mode 在进入最高优先级的覆盖层之前先被规范化并校验，
	// 这样 config.LoadWithOverrides 会拒绝拼写错误，而不是悄悄放宽安全姿态。
	if set["privacy-mode"] {
		mode := strings.ToLower(strings.TrimSpace(*privacy.mode))
		switch mode {
		case privacyModeAny, privacyModeLocalOnly:
			_ = overrides.Set("privacyMode=" + mode)
		default:
			return nil, nil, &usageError{msg: fmt.Sprintf("--privacy-mode must be any or local-only, got %q", *privacy.mode)}
		}
	}
	maxParallel := intSetting(set, "max-parallel", *values.maxParallel, env, "ARGENT_MAX_PARALLEL",
		overrideInt(&overrides, "maxParallel", "max-parallel"))
	opts := &Options{
		Overrides:   overrides,
		Yes:         *values.yes,
		Mode:        *values.mode,
		Cwd:         *values.cwd,
		Session:     *values.session,
		Resume:      *values.resume,
		Undo:        *values.undo,
		Retry:       *values.retry,
		Title:       *values.title,
		Serve:       *values.serve,
		RPC:         *values.rpc,
		ACP:         *values.acp,
		Help:        *values.help || hasHelpFlag(fs, before),
		Version:     *values.version,
		MaxTurns:    *values.maxTurns,
		MaxParallel: maxParallel,
		TUI:         *values.tui,
		ServeToken:  firstNonEmpty(*values.token, env("ARGENT_SERVE_TOKEN"), env("ARGENT_TOKEN")),
		ServeAddr:   serveAddress(*values.addr, *values.port),
		SandboxMode: firstNonEmpty(*values.sandboxMode, env("ARGENT_SANDBOX_MODE")),
		Approval:    firstNonEmpty(*values.approval, env("ARGENT_APPROVAL")),
		Protocol:    firstNonEmpty(*values.protocol, env("ARGENT_PROTOCOL")),
		Locale:      firstNonEmpty(*values.locale, env("ARGENT_LOCALE")),
		Model:       firstNonEmpty(*values.model, env("ARGENT_MODEL")),
		Provider:    firstNonEmpty(*values.provider, env("ARGENT_PROVIDER")),
		Profile:     firstNonEmpty(*values.profile, env("ARGENT_PROFILE")),
		BaseURL:     firstNonEmpty(*values.baseURL, env("ARGENT_BASE_URL"), env("OPENAI_BASE_URL")),
		APIKey:      firstNonEmpty(*values.apiKey, env("ARGENT_API_KEY"), env("OPENAI_API_KEY")),
	}
	// 单独赋值而不是放进上面的对齐字面量，避免新字段名触发整块重新对齐。
	opts.ServeReadiness = *readiness.serveReadiness
	opts.DebugTrace = *trace.debugTrace

	opts.JSONLines = *values.jsonLines
	opts.ListProfiles = *values.listProfiles
	opts.ListSessions = *values.listSessions
	opts.ListModels = *values.listModels
	opts.NoColor = *values.noColor
	opts.Tokens = *values.tokens
	opts.TrustProject = *values.trustProject
	opts.NoAutoCompact = *values.noAutoCompact
	opts.NoPromptCache = *values.noPromptCache
	opts.Plugins = append([]string(nil), values.plugins.values...)
	opts.Background = *values.background
	opts.Worktree = values.worktree.value
	opts.EnableBrowser = *values.enableBrowser
	opts.EnableDesktop = *values.enableDesktop
	opts.EnableService = *values.enableService
	opts.WorkflowMode = *values.workflowMode
	opts.WorkflowResume = *values.workflowResume
	opts.WorkflowJournal = *values.workflowJournal
	opts.GUI = *values.gui
	opts.AddDirs = append([]string(nil), values.addDirs.values...)
	opts.OutputSchema = *values.outputSchema

	// --json-lines 是严格的一次性 JSONL 模式。
	if opts.JSONLines {
		opts.Mode = ModeJSON
	}
	// --continue-session、--continue 和 --resume 是别名，与 --resume 共享
	// 同一个存储指针（见 newFlags），因此 opts.Resume 已经携带了无论哪种
	// 写法所使用的值。不要根据 fs.Visit 强行把它置为 true：显式的
	// --resume=false / --continue=false 必须保持为 false，且命令行上最后的
	// 写法必须生效。

	opts.Thinking = firstNonEmpty(*values.thinking, env("ARGENT_THINKING"))
	opts.Theme = firstNonEmpty(*values.theme, env("ARGENT_THEME"))
	opts.FallbackModel = *values.fallbackModel
	opts.SecondaryModel = *values.secondaryModel

	opts.MaxTokens = intSetting(set, "max-tokens", *values.maxTokens, env, "ARGENT_MAX_TOKENS",
		overrideInt(&overrides, "maxTokens", "max-tokens"))
	opts.MaxCost = floatSetting(set, "max-cost", *values.maxCost, env, "ARGENT_MAX_COST",
		overrideFloat(&overrides, "maxCost", "max-cost"))
	opts.MaxSeconds = intSetting(set, "max-seconds", *values.maxSeconds, env, "ARGENT_MAX_SECONDS",
		overrideInt(&overrides, "maxSeconds", "max-seconds"))
	opts.PriceInput = floatSetting(set, "price-input", *values.priceInput, env, "ARGENT_PRICE_INPUT",
		overrideFloat(&overrides, "priceInput", "price-input"))
	opts.PriceOutput = floatSetting(set, "price-output", *values.priceOutput, env, "ARGENT_PRICE_OUTPUT",
		overrideFloat(&overrides, "priceOutput", "price-output"))

	// -c 覆盖值位于 env 和 flag 之下。不要把编译默认值放进 Options：
	// 空字段表示"未设置"，使 app.Resolve 优先采用配置文件 / profile 层，
	// 而不是编译默认值。
	opts.Model = firstNonEmpty(opts.Model, overrideValue(&overrides, "model"))
	opts.BaseURL = firstNonEmpty(opts.BaseURL, overrideValue(&overrides, "baseUrl", "base-url", "base_url"))
	opts.APIKey = firstNonEmpty(opts.APIKey, overrideValue(&overrides, "apiKey", "api-key", "api_key"))
	opts.Protocol = firstNonEmpty(opts.Protocol, overrideValue(&overrides, "protocol"))
	opts.Provider = firstNonEmpty(opts.Provider, overrideValue(&overrides, "provider"))
	opts.Locale = firstNonEmpty(opts.Locale, overrideValue(&overrides, "locale"))
	opts.SandboxMode = firstNonEmpty(opts.SandboxMode, overrideValue(&overrides, "sandboxMode", "sandbox-mode"))
	opts.Approval = firstNonEmpty(opts.Approval, overrideValue(&overrides, "approval"))
	opts.Mode = firstNonEmpty(opts.Mode, overrideValue(&overrides, "mode"))

	if !set["max-turns"] {
		if value := env("ARGENT_MAX_TURNS"); value != "" {
			if n, err := strconv.Atoi(value); err == nil {
				opts.MaxTurns = n
			}
		} else if value, ok := overrideValueOK(&overrides, "maxTurns", "max-turns"); ok {
			if n, err := strconv.Atoi(value); err == nil {
				opts.MaxTurns = n
			}
		}
	}
	opts.Yolo = *values.yolo || env("ARGENT_YOLO") == "1" || strings.EqualFold(overrideValue(&overrides, "yolo"), "true")

	if err := opts.validate(); err != nil {
		return nil, nil, &usageError{msg: err.Error()}
	}
	return opts, positional, nil
}

// intSetting 按 flag > env > override > 零值 的顺序解析数值 flag。
func intSetting(set map[string]bool, name string, flagValue int, env func(string) string, envKey string, override int) int {
	if set[name] {
		return flagValue
	}
	if value := env(envKey); value != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return n
		}
	}
	if override != 0 {
		return override
	}
	return flagValue
}

// floatSetting 按 flag > env > override > 零值 的顺序解析浮点 flag。
func floatSetting(set map[string]bool, name string, flagValue float64, env func(string) string, envKey string, override float64) float64 {
	if set[name] {
		return flagValue
	}
	if value := env(envKey); value != "" {
		if n, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return n
		}
	}
	if override != 0 {
		return override
	}
	return flagValue
}

func overrideInt(o *Overrides, keys ...string) int {
	value, ok := o.lookup(keys...)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}

func overrideFloat(o *Overrides, keys ...string) float64 {
	value, ok := o.lookup(keys...)
	if !ok {
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return n
}

func (o *Options) validate() error {
	// 空值表示"未设置"：编译默认值由 app.Resolve 在配置文件和 profile 层
	// 表达完自己的意见之后应用。
	switch o.Mode {
	case "", ModeText, ModeJSON:
	default:
		return fmt.Errorf("--mode must be text or json, got %q", o.Mode)
	}
	// 短 CLI 写法和配置线上的名称都被接受；app.Resolve 会在选择聊天客户端
	// 之前把它们规范化。
	switch strings.ToLower(strings.TrimSpace(o.Protocol)) {
	case "", ProtocolAnthropic, ProtocolOpenAI, ProtocolResponses,
		"anthropic-messages", "openai-chat-completions", "openai-responses", "messages":
	default:
		return fmt.Errorf("--protocol must be anthropic, openai or responses, got %q", o.Protocol)
	}
	switch o.SandboxMode {
	case "", SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
	default:
		return fmt.Errorf("--sandbox-mode must be read-only, workspace-write or danger-full-access, got %q", o.SandboxMode)
	}
	switch o.Approval {
	case "", ApprovalAsk, ApprovalNever, ApprovalAuto:
	default:
		return fmt.Errorf("--approval must be ask, never or auto, got %q", o.Approval)
	}
	if o.MaxTurns < 1 {
		return fmt.Errorf("--max-turns must be at least 1, got %d", o.MaxTurns)
	}
	if o.MaxParallel < 0 {
		return fmt.Errorf("--max-parallel must be >= 0, got %d", o.MaxParallel)
	}
	if o.MaxTokens < 0 {
		return fmt.Errorf("--max-tokens must be >= 0, got %d", o.MaxTokens)
	}
	if o.MaxSeconds < 0 {
		return fmt.Errorf("--max-seconds must be >= 0, got %d", o.MaxSeconds)
	}
	if o.MaxCost < 0 {
		return fmt.Errorf("--max-cost must be >= 0, got %v", o.MaxCost)
	}
	if o.PriceInput < 0 || o.PriceOutput < 0 {
		return fmt.Errorf("--price-input and --price-output must be >= 0")
	}
	return nil
}

// parseInterleaved 解析可以被位置参数分隔的 flag。
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		tail := fs.Args()
		if len(tail) == 0 {
			return positional, nil
		}
		positional = append(positional, tail[0])
		rest = tail[1:]
	}
}

func splitTerminator(args []string) (before, after []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// hasHelpFlag 识别 flag 包为两个 help 开关接受的所有写法：一个或两个前导
// 破折号，加上可选的 truthy =value（-h、--h、-help、--help、-h=true、
// --help=1 等）。原来的检查只匹配精确的 "-h"/"--help" token，因此其他写法
// 被解析进 values.help，但在根据这个狭窄检查重新计算 Options.Help 时被丢弃，
// 从而丢失了用户的 help 请求。false 值不是 help 请求，未知开关也绝不会被
// 当作 help。
//
// 扫描遍历原始 argv（因此 help 仍然优先于其他无效的命令行），但会跳过
// 被当作非 bool flag 值消费的 token。Go 的 flag 包无论下一个参数是否以 '-'
// 开头都会把它当作 flag 的值，因此在 "argent --model -h" 中，"-h" 是模型
// id，而非 help 请求。没有这个跳过逻辑，任何看起来像 help 的值都会把
// 整个调用变成打印 usage 并以退出码 0 结束。
func hasHelpFlag(fs *flag.FlagSet, args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		dashes := 0
		for dashes < len(arg) && arg[dashes] == '-' {
			dashes++
		}
		if dashes == 0 || dashes > 2 || dashes == len(arg) {
			continue
		}
		name, value, hasValue := strings.Cut(arg[dashes:], "=")
		def := fs.Lookup(name)
		if def == nil {
			continue // 未知开关：绝不是 help 请求
		}
		// 裸的非 bool flag 会吞掉下一个 token 作为它的值。跳过该 token，
		// 使看起来像 help 的值不会被误认为 help 开关。
		if !hasValue && !flagIsBool(def) {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if name != "h" && name != "help" {
			continue
		}
		if hasValue {
			enabled, err := strconv.ParseBool(value)
			if err != nil || !enabled {
				continue
			}
		}
		return true
	}
	return false
}

// flagIsBool 镜像 flag 包内部用来判断一个开关是否可以不带值书写的接口。
func flagIsBool(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func overrideValue(o *Overrides, keys ...string) string {
	value, _ := o.lookup(keys...)
	return value
}

func overrideValueOK(o *Overrides, keys ...string) (string, bool) {
	return o.lookup(keys...)
}

// serveAddress 把 --serve-address 和 --serve-port 组合成一个绑定地址。
// 单独一个冒号并不表示 "host:port"：裸的 IPv6 字面量（::1）此时还没有端口，
// 必须与 --serve-port 拼接。只有已经带有显式端口的地址才会原样返回。
func serveAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	portText := strconv.Itoa(port)

	// 带方括号的 IPv6：[::1] 仍需要端口，[::1]:8080 则已经有了。
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end >= 0 {
			inner, rest := host[1:end], host[end+1:]
			switch {
			case rest == "":
				return net.JoinHostPort(inner, portText)
			case strings.HasPrefix(rest, ":"):
				return host
			}
		}
	}

	// 解析出的 IP 字面量总是裸主机，绝不是 host:port。带 zone 的 IPv6
	// （fe80::1%eth0）不会被 net.ParseIP 解析，但仍有超过 1 个冒号，
	// 因此会落到下面的 JoinHostPort，由它正确加上方括号。
	if net.ParseIP(host) != nil {
		return net.JoinHostPort(host, portText)
	}

	// 恰好带一个冒号的主机名或 IPv4 地址已经是 host:port。
	if strings.Count(host, ":") == 1 {
		return host
	}
	return net.JoinHostPort(host, portText)
}
