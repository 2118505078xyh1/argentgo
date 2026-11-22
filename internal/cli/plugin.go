package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/2118505078xyh1/argentgo/internal/plugins"
)

// runPlugin 实现插件包管理：install / uninstall / list / enable / disable。
// 它只调用 plugins 包的既有库函数，不引入新依赖。
func runPlugin(app App, _ context.Context, args []string) int {
	if len(args) == 0 {
		pluginUsage(app.IO.Err)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		pluginUsage(app.IO.Out)
		return 0
	case "install":
		if len(args) < 2 {
			pluginUsage(app.IO.Err)
			return 2
		}
		dir, code := pluginTargetDir(app)
		if code != 0 {
			return code
		}
		bundle, err := plugins.Install(args[1], dir)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "installed plugin %s\n", bundle.Manifest.Name)
		return 0
	case "uninstall":
		if len(args) < 2 {
			pluginUsage(app.IO.Err)
			return 2
		}
		dir, code := pluginTargetDir(app)
		if code != 0 {
			return code
		}
		ok, err := plugins.Uninstall(args[1], dir)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		if !ok {
			fmt.Fprintf(app.IO.Err, "plugin %q not installed\n", args[1])
			return 1
		}
		fmt.Fprintf(app.IO.Out, "uninstalled plugin %s\n", args[1])
		return 0
	case "list":
		asJSON := len(args) > 1 && args[1] == "--json"
		dir, code := pluginTargetDir(app)
		if code != 0 {
			return code
		}
		bundles, problems, err := plugins.Discover(dir)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		if asJSON {
			data, err := json.MarshalIndent(struct {
				Bundles  []plugins.Bundle  `json:"bundles"`
				Problems []plugins.Problem `json:"problems"`
			}{bundles, problems}, "", "  ")
			if err != nil {
				fmt.Fprintln(app.IO.Err, "error:", err)
				return 1
			}
			fmt.Fprintln(app.IO.Out, string(data))
			return 0
		}
		for _, b := range bundles {
			fmt.Fprintf(app.IO.Out, "%s\t%s\n", b.Name, b.Dir)
		}
		for _, p := range problems {
			fmt.Fprintf(app.IO.Err, "warning: %s\n", p.Message)
		}
		return 0
	case "enable", "disable":
		if len(args) < 2 {
			pluginUsage(app.IO.Err)
			return 2
		}
		dir, code := pluginTargetDir(app)
		if code != 0 {
			return code
		}
		enabled := args[0] == "enable"
		if err := plugins.SetEnabled(dir, args[1], enabled); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "plugin %s %s\n", args[1], map[bool]string{true: "enabled", false: "disabled"}[enabled])
		return 0
	default:
		fmt.Fprintf(app.IO.Err, "argentgo: unknown plugin command %q\n", args[0])
		pluginUsage(app.IO.Err)
		return 2
	}
}

// pluginTargetDir 返回用户级插件目录。--project 时改用工作区目录。
func pluginTargetDir(app App) (string, int) {
	// 默认用户目录；项目级通过 ARGENT_PLUGIN_PROJECT=1 切换。
	if app.env("ARGENT_PLUGIN_PROJECT") == "1" {
		root, err := workspaceRoot(app, "")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return "", 1
		}
		return plugins.ProjectPluginsDir(root), 0
	}
	dir, err := plugins.UserPluginsDir()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return "", 1
	}
	return dir, 0
}

func pluginUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo plugin install <path|zip>
       argentgo plugin uninstall <name>
       argentgo plugin list [--json]
       argentgo plugin enable|disable <name>

Manages user-level plugins. Set ARGENT_PLUGIN_PROJECT=1 for project-level plugins.
`)
}
