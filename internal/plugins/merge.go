package plugins

import "fmt"

// ServerSpec 是启用的 bundle 贡献的一个 MCP 服务器。它刻意是本地类型：调用方把它适配到自己的 MCP 客户端类型。Plugin 是声明服务器的 bundle。Name 是服务器的清单键。Dir 是 bundle 根，用作服务器的工作目录。
// 刻意是本地类型：调用方把它适配到自己的 MCP 客户端类型。
type ServerSpec struct {
	// Plugin 是声明服务器的 bundle。
	Plugin string
	// Name 是服务器的清单键。
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	// Dir 是 bundle 根，用作服务器的工作目录。
	Dir string
}

// Wiring 是启用的 bundle 贡献的一切。调用方把每个
// 切片交给自己的加载器（MCP、技能、命令、钩子）。
type Wiring struct {
	MCP        []ServerSpec
	Skills     []string
	Commands   []string
	HooksPaths []string

	// problems 记录因清单路径逃逸 bundle 根而被拒绝的贡献。
	// 它是包内部的：需要报告发现的调用方改用
	// Wiring 是启用的 bundle 贡献的一切。调用方把每个切片交给自己的加载器（MCP、技能、命令、钩子）。problems 记录因清单路径逃逸 bundle 根而被拒绝的贡献。它是包内部的：需要报告发现的调用方改用 Discover / Manifest.Validate。
	problems []Problem
}

// Merge 把启用的 bundle 展平为一个接线文档。未启用的
// bundle 不贡献任何东西；Resolve 已返回仅启用的集合，
// 因此传入其输出是正常情况。
//
// Merge 是纯函数：它针对每个 bundle 根解析路径而不触碰
// Merge 把启用的 bundle 展平为一个接线文档。未启用的 bundle 不贡献任何东西；Resolve 已返回仅启用的集合，因此传入其输出是正常情况。Merge 是纯函数：它针对每个 bundle 根解析路径而不触碰文件系统。缺失文件由 Manifest.Validate 报告。清单路径逃逸其根的 bundle 完全不贡献 —— 绝对或 ../ 路径绝不能成为指令或命令来源。
// 清单路径逃逸其根的 bundle 完全不贡献任何东西 ——
// 绝对或 ../ 路径绝不能成为指令或命令来源。
func Merge(bundles []Bundle) Wiring {
	var wiring Wiring
	serverNames := make(map[string]bool)

	for _, bundle := range bundles {
		if !bundle.Enabled {
			continue
		}
		// 把路径逃逸当作整个 bundle 的致命问题，而非仅对
		// 违规路径：一个伸到其目录之外的清单也不是
		// 我们可以信任用于 MCP 执行的。
		if bundle.Manifest.hasPathEscape() {
			wiring.problems = append(wiring.problems, Problem{
				Plugin:  bundle.Name,
				Path:    bundle.Manifest.Path,
				Message: "manifest path escapes the bundle; all contributions skipped",
			})
			continue
		}
		for _, name := range sortedServerNames(bundle.Manifest.MCPServers) {
			// 服务器名称映射到调用方注册表中的一个进程；
			// 第一个 bundle 胜出，就像重复的插件名一样。
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

// appendResolved 针对 bundle 根解析清单路径，拒绝
// 任何逃逸。该检查从 hasPathEscape 复制而来，使
// 把路径逃逸当作整个 bundle 的致命问题，而非仅对违规路径：一个伸到其目录之外的清单也不值得我们信任用于 MCP 执行。服务器名称映射到调用方注册表中的一个进程；第一个 bundle 胜出，就像重复的插件名一样。
// 上面的守卫。
//
// 路径按其解析形式去重，而非原始清单
// appendResolved 针对 bundle 根解析清单路径，拒绝任何逃逸。该检查从 hasPathEscape 复制而来，使直接 Merge 调用方即使绕过上面的整 bundle 守卫也受保护。路径按其解析形式去重，而非原始清单字符串："skills"、"./skills" 和 "sub/../skills" 都指向同一来源且必须贡献一个条目。否则下游技能/命令加载器会把同一项目注册多次。
// 必须贡献一个条目。否则下游技能/命令加载器
// 会把同一项目注册多次。
func (w *Wiring) appendResolved(dest []string, bundle Bundle, paths []string) []string {
	seen := make(map[string]bool, len(paths))
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
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
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
