// Package config 为 argentgo 解析分层配置。
//
// 优先级，从低到高：
//
//  1. 编译期默认值
//  2. $HOME/.argentgo/config.json
//  3. <cwd>/.argent/config.json
//  4. <cwd>/argent.json
//  5. 选定的 profile（ARGENT_PROFILE，或文件中的 "profile"）
//  6. ARGENT_* 环境变量
//  7. 显式 flag 覆盖
//
// 未知键会保留在 Raw 中，以便后续的设置或集成读取
// 它们，而无需在此添加结构体字段。
package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Validate 接受的沙箱模式。
const (
	SandboxReadOnly         = "read-only"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxDangerFullAccess = "danger-full-access"
)

// validatePrivacyMode 接受的网络模式。它们对应
// security.PrivacyAny / security.PrivacyLocalOnly；config 有意保持
// 与 security 包独立。
const (
	PrivacyModeAny       = "any"
	PrivacyModeLocalOnly = "local-only"
)

const (
	appDir          = ".argentgo"
	defaultModel    = "gpt-4o-mini"
	defaultBaseURL  = "https://api.openai.com/v1"
	defaultProtocol = "openai-chat-completions"
	defaultLocale   = "en-US"
	// 默认轮次预算抬高到 100，与引擎 agent.DefaultMaxTurns 对齐（config 包
	// 保持零依赖，故用字面量而非跨包引用）。这是抬高默认天花板而非剥夺
	// 控制：ARGENT_* 环境变量、-c 覆盖、profile 仍可任意收窄该预算。
	defaultMaxTurns = 100
	// defaultVerifyTimeoutMS 在 decode 中应用（而非 defaults()），因为该
	// 设置存在别名；在基础层注入规范键会遮蔽文件中的
	// "verify_timeout_ms" 拼写。
	defaultVerifyTimeoutMS = 30000
)

// Config 是合并后的配置。
type Config struct {
	Model       string
	BaseURL     string
	APIKey      string
	Protocol    string
	Locale      string
	SandboxMode string // read-only | workspace-write | danger-full-access
	// PrivacyMode 是网络姿态：PrivacyModeAny（"any"，默认值）
	// 或 PrivacyModeLocalOnly（"local-only"）。local-only 会阻止远程端点
	// 和 web 工具。它与管理文件工具的 SandboxMode 相互独立。
	// 无效值会导致配置加载失败，而不是悄悄放宽姿态；
	// app.ApplyPrivacyPosture 是实际使用方。
	PrivacyMode string
	MaxTurns    int
	Yolo        bool
	StorageDir  string

	// VerifyCommand 是选择启用的编辑后校验 argv（例如
	// ["go", "test", "./..."]）。为空则禁用有界的反思循环。
	// app.NewVerifyFunc 在使用前会按严格白名单再次校验它。
	VerifyCommand []string
	// VerifyCommands 是可选的映射，把语言或文件扩展名
	// （例如 "go"、".go"、"py"、".py"）映射到以空格分隔的
	// 命令字符串（例如 "go test ./..."）。非空时，应用会
	// 按每个被编辑文件的语言派发一条命令，而不使用 VerifyCommand；
	// 没有匹配键的路径在设置了 VerifyCommand 时回退到它。
	// 值通过 strings.Fields 解析，并由 app.NewVerifyFunc 按与
	// VerifyCommand 相同的严格白名单校验；不涉及 shell，
	// 因此不支持引号和 shell 元字符。为空则完全保持
	// 单命令的行为。
	VerifyCommands map[string]string
	// VerifyTimeoutMS 限制单个校验进程（默认 30000，上限
	// 120000）。零值选择默认值。
	VerifyTimeoutMS int
	// VerifyMaxReflections 限制 FAIL 反馈轮次；零值选择循环自身的
	// 默认值 3。
	VerifyMaxReflections int

	// ContextWindow 是解析后的模型上下文窗口（以 token 计）。它是
	// 可选的：零表示"未指定"，调用方会回退到固定阈值。
	// 可在顶层或某个 model/provider 条目上设置。
	ContextWindow int
	// StopTokenIDs 是终止补全的 token id（例如 FIM 的
	// 填充结束 token）。可选：空切片不改变 LLM 层现有的
	// 停止处理。从 stopTokenIds/stop_token_ids 解码。
	StopTokenIDs []int64
	// ContextWindowInfo 携带可选的长上下文元数据（目前是 rope
	// scaling），长上下文提供方可能会把它转发到上游端点。
	// 其零值表示"未指定"，会被调用方忽略。
	ContextWindowInfo ContextWindowInfo
	// AutoCondenseContextPercent 是每个 profile 触发自动压缩的
	// 上下文窗口百分比。0 或 -1 继承全局 token
	// 阈值；正值则覆盖它。
	AutoCondenseContextPercent int

	// ProviderName 是选定的提供方模板 id。
	ProviderName string
	// ActiveProfile 是合并到文件层之上的 profile（如果有）。
	ActiveProfile string
	// Profiles 保存所有内联 profile 定义，以名称为键。
	Profiles map[string]map[string]any
	// Providers 保存配置中的提供方模板，以小写名称为键。
	Providers map[string]Provider
	// Sources 列出被合并的配置文件，优先级从低到高。
	Sources []string
	// Raw 是合并后的 JSON 树，包含未知键。
	Raw map[string]any
	// CWD 是解析此配置时所在的工作区。
	CWD string

	// 下面的字段是未导出的重载元数据。它们由
	// Load/LoadWithOverrides 填充，让长生命周期的调用方可以检测磁盘改动
	// （Changed）并重建配置（Reload），而无需重启。它们是
	// 附加性的：用键名字面量构造 Config 的外部包
	// 不受影响。
	loadEnv       func(string) string
	loadOverrides map[string]any
	generation    uint64
	fingerprint   configFingerprint
}

// ContextWindowInfo 是提供方可以转发到上游端点的可选长上下文元数据。
// 当相关键缺失时，所有成员保持零值，
// 因此不使用它们的配置解码行为与之前完全一致。
type ContextWindowInfo struct {
	RopeScaling RopeScaling
}

// RopeScaling 对应 vLLM/llama.cpp 风格端点使用的 `rope_scaling` 对象。
// 解码时会忽略未知键。
type RopeScaling struct {
	Factor      float64
	Type        string
	OriginalMax int64
}

// fileStamp 是单个配置文件的免解析身份标识。mtime 和 size
// 足以发现普通的重写；hash 弥补了重写前后两者都不变的缺口
// （例如在同一 mtime 周期内进行的等长编辑）。Existence 和 statErr 用于捕获
// 新建、删除或不可读的文件。文件缺失或不可读时 hash 为零，
// 此时仅依靠 stat 相关的字段作为身份标识（保持
// 原有的保守行为）。
type fileStamp struct {
	path    string
	modTime int64
	size    int64
	hash    [sha256.Size]byte // 原始字节的 SHA-256；不可读时为零。
	exists  bool
	statErr bool
}

// configFingerprint 是每一层配置文件有序的快照。文件列表
// 始终包含所有候选路径（包括缺失的），以便
// 把新建配置文件检测为一次变更。
type configFingerprint struct {
	files []fileStamp
}

// Load 为 cwd 合并所有配置层。env 提供进程环境，
// 且是 Load 读取环境的唯一途径，从而保证测试隔离。
func Load(cwd string, env func(string) string) (Config, error) {
	return LoadWithOverrides(cwd, env, nil)
}

// LoadWithOverrides 是 Load 加上优先级最高的 flag 层。
// overrides map 使用与配置文件相同的 camelCase 键。
func LoadWithOverrides(cwd string, env func(string) string, overrides map[string]any) (Config, error) {
	if env == nil {
		env = func(string) string { return "" }
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}

	home := homeDir(env)
	tree := defaults(cwd)
	sources := make([]string, 0, 3)
	warn := func(format string, args ...any) { log.Printf(format, args...) }
	for _, file := range configFiles(cwd, home) {
		overlay, err := readConfig(file, env, warn)
		if err != nil {
			return Config{}, err
		}
		if overlay == nil {
			continue
		}
		mergeTree(tree, overlay)
		sources = append(sources, file)
	}

	profile := strings.TrimSpace(env("ARGENT_PROFILE"))
	if profile == "" {
		// `-c profile=<name>` 覆盖也会选定 profile。它位于
		// 文件覆盖层：胜过配置文件中的 "profile" 键，但
		// ARGENT_PROFILE（同样承载 --profile flag）仍然优先。
		// 这里只读取选择器；其他所有覆盖保持其在 env 层之后的
		// 原始位置，因此把 profile 选择提前
		// 不会改变这些值的合并方式。
		profile = strings.TrimSpace(stringField(overrides, "profile", "activeProfile"))
	}
	if profile == "" {
		profile = stringField(tree, "profile", "activeProfile")
	}
	if profile != "" {
		profiles := profilesFrom(tree)
		if overlay, ok := lookupFold(profiles, profile); ok {
			mergeTree(tree, overlay)
		} else if len(profiles) > 0 {
			return Config{}, fmt.Errorf("config: unknown profile %q", profile)
		}
	}

	if err := applyEnv(tree, env); err != nil {
		return Config{}, err
	}
	if len(overrides) > 0 {
		mergeTree(tree, overrides)
	}

	cfg := decode(tree, cwd, home, sources)
	// 对 fail-closed 诊断摘要报告的每一项不变量，都在加载时 fail closed。
	// 让 Load、Diagnostics（doctor）和 Validate 共享同一事实来源，
	// 这样不安全的配置不会在一条路径上被接受，
	// 却在另一条路径上被悄悄标记。
	if issues := cfg.failClosedDiagnostics(); len(issues) > 0 {
		return Config{}, fmt.Errorf("config: %s", issues[0].Message)
	}
	cfg.ActiveProfile = profile
	// 不要把提供方的 env 键折叠进 cfg.APIKey。app.Resolve 已经
	// 把 env(provider.APIKeyEnv) 列为凭据链的最后手段；
	// 在这里复制会让它凌驾于 providers.<name>.apiKey 下
	// `argent login` 保存的键之上，破坏文档记录的优先级。
	//
	// 附加重载元数据，让长生命周期的调用方可以轮询 Changed 并调用
	// Reload。首次成功加载为第 1 代；每次 Reload 都会递增它。
	cfg.loadEnv = env
	cfg.loadOverrides = cloneTree(overrides)
	cfg.generation = 1
	cfg.fingerprint = newFingerprint(cwd, home)
	return cfg, nil
}

// Validate 拒绝程序其余部分无法执行的配置。
// 旧的值级失败保留其首个错误的顺序以兼容旧行为；
// 随后立即检查 fail-closed 不变量。使用 Diagnostics 可以
// 一次看到所有问题。
func (c Config) Validate() error {
	diagnostics := c.validateDiagnostics()
	diagnostics = append(diagnostics, c.failClosedDiagnostics()...)
	if len(diagnostics) == 0 {
		return nil
	}
	first := diagnostics[0]
	return fmt.Errorf("config: %s", first.Message)
}

// Generation 返回此配置快照的版本。首次加载为
// 第 1 代；每次成功的 Reload 递增一。调用方可以用它
// 发现对等方重建了配置，即使它没有在重写的那一刻轮询 Changed。
// 在重写的确切时刻。
func (c Config) Generation() uint64 {
	return c.generation
}

// Changed 报告磁盘上的配置文件是否与此 Config 加载自的快照
// 不同。它是面向长生命周期调用方的轮询钩子：
// 对每个候选文件比较 mtime、size 和存在性，外加原始字节的
// SHA-256 hash。hash 弥补了重写同时保留 size 和 mtime 的盲区
// （在文件系统时间戳较粗时可能发生），因此
// 等长的原地编辑也能被检测到。配置文件很小，而且 Load 本来就会
// 完整读取它们，所以每次轮询额外读取的开销很低；Changed
// 从不解析或合并它们。检测到变化时，调用 Reload 获取
// 新值。手工构造的 Config（非经 Load）没有可比较的快照，
// 因此总是返回 false。
//
// Changed 只观察配置文件。环境变量和 flag 覆盖
// 在加载时即被捕获；Changed 无法看到它们的变化。Reload 复用
// 原始的 env 函数，因此基于 os.Getenv 的 env 函数在重载时
// 仍能拾取进程级的变化。
//
// 缺失或不可读的文件保持零 hash，并回退到
// 仅依靠 stat 的身份标识（mtime、size、存在性），与原有行为一致。
func (c Config) Changed() bool {
	if len(c.fingerprint.files) == 0 {
		return false
	}
	paths := make([]string, len(c.fingerprint.files))
	for i, stamp := range c.fingerprint.files {
		paths[i] = stamp.path
	}
	return !fingerprintPaths(paths).equal(c.fingerprint)
}

// Reload 使用与原始加载相同的 cwd、env 和 flag 覆盖，从磁盘
// 重新读取每一层配置，并返回一个 Generation 加一的
// 新 Config。接收者保持不变，出错时返回 nil，
// 以便调用方继续使用当前配置。优先级和
// 合并语义与 Load 完全相同，因为 Reload 委托给
// LoadWithOverrides。
func (c Config) Reload() (*Config, error) {
	cwd := c.CWD
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	next, err := LoadWithOverrides(cwd, c.loadEnv, c.loadOverrides)
	if err != nil {
		return nil, err
	}
	next.generation = c.generation + 1
	return &next, nil
}

func defaults(cwd string) map[string]any {
	// 这里只放没有可接受别名的键。在基础层注入规范默认值
	// 会遮蔽配置文件中的别名拼写
	// （例如 "modelName" 或 "max_turns"）：mergeTree 会保留两个键，
	// stringField/intField 先读规范键，从而悄悄丢弃
	// 文件中的值，甚至在诊断中报告虚假的"冲突"。
	// 带别名的设置改为在 decode 中应用默认值，正如
	// decodePrivacyMode 对 privacyMode/privacy_mode 所做的那样。
	return map[string]any{
		"protocol": defaultProtocol,
		"locale":   defaultLocale,
		"yolo":     false,
		"provider": "",
		"cwd":      cwd,
		// 选择启用的校验默认关闭：空的 argv 会让 runner 的
		// Verify 回调为 nil，因此现有运行保持字节级一致。超时
		// 默认值通过 intFieldDefault 在 decode 中应用，以免这里
		// 的规范键遮蔽带别名的文件键。
	}
}

// configFiles 列出各文件层，优先级从低到高。
func configFiles(cwd, home string) []string {
	return []string{
		filepath.Join(home, appDir, "config.json"),
		filepath.Join(cwd, ".argent", "config.json"),
		filepath.Join(cwd, "argent.json"),
	}
}

// newFingerprint 为 cwd/home 快照每个候选配置文件，包括
// 缺失的文件（记录为 exists=false），以便检测之后的创建。
func newFingerprint(cwd, home string) configFingerprint {
	return fingerprintPaths(configFiles(cwd, home))
}

// fingerprintPaths 对每个路径执行 stat 并记录免解析身份，然后
// 对任何已存在文件的原始字节做 hash。对内容（而非解析后的 JSON）
// 做 hash 意味着即使是等长、等 mtime 的重写也能被检测到，同时
// BOM、注释或纯空白的编辑也能被忠实记录。非"不存在"的 stat
// 错误会记录为 statErr，从而把暂时不可读的文件保守地
// 当作变更，而不是匹配
// 一个缺失文件的快照。缺失文件和读取失败保持零 hash，
// 并依赖仅基于 stat 的身份标识，保持原有行为。
func fingerprintPaths(paths []string) configFingerprint {
	stamps := make([]fileStamp, 0, len(paths))
	for _, path := range paths {
		stamp := fileStamp{path: path}
		switch info, err := os.Stat(path); {
		case err == nil:
			stamp.exists = true
			stamp.size = info.Size()
			stamp.modTime = info.ModTime().UnixNano()
			// Load 本来就会完整读取配置文件；在这里对相同的
			// 字节做 hash，可保证快照与每次 Changed() 轮询一致。
			if data, readErr := os.ReadFile(path); readErr == nil {
				stamp.hash = sha256.Sum256(data)
			}
		case os.IsNotExist(err):
			// 文件缺失：保持零身份标识不变。
		default:
			stamp.statErr = true
		}
		stamps = append(stamps, stamp)
	}
	return configFingerprint{files: stamps}
}

// equal 报告两个指纹是否描述相同的文件身份。
func (f configFingerprint) equal(other configFingerprint) bool {
	if len(f.files) != len(other.files) {
		return false
	}
	for i := range f.files {
		a, b := f.files[i], other.files[i]
		if a.path != b.path || a.exists != b.exists || a.size != b.size ||
			a.modTime != b.modTime || a.statErr != b.statErr || a.hash != b.hash {
			return false
		}
	}
	return true
}

func homeDir(env func(string) string) string {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	return "."
}

// readConfig 读取一个 JSONC 对象。解码前会替换变量引用，
// 并剥离注释和尾随逗号。文件缺失不算错误；
// 格式错误则是错误，因为运维人员需要知道某项设置
// 并未生效。
func readConfig(path string, env func(string) string, warn WarnFunc) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	// PowerShell 5.x 写出的是"UTF-8 with BOM"；encoding/json 会把 BOM 当作
	// 非法的首字符而拒绝。在空检查之前剥离它，使
	// 只含 BOM 的文件与空文件完全一样处理。
	text := strings.TrimPrefix(string(data), "\ufeff")
	if len(strings.TrimSpace(text)) == 0 {
		return nil, nil
	}
	substituted := Substitute(text, filepath.Dir(path), env, warn)
	clean := StripJSONC(substituted)
	var tree map[string]any
	if err := json.Unmarshal([]byte(clean), &tree); err != nil {
		return nil, fmt.Errorf("config: invalid JSON in %s: %w", path, err)
	}
	if tree == nil {
		return nil, nil
	}
	return tree, nil
}

func applyEnv(tree map[string]any, env func(string) string) error {
	setString := func(key, name string) {
		if value := strings.TrimSpace(env(name)); value != "" {
			tree[key] = value
		}
	}
	setString("model", "ARGENT_MODEL")
	setString("baseURL", "ARGENT_BASE_URL")
	setString("apiKey", "ARGENT_API_KEY")
	setString("protocol", "ARGENT_PROTOCOL")
	setString("locale", "ARGENT_LOCALE")
	setString("sandboxMode", "ARGENT_SANDBOX_MODE")
	setString("privacyMode", "ARGENT_PRIVACY_MODE")
	setString("storageDir", "ARGENT_STORAGE_DIR")
	setString("provider", "ARGENT_PROVIDER")

	if value := strings.TrimSpace(env("ARGENT_MAX_TURNS")); value != "" {
		turns, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config: ARGENT_MAX_TURNS=%q is not an integer", value)
		}
		tree["maxTurns"] = float64(turns)
	}
	if value := strings.TrimSpace(env("ARGENT_YOLO")); value != "" {
		flag, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("config: ARGENT_YOLO=%q is not a boolean", value)
		}
		tree["yolo"] = flag
	}
	return nil
}

// decode 从完全合并后的树构造 Config。home 仅用于
// storageDir 的编译期默认值，它（与其他带别名的设置一样）
// 在这里而非 defaults() 中应用，以便文件中的别名优先。
func decode(tree map[string]any, cwd, home string, sources []string) Config {
	return Config{
		Model:       stringFieldDefault(tree, defaultModel, "model", "modelName"),
		BaseURL:     stringFieldDefault(tree, defaultBaseURL, "baseURL", "baseUrl", "base_url"),
		APIKey:      stringFieldDefault(tree, "", "apiKey", "api_key"),
		Protocol:    stringField(tree, "protocol"),
		Locale:      stringField(tree, "locale"),
		SandboxMode: stringFieldDefault(tree, SandboxWorkspaceWrite, "sandboxMode", "sandbox_mode"),
		PrivacyMode: decodePrivacyMode(tree),
		MaxTurns:    intFieldDefault(tree, defaultMaxTurns, "maxTurns", "max_turns"),
		Yolo:        boolField(tree, "yolo"),
		StorageDir:  stringFieldDefault(tree, filepath.Join(home, appDir), "storageDir", "storage_dir"),

		VerifyCommand:        stringSliceField(tree, "verifyCommand", "verify_command"),
		VerifyCommands:       stringMapField(tree, "verifyCommands", "verify_commands"),
		VerifyTimeoutMS:      intFieldDefault(tree, defaultVerifyTimeoutMS, "verifyTimeoutMS", "verify_timeout_ms"),
		VerifyMaxReflections: intField(tree, "verifyMaxReflections", "verify_max_reflections"),

		ContextWindow:              contextWindowFrom(tree),
		StopTokenIDs:               int64SliceField(tree, "stopTokenIds", "stop_token_ids"),
		ContextWindowInfo:          contextWindowInfoFrom(tree),
		AutoCondenseContextPercent: intField(tree, "autoCondenseContextPercent", "auto_condense_context_percent"),
		ProviderName:               stringField(tree, "provider"),
		Profiles:                   profilesFrom(tree),
		Providers:                  providersFrom(tree),
		Sources:                    sources,
		Raw:                        cloneTree(tree),
		CWD:                        cwd,
	}
}

// mergeTree 把 overlay 合并进 base：对象递归合并，其他
// 值（包括数组）被替换。
func mergeTree(base, overlay map[string]any) {
	for key, value := range overlay {
		if child, ok := value.(map[string]any); ok {
			if existing, ok := base[key].(map[string]any); ok {
				mergeTree(existing, child)
				continue
			}
			base[key] = cloneTree(child)
			continue
		}
		base[key] = value
	}
}

func cloneTree(tree map[string]any) map[string]any {
	out := make(map[string]any, len(tree))
	for key, value := range tree {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneTree(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return typed
	}
}

func stringField(tree map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := tree[key].(string); ok {
			return text
		}
	}
	return ""
}

// anyKeyPresent 报告 keys 中是否至少有一个存在于 tree 中，无论其
// 值类型如何。默认值不能覆盖显式设置，即使
// 该设置类型错误：Validate/诊断仍然需要看到那个错误的值。
func anyKeyPresent(tree map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := tree[key]; ok {
			return true
		}
	}
	return false
}

// stringFieldDefault 是带编译期回退值的 stringField，仅当
// 所有别名都不存在时才使用。这与向 defaults() 注入规范键相反：
// 现在文件中写作 "base_url" 的设置会胜过
// 默认值 "baseURL"，而不再被它悄悄遮蔽。
func stringFieldDefault(tree map[string]any, def string, keys ...string) string {
	if !anyKeyPresent(tree, keys...) {
		return def
	}
	return stringField(tree, keys...)
}

// intFieldDefault 是采用相同"基于存在性"回退的 intField。
func intFieldDefault(tree map[string]any, def int, keys ...string) int {
	if !anyKeyPresent(tree, keys...) {
		return def
	}
	return intField(tree, keys...)
}

// stringSliceField 从合并后的树解码 argv 风格的 JSON 数组。
// []any（来自 JSON/CLI 覆盖）和 []string（测试中构造）
// 都可接受；非字符串成员会被跳过，而不会让整个配置失败。
func stringSliceField(tree map[string]any, keys ...string) []string {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case []any:
			out := make([]string, 0, len(value))
			for _, item := range value {
				if text, ok := item.(string); ok {
					out = append(out, text)
				}
			}
			return out
		case []string:
			return append([]string(nil), value...)
		}
	}
	return nil
}

// stringMapField 从合并后的树解码 string -> string 的 JSON 对象
// （例如 verifyCommands 的语言映射）。map[string]any
// （来自 JSON 和 CLI 覆盖）和 map[string]string（测试中构造）
// 都可接受；非字符串值会被跳过，而不会让配置失败。
func stringMapField(tree map[string]any, keys ...string) map[string]string {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case map[string]any:
			out := make(map[string]string, len(value))
			for name, raw := range value {
				if text, ok := raw.(string); ok {
					out[name] = text
				}
			}
			return out
		case map[string]string:
			out := make(map[string]string, len(value))
			for name, text := range value {
				out[name] = text
			}
			return out
		}
	}
	return nil
}

func intField(tree map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case int64:
			return int(value)
		case json.Number:
			if n, err := value.Int64(); err == nil {
				return int(n)
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				return n
			}
		}
	}
	return 0
}

// int64SliceField 从合并后的树解码整数 id 数组（例如 stopTokenIds）。
// []any（来自 JSON/CLI 覆盖）和 []int64（测试中构造）
// 都可接受；不在 int64 范围内的非整数成员会被跳过，
// 而不会让整个配置失败，与 stringSliceField 的
// 容忍度一致。
func int64SliceField(tree map[string]any, keys ...string) []int64 {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case []any:
			out := make([]int64, 0, len(value))
			for _, item := range value {
				if id, ok := int64Value(item); ok {
					out = append(out, id)
				}
			}
			return out
		case []int64:
			return append([]int64(nil), value...)
		}
	}
	return nil
}

// int64Value 把 JSON、flag 和测试产生的数值形式转换为
// int64。小数或超出范围的值会被拒绝。
func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		// 2^63 是精确的 float64 边界：int64 的最大值（2^63-1）会
		// 向上舍入到它，因此需要严格的上界以避免溢出。
		const limit = 9223372036854775808.0
		if typed == math.Trunc(typed) && typed >= -limit && typed < limit {
			return int64(typed), true
		}
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return n, true
		}
	}
	return 0, false
}

// int64Field 解码一个整数，既接受 int64Value 的数值形式，
// 也接受数字字符串。
func int64Field(tree map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if id, ok := int64Value(tree[key]); ok {
			return id
		}
		if text, ok := tree[key].(string); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// float64Field 从合并后的树解码浮点设置。它
// 用于 rope scaling 的 factor；缺失或无效的值保持为零。
func float64Field(tree map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case float64:
			return value
		case int:
			return float64(value)
		case int64:
			return float64(value)
		case json.Number:
			if f, err := value.Float64(); err == nil {
				return f
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				return f
			}
		}
	}
	return 0
}

// objectField 返回第一个持有 JSON 对象的键，这样调用方可以
// 通过任一拼写读取嵌套设置，而无需单独做 nil 检查循环。
func objectField(tree map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if obj, ok := tree[key].(map[string]any); ok {
			return obj
		}
	}
	return nil
}

func boolField(tree map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case bool:
			return value
		case string:
			if flag, err := parseBool(value); err == nil {
				return flag
			}
		}
	}
	return false
}

func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("not a boolean: %q", value)
}

// decodePrivacyMode 从任一拼写读取网络姿态，并将其
// 规范化（去空白、转小写）以匹配 security 枚举。
// 未设置或空值回退到默认的 "any" 姿态。默认值
// 在这里而非 defaults() 中应用，这样显式的 "privacy_mode" 键
// 永远不会被基础层注入的规范 "privacyMode" 键
// 遮蔽。
func decodePrivacyMode(tree map[string]any) string {
	mode := strings.ToLower(strings.TrimSpace(stringField(tree, "privacyMode", "privacy_mode")))
	if mode == "" {
		return PrivacyModeAny
	}
	return mode
}

// validatePrivacyMode 拒绝隐私辅助函数无法执行的值。拼写错误
// 会导致配置加载失败，而不是悄悄回退到宽松的 "any"
// 姿态：security.AllowRemote 对未知模式本就 fail closed，而加载时
// 报错能让该不变量保持一致。错误不带 "config:"
// 前缀，因此可原样复用为 Diagnostic 消息；调用方在
// 把它作为错误呈现时再加上前缀。
func validatePrivacyMode(mode string) error {
	switch mode {
	case PrivacyModeAny, PrivacyModeLocalOnly:
		return nil
	default:
		return fmt.Errorf("privacyMode %q must be %q or %q", mode, PrivacyModeAny, PrivacyModeLocalOnly)
	}
}

// contextWindowFrom 从合并后的树解析模型上下文窗口。
// 顶层的 contextWindow（或别名）优先；然后是 "models" 下以
// 所配置模型为键的条目，最后是活动的提供方条目。省略这些键的
// 配置仍解码为零，因此现有行为不变。
func contextWindowFrom(tree map[string]any) int {
	if value := intField(tree, "contextWindow", "contextWindowTokens", "context_window"); value > 0 {
		return value
	}
	model := stringField(tree, "model", "modelName")
	if model != "" {
		if models, ok := tree["models"].(map[string]any); ok {
			if entry, ok := lookupFold(models, model); ok {
				if modelEntry, ok := entry.(map[string]any); ok {
					if value := intField(modelEntry, "contextWindow", "contextWindowTokens", "context_window"); value > 0 {
						return value
					}
				}
			}
		}
	}
	provider := stringField(tree, "provider")
	if provider != "" {
		if providers, ok := tree["providers"].(map[string]any); ok {
			if entry, ok := lookupFold(providers, provider); ok {
				if providerEntry, ok := entry.(map[string]any); ok {
					if value := intField(providerEntry, "contextWindow", "contextWindowTokens", "context_window"); value > 0 {
						return value
					}
				}
			}
		}
	}
	return 0
}

// contextWindowInfoFrom 从外层和嵌套键的任一拼写解码可选的长上下文元数据。
// 缺失或类型错误的对象解码为零值；分层配置文件在本次调用前
// 仍会递归合并嵌套对象，因此部分覆盖会被保留。
// 在此之前递归合并，因此部分覆盖会被保留。
func contextWindowInfoFrom(tree map[string]any) ContextWindowInfo {
	obj := objectField(tree, "contextWindowInfo", "context_window_info")
	if obj == nil {
		return ContextWindowInfo{}
	}
	scaling := objectField(obj, "ropeScaling", "rope_scaling")
	if scaling == nil {
		return ContextWindowInfo{}
	}
	return ContextWindowInfo{RopeScaling: RopeScaling{
		Factor:      float64Field(scaling, "factor"),
		Type:        stringField(scaling, "type"),
		OriginalMax: int64Field(scaling, "originalMax", "original_max"),
	}}
}

// CondensePercent 返回为 profile 配置的自动压缩百分比。
// 显式的每 profile 值胜过合并后的全局值；0 和 -1
// 会被保留，以便调用方继承全局 token 阈值。
func (c Config) CondensePercent(profile string) int {
	if strings.TrimSpace(profile) != "" {
		if entry, ok := lookupFold(c.Profiles, profile); ok {
			for _, key := range []string{"autoCondenseContextPercent", "auto_condense_context_percent"} {
				if _, exists := entry[key]; exists {
					return intField(entry, key)
				}
			}
		}
	}
	return c.AutoCondenseContextPercent
}

func profilesFrom(tree map[string]any) map[string]map[string]any {
	raw, ok := tree["profiles"].(map[string]any)
	if !ok {
		return map[string]map[string]any{}
	}
	out := make(map[string]map[string]any, len(raw))
	for name, value := range raw {
		if entry, ok := value.(map[string]any); ok {
			out[name] = entry
		}
	}
	return out
}

// lookupFold 按名称查找 map 条目，忽略大小写。
func lookupFold[V any](values map[string]V, name string) (V, bool) {
	if value, ok := values[name]; ok {
		return value, true
	}
	for key, value := range values {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	var zero V
	return zero, false
}
