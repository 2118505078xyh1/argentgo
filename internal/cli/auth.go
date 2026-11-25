package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"example.com/argentgo/internal/auth"
)

// authStore 打开按安装隔离的凭证存储：<storage>/auth。
func authStore(app App) (*auth.Store, error) {
	return auth.Open(filepath.Join(app.storageDir(), "auth"))
}

// runAuth 实现 `auth [--json]`：每个提供方的 token 状态，以及哪些
// 配置文件中的提供方持有 API key。密钥永远不会被打印。
func runAuth(app App, _ context.Context, args []string) int {
	asJSON := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			authUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected argument %q\n", arg)
			authUsage(app.IO.Err)
			return 2
		}
	}

	store, err := authStore(app)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	tokens, err := store.List()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	configProviders, err := configProviderKeys(app.configPath())
	if err != nil {
		fmt.Fprintln(app.IO.Err, "warning:", err)
	}

	now := time.Now()
	type row struct {
		Provider string `json:"provider"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		Account  string `json:"account,omitempty"`
		Source   string `json:"source"`
	}
	rows := make([]row, 0, len(tokens)+len(configProviders))
	for _, token := range tokens {
		rows = append(rows, row{
			Provider: token.Provider,
			Kind:     token.Kind,
			Status:   store.Status(token.Provider, now).String(),
			Account:  token.Account,
			Source:   "auth store",
		})
	}
	for provider := range configProviders {
		rows = append(rows, row{Provider: provider, Kind: auth.KindAPIKey, Status: "configured", Source: "config"})
	}
	if asJSON {
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(app.IO.Out, "no stored credentials")
		return 0
	}
	for _, row := range rows {
		fmt.Fprintf(app.IO.Out, "%-12s %-8s %-11s %-10s %s\n", row.Provider, row.Kind, row.Status, row.Source, row.Account)
	}
	return 0
}

// configProviderKeys 列出配置文件中 apiKey 非空的提供方。
// 它镜像 config.Provider 所读取的 apiKey 别名（app.Resolve 现在使用的来源），
// 因此 "configured" 意味着该 key 确实可用。
// 它只返回名称，绝不返回密钥本身。
func configProviderKeys(path string) (map[string]bool, error) {
	cfg, err := loadConfigFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	providers, _ := cfg["providers"].(map[string]any)
	for name, raw := range providers {
		entry, _ := raw.(map[string]any)
		for _, key := range []string{"apiKey", "api_key", "api-key"} {
			if text, _ := entry[key].(string); strings.TrimSpace(text) != "" {
				out[name] = true
				break
			}
		}
	}
	return out, nil
}

func authUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo auth [--json]

Shows credential status: OAuth/device tokens from <storage>/auth and which
providers hold an API key in the config file. Secrets are never printed.
`)
}
