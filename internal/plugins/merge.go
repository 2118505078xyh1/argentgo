package plugins

import "fmt"

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

	// problems records contributions that were refused because their manifest
	// paths escape the bundle root. It is package-internal: callers that need
	// to report findings use Discover / Manifest.Validate instead.
	problems []Problem
}

// Merge flattens enabled bundles into one wiring document. Bundles that are
// not enabled contribute nothing; Resolve already returns an enabled-only set,
// so passing its output is the normal case.
//
// Merge is pure: it resolves paths against each bundle root without touching
// the filesystem. Missing files are reported by Manifest.Validate instead.
// A bundle whose manifest path escapes its root contributes nothing at all —
// an absolute or ../ path must never become an instruction or command source.
func Merge(bundles []Bundle) Wiring {
	var wiring Wiring
	serverNames := make(map[string]bool)

	for _, bundle := range bundles {
		if !bundle.Enabled {
			continue
		}
		// Treat a path escape as fatal for the whole bundle, not just for the
		// offending path: a manifest that reaches outside its directory is not
		// one we should trust for MCP execution either.
		if bundle.Manifest.hasPathEscape() {
			wiring.problems = append(wiring.problems, Problem{
				Plugin:  bundle.Name,
				Path:    bundle.Manifest.Path,
				Message: "manifest path escapes the bundle; all contributions skipped",
			})
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
		wiring.Skills = wiring.appendResolved(wiring.Skills, bundle, bundle.Manifest.Skills)
		wiring.Commands = wiring.appendResolved(wiring.Commands, bundle, bundle.Manifest.Commands)
		wiring.HooksPaths = wiring.appendResolved(wiring.HooksPaths, bundle, bundle.Manifest.Hooks.Paths)
	}
	return wiring
}

// appendResolved resolves manifest paths against the bundle root, refusing
// any that escape it. The check is duplicated from hasPathEscape so that a
// direct Merge caller is protected even if a bundle slips past the whole-bundle
// guard above.
func (w *Wiring) appendResolved(dest []string, bundle Bundle, paths []string) []string {
	for _, relative := range paths {
		resolved := bundle.Manifest.ResolvePath(relative)
		if !within(bundle.Manifest.Root, resolved) {
			w.problems = append(w.problems, Problem{
				Plugin:  bundle.Name,
				Path:    bundle.Manifest.Path,
				Message: fmt.Sprintf("path %q escapes the bundle", relative),
			})
			continue
		}
		dest = append(dest, resolved)
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
