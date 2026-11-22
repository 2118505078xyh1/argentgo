package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// runModels 实现 `models [--json]`：列出提供方模板表，不访问网络。
func runModels(app App, _ context.Context, args []string) int {
	// --profile <provider> [model]：输出能力画像（只读诊断）。
	// 必须在通用 flag 循环之前处理，否则 --profile 会被当作未知 flag 拒绝。
	if len(args) > 0 && args[0] == "--profile" {
		if len(args) < 2 {
			fmt.Fprintln(app.IO.Err, "argentgo: --profile needs a provider name")
			return 2
		}
		model := ""
		if len(args) > 2 {
			model = args[2]
		}
		profile := provider.ProfileFor(args[1], model)
		data, err := json.MarshalIndent(profile, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}

	asJSON := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			modelsUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(app.IO.Err, "argentgo: unknown flag %q\n", arg)
			modelsUsage(app.IO.Err)
			return 2
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected argument %q\n", arg)
			modelsUsage(app.IO.Err)
			return 2
		}
	}

	templates := provider.List()
	if asJSON {
		data, err := json.MarshalIndent(templates, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	fmt.Fprintf(app.IO.Out, "%-12s %-24s %-38s %s\n", "provider", "protocol", "base URL", "default model")
	for _, template := range templates {
		fmt.Fprintf(app.IO.Out, "%-12s %-24s %-38s %s\n",
			template.Name, template.Protocol, template.BaseURL, template.DefaultModel)
	}
	return 0
}

func modelsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo models [--json] [--profile <provider> [model]]

Lists the built-in provider templates: the id to pass to --provider, the wire
protocol, the API root and the default model. Credentials are read from each
provider's API key environment variable.
--profile prints a provider+model capability snapshot (thinking/image/tools/contextWindow).
`)
}
