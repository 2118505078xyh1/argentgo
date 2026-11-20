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

// runDoctor 报告配置、MCP 配置、存储、提供方、git 和 Go 的健康状况，
// 随后是一个提供信息的结构化环境探测小节。
func runDoctor(app App, ctx context.Context, args []string) int {
	baseURL, help, err := doctorFlags(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		doctorUsage(app.IO.Err)
		return 2
	}
	if help {
		doctorUsage(app.IO.Out)
		return 0
	}
	if baseURL == "" {
		baseURL = firstNonEmpty(app.env("ARGENT_BASE_URL"), app.env("OPENAI_BASE_URL"), "https://api.openai.com/v1")
	}

	checks := []doctorCheck{
		checkConfigFile(app),
		checkMCPConfig(app),
		checkStorage(app),
		checkProvider(ctx, baseURL),
		checkGit(),
		{name: "go", status: statusOK, detail: runtime.Version()},
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

func doctorFlags(args []string) (baseURL string, help bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return "", true, nil
		case arg == "--base-url":
			if i+1 >= len(args) {
				return "", false, errors.New("--base-url needs a value")
			}
			i++
			baseURL = args[i]
		case strings.HasPrefix(arg, "--base-url="):
			baseURL = strings.TrimPrefix(arg, "--base-url=")
		case strings.HasPrefix(arg, "-"):
			return "", false, fmt.Errorf("unknown flag %q", arg)
		default:
			return "", false, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return baseURL, false, nil
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
	fmt.Fprint(w, `usage: argent doctor [--base-url <url>]

Checks config loading, local mcpServers config integrity, storage writability,
provider reachability (3s timeout), git availability and the Go runtime version.
The mcpServers check is fully local: it never contacts any server.
`)
}
