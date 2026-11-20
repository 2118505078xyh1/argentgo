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

// connectMCP is the MCP transport factory. Production uses the package
// default; tests replace it so no real MCP process is launched.
var connectMCP = mcp.Connect

// mcpBinding is one discovered MCP tool and the client that serves it.
type mcpBinding struct {
	client mcp.Client
	remote string
	spec   llm.ToolSpec
	// readOnly mirrors the tool's readOnlyHint annotation. It lets the guard
	// treat the tool as safe (no operator prompt) without weakening deny rules.
	readOnly bool
}

// toolset is the agent's tool view: built-in registry, MCP tools under
// `server__tool`, and use_skill when skills are installed.
type toolset struct {
	base    *multiRootTools
	guard   tools.Guard
	skills  *skills.Registry
	mcp     []mcpBinding
	clients []mcp.Client
	// usage is the thread-safe holder behind the `usage` tool. One holder is
	// shared by every registry in base, so a single UpdateUsage call updates the
	// tool regardless of which write root the call routes through.
	usage *usageSource
	// skillShellOn is the opt-in gate for executing `!`cmd`` spans found in a
	// loaded skill body. False by default keeps use_skill byte-identical for an
	// unconfigured run.
	skillShellOn bool
	// skillShellRun overrides the shell executor in tests. Production leaves it
	// nil, which routes each command through base.Run (security approval chain).
	skillShellRun func(context.Context, string) (string, error)
}

// multiRootTools fans built-in tool calls out to one registry per write root.
// The first registry is the workspace itself; the others let the same
// write/edit/multi_edit implementations reach --add-dir directories without
// duplicating filesystem logic in this package.
type multiRootTools struct {
	roots []string
	regs  []*tools.Registry
}

func newMultiRootTools(res Resolved, sess *session.Session, guard tools.Guard, usage *usageSource) *multiRootTools {
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
		wireUsage(registry, usage)
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
		wireUsage(registry, usage)
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
//
// Connection order defines namespace priority when two servers expose the same
// namespaced tool name: user config first, then user-installed plugins, then
// the trusted project's mcp.json and project plugins. connect() dedupes so a
// later server can never silently shadow an earlier one.
//
// Plugins are split by where they were installed. A bundle under the user's
// home plugins directory was deliberately installed by the user and is always
// loaded. A bundle under the workspace's plugins directory comes from
// repository content and, like <root>/.argent/mcp.json, is gated on
// TrustProject: opening an untrusted repository must not start its processes or
// inject its instructions.
func newToolset(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, guard tools.Guard) *toolset {
	// The base skill registry comes from the shared cache (only the skill half
	// is used here). wirePlugins/addSkillPath add plugin skills by mutating the
	// registry, so hand the toolset a copy: otherwise a plugin from one run
	// would pollute the cached snapshot reused by every later run.
	_, cachedSkills := sharedDefs().Get(nil, skills.DefaultRoots(res.Root, res.Home))
	skillRegistry := skills.New()
	for _, skill := range cachedSkills.All() {
		skillRegistry.Add(skill)
	}
	// One usage holder per toolset (hence per run). It is wired into every
	// registry below and exposed to the Execute loop through UpdateUsage, so the
	// `usage` tool reports the live token totals of this run instead of the
	// registry's default "unavailable" placeholder.
	usage := newUsageSource()
	set := &toolset{
		base:         newMultiRootTools(res, sess, guard, usage),
		guard:        guard,
		skills:       skillRegistry,
		usage:        usage,
		skillShellOn: skillShellEnabled(res.Cfg.Raw, os.Getenv),
	}
	// The policy is built before tool discovery, so MCP tools advertising
	// readOnlyHint are not in its read-only set yet. Fold their names in once
	// discovery finishes (on every return path) so they skip the operator
	// prompt while explicit deny rules still win.
	defer func() { applyMCPReadOnlyPolicy(guard, res, set.readOnlyMCPNames()) }()

	// User config is under the user's control, so it is always available.
	for _, spec := range mcpSpecsFromConfig(res.Cfg.Raw["mcpServers"]) {
		set.connect(ctx, stdio, spec)
	}

	var userBundles, projectBundles []plugins.Bundle
	if bundles, err := plugins.Resolve(res.Root); err != nil {
		note(stdio, "plugins", err)
	} else {
		userBundles, projectBundles = splitPluginsBySource(res.Root, bundles)
	}
	// Home-installed plugins are trusted independently of the repository that
	// happens to be open; they must survive an untrusted checkout.
	set.wirePlugins(ctx, stdio, userBundles, res.Plugins)

	if !res.TrustProject {
		return set
	}

	// Project-local MCP servers, like project plugins, only load for a trusted
	// workspace.
	for _, spec := range projectMCPServers(res.Root) {
		set.connect(ctx, stdio, spec)
	}
	set.wirePlugins(ctx, stdio, projectBundles, res.Plugins)
	return set
}

// readOnlyMCPNames returns the namespaced names of MCP tools that advertised
// readOnlyHint. A shadowed tool never reaches the binding list, so its name is
// never granted read-only status on behalf of the winning binding.
func (t *toolset) readOnlyMCPNames() []string {
	var names []string
	for _, binding := range t.mcp {
		if binding.readOnly {
			names = append(names, binding.spec.Function.Name)
		}
	}
	return names
}

// applyMCPReadOnlyPolicy replaces the chain's policy with one that also treats
// read-only MCP tools as safe. The chain is the single gate consulted on every
// tool call and reads its Policy at check time, so this reaches both the plain
// chain and the inspection-wrapped guard. The replacement is rebuilt from the
// same config, so deny rules, allow lists and ordered rules are unchanged.
func applyMCPReadOnlyPolicy(guard tools.Guard, res Resolved, names []string) {
	if len(names) == 0 || guard == nil {
		return
	}
	var chain *security.Chain
	switch g := guard.(type) {
	case *security.Chain:
		chain = g
	case *inspectionGuard:
		chain = g.chain
	}
	if chain != nil {
		chain.Policy = policyFor(res, names...)
	}
}

// splitPluginsBySource partitions resolved bundles into home-installed ones
// (always trusted) and workspace ones (trust-gated). Discovery scans exactly
// two roots — the project plugins dir and the user plugins dir — so the bundle
// Dir recorded by LoadManifest is enough to tell them apart. The user root is
// checked explicitly and anything not clearly under it falls back to the gated
// side, so an ambiguous path can never relax the trust boundary.
func splitPluginsBySource(root string, bundles []plugins.Bundle) (user, project []plugins.Bundle) {
	projectRoot := plugins.ProjectPluginsDir(root)
	userRoot, userErr := plugins.UserPluginsDir()
	for _, bundle := range bundles {
		if userErr == nil && withinRoot(userRoot, bundle.Dir) && !withinRoot(projectRoot, bundle.Dir) {
			user = append(user, bundle)
		} else {
			project = append(project, bundle)
		}
	}
	return user, project
}

// wirePlugins injects one source's enabled bundles. Merge also refuses bundles
// whose manifest paths escape their root; splitting the sources must not
// weaken that guard (#4) or the name-collision dedupe in connect (#2).
func (t *toolset) wirePlugins(ctx context.Context, stdio cli.IO, bundles []plugins.Bundle, names []string) {
	wiring := plugins.Merge(filterPlugins(bundles, names))
	for _, path := range wiring.Skills {
		addSkillPath(t.skills, path)
	}
	for _, server := range wiring.MCP {
		env := make([]string, 0, len(server.Env))
		for key, value := range server.Env {
			env = append(env, key+"="+value)
		}
		sort.Strings(env)
		t.connect(ctx, stdio, mcp.Spec{
			Name:    server.Name,
			Command: server.Command,
			Args:    server.Args,
			Env:     env,
			URL:     server.URL,
		})
	}
}

// connect opens one server and records the tools it advertises. Tools are
// deduplicated by their final namespaced name: bindings registered earlier win,
// so a server connected later (a plugin) cannot silently shadow — and intercept
// calls to — a server connected earlier (user config or the trusted project).
func (t *toolset) connect(ctx context.Context, stdio cli.IO, server mcp.Spec) {
	attemptCtx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()

	client, err := connectMCP(attemptCtx, server)
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

	existing := make(map[string]bool, len(t.mcp))
	for _, binding := range t.mcp {
		existing[binding.spec.Function.Name] = true
	}
	added := 0
	var shadowed []string
	for _, tool := range advertised {
		full := mcp.Namespaced(server.Name, tool.Name)
		if existing[full] {
			// Another binding already owns this exact model-facing name; keep
			// the first one and report the collision instead of hijacking it.
			shadowed = append(shadowed, full)
			continue
		}
		existing[full] = true
		schema := tool.Schema
		if len(strings.TrimSpace(string(schema))) == 0 || !json.Valid(schema) {
			schema = json.RawMessage(`{}`)
		}
		toolSpec := llm.ToolSpec{Type: "function"}
		toolSpec.Function.Name = full
		toolSpec.Function.Description = tool.Description
		if strings.TrimSpace(tool.Description) == "" {
			toolSpec.Function.Description = fmt.Sprintf("MCP tool %q from server %q", tool.Name, server.Name)
		}
		toolSpec.Function.Parameters = schema
		t.mcp = append(t.mcp, mcpBinding{
			client: client, remote: tool.Name, spec: toolSpec, readOnly: tool.ReadOnly,
		})
		added++
	}
	if len(shadowed) > 0 && stdio.Err != nil {
		fmt.Fprintf(stdio.Err, "mcp %s: tools shadowed by existing bindings: %s (skipped)\n",
			server.Name, strings.Join(shadowed, ", "))
	}
	if added == 0 {
		// A fully shadowed (or empty) server exposes no callable tool, so do
		// not keep its process alive.
		_ = client.Close()
		return
	}
	t.clients = append(t.clients, client)
}

// Specs advertises the built-in tools, MCP tools and use_skill.
func (t *toolset) Guarded() bool { return t.guard != nil }

// UpdateUsage feeds one EventUsage into the holder behind the `usage` tool.
// The Execute loop's Emit callback calls this when event.Kind is EventUsage;
// the holder is shared by every registry in the toolset, so a single call keeps
// the tool's JSON current on all write roots. It is a no-op on a nil toolset or
// holder, keeping hand-built test toolsets safe.
func (t *toolset) UpdateUsage(turn int, usage, total llm.Usage) {
	if t == nil {
		return
	}
	t.usage.Update(turn, usage, total)
}

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
		body, err := t.skills.HandleUseSkill(json.RawMessage(args))
		if err != nil || body == "" || !t.skillShellOn {
			return body, err
		}
		// Only a named, fully loaded skill body is executable. The nameless
		// call lists skills, whose descriptions are untrusted metadata that may
		// contain `!`cmd`` spans; those must never reach the shell.
		var call struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(json.RawMessage(args), &call)
		if strings.TrimSpace(call.Name) == "" {
			return body, nil
		}
		// The operator opted in: run each executable `!`cmd`` span through the
		// same approval chain a model-initiated shell call uses, then fold the
		// results back into the body the model sees.
		return t.expandSkillShell(ctx, body), nil
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
//
// This discovery is deliberately left uncached: a plugin path is unique per
// bundle, so routing it through the single DefinitionCache slot would thrash
// the snapshot shared by buildSystemWithTrust/contextPrompt/newToolset. The
// caller passes a private registry copy so these mutations never touch the
// cached snapshot.
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
