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
}

// runLogin implements `login <provider>`: it stores an API key in the config
// file and never echoes the key back.
func runLogin(app App, _ context.Context, args []string) int {
	parsed, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		loginUsage(app.IO.Err)
		return 2
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
