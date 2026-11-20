package app

import (
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/plugins"
)

// trustedPluginBundles 返回可为一次运行提供命令和钩子的已启用包。拆分方式与 newToolset 的信任门槛一致：
// 位于用户插件目录下的包是有意安装的，总是生效；而仓库附带的包只在设置了 res.TrustProject 时才生效。
// --plugins 过滤仍然适用，因此被标志禁用的插件无法把命令或钩子泄漏进会话。
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

// pluginCommandSources 把每个受信任包的清单命令路径转换为带命名空间的 commands.Source。
// Merge 刻意一次只处理一个包：它把包名与其自身的路径绑定，并原样复用 plugins 包中的所有路径越界检查。
// 因此越出自身包范围的清单不会提供任何命令，正如它不会提供 MCP 服务或技能一样。
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

// pluginHookPaths 是同一信任门槛在钩子侧的部分：只有受信任包的钩子文档才会交给钩子加载器。
// 每条路径仍需在 loadHookDocuments 中通过钩子信任存储的 SHA-256 批准，
// 因此不受信任的文档绝不会因为其插件被启用而得以运行。
func pluginHookPaths(res Resolved) []string {
	var paths []string
	for _, bundle := range trustedPluginBundles(res) {
		wiring := plugins.Merge([]plugins.Bundle{bundle})
		paths = append(paths, wiring.HooksPaths...)
	}
	return paths
}
