package agent

import (
	"strings"
	"sync"
)

// 本文件是 ScoreContextHealth 的只读观测适配层：把调用方手里的消息样本投影成
// 健康度评分所需的 []string，并给出评分。它刻意不接入运行循环、不自动触发压缩
// 或裁剪，也不改变任何现有行为——只有显式调用本文件的函数时才会被计算。
//
// 默认没有日志钩子，适配过程无任何输出；日志是显式开启的旁路（见
// SetContextHealthLogger），仅用于 doctor / 诊断 / 测试观测。

// MessageSample 是一条用于上下文健康度评估的轻量消息样本。
// 它刻意不依赖 agent / llm 的具体消息类型，使本适配层保持无调用方、零依赖
// （除标准库）。Role 为角色（"user"/"assistant"/"tool" 等，大小写与首尾空白不敏感），
// Content 为文本内容。
type MessageSample struct {
	Role    string
	Content string
}

// ContextHealthRoleFilter 是可选的按角色过滤开关。
// 零值（IncludeRoles 与 ExcludeRoles 均为空）表示纳入全部样本。
// 角色名按大小写与首尾空白归一化后比较，与 MeasureFocus 同口径。
type ContextHealthRoleFilter struct {
	// IncludeRoles 非空时，仅保留 Role（归一化后）命中其中之一的样本。
	IncludeRoles []string
	// ExcludeRoles 非空时，剔除 Role（归一化后）命中其中之一的样本。
	// 同时命中包含与排除时，排除优先。
	ExcludeRoles []string
}

// ContextHealthFromMessages 把消息样本投影成 ScoreContextHealth 的输入并给出评分。
//
// 它是纯函数：按样本顺序只取每条的 Content 组成 []string 交给 ScoreContextHealth；
// 不修改 entries 或其字符串，不持有任何共享可变状态，相同输入总产生相同评分。
// 角色字段默认不参与筛选，仅作为可选过滤维度（见 ContextHealthFromMessagesWithFilter）。
//
// 若显式设置了日志钩子，会在返回前把评分发给钩子——这是默认关闭的旁路，
// 不影响返回值或确定性；钩子为 nil 时全程无任何输出。
func ContextHealthFromMessages(entries []MessageSample) ContextHealth {
	return ContextHealthFromMessagesWithFilter(entries, ContextHealthRoleFilter{})
}

// ContextHealthFromMessagesWithFilter 与上者相同，但支持按角色过滤参与评分的样本。
// 过滤后若没有任何样本，交给 ScoreContextHealth 的是空输入，依其约定返回满分 1
// （没有污染证据即视为健康）。
func ContextHealthFromMessagesWithFilter(entries []MessageSample, filter ContextHealthRoleFilter) ContextHealth {
	messages := projectMessageContents(entries, filter)
	h := ScoreContextHealth(messages)
	emitContextHealth(h)
	return h
}

// projectMessageContents 按 filter 筛选样本，并按原顺序抽取 Content。
// 返回新切片；entries 为空时返回 nil，绝不复用或改动入参底层数组。
func projectMessageContents(entries []MessageSample, filter ContextHealthRoleFilter) []string {
	if len(entries) == 0 {
		return nil
	}
	include := normalizedRoleSet(filter.IncludeRoles)
	exclude := normalizedRoleSet(filter.ExcludeRoles)
	messages := make([]string, 0, len(entries))
	for _, e := range entries {
		role := normalizeRole(e.Role)
		if len(include) > 0 {
			if _, ok := include[role]; !ok {
				continue
			}
		}
		if len(exclude) > 0 {
			if _, ok := exclude[role]; ok {
				continue
			}
		}
		messages = append(messages, e.Content)
	}
	return messages
}

// normalizeRole 把角色名折叠为比较用的键：忽略大小写与首尾空白。
func normalizeRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

// normalizedRoleSet 把角色名列表转成归一化集合；空列表返回 nil。
func normalizedRoleSet(roles []string) map[string]struct{} {
	if len(roles) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		set[normalizeRole(r)] = struct{}{}
	}
	return set
}

// 日志钩子默认 nil（关闭输出）。用读写锁保护，避免设置与触发之间的数据竞争。
var (
	contextHealthLoggerMu sync.RWMutex
	contextHealthLogger   func(ContextHealth)
)

// SetContextHealthLogger 设置上下文健康度日志钩子。传入 nil 关闭输出（默认即关闭）。
// 钩子只在显式调用 ContextHealthFromMessages[WithFilter] 且钩子非 nil 时被调用，
// 不接入运行循环；因此默认配置下不会有任何输出或行为变化。
func SetContextHealthLogger(fn func(ContextHealth)) {
	contextHealthLoggerMu.Lock()
	contextHealthLogger = fn
	contextHealthLoggerMu.Unlock()
}

// currentContextHealthLogger 返回当前钩子，供触发与测试读取。
func currentContextHealthLogger() func(ContextHealth) {
	contextHealthLoggerMu.RLock()
	defer contextHealthLoggerMu.RUnlock()
	return contextHealthLogger
}

// emitContextHealth 把评分发给当前钩子；钩子为 nil 时什么也不做。
func emitContextHealth(h ContextHealth) {
	if fn := currentContextHealthLogger(); fn != nil {
		fn(h)
	}
}
