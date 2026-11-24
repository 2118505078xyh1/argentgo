package app

import (
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/extension"
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
// 信任门槛（trustedPluginBundles）留在 app；逐包 Merge 与单文件归一已下沉到
// 统一门面 extension.PluginCommandSources，这里只做薄转发，行为逐字节不变。
func pluginCommandSources(res Resolved) []commands.Source {
	return extension.PluginCommandSources(trustedPluginBundles(res))
}

// pluginHookPaths 是同一信任门槛在钩子侧的部分：只有受信任包的钩子文档才会交给钩子加载器。
// 逐包 Merge 下沉到 extension.PluginHookPaths；每条路径仍需在 loadHookDocuments
// 中通过钩子信任存储的 SHA-256 批准，不受信任的文档不会因插件启用而得以运行。
func pluginHookPaths(res Resolved) []string {
	return extension.PluginHookPaths(trustedPluginBundles(res))
}
