package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/mcp"
	"github.com/2118505078xyh1/argentgo/internal/plugins"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/skills"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// mcpConnectTimeout bounds one server's handshake so a broken server cannot
// stall a run.
const mcpConnectTimeout = 5 * time.Second

// mcpBinding is one discovered MCP tool and the client that serves it.
type mcpBinding struct {
	client mcp.Client
	remote string
	spec   llm.ToolSpec
}

// toolset is the agent's tool view: built-in registry, MCP tools under
// `server__tool`, and use_skill when skills are installed.
type toolset struct {
	base    *multiRootTools
	guard   tools.Guard
	skills  *skills.Registry
	mcp     []mcpBinding
	clients []mcp.Client
}

// multiRootTools fans built-in tool calls out to one registry per write root.
// The first registry is the workspace itself; the others let the same
// write/edit/multi_edit implementations reach --add-dir directories without
// duplicating filesystem logic in this package.
type multiRootTools struct {
	roots []string
	regs  []*tools.Registry
}

func newMultiRootTools(res Resolved, sess *session.Session, guard tools.Guard) *multiRootTools {
	roots := res.WriteRoots
	if len(roots) == 0 {
		roots = security.WriteRootsFrom(res.Root, res.AddDirs)
	}
	m := &multiRootTools{}
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		registry := tools.NewWithLimit(absolute, res.Mutating, 0)
		registry.SetSessionDir(filepath.Join(res.Storage, "sessions", sess.ID()))
		registry.SetGuard(guard)
		registry.SetLspIde(NewLspIdeAdapter(absolute))
		wireCheckpointDiff(registry, sess, absolute)
		wireHistory(registry, sess)
		m.roots = append(m.roots, filepath.Clean(absolute))
		m.regs = append(m.regs, registry)
	}
	if len(m.regs) == 0 {
		registry := tools.NewWithLimit(res.Root, res.Mutating, 0)
		registry.SetSessionDir(filepath.Join(res.Storage, "sessions", sess.ID()))
		registry.SetGuard(guard)
		registry.SetLspIde(NewLspIdeAdapter(res.Root))
		wireCheckpointDiff(registry, sess, res.Root)
		wireHistory(registry, sess)
		m.roots = []string{res.Root}
		m.regs = []*tools.Registry{registry}
	}
	return m
}

func (m *multiRootTools) Specs() []llm.ToolSpec {
	if len(m.regs) == 0 {
		return nil
	}
	return m.regs[0].Specs()
}

func (m *multiRootTools) Run(ctx context.Context, name, args string) (string, error) {
	index, routed, err := m.route(name, args)
	if err != nil {
		return "", err
	}
	return m.regs[index].Run(ctx, name, routed)
}

// route selects the registry whose root contains the tool's path argument.
// Relative paths that climb out of the workspace stay errors even when they
// would land in an extra root: `..` is never a way to widen the boundary.
func (m *multiRootTools) route(name, args string) (int, string, error) {
	if len(m.regs) == 0 {
		return 0, args, nil
	}
	path, ok := pathArg(args)
	if !ok || strings.TrimSpace(path) == "" {
		return 0, args, nil
	}
	base := m.roots[0]
	relativeInput := !filepath.IsAbs(path)
	absolute := path
	if relativeInput {
		absolute = filepath.Join(base, absolute)
	}
	absolute, err := filepath.Abs(absolute)
	if err != nil {
		return 0, args, fmt.Errorf("tool %q: %w", name, err)
	}
	absolute = filepath.Clean(absolute)
	if relativeInput {
		if rel, err := filepath.Rel(base, absolute); err == nil &&
			(rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return 0, args, fmt.Errorf("tool %q: path %q is outside the workspace", name, path)
		}
	}
	for i, root := range m.roots {
		if !withinRoot(root, absolute) {
			continue
		}
		// The lexical match is not the boundary: a symlink or Windows junction
		// under this root can point outside it. Resolve the real location before
		// handing the call to the registry (and therefore before approval).
		if err := security.CheckPathInRoots(absolute, []string{root}); err != nil {
			return 0, args, fmt.Errorf("tool %q: %w", name, err)
		}
		if i == 0 {
			return 0, args, nil
		}
		routed, err := withPath(args, absolute)
		if err != nil {
			return 0, args, err
		}
		return i, routed, nil
	}
	// Let the primary registry produce its familiar "outside the workspace"
	// error rather than inventing a second message here.
	return 0, args, nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

// pathArg reads the path argument shared by the filesystem tools.
func pathArg(args string) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &fields); err != nil {
		return "", false
	}
	for _, key := range []string{"path", "file", "dir", "directory", "target"} {
		if raw, ok := fields[key]; ok {
			var path string
			if json.Unmarshal(raw, &path) == nil {
				return path, true
			}
		}
	}
	return "", false
}

// withPath replaces the path argument while preserving every other field and
// its JSON number formatting.
func withPath(args, path string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return "", fmt.Errorf("tool arguments: %w", err)
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["path"] = path
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("tool arguments: %w", err)
	}
	return string(encoded), nil
}

// newToolset wires the built-in registry, plugin/skill discovery and MCP.
// Every external piece is fail-soft: a broken plugin, skill or server is
// reported once and skipped.
func newToolset(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, guard tools.Guard) *toolset {
	set := &toolset{
		base:   newMultiRootTools(res, sess, guard),
		guard:  guard,
		skills: skills.Discover(skills.DefaultRoots(res.Root, res.Home)),
	}

	// Enabled plugins contribute skills, commands and MCP servers.
	if bundles, err := plugins.Resolve(res.Root); err != nil {
		note(stdio, "plugins", err)
	} else {
		wiring := plugins.Merge(filterPlugins(bundles, res.Plugins))
		for _, path := range wiring.Skills {
			addSkillPath(set.skills, path)
		}
		for _, server := range wiring.MCP {
			env := make([]string, 0, len(server.Env))
			for key, value := range server.Env {
				env = append(env, key+"="+value)
			}
			sort.Strings(env)
			set.connect(ctx, stdio, mcp.Spec{
				Name:    server.Name,
				Command: server.Command,
				Args:    server.Args,
				Env:     env,
				URL:     server.URL,
			})
		}
	}

	for _, spec := range mcpSpecsFromConfig(res.Cfg.Raw["mcpServers"]) {
		set.connect(ctx, stdio, spec)
	}
	if res.TrustProject {
		for _, spec := range projectMCPServers(res.Root) {
			set.connect(ctx, stdio, spec)
		}
	}
	return set
}

// connect opens one server and records the tools it advertises.
func (t *toolset) connect(ctx context.Context, stdio cli.IO, server mcp.Spec) {
	attemptCtx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()

	client, err := mcp.Connect(attemptCtx, server)
	if err == nil {
		_, err = client.Initialize(attemptCtx)
	}
	var advertised []mcp.Tool
	if err == nil {
		advertised, err = client.ListTools(attemptCtx)
	}
	if err != nil {
		if client != nil {
			_ = client.Close()
		}
		note(stdio, "mcp "+server.Name, err)
		return
	}
	t.clients = append(t.clients, client)
	for _, tool := range advertised {
		schema := tool.Schema
		if len(strings.TrimSpace(string(schema))) == 0 || !json.Valid(schema) {
			schema = json.RawMessage(`{}`)
		}
		full := mcp.Namespaced(server.Name, tool.Name)
		toolSpec := llm.ToolSpec{Type: "function"}
		toolSpec.Function.Name = full
		toolSpec.Function.Description = tool.Description
		if strings.TrimSpace(tool.Description) == "" {
			toolSpec.Function.Description = fmt.Sprintf("MCP tool %q from server %q", tool.Name, server.Name)
		}
		toolSpec.Function.Parameters = schema
		t.mcp = append(t.mcp, mcpBinding{client: client, remote: tool.Name, spec: toolSpec})
	}
}

// Specs advertises the built-in tools, MCP tools and use_skill.
func (t *toolset) Guarded() bool { return t.guard != nil }

func (t *toolset) Specs() []llm.ToolSpec {
	specs := t.base.Specs()
	for _, binding := range t.mcp {
		specs = append(specs, binding.spec)
	}
	if t.skills != nil && t.skills.Len() > 0 {
		useSkill := skills.UseSkillSpec()
		spec := llm.ToolSpec{Type: useSkill.Type}
		spec.Function.Name = useSkill.Function.Name
		spec.Function.Description = useSkill.Function.Description
		spec.Function.Parameters = useSkill.Function.Parameters
		specs = append(specs, spec)
	}
	return specs
}

// Run routes a call to MCP, use_skill or the built-in registry.
func (t *toolset) Run(ctx context.Context, name, args string) (string, error) {
	for _, binding := range t.mcp {
		if binding.spec.Function.Name != name {
			continue
		}
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		if t.guard != nil {
			if err := t.guard.Check(ctx, name, json.RawMessage(args)); err != nil {
				return "", err
			}
		}
		return binding.client.CallTool(ctx, binding.remote, json.RawMessage(args))
	}
	if name == "use_skill" && t.skills != nil {
		return t.skills.HandleUseSkill(json.RawMessage(args))
	}
	return t.base.Run(ctx, name, args)
}

// Close releases every MCP client.
func (t *toolset) Close() error {
	var first error
	for _, client := range t.clients {
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	t.clients = nil
	return first
}

// filterPlugins honours --plugins: "all" (or no list) keeps every enabled
// bundle, otherwise only the named ones.
func filterPlugins(bundles []plugins.Bundle, names []string) []plugins.Bundle {
	if len(names) == 0 {
		return bundles
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if strings.EqualFold(name, "all") {
			return bundles
		}
		wanted[strings.ToLower(name)] = true
	}
	out := make([]plugins.Bundle, 0, len(bundles))
	for _, bundle := range bundles {
		if wanted[strings.ToLower(bundle.Name)] {
			out = append(out, bundle)
		}
	}
	return out
}

// addSkillPath registers one plugin skill directory or SKILL.md file.
func addSkillPath(registry *skills.Registry, path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.IsDir() {
		for _, skill := range skills.Discover([]string{path}).All() {
			if _, exists := registry.Get(skill.Name); !exists {
				registry.Add(skill)
			}
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	skill, err := skills.Parse(string(data), path)
	if err != nil {
		return
	}
	if _, exists := registry.Get(skill.Name); !exists {
		registry.Add(skill)
	}
}

// mcpSpecsFromConfig reads the `mcpServers` object (or list) shape, including
// the per-server enabled/readOnly/allowedTools/deniedTools controls.
func mcpSpecsFromConfig(raw any) []mcp.Spec {
	// DecodeConfig expects the whole config tree; callers here may pass either
	// that tree or just its `mcpServers` object (or list).
	object, ok := raw.(map[string]any)
	if !ok {
		if list, ok := raw.([]any); ok {
			object = map[string]any{"mcpServers": list}
		} else {
			return nil
		}
	} else if _, hasKey := object["mcpServers"]; !hasKey {
		object = map[string]any{"mcpServers": object}
	}
	specs, _ := mcp.DecodeConfig(object)
	return specs
}

// projectMCPServers reads <root>/.argent/mcp.json, which only loads when the
// project is trusted.
func projectMCPServers(root string) []mcp.Spec {
	path := filepath.Join(root, ".argent", "mcp.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil
	}
	if servers, ok := document["mcpServers"]; ok {
		return mcpSpecsFromConfig(servers)
	}
	return mcpSpecsFromConfig(document)
}

func stringsOf(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func numOf(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case json.Number:
		n, err := value.Float64()
		if err == nil {
			return n
		}
	}
	return 0
}

// note writes one fail-soft warning line. A nil stderr is discarded.
func note(stdio cli.IO, what string, err error) {
	if stdio.Err == nil {
		return
	}
	fmt.Fprintf(stdio.Err, "%s: %v (skipped)\n", what, err)
}
