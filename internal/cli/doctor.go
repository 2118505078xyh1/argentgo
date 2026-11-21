package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/mcp"
	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// `doctor` 打印的状态值。
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

type doctorCheck struct {
	name   string
	status string
	detail string
}

// doctorIssue 是 doctor 报告的安全告警。它与 doctorCheck 分开存放：
// checks 是 doctor 一直打印的健康检查，issues 只在发现风险时才出现，
// 因此加入新的告警不会改变既有 doctor 输出（文本与 JSON 的 golden 测试
// 都依赖这一点）。level 当前恒为 statusWarn，不参与失败计数与退出码。
type doctorIssue struct {
	Name    string `json:"name"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// doctorBaseURLIssueMessage 是不安全 baseURL 的统一提示文案。
const doctorBaseURLIssueMessage = "配置中的 baseURL 来自未信任项目或协议不安全"

// doctorOptions 是 doctorFlags 解析出的选项集合。baseURL 供 provider 检查
// 使用；asJSON 决定输出走机器可读分支还是原有的文本分支。
type doctorOptions struct {
	baseURL string
	asJSON  bool
}

// runDoctor 报告配置、MCP 配置、存储、提供方、git 和 Go 的健康状况，
// 随后是一个提供信息的结构化环境探测小节。
func runDoctor(app App, ctx context.Context, args []string) int {
	opts, help, err := doctorFlags(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		doctorUsage(app.IO.Err)
		return 2
	}
	if help {
		doctorUsage(app.IO.Out)
		return 0
	}
	if opts.baseURL == "" {
		opts.baseURL = firstNonEmpty(app.env("ARGENT_BASE_URL"), app.env("OPENAI_BASE_URL"), "https://api.openai.com/v1")
	}

	checks := []doctorCheck{
		checkConfigFile(app),
		checkMCPConfig(app),
		checkStorage(app),
		checkProvider(ctx, opts.baseURL),
		checkProviderCatalog(),
		checkWebSearch(app),
		checkVerify(app),
		checkGit(),
		{name: "go", status: statusOK, detail: runtime.Version()},
	}
	// baseURL 告警独立于常规检查：它只读取配置文件（项目级配置可能来自
	// 未信任来源），不访问网络，也不改变既有检查集合与输出前缀。
	issues := doctorBaseURLIssues(app)
	// --json 是纯新增分支：检查的计算与文本分支完全共享，下面原有的
	// 打印循环（含环境小节）逐字保持不变，从而保证无 flag 时输出不变。
	if opts.asJSON {
		return runDoctorJSON(app, ctx, opts, checks...)
	}
	failures := 0
	for _, check := range checks {
		// doctor 的输出会被逐字粘贴到 issue 中，因此绝不要打印原始的
		// detail：它可能带有内联凭证的 base URL，或是引用凭证的错误
		// 字符串。干净的 detail 会逐字节保持不变。
		fmt.Fprintf(app.IO.Out, "%-4s %-9s %s\n", check.status, check.name, redactDoctorDetail(check.detail))
		if check.status == statusFail {
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(app.IO.Out, "\n%d check(s) failed\n", failures)
	}
	// 环境小节仅提供信息：它从不改变退出码或失败计数，而且最后写入，
	// 因此所有已有的输出都保持为逐字节一致的前缀。git 检测复用
	// exec.LookPath，与 checkGit 保持一致。
	environment := probeEnvironment(app.env, exec.LookPath)
	writeDoctorEnvironment(app.IO.Out, environment, doctorAdvice(environment))
	writeDoctorIssues(app.IO.Out, issues)
	if failures > 0 {
		return 1
	}
	return 0
}

func doctorFlags(args []string) (opts doctorOptions, help bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return opts, true, nil
		case arg == "--json":
			opts.asJSON = true
		case arg == "--base-url":
			if i+1 >= len(args) {
				return opts, false, errors.New("--base-url needs a value")
			}
			i++
			opts.baseURL = args[i]
		case strings.HasPrefix(arg, "--base-url="):
			opts.baseURL = strings.TrimPrefix(arg, "--base-url=")
		case strings.HasPrefix(arg, "-"):
			return opts, false, fmt.Errorf("unknown flag %q", arg)
		default:
			return opts, false, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return opts, false, nil
}

func checkConfigFile(app App) doctorCheck {
	path := app.configPath()
	cfg, err := loadConfigFile(path)
	if err != nil {
		return doctorCheck{name: "config", status: statusFail, detail: err.Error()}
	}
	if len(cfg) == 0 {
		return doctorCheck{name: "config", status: statusOK, detail: path + " (defaults)"}
	}
	return doctorCheck{name: "config", status: statusOK, detail: path}
}

// doctorBaseURLIssues 报告配置文件中不安全或可疑的 baseURL。
//
// 威胁模型：项目级配置（例如提交进仓库的 argent.json / .argent/config.json）
// 可能来自未信任来源，其中写入明文 http 端点会让 Authorization 头里的密钥
// 明文出网，指向云元数据服务则可直接窃取云凭证。命令行 --base-url 与环境变量
// 由操作者显式提供，视为可信，因此这里只检查配置文件；这样既不误报用户主动
// 选择的本地/测试端点，也保持 doctor 既有输出前缀不变。
//
// 判定规则与 internal/config 的 validateBaseURLScheme 保持一致：只接受 https，
// 或指向回环地址的 http（本地测试）；此外单独标记已知的元数据服务主机。
func doctorBaseURLIssues(app App) []doctorIssue {
	raw, err := loadConfigFile(app.configPath())
	if err != nil {
		return nil
	}
	var out []doctorIssue
	add := func(path, value string) {
		reason := insecureBaseURLReason(value)
		if reason == "" {
			return
		}
		out = append(out, doctorIssue{
			Name:    "baseURL",
			Level:   statusWarn,
			Message: doctorBaseURLIssueMessage + ": " + path + " " + reason,
		})
	}

	// 顶层 baseURL 及其别名。config 的解码器取第一个类型正确的别名，
	// 这里照搬该规则，避免同一配置项重复报告。
	if value, ok := firstStringValue(raw, "baseURL", "baseUrl", "base_url"); ok {
		add("$.baseURL", value)
	}

	// 每个 provider 的 baseURL。按名字排序，保证输出稳定。
	providers, ok := raw["providers"].(map[string]any)
	if !ok {
		return out
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, ok := providers[name].(map[string]any)
		if !ok {
			continue
		}
		if value, ok := firstStringValue(entry, "baseURL", "baseUrl", "base_url"); ok {
			add("$.providers."+name+".baseURL", value)
		}
	}
	return out
}

// firstStringValue 按别名顺序返回第一个非空字符串值，模拟 config 解码器
// "取第一个类型正确的值" 的规则。
func firstStringValue(raw map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}

// insecureBaseURLReason 返回 baseURL 不安全的中文原因；可接受时返回空串。
func insecureBaseURLReason(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Sprintf("%q 不是合法 URL: %v", value, err)
	}
	host := parsed.Hostname()
	if suspiciousDoctorHost(host) {
		return fmt.Sprintf("指向可疑主机 %q（云元数据等敏感地址）", host)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return ""
	case "http":
		if doctorLoopbackHost(host) {
			return ""
		}
		return fmt.Sprintf("使用非回环明文 http（%s），Authorization 中的密钥可能明文出网", host)
	default:
		return fmt.Sprintf("使用不受支持的协议 %q（仅允许 https 或回环 http）", parsed.Scheme)
	}
}

// doctorLoopbackHost 报告 host 是否指向本机（localhost、IPv4/IPv6 回环地址）。
// 与 internal/config 的同名规则保持一致。
func doctorLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// suspiciousDoctorHost 标记在 LLM provider baseURL 语境下几乎不可能合法、
// 却常被用来窃取云凭证的主机（云元数据服务与未指定地址）。
func suspiciousDoctorHost(host string) bool {
	h := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	switch h {
	case "169.254.169.254", "metadata.google.internal", "metadata.goog", "100.100.100.200", "fd00:ec2::254":
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsUnspecified()
}

// writeDoctorIssues 仅在存在告警时追加一段输出，保持无告警时 doctor 输出
// 逐字节不变。告警永远是 warn 级别，不改变退出码。
func writeDoctorIssues(w io.Writer, issues []doctorIssue) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintln(w, "\nwarnings:")
	for _, issue := range issues {
		fmt.Fprintf(w, "  %-4s %-9s %s\n", issue.Level, issue.Name, redactDoctorDetail(issue.Message))
	}
}

// mcpConfigReport 汇总已配置 mcpServers 小节的完整性。servers 统计运行时
// 解码器接受的条目数；problems 包含 doctor 应报告的所有问题，包括解码器
// 跳过的问题。
type mcpConfigReport struct {
	servers  int
	problems []mcp.Problem
	shapeOK  bool
}

// inspectMCPConfig 仅根据解析后的配置数据校验 mcpServers 小节；不会联系
// 任何服务器。它复用 mcp.DecodeConfig，使 doctor 和 agent 对有效条目的
// 定义达成一致，然后添加两项解码器有意留给 Connect 的可达性检查：
// 缺失 command/url 目标，以及不支持的 transport 值。已禁用的服务器除外。
func inspectMCPConfig(raw map[string]any) mcpConfigReport {
	report := mcpConfigReport{shapeOK: true}
	if raw == nil {
		return report
	}
	value, exists := raw["mcpServers"]
	if !exists || value == nil {
		return report
	}
	switch value.(type) {
	case map[string]any, []any:
	default:
		report.shapeOK = false
		report.problems = append(report.problems, mcp.Problem{
			Reason: fmt.Sprintf("mcpServers must be an object or array, got %T", value),
		})
		return report
	}

	specs, problems := mcp.DecodeConfig(raw)
	report.problems = append(report.problems, problems...)
	report.servers = len(specs)
	for _, spec := range specs {
		if spec.Enabled != nil && !*spec.Enabled {
			continue
		}
		if spec.Transport != "" && spec.Transport != "stdio" && spec.Transport != "http" {
			report.problems = append(report.problems, mcp.Problem{
				Server: spec.Name,
				Field:  "transport",
				Reason: fmt.Sprintf("unsupported transport %q (want stdio or http)", spec.Transport),
			})
		}
		if spec.Command == "" && spec.URL == "" {
			report.problems = append(report.problems, mcp.Problem{
				Server: spec.Name,
				Reason: "missing command and url; server cannot connect",
			})
		}
	}
	// DecodeConfig 会对自己的问题排序；我们追加的问题不应使
	// 报告顺序依赖于 spec 的遍历顺序。
	sort.Slice(report.problems, func(i, j int) bool {
		return report.problems[i].Error() < report.problems[j].Error()
	})
	return report
}

// checkMCPConfig 报告配置文件中 mcpServers 小节的完整性。缺失的目标、
// 错误的字段类型和不支持的 transport 都属于警告（agent 仍会运行）；
// mcpServers 值在结构上错误则是失败，因为这意味着整个小节都被忽略了。
func checkMCPConfig(app App) doctorCheck {
	cfg, err := loadConfigFile(app.configPath())
	if err != nil {
		return doctorCheck{name: "mcp", status: statusWarn, detail: "config unreadable: " + err.Error()}
	}
	report := inspectMCPConfig(cfg)
	if !report.shapeOK {
		return doctorCheck{name: "mcp", status: statusFail, detail: formatMCPProblems(report)}
	}
	if len(report.problems) == 0 {
		if report.servers == 0 {
			return doctorCheck{name: "mcp", status: statusOK, detail: "no servers configured"}
		}
		return doctorCheck{name: "mcp", status: statusOK, detail: fmt.Sprintf("%d server(s) valid", report.servers)}
	}
	return doctorCheck{name: "mcp", status: statusWarn, detail: formatMCPProblems(report)}
}

// checkWebSearch 只提供可发现性提示，不发起任何请求：web_search 工具需要一个
// 用户自备的兼容端点（ARGENT_WEB_SEARCH_URL）或搜索服务 key（BRAVE_API_KEY /
// TAVILY_API_KEY）。未配置时给出 warn 引导，绝不建议用 HTML 抓取冒充内置搜索。
// detail 只含固定文案，不打印任何 env 值，避免凭证泄漏进 issue。
func checkWebSearch(app App) doctorCheck {
	if strings.TrimSpace(app.env("ARGENT_WEB_SEARCH_URL")) != "" ||
		strings.TrimSpace(app.env("BRAVE_API_KEY")) != "" ||
		strings.TrimSpace(app.env("TAVILY_API_KEY")) != "" {
		return doctorCheck{name: "web_search", status: statusOK, detail: "search endpoint configured"}
	}
	return doctorCheck{name: "web_search", status: statusWarn,
		detail: "not configured (optional): set ARGENT_WEB_SEARCH_URL or BRAVE_API_KEY/TAVILY_API_KEY to enable web_search"}
}

// verifyConfigKeys 是 config 解码 verifyCommand/verifyCommands 时接受的全部键名，
// 与 internal/config 的 decode 保持一致。cli 不 import config 的解码细节，
// 因此这里只列字符串键名；新增别名时两处需同步。
var verifyConfigKeys = []string{"verifyCommand", "verify_command", "verifyCommands", "verify_commands"}

// checkVerify 只提供可发现性提示，不运行任何验证命令：编辑后验证需要用户
// 在配置中设置 verifyCommand/verifyCommands（或别名），或通过 opt-in 开启
// auto_verify / ARGENT_EDIT_DIAGNOSTICS。它只检查键是否存在且非空，绝不读取
// 或打印配置值与 env 值，避免把命令内容或凭证泄漏进 doctor 输出（输出会被
// 逐字粘贴到 issue）。状态只可能是 ok/warn，永不返回 fail，因此不会改变退出码。
func checkVerify(app App) doctorCheck {
	cfg, err := loadConfigFile(app.configPath())
	if (err == nil && verifyConfigured(cfg)) || verifyOptedIn(app) {
		return doctorCheck{name: "verify", status: statusOK,
			detail: "verification configured or opted in"}
	}
	return doctorCheck{name: "verify", status: statusWarn,
		detail: "not configured (optional): set verifyCommand/verifyCommands in config, or enable auto_verify via ARGENT_OPT_INS or set ARGENT_EDIT_DIAGNOSTICS for post-edit verification"}
}

// verifyConfigured 只检查原始配置 map 中验证相关键是否存在且非空，不读取具体值。
// 接受列表、映射和非空字符串三种形态，覆盖 config 解码器与手工配置的写法。
func verifyConfigured(raw map[string]any) bool {
	for _, key := range verifyConfigKeys {
		value, exists := raw[key]
		if !exists || value == nil {
			continue
		}
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return true
			}
		case []any:
			if len(v) > 0 {
				return true
			}
		case []string:
			if len(v) > 0 {
				return true
			}
		case map[string]any:
			if len(v) > 0 {
				return true
			}
		case map[string]string:
			if len(v) > 0 {
				return true
			}
		}
	}
	return false
}

// verifyOptedIn 报告是否通过 opt-in 开关或编辑诊断 env 开启了验证提示。
// auto_verify 键由 app 侧注册表提供，经 OptInFromEnv 解析；cli 不 import app。
// 它与 ARGENT_EDIT_DIAGNOSTICS=1 做或运算，任一开启即视为已启用。
func verifyOptedIn(app App) bool {
	return OptInFromEnv(app.env)["auto_verify"] || app.env("ARGENT_EDIT_DIAGNOSTICS") == "1"
}

func formatMCPProblems(report mcpConfigReport) string {
	parts := make([]string, 0, len(report.problems))
	for _, problem := range report.problems {
		parts = append(parts, problem.Error())
	}
	return fmt.Sprintf("%d problem(s): %s", len(parts), strings.Join(parts, "; "))
}

func checkStorage(app App) doctorCheck {
	dir := app.storageDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return doctorCheck{name: "storage", status: statusFail, detail: err.Error()}
	}
	file, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return doctorCheck{name: "storage", status: statusFail, detail: err.Error()}
	}
	name := file.Name()
	if _, err := file.WriteString("ok"); err != nil {
		file.Close()
		os.Remove(name)
		return doctorCheck{name: "storage", status: statusFail, detail: err.Error()}
	}
	file.Close()
	os.Remove(name)
	return doctorCheck{name: "storage", status: statusOK, detail: dir + " writable"}
}

// checkProviderCatalog 只做本地内置 provider 模板校验：它复用 provider.ValidateAll，
// 把模板或别名的问题渲染成一条 warn 级别的诊断。校验不访问网络、不读取配置，
// 因此既有 doctor 输出的退出码语义（只由 statusFail 决定）保持不变。
func checkProviderCatalog() doctorCheck {
	diagnostics := provider.ValidateAll()
	if len(diagnostics) == 0 {
		return doctorCheck{name: "provider_catalog", status: statusOK,
			detail: fmt.Sprintf("%d template(s) valid", len(provider.List()))}
	}
	parts := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		parts = append(parts, diagnostic.String())
	}
	return doctorCheck{name: "provider_catalog", status: statusWarn,
		detail: fmt.Sprintf("%d problem(s): %s", len(diagnostics), strings.Join(parts, "; "))}
}

func checkProvider(ctx context.Context, baseURL string) doctorCheck {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return doctorCheck{name: "provider", status: statusFail, detail: err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return doctorCheck{name: "provider", status: statusWarn, detail: baseURL + " unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return doctorCheck{name: "provider", status: statusWarn, detail: fmt.Sprintf("%s returned HTTP %d", baseURL, resp.StatusCode)}
	}
	return doctorCheck{name: "provider", status: statusOK, detail: fmt.Sprintf("%s returned HTTP %d", baseURL, resp.StatusCode)}
}

func checkGit() doctorCheck {
	path, err := exec.LookPath("git")
	if err != nil {
		return doctorCheck{name: "git", status: statusWarn, detail: "git not found on PATH"}
	}
	return doctorCheck{name: "git", status: statusOK, detail: path}
}

func doctorUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo doctor [--base-url <url>] [--json]

Checks config loading, local mcpServers config integrity, storage writability,
provider reachability (3s timeout), built-in provider template validation,
git availability and the Go runtime version.
The mcpServers and template checks are fully local: they never contact any server.
With --json, print a stable JSON report (checks, failures, environment) instead;
every detail string is redacted and the exit code still reflects failed checks.
`)
}
