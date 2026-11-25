package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"example.com/argentgo/internal/provider"
)

// runModels 实现 `models [--json] [--refresh]`：默认列出静态模板（不联网），
// --refresh 时从 provider 的 /models 端点抓取动态目录。
func runModels(app App, ctx context.Context, args []string) int {
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
	refreshBaseURL := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			modelsUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		case arg == "--refresh":
			// --refresh [baseURL]：从 provider 的 /models 端点抓取动态目录。
			// 未提供 baseURL 时取当前 provider 模板的 BaseURL（需 --provider 或 env）。
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				refreshBaseURL = args[i]
			} else {
				refreshBaseURL = "auto"
			}
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

	// --refresh：抓取动态模型目录并与静态模板合并（ETag 条件请求由 provider 层处理）。
	if refreshBaseURL != "" {
		base := refreshBaseURL
		if base == "auto" {
			base = strings.TrimSpace(app.IO.Env("ARGENT_BASE_URL"))
			if base == "" {
				base = "https://api.openai.com/v1"
			}
		}
		authValue := strings.TrimSpace(app.IO.Env("ARGENT_API_KEY"))
		opts := provider.FetchOptions{BaseURL: base}
		if authValue != "" {
			opts.AuthHeader = "Authorization"
			opts.AuthValue = "Bearer " + authValue
		}
		result, err := provider.FetchCatalog(ctx, opts)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		if result.NotModified {
			fmt.Fprintln(app.IO.Err, "argentgo: model catalog not modified")
		} else {
			fmt.Fprintf(app.IO.Err, "argentgo: discovered %d models from %s\n", len(result.Models), base)
			if asJSON {
				data, _ := json.MarshalIndent(result.Models, "", "  ")
				fmt.Fprintln(app.IO.Out, string(data))
			} else {
				for _, m := range result.Models {
					fmt.Fprintf(app.IO.Out, "%-24s %s\n", m.ID, m.Name)
				}
			}
			return 0
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
	fmt.Fprint(w, `usage: argentgo models [--json] [--profile <provider> [model]] [--refresh [baseURL]]

Lists the built-in provider templates: the id to pass to --provider, the wire
protocol, the API root and the default model. Credentials are read from each
provider's API key environment variable.
--profile prints a provider+model capability snapshot (thinking/image/tools/contextWindow).
--refresh fetches the live /models catalog from baseURL (or ARGENT_BASE_URL, or
  the OpenAI default) and prints discovered models. Requires ARGENT_API_KEY when
  the endpoint needs auth. Offline by default.
`)
}
