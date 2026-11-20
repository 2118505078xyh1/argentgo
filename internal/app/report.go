package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// TokenReport renders the --tokens breakdown: the fixed part of each request
// (system prompt plus tool schemas) and the configured limits. It does not call
// the provider.
func TokenReport(stdio cli.IO, opts cli.Options) (string, error) {
	res, err := Resolve(stdio, opts)
	if err != nil {
		return "", err
	}
	registry := tools.NewWithLimit(res.Root, res.Mutating, 0)
	specs := registry.Specs()

	systemTokens := agent.EstimateTokens(res.System)
	toolTokens := agent.EstimateTools(specs)

	var b strings.Builder
	fmt.Fprintf(&b, "model: %s (%s)\n", res.Model, res.Protocol)
	fmt.Fprintf(&b, "provider: %s\n", firstNonEmpty(res.Provider, "(default)"))
	fmt.Fprintf(&b, "workspace: %s\n", res.Root)
	fmt.Fprintf(&b, "system prompt: ~%d tokens\n", systemTokens)
	fmt.Fprintf(&b, "tools: %d specs, ~%d tokens\n", len(specs), toolTokens)
	fmt.Fprintf(&b, "fixed request overhead: ~%d tokens\n", systemTokens+toolTokens)
	fmt.Fprintf(&b, "limits: turns=%d tokens=%d cost=$%.4f seconds=%d\n",
		res.MaxTurns, res.MaxTokens, res.MaxCostUSD, res.MaxSeconds)
	if len(res.Prices) > 0 {
		for model, price := range res.Prices {
			fmt.Fprintf(&b, "price[%s]: input=$%.4f/M output=$%.4f/M\n", model, price.Input, price.Output)
		}
	}
	if len(res.AddDirs) > 0 {
		fmt.Fprintf(&b, "extra dirs: %s\n", strings.Join(res.AddDirs, ", "))
	}
	// --session makes --tokens also report the persisted cross-resume totals.
	// Without it the report is byte-for-byte identical to before this change.
	if err := appendSessionCumulative(&b, res); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ListProfiles reports the config profile names and the selected one.
func ListProfiles(stdio cli.IO, opts cli.Options) ([]string, string, error) {
	env := stdio.Env
	if env == nil {
		env = func(string) string { return "" }
	}
	root := strings.TrimSpace(opts.Cwd)
	if root == "" {
		root = "."
	}
	cfg, err := config.Load(root, env)
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, cfg.ActiveProfile, nil
}
