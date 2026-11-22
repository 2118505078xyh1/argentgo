package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// mcpConnectTimeout 限制单个服务的握手时间，使故障服务不会拖住运行。
const mcpConnectTimeout = 5 * time.Second

// connectMCP 是 MCP 传输工厂。生产环境使用包默认值；测试会替换它，使不会启动真实的 MCP 进程。
var connectMCP = mcp.Connect

// mcpHealthEnabled 报告是否开启 MCP 运行时健康接线。env 为 nil 时退化为 os.Getenv；
// 仅首尾空白修剪后等于 "1" 才开启。默认（未设置）为 false，整段接线零调用。
// 通过注入 getenv 使测试能在不读进程环境的情况下精确控制两态。
func mcpHealthEnabled(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	return strings.TrimSpace(getenv(mcp.MCPHealthEnv)) == "1"
}

// mcpBinding 是一个被发现的 MCP 工具以及服务它的客户端。
type mcpBinding struct {
	client mcp.Client
	remote string
	spec   llm.ToolSpec
	// readOnly 对应工具的 readOnlyHint 注解。它使防护器能把该工具视为安全（无需操作人员提示），
	// 而不会削弱拒绝规则。
	readOnly bool
	// server 是提供该工具的服务名；RefreshTools 重新发现时需要它重建带命名空间的名称。
	server string
}

// toolset 是 agent 的工具视图：内置注册表、`server__tool` 形式命名的 MCP 工具，
// 以及安装技能后提供的 use_skill。
type toolset struct {
	base   *multiRootTools
	guard  tools.Guard
	skills *skills.Registry
	// mcpMu 保护 mcp 绑定与 clients：Setup 阶段写入，运行期 RefreshTools
	// 会因 list_changed 重新发现而替换绑定，Specs/Run 与之并发读取。
	mcpMu   sync.RWMutex
	mcp     []mcpBinding
	clients []mcp.Client
	// sandbox 是已解析的沙箱模式，用于在 MCP 执行分支做只读兜底：
	// MCP 工具不经过内置注册表的路径边界与变更性兜底，必须在此单独门控。
	sandbox security.SandboxMode
	// usage 是 `usage` 工具背后线程安全的持有者。base 中的每个注册表共享同一个持有者，
	// 因此无论调用路由到哪个写入根目录，单次 UpdateUsage 调用都会更新该工具。
	usage *usageSource
	// skillShellOn 是对已加载技能正文中 `!`cmd`` 片段执行的选择性启用门控。
	// 默认 false 使未配置的运行保持 use_skill 逐字节不变。
	skillShellOn bool
	// skillShellRun 在测试中覆盖 shell 执行器。生产环境保持 nil，
	// 使每条命令经由 base.Run（安全审批链）执行。
	skillShellRun func(context.Context, string) (string, error)

	// health 聚合各 MCP 客户端的运行时健康快照。仅当 ARGENT_MCP_HEALTH 开启时
	// 分配；默认 nil 时 connect 不设置 Spec.HealthTracking，也不写任何文件。
	health *mcp.HealthAggregator
	// healthFile 是健康快照的落盘路径（来自 Resolved.Storage）。仅在 health 非 nil
	// 且路径非空时写入，使未配置存储目录的运行不会因无法落盘而改变行为。
	healthFile string
	// healthTracked 记录已 Track 的服务名，Close 时逐一 Untrack 后再落盘。
	healthTracked []string
}

// multiRootTools 把内置工具调用分发到每个写入根目录对应的注册表。第一个注册表是工作区本身；
// 其余注册表让同一套 write/edit/multi_edit 实现能够访问 --add-dir 目录，
// 而无需在本包中重复文件系统逻辑。
type multiRootTools struct {
	roots []string
	regs  []*tools.Registry
}

// wireOptInTools 按 opt-in 注册表把新工具尾部追加到 registry。默认（reg 为 nil 或
// 对应开关关闭）时不注册任何工具，因此 Specs()/Names() 与 golden 逐字节不变。
// 工具注册函数自身是幂等的：重复注册或同名已存在时不再插入。
func wireOptInTools(registry *tools.Registry, reg *OptInRegistry) {
	if registry == nil || reg == nil {
		return
	}
	if reg.Enabled(OptInApplyPatch) {
		tools.RegisterApplyPatch(registry)
	}
	if reg.Enabled(OptInGrepEx) {
		tools.RegisterGrepEx(registry)
	}
	if reg.Enabled(OptInCodeGraph) {
		tools.RegisterCodeGraph(registry)
	}
}

func newMultiRootTools(res Resolved, sess *session.Session, guard tools.Guard, usage *usageSource, reg *OptInRegistry) *multiRootTools {
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
		wireOptInTools(registry, reg)
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
		wireOptInTools(registry, reg)
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

// route 选择其根目录包含工具 path 参数的注册表。即使相对路径最终落在某个额外根目录中，
// 向上越出工作区的相对路径仍然是错误：`..` 绝不是拓宽边界的手段。
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
		// 词法匹配并非边界：该根目录下的符号链接或 Windows 联结点可能指向外部。
		// 在把调用交给注册表之前（也就是在审批之前），先解析真实位置。
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
	// 让主注册表产生它熟悉的"在工作区之外"错误，而不是在这里另造一条消息。
	return 0, args, nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

// pathArg 读取文件系统工具共用的 path 参数。
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

// withPath 替换 path 参数，同时保留其他每个字段及其 JSON 数字格式。
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

// newToolset 接线内置注册表、插件/技能发现以及 MCP。每个外部部件都是安全失败的：
// 损坏的插件、技能或服务会被报告一次然后跳过。
//
// 当两个服务暴露相同的带命名空间工具名时，连接顺序决定命名空间优先级：
// 先是用户配置，然后是用户安装的插件，再是受信任项目的 mcp.json 和项目插件。
// connect() 会去重，使后连接的服务绝不会静默遮蔽先连接的服务。
//
// 插件按安装位置区分。位于用户主目录插件目录下的包是用户有意安装的，总是会被加载。
// 位于工作区插件目录下的包来自仓库内容，与 <root>/.argent/mcp.json 一样受 TrustProject 门控：
// 打开不受信任的仓库绝不能启动其进程或注入其指令。
func newToolset(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, guard tools.Guard, reg *OptInRegistry) *toolset {
	// 基础技能注册表来自共享缓存（这里只使用技能那一半）。wirePlugins/addSkillPath
	// 通过修改注册表来添加插件技能，因此交给工具集一份副本：否则某次运行的插件
	// 会污染被之后每次运行复用的缓存快照。
	_, cachedSkills := sharedDefs().Get(nil, skills.DefaultRoots(res.Root, res.Home))
	skillRegistry := skills.New()
	for _, skill := range cachedSkills.All() {
		skillRegistry.Add(skill)
	}
	// 每个工具集（也就是每次运行）一个用量持有者。它被接入下方的每个注册表，
	// 并通过 UpdateUsage 暴露给 Execute 循环，使 `usage` 工具报告本次运行的实时 token 总量，
	// 而不是注册表的默认"不可用"占位符。
	usage := newUsageSource()
	set := &toolset{
		base:         newMultiRootTools(res, sess, guard, usage, reg),
		guard:        guard,
		skills:       skillRegistry,
		usage:        usage,
		sandbox:      res.Sandbox,
		skillShellOn: skillShellEnabled(res.Cfg.Raw, os.Getenv),
	}
	// opt-in 默认关闭：未开启时 health 保持 nil，connect 不设置 Spec.HealthTracking，
	// mcp 客户端不分配 HealthTracker，discoverTools 的观测路径零执行。
	if mcpHealthEnabled(stdio.Env) {
		set.health = &mcp.HealthAggregator{}
		if strings.TrimSpace(res.Storage) != "" {
			set.healthFile = filepath.Join(res.Storage, mcp.MCPHealthReportFile)
		}
	}
	// 策略在工具发现之前构建，因此声明了 readOnlyHint 的 MCP 工具还不在其只读集合中。
	// 发现完成后（在每条返回路径上）把它们的名字折进去，
	// 使这些工具跳过操作人员提示，同时显式的拒绝规则仍然优先。
	defer func() { applyMCPReadOnlyPolicy(guard, res, set.readOnlyMCPNames()) }()

	// 用户配置受用户掌控，因此始终可用。
	for _, spec := range mcpSpecsFromConfig(res.Cfg.Raw["mcpServers"]) {
		set.connect(ctx, stdio, spec)
	}

	var userBundles, projectBundles []plugins.Bundle
	if bundles, err := plugins.Resolve(res.Root); err != nil {
		note(stdio, "plugins", err)
	} else {
		userBundles, projectBundles = splitPluginsBySource(res.Root, bundles)
	}
	// 主目录安装的插件的信任与碰巧打开的仓库无关；它们必须在不受信任的检出中仍然可用。
	set.wirePlugins(ctx, stdio, userBundles, res.Plugins)

	if !res.TrustProject {
		return set
	}

	// 项目本地的 MCP 服务与项目插件一样，只在受信任的工作区中加载。
	for _, spec := range projectMCPServers(res.Root) {
		set.connect(ctx, stdio, spec)
	}
	set.wirePlugins(ctx, stdio, projectBundles, res.Plugins)
	return set
}

// readOnlyMCPNames 返回声明了 readOnlyHint 的 MCP 工具的带命名空间名称。
// 被遮蔽的工具永远不会进入绑定列表，因此它的名字绝不会代表胜出的绑定而被授予只读状态。
func (t *toolset) readOnlyMCPNames() []string {
	t.mcpMu.RLock()
	defer t.mcpMu.RUnlock()
	var names []string
	for _, binding := range t.mcp {
		if binding.readOnly {
			names = append(names, binding.spec.Function.Name)
		}
	}
	return names
}

// applyMCPReadOnlyPolicy 把链的策略替换为同时把只读 MCP 工具视为安全的策略。
// 链是每次工具调用都会查询的唯一门控，并在检查时读取其 Policy，
// 因此这既作用于普通链，也作用于带检查包装的防护器。
// 替换策略由同一份配置重建，因此拒绝规则、白名单和有序规则都不变。
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

// splitPluginsBySource 把已解析的包划分为主目录安装的（总是受信任）和工作区的（受信任门控）。
// 发现过程恰好扫描两个根目录——项目插件目录和用户插件目录——因此 LoadManifest 记录的包 Dir
// 足以区分它们。用户根目录会被显式检查，任何不明确位于其下的项都会落到受门控的一侧，
// 因此歧义路径绝不会放松信任边界。
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

// wirePlugins 注入某个来源启用的包。Merge 也会拒绝清单路径越出其根目录的包；
// 对来源的拆分不得削弱该防护（#4）或 connect 中的名称冲突去重（#2）。
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
			Dir:     server.Dir,
			Env:     env,
			URL:     server.URL,
		})
	}
}

// appendAdvertisedTools 把一次发现得到的工具列表转换成绑定并追加到 dst。existing
// 在调用前已包含所有被占用的面向模型名称；函数原地补入本次新增的名字，并返回新增数
// 与被遮蔽的名字。connect 与 RefreshTools 共用它，避免两条路径的绑定语义漂移。
func appendAdvertisedTools(dst *[]mcpBinding, existing map[string]bool, serverName string, client mcp.Client, advertised []mcp.Tool) (int, []string) {
	added := 0
	var shadowed []string
	for _, tool := range advertised {
		full := mcp.Namespaced(serverName, tool.Name)
		if existing[full] {
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
			toolSpec.Function.Description = fmt.Sprintf("MCP tool %q from server %q", tool.Name, serverName)
		}
		toolSpec.Function.Parameters = schema
		*dst = append(*dst, mcpBinding{
			client: client, remote: tool.Name, spec: toolSpec, readOnly: tool.ReadOnly,
			server: serverName,
		})
		added++
	}
	return added, shadowed
}

// connect 打开一个服务并记录它公布的工具。工具按最终的带命名空间名称去重：先注册的绑定优先，
// 因此后连接的服务（插件）不能静默遮蔽——并拦截对——先连接的服务（用户配置或受信任项目）
// 的调用。
func (t *toolset) connect(ctx context.Context, stdio cli.IO, server mcp.Spec) {
	attemptCtx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()

	// opt-in 开启时把健康观测接到这一台客户端：mcp.Connect 只按 Spec.HealthTracking
	// 决定是否挂接 HealthTracker，默认（app 未设置）时该字段为 false、零观测。
	if t.health != nil {
		server.HealthTracking = true
	}

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
		// 连接/握手/发现失败也是运行时健康信息：记录一条不健康快照，使 doctor
		// 能报出失败服务名与 LastError。未开启 opt-in 时 t.health 为 nil，整段空操作。
		if t.health != nil {
			t.health.Observe(server.Name, mcp.HealthSnapshot{
				Healthy:   false,
				LastError: err.Error(),
			})
		}
		note(stdio, "mcp "+server.Name, err)
		return
	}
	// 发现成功后注册可选健康视图：*client 始终实现 HealthReporter，但 fake 客户端
	// 可能不实现，此时仍以名字注册（PollReporters 会跳过 nil），保证名字进入报告。
	if t.health != nil {
		var reporter mcp.HealthReporter
		if typed, ok := client.(mcp.HealthReporter); ok {
			reporter = typed
		}
		t.health.Track(server.Name, reporter)
		t.healthTracked = append(t.healthTracked, server.Name)
	}

	t.mcpMu.Lock()
	defer t.mcpMu.Unlock()

	existing := make(map[string]bool, len(t.mcp))
	for _, binding := range t.mcp {
		existing[binding.spec.Function.Name] = true
	}
	added, shadowed := appendAdvertisedTools(&t.mcp, existing, server.Name, client, advertised)
	if len(shadowed) > 0 && stdio.Err != nil {
		fmt.Fprintf(stdio.Err, "mcp %s: tools shadowed by existing bindings: %s (skipped)\n",
			server.Name, strings.Join(shadowed, ", "))
	}
	if added == 0 {
		// 被完全遮蔽（或为空）的服务没有可调用的工具，因此不要让它的进程继续存活。
		_ = client.Close()
		return
	}
	t.clients = append(t.clients, client)
}

// RefreshTools 在所有已连接的客户端上重新执行工具发现，并用最新结果重建绑定。
// 它只在 list_changed 跟踪开启时由 Run 的“工具不存在”分支调用：服务器通过通知
// 告知目录变化后，下一次指向新工具的调用会先走这里，使新工具无需重启即可用。
// 单台服务器发现失败时保留它的旧绑定，避免整个工具集因一台故障服务器而清空。
func (t *toolset) RefreshTools(ctx context.Context) {
	t.mcpMu.Lock()
	defer t.mcpMu.Unlock()

	if len(t.mcp) == 0 {
		return
	}
	// 按首次出现顺序收集仍存活的客户端及其服务名（多个绑定可共享一个客户端）。
	type source struct {
		client mcp.Client
		server string
	}
	seen := make(map[mcp.Client]bool)
	var sources []source
	for _, b := range t.mcp {
		if !seen[b.client] {
			seen[b.client] = true
			sources = append(sources, source{client: b.client, server: b.server})
		}
	}
	rebuilt := make([]mcpBinding, 0, len(t.mcp))
	existing := make(map[string]bool, len(t.mcp))
	for _, s := range sources {
		advertised, err := s.client.ListTools(ctx)
		if err != nil {
			for _, b := range t.mcp {
				if b.client == s.client {
					rebuilt = append(rebuilt, b)
					existing[b.spec.Function.Name] = true
				}
			}
			continue
		}
		appendAdvertisedTools(&rebuilt, existing, s.server, s.client, advertised)
	}
	t.mcp = rebuilt
}

// findMCP 按面向模型的名字查找一个 MCP 绑定。
func (t *toolset) findMCP(name string) *mcpBinding {
	t.mcpMu.RLock()
	defer t.mcpMu.RUnlock()
	for i := range t.mcp {
		if t.mcp[i].spec.Function.Name == name {
			return &t.mcp[i]
		}
	}
	return nil
}

// Specs 公布内置工具、MCP 工具和 use_skill。
func (t *toolset) Guarded() bool { return t.guard != nil }

// UpdateUsage 把一个 EventUsage 送入 `usage` 工具背后的持有者。
// Execute 循环的 Emit 回调在 event.Kind 为 EventUsage 时调用它；该持有者由工具集中的每个注册表共享，
// 因此单次调用就能让该工具的 JSON 在所有写入根目录上保持最新。
// 对 nil 的工具集或持有者它是空操作，使手工构建的测试工具集保持安全。
func (t *toolset) UpdateUsage(turn int, usage, total llm.Usage) {
	if t == nil {
		return
	}
	t.usage.Update(turn, usage, total)
}

func (t *toolset) Specs() []llm.ToolSpec {
	t.mcpMu.RLock()
	defer t.mcpMu.RUnlock()
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

// Run 把调用路由到 MCP、use_skill 或内置注册表。
func (t *toolset) Run(ctx context.Context, name, args string) (string, error) {
	binding := t.findMCP(name)
	// opt-in 默认关闭：未开启 list_changed 跟踪时完全不走刷新路径。开启后，
	// 指向“当前绑定里不存在”的工具的调用先重新发现一次并重建绑定，再查一遍，
	// 使服务器刚宣告的工具在下一次调用即可命中；仍找不到才回落到内置注册表。
	if binding == nil && mcp.ListChangedRefreshEnabled() {
		t.RefreshTools(ctx)
		binding = t.findMCP(name)
	}
	if binding != nil {
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		// 只读沙箱 fail-closed：修改类 MCP 工具绝不能触达传输层。
		// 它没有声明 readOnlyHint，也不经过内置注册表的 resolve 边界，
		// 因此必须在此单独拦截（对齐 mcp.client.CallTool 的失败前置风格）。
		if t.sandbox == security.SandboxReadOnly && security.MutatingTool(binding.spec.Function.Name) {
			return "", fmt.Errorf("%w: tool %q looks mutating and sandbox is read-only; call blocked before contacting the MCP server, no request was sent", mcp.ErrReadOnly, binding.spec.Function.Name)
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
		// 只有具名且完整加载的技能正文才是可执行的。无名调用会列出技能，
		// 其描述是不可信的元数据，可能包含 `!`cmd`` 片段；这些绝不能进入 shell。
		var call struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(json.RawMessage(args), &call)
		if strings.TrimSpace(call.Name) == "" {
			return body, nil
		}
		// 操作人员已选择启用：让每个可执行的 `!`cmd`` 片段经过与模型发起的 shell 调用相同的
		// 审批链，然后把结果折回模型看到的正文。
		return t.expandSkillShell(ctx, body), nil
	}
	return t.base.Run(ctx, name, args)
}

// MCPHealthReport 返回当前所有 MCP 服务器的只读健康报告。它先从各已 Track 的
// HealthReporter 拉取最新快照（纯内存同步、不发起任何 I/O），再聚合为独立副本；
// 调用方修改返回报告不会影响工具集内部状态。
//
// opt-in 关闭（t.health 为 nil）时返回空报告（OverallHealthy=true、Servers 为空），
// 不触碰任何客户端，保证默认路径逐字节不变。
func (t *toolset) MCPHealthReport() mcp.MCPHealthReport {
	if t.health == nil {
		return mcp.MCPHealthReport{OverallHealthy: true}
	}
	t.health.PollReporters()
	return t.health.Report()
}

// writeMCPHealthReport 把最新健康快照持久化到存储目录，供下一次 `doctor` 进程读取。
// 它只在 opt-in 开启且已配置落盘路径时生效；写失败被刻意吞掉，绝不改变关闭流程。
// 空报告（没有任何服务被观测）不写文件，使默认健康运行不留下任何副作用。
func (t *toolset) writeMCPHealthReport() {
	if t.health == nil || t.healthFile == "" {
		return
	}
	report := t.MCPHealthReport()
	if len(report.Servers) == 0 {
		return
	}
	data, err := json.Marshal(report)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.healthFile), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(t.healthFile, data, 0o600)
}

// Close 释放每一个 MCP 客户端。opt-in 开启时先聚合并落盘健康快照，再 Untrack
// 各客户端；顺序保证报告包含最后一次观测，且不会因后续关闭而丢失。
func (t *toolset) Close() error {
	t.mcpMu.Lock()
	defer t.mcpMu.Unlock()
	if t.health != nil {
		t.writeMCPHealthReport()
		for _, name := range t.healthTracked {
			t.health.Untrack(name)
		}
		t.healthTracked = nil
	}
	var first error
	for _, client := range t.clients {
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	t.clients = nil
	return first
}

// filterPlugins 遵循 --plugins："all"（或无列表）保留所有启用的包，否则只保留指定的包。
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

// addSkillPath 注册一个插件技能目录或 SKILL.md 文件。
//
// 这个发现过程刻意不做缓存：插件路径对每个包都是唯一的，
// 若让它走单一 DefinitionCache 槽位，会不断破坏 buildSystemWithTrust/contextPrompt/newToolset
// 共享的快照。调用方传入私有的注册表副本，使这些修改绝不会触碰缓存的快照。
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

// ---- 技能触发器 opt-in ----
//
// 用户 SKILL.md 的 `triggers:`（paths/keywords/models）此前只是元数据：注册表解析了它们，
// 但 use_skill 仍可按任意名称加载任何技能。本开关默认关闭时整段接线为零调用，
// 系统提示、use_skill 输出与事件流逐字节不变。开启后由 Execute 在轮次开始前把当前
// 上下文解析为 active 技能集，收紧 use_skill 可见性（见 Registry.SetTriggerContext）。

// skillTriggersOptInEnv 是技能触发器接线专用的环境变量。它与 ARGENT_OPT_INS 并列，
// 使本实验能力可以单独开启而不触碰共享的 opt-in 清单文件。
const skillTriggersOptInEnv = "ARGENT_SKILL_TRIGGERS"

// skillTriggersOptInKeys 是 Config.Raw 上等价的配置键，与 skillShell 采用相同的
// 多写法约定，便于配置与环境变量两条入口共存。
var skillTriggersOptInKeys = []string{"skillTriggers", "skill-triggers", "skill_triggers"}

// skillTriggersEnabled 报告是否开启技能触发器接线。env 为 "1" 或配置键为真都算开启；
// env/config 为 nil 时退化为 os.Getenv / 空配置。默认（未设置）为 false。
func skillTriggersEnabled(raw map[string]any, getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv(skillTriggersOptInEnv)) == "1" {
		return true
	}
	return cfgBool(raw, skillTriggersOptInKeys...)
}

// skillTriggerModelFamily 把模型名归一化为技能触发器使用的家族名：剥离
// "anthropic/claude-..." 这类供应商前缀、去分隔符、转小写，再按已知家族前缀匹配。
// 未知家族原样返回（小写），使其只能匹配显式的同名触发器。
func skillTriggerModelFamily(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	for _, family := range []string{"claude", "gpt", "gemini", "deepseek", "kimi", "moonshot", "grok", "glm", "qwen", "minimax"} {
		if strings.Contains(name, family) {
			return family
		}
	}
	return strings.Trim(name, "-_ ")
}

// skillTriggerPath 从提示文本中抽取第一个像文件路径的片段（含分隔符或带扩展名），
// 作为路径触发器的当前上下文；找不到时回落到工作区根目录。
//
// 扫描按"ASCII 路径字符run"进行：CJK 词、标点与引号都是天然边界，因此
// "编辑 src/main.go，然后提交" 得到 "src/main.go"，而不是连同尾随中文的整段。
func skillTriggerPath(prompt, root string) string {
	var run strings.Builder
	take := func() string {
		token := run.String()
		run.Reset()
		if token == "" {
			return ""
		}
		if strings.ContainsAny(token, "/\\") || len(filepath.Ext(token)) > 1 {
			return token
		}
		return ""
	}
	for _, r := range prompt {
		if isSkillPathRune(r) {
			run.WriteRune(r)
			continue
		}
		if got := take(); got != "" {
			return got
		}
	}
	if got := take(); got != "" {
		return got
	}
	return root
}

// isSkillPathRune 判定一个字符是否可出现在 ASCII 风格的文件路径中。
// CJK、标点、引号一律不是，因而自然成为词边界。
func isSkillPathRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '.' || r == '/' || r == '\\' || r == '_' || r == '-'
}

// mcpSpecsFromConfig 读取 `mcpServers` 对象（或列表）形状，包括每个服务的
// enabled/readOnly/allowedTools/deniedTools 控制项。
func mcpSpecsFromConfig(raw any) []mcp.Spec {
	// DecodeConfig 期望整棵配置树；这里的调用方可以传入整棵树，也可以只传它的
	// `mcpServers` 对象（或列表）。
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

// projectMCPServers 读取 <root>/.argent/mcp.json，它只在项目受信任时加载。
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

// note 写一行安全失败的警告。nil 的 stderr 会被丢弃。
func note(stdio cli.IO, what string, err error) {
	if stdio.Err == nil {
		return
	}
	fmt.Fprintf(stdio.Err, "%s: %v (skipped)\n", what, err)
}
