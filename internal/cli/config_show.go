package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"example.com/argentgo/internal/config"
)

// configRedacted 在任何非空密钥的位置打印。它故意不是真实值，
// 因此 `config show` 的输出可以安全地粘贴到 issue 中。
const configRedacted = "<redacted>"

// configShowField 是一个有效值及其来源。来源标签是
// "default"、"file"、"env:ARGENT_*" 或 "flag:<key>" 之一。
type configShowField struct {
	label  string
	value  any
	source string
}

// runConfigShow 实现 `config show [--json] [-C dir] [-c key=value]`。
// 它解析与引擎完全相同的分层配置，然后在替换密钥并为每个字段附上来源标签后
// 打印出来。不涉及网络或模型，因此该命令可安全地在 CI 和 bug 报告中使用。
func runConfigShow(app App, _ context.Context, args []string) int {
	cwd, overrides, asJSON, help, err := configShowFlags(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		configShowUsage(app.IO.Err)
		return 2
	}
	if help {
		configShowUsage(app.IO.Out)
		return 0
	}
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}

	var overrideTree map[string]any
	if overrides != nil {
		overrideTree = overrides.Nested()
	}
	cfg, err := config.LoadWithOverrides(root, app.env, overrideTree)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}

	fields := configShowFields(cfg, app.env, overrides)
	paths := newConfigPathsReport(app, cwd)
	if warning := paths.warning(); warning != "" {
		fmt.Fprintln(app.IO.Err, warning)
	}
	if asJSON {
		return writeConfigShowJSON(app.IO.Out, cfg, fields, app.env, paths)
	}
	writeConfigShowText(app.IO.Out, cfg, fields, app.env, paths)
	return 0
}

func configShowFlags(args []string) (cwd string, overrides *Overrides, asJSON, help bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			help = true
		case arg == "--json":
			asJSON = true
		case arg == "-C" || arg == "--cwd":
			if i+1 >= len(args) {
				return "", nil, false, false, errors.New(arg + " needs a value")
			}
			i++
			cwd = args[i]
		case strings.HasPrefix(arg, "--cwd="):
			cwd = strings.TrimPrefix(arg, "--cwd=")
		case strings.HasPrefix(arg, "-C="):
			cwd = strings.TrimPrefix(arg, "-C=")
		case arg == "-c" || arg == "--config":
			if i+1 >= len(args) {
				return "", nil, false, false, errors.New(arg + " needs a value")
			}
			i++
			if err := addOverride(&overrides, args[i]); err != nil {
				return "", nil, false, false, err
			}
		case strings.HasPrefix(arg, "--config="):
			if err := addOverride(&overrides, strings.TrimPrefix(arg, "--config=")); err != nil {
				return "", nil, false, false, err
			}
		case strings.HasPrefix(arg, "-c="):
			if err := addOverride(&overrides, strings.TrimPrefix(arg, "-c=")); err != nil {
				return "", nil, false, false, err
			}
		default:
			return "", nil, false, false, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return cwd, overrides, asJSON, help, nil
}

func addOverride(overrides **Overrides, spec string) error {
	if *overrides == nil {
		*overrides = &Overrides{}
	}
	return (*overrides).Set(spec)
}

// configShowFieldSpec 把一个显示的字段与它可以设置的配置键
// （规范键在前，别名在后）以及覆盖它的环境变量配对。
// 别名列表可防止 snake_case 文件被误标为 "default"。
type configShowFieldSpec struct {
	label   string
	aliases []string
	envVar  string
}

var configShowFieldSpecs = []configShowFieldSpec{
	{"model", []string{"model", "modelName"}, "ARGENT_MODEL"},
	{"baseURL", []string{"baseURL", "baseUrl", "base_url"}, "ARGENT_BASE_URL"},
	{"apiKey", []string{"apiKey", "api_key"}, "ARGENT_API_KEY"},
	{"protocol", []string{"protocol"}, "ARGENT_PROTOCOL"},
	{"locale", []string{"locale"}, "ARGENT_LOCALE"},
	{"sandboxMode", []string{"sandboxMode", "sandbox_mode"}, "ARGENT_SANDBOX_MODE"},
	{"privacyMode", []string{"privacyMode", "privacy_mode"}, "ARGENT_PRIVACY_MODE"},
	{"maxTurns", []string{"maxTurns", "max_turns"}, "ARGENT_MAX_TURNS"},
	{"yolo", []string{"yolo"}, "ARGENT_YOLO"},
	{"provider", []string{"provider"}, "ARGENT_PROVIDER"},
	{"profile", []string{"profile", "activeProfile"}, "ARGENT_PROFILE"},
	{"storageDir", []string{"storageDir", "storage_dir"}, "ARGENT_STORAGE_DIR"},
}

func configShowFields(cfg config.Config, env func(string) string, overrides *Overrides) []configShowField {
	values := map[string]any{
		"model":       cfg.Model,
		"baseURL":     cfg.BaseURL,
		"apiKey":      redactSecret(cfg.APIKey),
		"protocol":    cfg.Protocol,
		"locale":      cfg.Locale,
		"sandboxMode": cfg.SandboxMode,
		"privacyMode": cfg.PrivacyMode,
		"maxTurns":    cfg.MaxTurns,
		"yolo":        cfg.Yolo,
		"provider":    cfg.ProviderName,
		"profile":     cfg.ActiveProfile,
		"storageDir":  cfg.StorageDir,
	}

	// 每个参与贡献的文件只读一次，这样某个键才能被归因到
	// 实际设置它的最高优先级文件。
	fileTrees := configShowFileTrees(cfg.Sources)

	out := make([]configShowField, 0, len(configShowFieldSpecs))
	for _, spec := range configShowFieldSpecs {
		out = append(out, configShowField{
			label:  spec.label,
			value:  values[spec.label],
			source: configShowSource(spec, fileTrees, env, overrides),
		})
	}
	return out
}

func configShowFileTrees(sources []string) []map[string]any {
	trees := make([]map[string]any, 0, len(sources))
	for _, path := range sources {
		tree, err := loadConfigFile(path)
		if err != nil {
			continue // 值已经解析完成；这里只影响来源标签
		}
		trees = append(trees, tree)
	}
	return trees
}

func configShowSource(spec configShowFieldSpec, fileTrees []map[string]any, env func(string) string, overrides *Overrides) string {
	if overrides != nil {
		for _, key := range spec.aliases {
			if _, ok := overrides.Get(key); ok {
				return "flag:" + key
			}
		}
	}
	// 存储目录有三个等价环境变量，优先级与 app.DefaultStorage / config 一致；
	// 只认 ARGENT_STORAGE_DIR 会把 $ARGENT_STORAGE 误报成 default。
	if spec.label == "storageDir" {
		for _, key := range []string{"ARGENT_STORAGE", "ARGENT_STORAGE_DIR", "ARGENT_HOME"} {
			if strings.TrimSpace(env(key)) != "" {
				return "env:" + key
			}
		}
	}
	if spec.envVar != "" && strings.TrimSpace(env(spec.envVar)) != "" {
		return "env:" + spec.envVar
	}
	for i := len(fileTrees) - 1; i >= 0; i-- {
		for _, key := range spec.aliases {
			if _, ok := fileTrees[i][key]; ok {
				return "file"
			}
		}
	}
	return "default"
}

func redactSecret(value string) string {
	if value == "" {
		return ""
	}
	return configRedacted
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, needle := range []string{"apikey", "api_key", "token", "secret", "password", "credential"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// redactConfigTree 复制一棵配置树，并对每个含密钥的叶子进行掩码。
// 它用于 --json 中的原始树，使粘贴的输出不会泄露位于
// providers.<name> 或任何其他嵌套对象中的 key。
func redactConfigTree(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if secret, ok := child.(string); ok && secret != "" && isSecretKey(key) {
				out[key] = configRedacted
				continue
			}
			out[key] = redactConfigTree(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactConfigTree(child)
		}
		return out
	default:
		return value
	}
}

func configShowEnvOverrides(env func(string) string) []string {
	var out []string
	for _, spec := range configShowFieldSpecs {
		if spec.envVar != "" && strings.TrimSpace(env(spec.envVar)) != "" {
			out = append(out, spec.envVar)
		}
	}
	for _, key := range []string{"ARGENT_STORAGE", "ARGENT_HOME"} {
		if strings.TrimSpace(env(key)) != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

type configShowJSON struct {
	Values          map[string]any    `json:"values"`
	Sources         map[string]string `json:"sources"`
	ConfigFiles     []string          `json:"configFiles"`
	CLIWritePath    string            `json:"cliWritePath"`
	EngineReadPaths []string          `json:"engineReadPaths"`
	PathWarning     string            `json:"pathWarning,omitempty"`
	EnvOverrides    []string          `json:"envOverrides"`
	Raw             map[string]any    `json:"raw"`
}

func writeConfigShowJSON(w io.Writer, cfg config.Config, fields []configShowField, env func(string) string, paths configPathsReport) int {
	values := make(map[string]any, len(fields))
	sources := make(map[string]string, len(fields))
	for _, field := range fields {
		values[field.label] = field.value
		sources[field.label] = field.source
	}
	report := configShowJSON{
		Values:          values,
		Sources:         sources,
		ConfigFiles:     append([]string{}, cfg.Sources...),
		CLIWritePath:    paths.writePath,
		EngineReadPaths: append([]string{}, paths.enginePaths...),
		PathWarning:     paths.warning(),
		EnvOverrides:    configShowEnvOverrides(env),
		Raw:             redactConfigTree(cfg.Raw).(map[string]any),
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return 1
	}
	fmt.Fprintln(w, string(data))
	return 0
}

func writeConfigShowText(w io.Writer, cfg config.Config, fields []configShowField, env func(string) string, paths configPathsReport) {
	fmt.Fprintln(w, "effective configuration (precedence: default < file < env < flag)")
	for _, field := range fields {
		value := formatConfigValue(field.value)
		if field.label == "profile" && value == "" {
			value = "-"
		}
		fmt.Fprintf(w, "  %-13s %-42s [%s]\n", field.label, value, field.source)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "config sources (lowest to highest precedence):")
	if len(cfg.Sources) == 0 {
		fmt.Fprintln(w, "  (none: compiled defaults only)")
	}
	for i, path := range cfg.Sources {
		fmt.Fprintf(w, "  %d. %s\n", i+1, path)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "配置路径对照:")
	fmt.Fprintf(w, "  命令行写入路径: %s\n", paths.writePath)
	fmt.Fprintln(w, "  引擎读取路径（候选，优先级从低到高）:")
	for i, path := range paths.enginePaths {
		state := "不存在"
		for _, src := range cfg.Sources {
			if sameConfigFilePath(src, path) {
				state = "已读取"
				break
			}
		}
		fmt.Fprintf(w, "    %d. %s (%s)\n", i+1, path, state)
	}
	if envVars := configShowEnvOverrides(env); len(envVars) > 0 {
		fmt.Fprintf(w, "environment overrides set: %s\n", strings.Join(envVars, ", "))
	}
	fmt.Fprintln(w, "note: -c/--config overrides beat environment; config show applies them as the flag layer")
}

func configShowUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo config show [--json] [-C <dir>] [-c <key=value>]

Prints the effective configuration after all layers are merged, with secrets
replaced by `+"`"+`<redacted>`+"`"+`. Each value is labelled with its source:
default, file, env:ARGENT_* or flag:<key>. -c/--config may be repeated.
Use --json for a machine-readable report that also includes a redacted raw tree.
`)
}
