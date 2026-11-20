package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// runPolicy 实现 `policy [--json] [-C root]`：展示某个工作区有效的沙箱、
// 审批模式、提供方以及策略文档。
func runPolicy(app App, _ context.Context, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	asJSON := false
	for _, arg := range rest {
		switch {
		case arg == "-h" || arg == "--help":
			policyUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
			policyUsage(app.IO.Err)
			return 2
		}
	}
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	cfg, err := config.Load(root, app.env)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sandbox, err := security.FromString(cfg.SandboxMode)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	approval := "ask"
	if cfg.Yolo {
		approval = "auto"
	}

	report := map[string]any{
		"workspace":    root,
		"sandbox":      sandbox.String(),
		"mutating":     sandbox != security.SandboxReadOnly,
		"approval":     approval,
		"provider":     cfg.ProviderName,
		"protocol":     cfg.Protocol,
		"model":        cfg.Model,
		"maxTurns":     cfg.MaxTurns,
		"configSource": cfg.Sources,
	}
	if raw, ok := cfg.Raw["policy"]; ok {
		report["policy"] = raw
	} else {
		report["policy"] = "default: path traversal denied, unknown tools ask"
	}

	if asJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}

	fmt.Fprintf(app.IO.Out, "workspace: %s\n", root)
	fmt.Fprintf(app.IO.Out, "sandbox:   %s (mutating tools %s)\n", sandbox, enabledWord(sandbox != security.SandboxReadOnly))
	fmt.Fprintf(app.IO.Out, "approval:  %s\n", approval)
	fmt.Fprintf(app.IO.Out, "provider:  %s  protocol: %s  model: %s\n",
		firstNonEmpty(cfg.ProviderName, "(default)"), cfg.Protocol, cfg.Model)
	fmt.Fprintf(app.IO.Out, "max turns: %d\n", cfg.MaxTurns)
	if raw, ok := cfg.Raw["policy"]; ok {
		data, _ := json.Marshal(raw)
		fmt.Fprintf(app.IO.Out, "policy:    %s\n", string(data))
	} else {
		fmt.Fprintln(app.IO.Out, "policy:    default (path traversal denied; unknown tools ask)")
	}
	if len(cfg.Sources) > 0 {
		fmt.Fprintf(app.IO.Out, "config:    %s\n", strings.Join(cfg.Sources, ", "))
	}
	return 0
}

func enabledWord(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

func policyUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent policy [--json] [-C <dir>]

Prints the effective sandbox mode, approval mode, provider selection and the
policy document from the merged config. No tool is run.
`)
}
