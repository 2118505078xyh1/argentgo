package plugins

// ServerSpec is one MCP server contributed by an enabled bundle. It is
// deliberately local: the caller adapts it to its own MCP client type.
type ServerSpec struct {
	// Plugin is the bundle that declared the server.
	Plugin string
	// Name is the manifest key of the server.
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	// Dir is the bundle root, used as the server's working directory.
	Dir string
}

// Wiring is everything the enabled bundles contribute. The caller hands each
// slice to its own loader (MCP, skills, commands, hooks).
type Wiring struct {
	MCP        []ServerSpec
	Skills     []string
	Commands   []string
	HooksPaths []string
}

// Merge flattens enabled bundles into one wiring document. Bundles that are
// not enabled contribute nothing; Resolve already returns an enabled-only set,
// so passing its output is the normal case.
//
// Merge is pure: it resolves paths against each bundle root without touching
// the filesystem. Missing files are reported by Manifest.Validate instead.
func Merge(bundles []Bundle) Wiring {
	var wiring Wiring
	serverNames := make(map[string]bool)

	for _, bundle := range bundles {
		if !bundle.Enabled {
			continue
		}
		for _, name := range sortedServerNames(bundle.Manifest.MCPServers) {
			// A server name maps to one process in the caller's registry; the
			// first bundle wins, like duplicate plugin names do.
			if serverNames[name] {
				continue
			}
			serverNames[name] = true
			spec := bundle.Manifest.MCPServers[name]
			wiring.MCP = append(wiring.MCP, ServerSpec{
				Plugin:  bundle.Name,
				Name:    name,
				Command: spec.Command,
				Args:    append([]string(nil), spec.Args...),
				Env:     copyEnv(spec.Env),
				URL:     spec.URL,
				Dir:     bundle.Dir,
			})
		}
		wiring.Skills = appendResolved(wiring.Skills, bundle, bundle.Manifest.Skills)
		wiring.Commands = appendResolved(wiring.Commands, bundle, bundle.Manifest.Commands)
		wiring.HooksPaths = appendResolved(wiring.HooksPaths, bundle, bundle.Manifest.Hooks.Paths)
	}
	return wiring
}

func appendResolved(dest []string, bundle Bundle, paths []string) []string {
	for _, relative := range paths {
		dest = append(dest, bundle.Manifest.ResolvePath(relative))
	}
	return dest
}

func copyEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}
