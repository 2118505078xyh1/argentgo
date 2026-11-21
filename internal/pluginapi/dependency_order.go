package pluginapi

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Feature 描述一个功能及其声明式依赖。
// Name 必须唯一；Deps 列出必须先于本功能加载的功能名；
// Order 仅用于同一拓扑层内打破平局（值越小越靠前），
// 与 Registry.Factories() 的 (order, name) tie-break 保持一致。
type Feature struct {
	Name  string
	Deps  []string
	Order int
}

// 诊断哨兵错误。调用方可用 errors.Is 精确匹配某类错误，
// 同时通过错误文本获得可读的路径信息。
var (
	// ErrDuplicateFeature 表示存在重名的功能声明。
	ErrDuplicateFeature = errors.New("pluginapi: duplicate feature")
	// ErrMissingDependency 表示依赖了未声明的功能。
	ErrMissingDependency = errors.New("pluginapi: missing dependency")
	// ErrCycle 表示依赖图中存在环。
	ErrCycle = errors.New("pluginapi: dependency cycle")
)

// topoNode 是拓扑排序使用的内部节点，派生自 Feature，绝不写回入参。
type topoNode struct {
	name       string
	order      int
	deps       []string
	indegree   int      // 尚未被加载的依赖数量
	dependents []string // 依赖于本节点的功能名
}

// TopoSort 根据 Feature 声明的依赖推导加载顺序。
//
// 采用分层 Kahn 算法：每一轮收集当前入度为 0 的节点，
// 按 (Order, Name) 稳定排序后输出，再将其后继的入度减一。
// 输出因此是确定性的，与入参的排列顺序无关。
//
// 错误以哨兵错误为起点用 %w 包装，可用 errors.Is 匹配：
//   - ErrDuplicateFeature：重名功能；
//   - ErrMissingDependency：依赖了未声明的功能，错误文本含 "功能 -> 缺失依赖"；
//   - ErrCycle：存在环，错误文本含 "a -> b -> ... -> a" 形式的环路径。
//
// 本函数是纯函数，不修改 features 及其 Deps；出错时返回 nil 结果。
func TopoSort(features []Feature) ([]string, error) {
	index := make(map[string]*topoNode, len(features))
	nodes := make([]*topoNode, 0, len(features))

	// 第一遍：建索引并检测重名。
	for i := range features {
		f := &features[i]
		if _, exists := index[f.Name]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateFeature, f.Name)
		}
		n := &topoNode{name: f.Name, order: f.Order, deps: f.Deps}
		index[f.Name] = n
		nodes = append(nodes, n)
	}

	// 第二遍：建立依赖边并统计入度。
	// 同一功能重复声明同一依赖时去重，使入度等于「不同依赖」的数量。
	// 自环在此也建立边（dep == 自身），留到环检测阶段统一报告。
	for _, n := range nodes {
		seen := make(map[string]struct{}, len(n.deps))
		for _, dep := range n.deps {
			if _, dup := seen[dep]; dup {
				continue
			}
			seen[dep] = struct{}{}
			parent, ok := index[dep]
			if !ok {
				return nil, fmt.Errorf("%w: %q -> %q", ErrMissingDependency, n.name, dep)
			}
			parent.dependents = append(parent.dependents, n.name)
			n.indegree++
		}
	}

	// 第三遍：分层 Kahn 排序。
	out := make([]string, 0, len(nodes))
	layer := make([]*topoNode, 0, len(nodes))
	for _, n := range nodes {
		if n.indegree == 0 {
			layer = append(layer, n)
		}
	}
	for len(layer) > 0 {
		sort.Slice(layer, func(i, j int) bool {
			if layer[i].order != layer[j].order {
				return layer[i].order < layer[j].order
			}
			return layer[i].name < layer[j].name
		})
		next := make([]*topoNode, 0)
		for _, n := range layer {
			out = append(out, n.name)
			for _, childName := range n.dependents {
				child := index[childName]
				child.indegree--
				if child.indegree == 0 {
					next = append(next, child)
				}
			}
		}
		layer = next
	}

	if len(out) != len(nodes) {
		path := findCyclePath(index, nodes)
		if len(path) == 0 {
			// 理论上不可达：未输出的节点必然构成环。
			return nil, fmt.Errorf("%w: cycle not located", ErrCycle)
		}
		return nil, fmt.Errorf("%w: %s", ErrCycle, strings.Join(path, " -> "))
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// findCyclePath 在「未输出」节点构成的子图中做 DFS，
// 返回一条形如 a -> b -> c -> a 的环路径用于诊断。
// 未输出节点的入度仍大于 0，说明其至少一个依赖也未输出，
// 因此沿依赖方向必然能找到环。
func findCyclePath(index map[string]*topoNode, nodes []*topoNode) []string {
	names := make([]string, 0)
	for _, n := range nodes {
		if n.indegree > 0 {
			names = append(names, n.name)
		}
	}
	sort.Strings(names)

	// 只保留仍然未输出的依赖，已加载的节点不可能参与环。
	remaining := make(map[string][]*topoNode, len(names))
	for _, name := range names {
		n := index[name]
		deps := make([]*topoNode, 0, len(n.deps))
		seen := make(map[string]struct{}, len(n.deps))
		for _, dep := range n.deps {
			if _, dup := seen[dep]; dup {
				continue
			}
			seen[dep] = struct{}{}
			if parent, ok := index[dep]; ok && parent.indegree > 0 {
				deps = append(deps, parent)
			}
		}
		remaining[name] = deps
	}

	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(names))
	var stack []string

	var visit func(string) []string
	visit = func(name string) []string {
		state[name] = visiting
		stack = append(stack, name)
		for _, dep := range remaining[name] {
			switch state[dep.name] {
			case unvisited:
				if p := visit(dep.name); p != nil {
					return p
				}
			case visiting:
				for i, s := range stack {
					if s == dep.name {
						p := make([]string, 0, len(stack)-i+1)
						p = append(p, stack[i:]...)
						return append(p, dep.name)
					}
				}
			}
		}
		state[name] = done
		stack = stack[:len(stack)-1]
		return nil
	}

	for _, name := range names {
		if state[name] == unvisited {
			if p := visit(name); p != nil {
				return p
			}
		}
	}
	return nil
}
