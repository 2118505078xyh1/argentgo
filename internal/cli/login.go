package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type loginArgs struct {
	provider string
	apiKey   string
	baseURL  string
	help     bool
	device   bool
}

// runLogin 实现 `login <provider>`：把 API key 存入配置文件，且绝不回显该 key。
// 传入 --device 时改走 RFC 8628 设备码流程；不带该标志时原有行为逐字节不变。
func runLogin(app App, ctx context.Context, args []string) int {
	parsed, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		loginUsage(app.IO.Err)
		return 2
	}
	// 设备流程优先：它的帮助与参数校验走独立入口，不能影响原有 API key 路径。
	if parsed.device {
		if parsed.help {
			deviceLoginUsage(app.IO.Out)
			return 0
		}
		if parsed.provider == "" {
			deviceLoginUsage(app.IO.Err)
			return 2
		}
		return runDeviceLogin(app, ctx, parsed.provider)
	}
	if parsed.help {
		loginUsage(app.IO.Out)
		return 0
	}
	if parsed.provider == "" {
		loginUsage(app.IO.Err)
		return 2
	}

	key := firstNonEmpty(
		parsed.apiKey,
		app.env(providerEnvKey(parsed.provider)),
		app.env("ARGENT_API_KEY"),
	)
	if key == "" {
		fmt.Fprintf(app.IO.Err, "api key for %s: ", parsed.provider)
		line, err := bufio.NewReader(app.IO.In).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(app.IO.Err, "error: read api key:", err)
			return 1
		}
		key = strings.TrimSpace(line)
	}
	if key == "" {
		fmt.Fprintln(app.IO.Err, "error: no api key provided")
		return 1
	}

	path := app.configPath()
	cfg, err := loadConfigFile(path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		providers = map[string]any{}
	}
	entry, ok := providers[parsed.provider].(map[string]any)
	if !ok {
		entry = map[string]any{}
	}
	entry["apiKey"] = key
	if parsed.baseURL != "" {
		entry["baseUrl"] = parsed.baseURL
	}
	providers[parsed.provider] = entry
	cfg["providers"] = providers
	if err := saveConfigFile(path, cfg); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}

	fmt.Fprintf(app.IO.Out, "saved credentials for %s in %s\n", parsed.provider, path)
	return 0
}

func parseLoginArgs(args []string) (loginArgs, error) {
	var parsed loginArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			parsed.help = true
		case arg == "--api-key" || arg == "-k":
			if i+1 >= len(args) {
				return parsed, errors.New("--api-key needs a value")
			}
			i++
			parsed.apiKey = args[i]
		case strings.HasPrefix(arg, "--api-key="):
			parsed.apiKey = strings.TrimPrefix(arg, "--api-key=")
		case arg == "--base-url":
			if i+1 >= len(args) {
				return parsed, errors.New("--base-url needs a value")
			}
			i++
			parsed.baseURL = args[i]
		case strings.HasPrefix(arg, "--base-url="):
			parsed.baseURL = strings.TrimPrefix(arg, "--base-url=")
		case arg == "--device":
			parsed.device = true
		case strings.HasPrefix(arg, "-"):
			return parsed, fmt.Errorf("unknown flag %q", arg)
		default:
			if parsed.provider != "" {
				return parsed, fmt.Errorf("unexpected argument %q", arg)
			}
			parsed.provider = arg
		}
	}
	return parsed, nil
}

func providerEnvKey(provider string) string {
	upper := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(provider))
	return upper + "_API_KEY"
}

func loginUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent login <provider> [--api-key <key>] [--base-url <url>]

The key is read from --api-key, <PROVIDER>_API_KEY, ARGENT_API_KEY or stdin and
written to the config file. It is never echoed.
`)
}
