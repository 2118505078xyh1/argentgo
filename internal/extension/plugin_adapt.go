package extension

import (
	"os"
	"path/filepath"

	"example.com/argentgo/internal/commands"
	"example.com/argentgo/internal/plugins"
)

// 本文件收拢"插件清单 → 各体系贡献"的纯适配逻辑（原在 internal/app/plugin_wiring.go）。
// 信任门槛（哪些 bundle 可信、--plugins 过滤）仍留在 app——它依赖 Resolved，
// 门面不碰。这里只接收已过信任过滤的 bundles，做逐包的 Merge 适配。
//
// 关键语义：命令与钩子刻意"逐包 Merge"（每个 bundle 单独一次 plugins.Merge），
// 这样包名与其自身路径绑定，且复用 plugins 包内所有路径越界检查；
// 这与 wirePlugins 的"一次性 Merge 全部"是不同目的，不可互换。

// PluginCommandSources 把每个已信任包的命令路径转换为带命名空间的命令来源。
// 清单允许把命令声明为单个文件，而 commands.Source.Root 只接受目录，因此
// 单文件归一到所在目录，避免合法声明被静默跳过。
func PluginCommandSources(bundles []plugins.Bundle) []commands.Source {
	var sources []commands.Source
	for _, bundle := range bundles {
		wiring := plugins.Merge([]plugins.Bundle{bundle})
		for _, path := range wiring.Commands {
			root := path
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				root = filepath.Dir(path)
			}
			sources = append(sources, commands.Source{Root: root, Prefix: bundle.Name})
		}
	}
	return sources
}

// PluginHookPaths 提取每个已信任包的钩子文档路径。逐包 Merge，语义同上。
// 每条路径仍需在 hooks 加载器侧通过 SHA-256 信任门——门面不放松该防护。
func PluginHookPaths(bundles []plugins.Bundle) []string {
	var paths []string
	for _, bundle := range bundles {
		wiring := plugins.Merge([]plugins.Bundle{bundle})
		paths = append(paths, wiring.HooksPaths...)
	}
	return paths
}
