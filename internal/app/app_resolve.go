package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/provider"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// DefaultStorage 返回存储根目录：$ARGENT_STORAGE，否则为 ~/.argentgo。
func DefaultStorage(env func(string) string) string {
	if env == nil {
		env = os.Getenv
	}
	for _, key := range []string{"ARGENT_STORAGE", "ARGENT_STORAGE_DIR", "ARGENT_HOME"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return expandHome(value, env)
		}
	}
	home := homeDir(env)
	if home == "" {
		return ".argentgo"
	}
	return filepath.Join(home, ".argentgo")
}

func homeDir(env func(string) string) string {
	for _, key := range []string{"USERPROFILE", "HOME"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func expandHome(path string, env func(string) string) string {
	if strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if home := homeDir(env); home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// Resolve 把 CLI 选项合并到分层配置之上。
func Resolve(stdio cli.IO, opts cli.Options) (Resolved, error) {
	env := stdio.Env
	if env == nil {
		env = os.Getenv
	}

	// 流空闲超时的非法值不再静默归零：把诊断函数返回的警告文本经本包
	// 既有的 warn 通道（stderr，沿用 "config: " 前缀）输出。未设置或合法值
	// 不产生任何输出，逐字节行为不变；stderr 为 nil 时同样静默。
	if _, warning := streamIdleTimeoutFromEnv(env); warning != "" && stdio.Err != nil {
		fmt.Fprintln(stdio.Err, "config: "+warning)
	}

	// config 通过 ARGENT_PROFILE 选择 profile，因此 --profile 标志在那里注入，
	// 而不是在本包中重复处理。
	profileEnv := env
	if profile := strings.TrimSpace(opts.Profile); profile != "" {
		base := env
		profileEnv = func(key string) string {
			if key == "ARGENT_PROFILE" {
				return profile
			}
			return base(key)
		}
	}

	root := strings.TrimSpace(opts.Cwd)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve workspace: %w", err)
		}
		root = cwd
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve workspace: %w", err)
	}

	var cfg config.Config
	if len(opts.Overrides.All()) > 0 {
		cfg, err = config.LoadWithOverrides(root, profileEnv, opts.Overrides.Nested())
	} else {
		cfg, err = config.Load(root, profileEnv)
	}
	if err != nil {
		return Resolved{}, fmt.Errorf("load config: %w", err)
	}

	template, haveTemplate, err := resolveTemplate(cfg, opts)
	if err != nil {
		return Resolved{}, err
	}

	// 空选项表示"未设置"：CLI 不把编译默认值放进 Options，使配置文件和 profile 能够生效。
	// config.Load 在最底层应用编译默认值，因此 unset() 仍能区分未被触碰的值和真实的选择。
	// 标志优先于配置；具名的提供方模板优先于编译的 OpenAI 默认值，但绝不会覆盖显式的
	// 非默认配置值。
	protocol := strings.TrimSpace(opts.Protocol)
	if protocol == "" {
		protocol = cfg.Protocol
		if haveTemplate && unset(protocol, openAIProtocol) {
			protocol = template.Protocol
		}
	}
	protocol = canonicalProtocol(protocol)
	baseURL := opts.BaseURL
	// 同时追踪端点出处：项目级配置（包括项目文件里定义的 profile 和
	// providers.<name>.baseURL 覆盖）不应能在用户不知情时改写收件主机。
	baseURLFromProject := false
	if baseURL == "" {
		baseURL = cfg.BaseURL
		baseURLFromProject = cfg.BaseURLFromProject()
		if haveTemplate && unset(baseURL, openAIBaseURL) {
			baseURL = template.BaseURL
			// 模板覆盖生效时，端点来自提供方覆盖层；内置模板地址视为非项目来源。
			if source := cfg.ProviderBaseURLSource(template.Name); source != config.ValueSourceUnset {
				baseURLFromProject = source == config.ValueSourceProject
			} else {
				baseURLFromProject = false
			}
		}
	}
	model := opts.Model
	if model == "" {
		model = cfg.Model
		if haveTemplate && unset(model, openAIModel) {
			model = template.DefaultModel
		}
	}
	protocol = firstNonEmpty(protocol, openAIProtocol)
	baseURL = firstNonEmpty(baseURL, openAIBaseURL)
	model = firstNonEmpty(model, openAIModel)
	model = provider.NormalizeModel(model)

	// 凭据优先级：显式标志 / ARGENT_* 环境变量 > 顶层配置 > `argentgo login` 存储在
	// providers.<name>.apiKey 下的密钥 > 提供方自身的 API key 环境变量。
	providerName := template.Name
	if providerName == "" {
		providerName = cfg.ProviderName
	}
	configuredProvider, _ := cfg.Provider(providerName)
	// 使用由配置解析出的提供方的 APIKeyEnv，而非模板的。在默认路径中，模板是零值
	// （resolveTemplate 无法匹配 "openai-chat-completions" 这个协议名），
	// 因此 template.APIKeyEnv 为空，提供方环境变量的回退会静默失败。configuredProvider
	// 在上面已通过 cfg.Provider(providerName) 解析，它会回退到协议对应的天然提供方（openai），
	// 并带有正确的 APIKeyEnv。
	//
	// 同时记录密钥来源：只有"项目配置自己提供的密钥"才算可随项目端点一起
	// 使用；flag、环境变量或全局配置里的密钥是用户凭据，不能被项目端点截走。
	apiKey := ""
	apiKeyFromProject := false
	switch {
	case strings.TrimSpace(opts.APIKey) != "":
		apiKey = strings.TrimSpace(opts.APIKey)
	case strings.TrimSpace(cfg.APIKey) != "":
		apiKey = strings.TrimSpace(cfg.APIKey)
		apiKeyFromProject = cfg.APIKeySource == config.ValueSourceProject
	case strings.TrimSpace(configuredProvider.APIKey) != "":
		apiKey = strings.TrimSpace(configuredProvider.APIKey)
		apiKeyFromProject = cfg.ProviderAPIKeySource(providerName) == config.ValueSourceProject
	default:
		if configuredProvider.APIKeyEnv != "" {
			apiKey = strings.TrimSpace(env(configuredProvider.APIKeyEnv))
		}
	}

	// P0 外泄守卫：未受信任的项目不得把端点改写到别处再把用户密钥发过去。
	// 只拦"项目改写 baseURL + 密钥并非项目自带"这一组合；项目配置只改
	// model/超时等设置，或密钥由项目配置自行提供时，行为完全不变。
	trustProject := opts.TrustProject || (env("ARGENT_TRUST_PROJECT") != "" && parseTrustEnv(env("ARGENT_TRUST_PROJECT")))
	if !trustProject && baseURLFromProject && apiKey != "" && !apiKeyFromProject {
		return Resolved{}, fmt.Errorf(
			"refusing to send credential to baseURL %q set by an untrusted project config; pass --trust-project to allow it",
			baseURL)
	}

	sandboxMode := firstNonEmpty(opts.SandboxMode, cfg.SandboxMode, "workspace-write")
	sandbox, err := security.FromString(sandboxMode)
	if err != nil {
		return Resolved{}, fmt.Errorf("sandbox mode %q: %w", sandboxMode, err)
	}
	approval := strings.ToLower(firstNonEmpty(opts.Approval, cfgString(cfg.Raw, "approval"), "ask"))
	// --yes 是 --yolo 的非交互同义开关：自动确认审批提示。
	// 此前 opts.Yes 虽被 flags 解析，却从未参与审批模式判定，导致非交互
	// --yes 仍走默认 ask → PromptApprover 向 stdout 打印 "Allow...?" 并因
	// stdin 关闭而误判为拒绝。与 --yolo 一致地映射到 auto。
	if opts.Yolo || cfg.Yolo || opts.Yes {
		approval = "auto"
	}

	maxTurns := opts.MaxTurns
	if maxTurns == 0 {
		maxTurns = cfg.MaxTurns
	}
	if maxTurns <= 0 {
		// 抬高默认天花板而非剥夺控制：与引擎 agent.DefaultMaxTurns 对齐，
		// 避免旧默认 12 轮过早截断长任务、把未完成的工作包装成正常结束。
		// 覆盖渠道依旧优先且顺序不变：ARGENT_MAX_TURNS / --max-turns
		// （opts.MaxTurns）> -c maxTurns / 配置 / profile（cfg.MaxTurns）>
		// 此处兜底。提示词中的 `+500k` 声明仍在 app_execute.go 中作为额外的
		// 累计预算叠加，不受此默认值影响。
		maxTurns = agent.DefaultMaxTurns
	}

	// 标志优先于配置；零表示"未设置"，选用保守的默认值。显式的 1（或任何负值）使批次串行执行。
	maxParallel := firstNonZeroInt(opts.MaxParallel, intField(cfg.Raw, "maxParallel", "max-parallel"))
	if maxParallel == 0 {
		maxParallel = defaultMaxParallel
	}

	storage := firstNonEmpty(env("ARGENT_STORAGE"), env("ARGENT_STORAGE_DIR"), cfg.StorageDir)
	if storage == "" {
		storage = DefaultStorage(env)
	}

	addDirs, err := resolveAddDirs(root, opts.AddDirs)
	if err != nil {
		return Resolved{}, err
	}
	schema, err := loadOutputSchema(root, opts.OutputSchema)
	if err != nil {
		return Resolved{}, err
	}
	system := buildSystem(root, env, opts.Thinking, schema, cfg)

	res := Resolved{
		Options:      opts,
		Cfg:          cfg,
		Root:         root,
		Home:         homeDir(env),
		Storage:      storage,
		Model:        model,
		Provider:     firstNonEmpty(template.Name, cfg.ProviderName),
		Profile:      firstNonEmpty(opts.Profile, cfg.ActiveProfile),
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Protocol:     protocol,
		Locale:       firstNonEmpty(opts.Locale, cfg.Locale),
		Sandbox:      sandbox,
		Approval:     approval,
		MaxTurns:     maxTurns,
		MaxParallel:  maxParallel,
		Mutating:     sandbox != security.SandboxReadOnly,
		MaxTokens:    firstNonZeroInt(opts.MaxTokens, intField(cfg.Raw, "maxTokens", "max-tokens")),
		MaxCostUSD:   firstNonZeroFloat(opts.MaxCost, floatField(cfg.Raw, "maxCost", "max-cost")),
		MaxSeconds:   firstNonZeroInt(opts.MaxSeconds, intField(cfg.Raw, "maxSeconds", "max-seconds")),
		AddDirs:      addDirs,
		WriteRoots:   security.WriteRootsFrom(root, addDirs),
		OutputSchema: schema,
		Thinking:     opts.Thinking,
		TrustProject: trustProject,
		Plugins:      append([]string(nil), opts.Plugins...),
		System:       system,
		Prices:       pricesFor(model, opts, cfg),

		AutoCompactTokens: firstNonZeroInt(
			intField(cfg.Raw, "autoCompactTokens", "auto-compact-tokens"),
			compactThresholdFrom(cfg.Raw),
		),
		NoAutoCompact: opts.NoAutoCompact,
	}
	wireOpts := cacheOptionsForConfig(cfg, opts, template.CacheSupport)
	// 续接时 opts.Session 非空，把它接入线上选项以派生跨 turn/fork
	// 稳定的 Responses 缓存键；新会话为空时应保持旧字节不变。
	wireOpts.SessionID = opts.Session
	wireOpts.Thinking = thinkingOptionsForProtocol(res.Protocol, res.Thinking)
	// 严格终止符校验与流恢复共用同一 opt-in 开关：开启回补时需要先把
	// "纯文本中途截断" 识别为 ErrStreamError，否则回补门永不触发。
	wireOpts.StrictStreamTerminator = streamRecoveryEnabled(env)
	// 输出上限拟合（默认开启）：注入当前模型的上下文窗口，使 chat completions
	// 在提示接近窗口时自动压低 max_tokens，避免 "prompt is too long" 的 400。
	// Anthropic 协议在窗口边界由宿主停生成，llm 侧自动短路，不受影响。
	wireOpts.ContextWindow = provider.ContextWindowFor(res.Provider, res.Model, res.Cfg.ContextWindow)
	res.Chatter = newChatter(res.Protocol, baseURL, apiKey, wireOpts)
	if fallback := strings.TrimSpace(opts.FallbackModel); fallback != "" {
		res.Chatter = &fallbackChatter{primary: res.Chatter, fallback: fallback}
	}
	return res, nil
}

// cacheOptionsForConfig 构建供 newChatter 使用的线上层缓存选项。合并后的配置可能携带显式的
// 缓存层级；该层级的"是否订阅"和"端点能力"输入目前还没有来源，
// 因此生产路径只接入显式配置分支：保守的事实（非订阅、支持长效）使未设置的层级停留在旧字面量
// 所产生的短缓存/提供方默认值条目上，而显式的 "long"/"none"/"short" 选择会被遵守。
// 订阅用户的长效默认值属于更晚的接线点，那里能拿到凭据类型和端点的长期保留能力。
func cacheOptionsForConfig(cfg config.Config, opts cli.Options, supportsCache bool) llm.Options {
	configTier := cfgString(cfg.Raw, "cacheTier", "cache-tier", "promptCacheTier", "prompt-cache-tier")
	out := resolveCacheOptions(configTier, false, supportsCache, opts.NoPromptCache)
	// 模板声明不支持缓存且用户未显式选择层级时，关闭线上缓存标记，
	// 避免兼容性差的网关收到 prompt_cache_key / cache_control。
	if !supportsCache && strings.TrimSpace(configTier) == "" {
		out.NoPromptCache = true
	}
	return out
}

// thinkingOptionsForProtocol 把用户的思考档位解析成协议对应的原生推理控制。
// 这是纯函数：thinking 为空表示未启用，返回 nil 使线上字节不变；解析结果全零
// （未知档位或不支持的协议）同样返回 nil。Prompt 侧的思考语义由 buildSystem 负责，
// 这里只决定 llm.Options 中的原生参数，二者互不替代。
func thinkingOptionsForProtocol(protocol, thinking string) *llm.ThinkingConfig {
	if strings.TrimSpace(thinking) == "" {
		return nil
	}
	cfg := provider.ResolveThinking(protocol, thinking, provider.DefaultThinkingBudget(thinking))
	if cfg.Effort == "" && cfg.BudgetTokens == 0 && cfg.Enabled == nil {
		return nil
	}
	return &cfg
}

// resolveCacheOptions 把已解析的层级映射到 llm.Options 的缓存开关。短层级（包括未设置的默认值）
// 保留提供方默认的 TTL（空的 CacheRetention），因此历史线上格式不变。长层级发出 1 小时的保留提示，
// none 则是显式的退出选择。CLI 的 NoPromptCache 始终优先：llm 把它置于其他所有缓存开关之上。
func resolveCacheOptions(configTier string, isSubscription, supportsLong, noPromptCache bool) llm.Options {
	tier := ResolveCacheTier(configTier, isSubscription, supportsLong)
	out := llm.Options{NoPromptCache: noPromptCache}
	switch tier {
	case CacheTierLong:
		out.CacheRetention = "1h"
	case CacheTierNone:
		out.NoPromptCache = true
	}
	return out
}

// resolveTemplate 依次遵循 --provider、配置中的提供方，然后是旧标志接口所使用的协议名。
func resolveTemplate(cfg config.Config, opts cli.Options) (providerChoice, bool, error) {
	if name := strings.TrimSpace(opts.Provider); name != "" {
		if choice, ok := lookupProvider(cfg, name); ok {
			return choice, true, nil
		}
		return providerChoice{}, false, fmt.Errorf("unknown provider %q (see `argentgo models`)", name)
	}
	for _, name := range []string{cfg.ProviderName, opts.Protocol, cfg.Protocol} {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if choice, ok := lookupProvider(cfg, name); ok {
			return choice, true, nil
		}
	}
	return providerChoice{}, false, nil
}

func lookupProvider(cfg config.Config, name string) (providerChoice, bool) {
	// cfg.Provider 把配置覆盖层合并到同名内置提供方之上（且已回退到内置项），
	// 因此必须首先查询它。否则 provider.Resolve 会返回硬编码的模板，
	// 而配置中的 providers.<name>.baseURL 等覆盖项会被静默丢弃。
	if entry, ok := cfg.Provider(name); ok {
		// 配置覆盖不携带 CacheSupport：从同名内置模板查回该能力位。
		supportsCache := false
		if builtin, ok := provider.Resolve(entry.Name); ok {
			supportsCache = builtin.CacheSupport
		}
		return providerChoice{
			Name:         entry.Name,
			Protocol:     entry.Protocol,
			BaseURL:      entry.BaseURL,
			DefaultModel: entry.DefaultModel,
			APIKeyEnv:    entry.APIKeyEnv,
			CacheSupport: supportsCache,
		}, true
	}
	// provider.Resolve 仍然是别名（claude、kimi 等）以及配置层不认识的内置提供方
	// （deepseek、xai 等）的回退。
	if template, ok := provider.Resolve(name); ok {
		return providerChoice{
			Name:         template.Name,
			Protocol:     string(template.Protocol),
			BaseURL:      template.BaseURL,
			DefaultModel: template.DefaultModel,
			APIKeyEnv:    template.APIKeyEnv,
			CacheSupport: template.CacheSupport,
		}, true
	}
	return providerChoice{}, false
}

// resolveAddDirs 把 --add-dir 的值转换为相对于工作区的绝对路径。
func resolveAddDirs(root string, dirs []string) ([]string, error) {
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("--add-dir %q: %w", dir, err)
		}
		out = append(out, abs)
	}
	return out, nil
}

// cfgString 从合并后的配置树中读取第一个字符串键。
func cfgString(tree map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := tree[key].(string); ok {
			return text
		}
	}
	return ""
}

// canonicalProtocol 把 CLI 的简短写法和配置的线上名称映射到统一的词汇表，
// 使 unset() 和 chatter 工厂对 "openai" 的含义达成一致。未知值原样透传。
func canonicalProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "openai", "openai-chat-completions":
		return openAIProtocol
	case "anthropic", "anthropic-messages", "messages":
		return string(provider.ProtocolAnthropic)
	case "responses", "openai-responses":
		return string(provider.ProtocolResponses)
	default:
		return strings.TrimSpace(value)
	}
}

// intField 从合并后的配置树中读取第一个数值键。
func intField(tree map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case json.Number:
			n, err := value.Int64()
			if err == nil {
				return int(n)
			}
		}
	}
	return 0
}

// floatField 从合并后的配置树中读取第一个浮点数值键。
func floatField(tree map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case float64:
			return value
		case int:
			return float64(value)
		case json.Number:
			n, err := value.Float64()
			if err == nil {
				return n
			}
		}
	}
	return 0
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroFloat(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// pricesFor 根据 --price-input/--price-output 构建 agent 价格表。
func pricesFor(model string, opts cli.Options, cfg config.Config) map[string]agent.Price {
	if opts.PriceInput == 0 && opts.PriceOutput == 0 {
		raw, ok := cfg.Raw["prices"].(map[string]any)
		if !ok {
			return nil
		}
		prices := make(map[string]agent.Price, len(raw))
		for name, value := range raw {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			prices[name] = agent.Price{Input: floatField(entry, "input"), Output: floatField(entry, "output")}
		}
		return prices
	}
	return map[string]agent.Price{
		model: {Input: opts.PriceInput, Output: opts.PriceOutput},
	}
}

const (
	openAIProtocol = "openai-chat-completions"
	openAIBaseURL  = "https://api.openai.com/v1"
	openAIModel    = "gpt-4o-mini"

	// defaultMaxParallel 在标志、环境变量和配置都没有选值时，限制只读工具批次。
	// 它刻意取较小值，使文件系统和钩子的负载保持可预测。
	defaultMaxParallel = 4
)

// unset 报告某个配置值是否仍是编译默认值，这是判断"用户没有选择此项"的唯一可用信号。
func unset(value, compiledDefault string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || strings.EqualFold(trimmed, compiledDefault)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
