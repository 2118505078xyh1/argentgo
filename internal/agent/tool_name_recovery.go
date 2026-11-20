package agent

import (
	"strings"
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// toolNameRecovery 控制是否把工具名还原接线到派发路径，默认关闭。
// 与工具参数强转一样，这是进程级开关，由 app 层在启动时统一设置：
// 工具名重写会改变派发语义，因此绝不能悄悄引入既有运行。
var toolNameRecovery atomic.Bool

// SetToolNameRecovery 打开或关闭工具名还原。关闭（默认）时，模型写出的工具名
// 逐字节保持原样；打开后，只有能被唯一还原为本次运行公布的真实工具名时才改写。
func SetToolNameRecovery(enabled bool) {
	toolNameRecovery.Store(enabled)
}

// toolNameRecoveryEnabled 报告当前是否启用了工具名还原。
func toolNameRecoveryEnabled() bool {
	return toolNameRecovery.Load()
}

// recoverMangledToolName 把带有提供方/MCP 命名空间前缀的模型工具名，还原为
// available 中的真实工具名；找不到唯一匹配时原样返回 name。
//
// 背景：模型在多 provider/MCP 命名空间下会写出 developer.shell、
// developer__shell 之类并不存在的名字，整轮调用都会浪费在 "tool not found" 上。
// 这里按命名空间切分并规范化后做精确/后缀匹配，模拟 goose 依据 owner 元数据
// 还原工具名的行为；由于本函数拿不到 owner 元数据，遇到无法区分的候选时
// 保守放弃猜测。
//
// 规则（按优先级）：
//  1. 原名已是真实工具名 —— 逐字节不变；
//  2. 小写、去空白后的精确匹配；
//  3. 去掉命名空间前缀后的精确匹配，优先保留更多命名空间（更具体）；
//  4. 模型漏写命名空间时，在真实名字中寻找以分隔符 + 候选结尾的名字；
//  5. 任何阶段出现多个无法区分的候选，返回原名。
//
// 匹配只使用 Unicode 安全的小写/去空白与 ASCII 分隔符，因此对 CJK 安全；
// 所有遍历都沿用 available 的输入顺序，不依赖 map 迭代顺序，结果确定。
func recoverMangledToolName(name string, available []string) string {
	if name == "" || len(available) == 0 {
		return name
	}

	// 已经正确的调用必须保持逐字节不变，这是对模型最重要、最保守的保证。
	for _, a := range available {
		if a == name {
			return name
		}
	}

	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

	// 规范化名字到真实名字的映射。保留 available 的输入顺序以保证确定性。
	index := make(map[string][]string, len(available))
	for _, a := range available {
		key := norm(a)
		if key == "" {
			continue
		}
		index[key] = append(index[key], a)
	}

	// resolveExact 优先返回与规范化键逐字节相同的真实名字。
	// 若存在多个无法区分的候选，报告歧义，由调用方决定是否放弃。
	resolveExact := func(key string) (string, bool) {
		matches := index[key]
		for _, a := range matches {
			if a == key {
				return a, false
			}
		}
		switch len(matches) {
		case 0:
			return "", false
		case 1:
			return matches[0], false
		default:
			return "", true
		}
	}

	// 阶段 2：规范化后的精确匹配。
	normName := norm(name)
	if hit, ambiguous := resolveExact(normName); ambiguous {
		return name
	} else if hit != "" {
		return hit
	}

	// 阶段 3：按命名空间切分后逐段回退。点号与双下划线是命名空间分隔符；
	// 单下划线属于工具名本身（如 multi_edit），不参与切分，避免误伤。
	segments := splitToolNamespace(name)
	candidates := make([]string, 0, len(segments)+1)
	for i := 0; i < len(segments); i++ {
		cand := norm(strings.Join(segments[i:], "__"))
		if cand != "" && cand != normName {
			candidates = append(candidates, cand)
		}
	}
	for _, cand := range candidates {
		hit, ambiguous := resolveExact(cand)
		if ambiguous {
			return name
		}
		if hit != "" {
			return hit
		}
	}

	// 阶段 4：模型漏写命名空间。在真实名字中寻找以分隔符 + 候选结尾的名字；
	// 分隔符边界避免把 my_shell 误判成 shell。normName 也要参与这一阶段，
	// 因为阶段 3 刻意跳过了与自身相同的候选。
	namesToTry := make([]string, 0, len(candidates)+1)
	namesToTry = append(namesToTry, normName)
	namesToTry = append(namesToTry, candidates...)
	for _, cand := range namesToTry {
		if cand == "" {
			continue
		}
		if _, exists := index[cand]; exists {
			continue // 精确匹配已在前两个阶段处理
		}
		var hits []string
		for _, a := range available {
			na := norm(a)
			if na == cand {
				continue
			}
			if !strings.HasSuffix(na, "__"+cand) && !strings.HasSuffix(na, "."+cand) {
				continue
			}
			duplicate := false
			for _, h := range hits {
				if h == a {
					duplicate = true
					break
				}
			}
			if !duplicate {
				hits = append(hits, a)
			}
		}
		switch len(hits) {
		case 1:
			return hits[0]
		case 0:
			continue
		default:
			// 没有 owner 元数据可供区分，保守返回原名。
			return name
		}
	}

	return name
}

// splitToolNamespace 按点号与双下划线切分工具名，并去掉空白与空段。
func splitToolNamespace(name string) []string {
	raw := strings.Split(strings.ReplaceAll(name, "__", "."), ".")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// availableToolNames 返回本次运行公布给模型的真实工具名，作为还原时的候选集。
func (s *runState) availableToolNames() []string {
	names := make([]string, 0, len(s.specs))
	for i := range s.specs {
		if n := s.specs[i].Function.Name; n != "" {
			names = append(names, n)
		}
	}
	return names
}

// recoverCallNames 在启用还原时返回一份工具名已被修正的调用副本。
// 它必须在循环 goroutine 上、任何决策之前调用，使重复防护、审批、事件、
// 记录与实际执行看到的都是同一个名字。返回副本而不原地修改入参，
// 保证调用方持有的原切片不被改动。
func (s *runState) recoverCallNames(calls []llm.ToolCall) []llm.ToolCall {
	available := s.availableToolNames()
	if len(available) == 0 {
		return calls
	}
	out := make([]llm.ToolCall, len(calls))
	copy(out, calls)
	changed := false
	for i := range out {
		recovered := recoverMangledToolName(out[i].Function.Name, available)
		if recovered != out[i].Function.Name {
			out[i].Function.Name = recovered
			changed = true
		}
	}
	if !changed {
		return calls
	}
	return out
}
