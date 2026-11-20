package app

import (
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/plugins"
)

// trustedPluginBundles returns the enabled bundles that may contribute
// commands and hooks for one run. The split mirrors newToolset's trust gate:
// a bundle under the user's plugins directory was installed deliberately and
// is always live, while a bundle shipped by the repository only contributes
// when res.TrustProject is set. --plugins filtering still applies, so a
// disabled-by-flag plugin cannot leak commands or hooks into the session.
func trustedPluginBundles(res Resolved) []plugins.Bundle {
	bundles, err := plugins.Resolve(res.Root)
	if err != nil {
		return nil
	}
	user, project := splitPluginsBySource(res.Root, bundles)
	trusted := user
	if res.TrustProject {
		trusted = append(trusted, project...)
	}
	return filterPlugins(trusted, res.Plugins)
}

// pluginCommandSources turns each trusted bundle's manifest command paths into
// a namespaced commands.Source. Merge is deliberately called one bundle at a
// time: it keeps the bundle name attached to its own paths and reuses every
// path-escape check in the plugins package unchanged. A manifest that reaches
// outside its bundle therefore contributes no commands, just as it contributes
// no MCP servers or skills.
func pluginCommandSources(res Resolved) []commands.Source {
	var sources []commands.Source
	for _, bundle := range trustedPluginBundles(res) {
		wiring := plugins.Merge([]plugins.Bundle{bundle})
		for _, path := range wiring.Commands {
			sources = append(sources, commands.Source{Root: path, Prefix: bundle.Name})
		}
	}
	return sources
}

// pluginHookPaths is the hook half of the same trust gate: only trusted
// bundles' hook documents are offered to the hook loader. Each path still has
// to pass the hook trust store's SHA-256 grant inside loadHookDocuments, so an
// untrusted document can never run just because its plugin was enabled.
func pluginHookPaths(res Resolved) []string {
	var paths []string
	for _, bundle := range trustedPluginBundles(res) {
		wiring := plugins.Merge([]plugins.Bundle{bundle})
		paths = append(paths, wiring.HooksPaths...)
	}
	return paths
}
