package extension

import (
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/plugins"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// 本文件把三个"目录扫描 → 命名条目"体系的发现结果投影成统一 Inventory。
// 它只是薄封装：实际发现算法仍在各包内，返回的具体集合也不被本包持有。
// hooks 不在其中——它是事件触发体系，其门面价值在 Authorizer（见 authorize.go）。

// DiscoverSkills 投影技能发现结果。skills.Discover 对坏条目静默跳过，
// 这里不额外制造诊断（保留"跳过"语义，只是清单里不出现该条目）。
func DiscoverSkills(roots []string) Inventory {
	registry := skills.Discover(roots)
	return Inventory{Descriptors: fromSkills(registry.All())}
}

// DiscoverCommands 投影斜杠命令发现结果。
func DiscoverCommands(sources []commands.Source) Inventory {
	set := commands.NewSetWithBuiltins(sources)
	return Inventory{Descriptors: fromCommands(set.List())}
}

// DiscoverPlugins 投影插件发现结果。plugins.Discover 把校验问题作为
// []Problem 返回（非致命），这里归一为统一诊断。整体发现失败才返回 error。
func DiscoverPlugins(root string) (Inventory, error) {
	bundles, problems, err := plugins.Discover(root)
	if err != nil {
		return Inventory{}, err
	}
	return Inventory{
		Descriptors: fromPlugins(bundles),
		Diagnostics: fromPluginProblems(problems),
	}, nil
}
