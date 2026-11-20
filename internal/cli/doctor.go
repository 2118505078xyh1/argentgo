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

// Status values printed by `doctor`.
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

// runDoctor reports config, MCP config, storage, provider, git and Go health.
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
		fmt.Fprintf(app.IO.Out, "%-4s %-9s %s\n", check.status, check.name, check.detail)
		if check.status == statusFail {
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(app.IO.Out, "\n%d check(s) failed\n", failures)
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

// mcpConfigReport summarises the integrity of the configured mcpServers
// section. servers counts entries the runtime decoder accepts; problems holds
// everything doctor should surface, including issues the decoder skips.
type mcpConfigReport struct {
	servers  int
	problems []mcp.Problem
	shapeOK  bool
}

// inspectMCPConfig validates the mcpServers section purely from parsed config
// data; no server is contacted. It reuses mcp.DecodeConfig so doctor and the
// agent agree on what a valid entry means, then adds two reachability checks
// the decoder deliberately leaves to Connect: a missing command/url target and
// an unsupported transport value. Disabled servers are exempt.
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
	// DecodeConfig sorts its own problems; our appended ones must not make the
	// report order depend on spec iteration.
	sort.Slice(report.problems, func(i, j int) bool {
		return report.problems[i].Error() < report.problems[j].Error()
	})
	return report
}

// checkMCPConfig reports the integrity of the mcpServers section of the config
// file. Missing targets, bad field types and unsupported transports are
// warnings (the agent still runs); a structurally wrong mcpServers value is a
// failure because it means the whole section was ignored.
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
