package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// runLogout implements `logout [provider]`: it removes OAuth tokens and any
// API key stored for that provider in the config file.
func runLogout(app App, _ context.Context, args []string) int {
	provider := ""
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			logoutUsage(app.IO.Out)
			return 0
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(app.IO.Err, "argent: unknown flag %q\n", arg)
			logoutUsage(app.IO.Err)
			return 2
		default:
			if provider != "" {
				fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
				logoutUsage(app.IO.Err)
				return 2
			}
			provider = arg
		}
	}

	store, err := authStore(app)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	var providers []string
	if provider != "" {
		providers = []string{provider}
	} else {
		tokens, err := store.List()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		for _, token := range tokens {
			providers = append(providers, token.Provider)
		}
		configured, err := configProviderKeys(app.configPath())
		if err != nil {
			fmt.Fprintln(app.IO.Err, "warning:", err)
		}
		for name := range configured {
			providers = append(providers, name)
		}
		providers = dedupeStrings(providers)
	}
	if len(providers) == 0 {
		fmt.Fprintln(app.IO.Out, "nothing to log out")
		return 0
	}

	for _, name := range providers {
		if err := store.Delete(name); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		if err := clearConfigAPIKey(app.configPath(), name); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "logged out %s\n", name)
	}
	return 0
}

// clearConfigAPIKey removes one provider's apiKey from the config file.
func clearConfigAPIKey(path, provider string) error {
	cfg, err := loadConfigFile(path)
	if err != nil {
		return err
	}
	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		return nil
	}
	entry, ok := providers[provider].(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := entry["apiKey"]; !ok {
		return nil
	}
	delete(entry, "apiKey")
	return saveConfigFile(path, cfg)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func logoutUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent logout [provider]

Removes the provider's stored token and config-file API key. Without a
provider every credential is removed.
`)
}
