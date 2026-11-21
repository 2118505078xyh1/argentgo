package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/mcp"
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
		checkWebSearch(app),
		checkVerify(app),
		checkGit(),
		{name: "go", status: statusOK, detail: runtime.Version()},
	}
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
provider reachability (3s timeout), git availability and the Go runtime version.
The mcpServers check is fully local: it never contacts any server.
With --json, print a stable JSON report (checks, failures, environment) instead;
every detail string is redacted and the exit code still reflects failed checks.
`)
}
