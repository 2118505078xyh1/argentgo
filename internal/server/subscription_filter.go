package server

// 本文件提供订阅过滤的纯函数，对应“事件按 agent/scope 过滤”的设计蓝图。
//
// Scope 由 agent/scope.go 提供，是一棵带父子关系、负责资源生命周期的作用域树；
// 这里不直接引用 agent.Scope（它强调 Track/Dispose 与 LIFO 释放，没有数值标识），
// 而是用一个 uint64 的 ScopeID 表示“事件归属的作用域”。调用方在把事件投递给
// 订阅者之前，用 Matches / FilterSubscriptions 做一次轻量谓词判断即可。
//
// 这里刻意只交付纯函数：不接 EventHub、不读全局状态、没有默认调用方，
// 行为完全不变。接线点见文件末尾注释。要启用时由调用方（例如 EventHub 的
// Publish，或 RPC 的 event/subscribe）在投递前调用本文件的函数。

// Subscription 描述一个订阅者对事件的过滤条件。
//
// ID           订阅者标识，仅用于区分不同订阅者，不参与匹配判断。
// Kinds        关心的事件 kind 列表；为空（nil 或空切片）表示匹配全部 kind。
//
//	每个元素与事件的 kind 字符串精确比较，区分大小写，支持 CJK。
//
// ScopeID      关心的作用域标识；为 0 表示匹配全部 scope（零值兼容：只关心
//
//	kind 的订阅者无须显式设置该字段）。非 0 时要求事件的 scopeID
//	与之一致。
//
// TerminalOnly 为 true 时只匹配终态事件（terminal 为 true）；为 false 时
//
//	终态与非终态事件都匹配。
type Subscription struct {
	ID           string
	Kinds        []string
	ScopeID      uint64
	TerminalOnly bool
}

// Matches 判断一个事件（由 kind / scopeID / terminal 三元组描述）是否命中订阅
// s。三个维度取逻辑与：
//
//  1. kind：s.Kinds 为空表示通配；否则要求 kind 精确等于其中某个元素；
//  2. scope：s.ScopeID == 0 表示通配；否则要求 scopeID 精确相等；
//  3. terminal：s.TerminalOnly 为真时要求 terminal 为真，否则不限制。
//
// 纯函数：不修改 s，也不持有任何状态。
func Matches(s Subscription, kind string, scopeID uint64, terminal bool) bool {
	if len(s.Kinds) > 0 && !containsKind(s.Kinds, kind) {
		return false
	}
	if s.ScopeID != 0 && s.ScopeID != scopeID {
		return false
	}
	if s.TerminalOnly && !terminal {
		return false
	}
	return true
}

// containsKind 报告 kinds 是否包含与 kind 完全相同的字符串。
// 提取成小函数是为了让 Matches 的三个维度一目了然，也便于单独验证 CJK 匹配。
func containsKind(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// FilterSubscriptions 返回 subs 中命中本次事件的订阅者，保持原有相对顺序。
//
// 性质：
//   - 稳定顺序：输出顺序与 subs 一致，不排序、不去重；
//   - 不改入参：在新建的切片上 append，传入切片的元素与底层数组都不受影响；
//     注意输出元素是按值复制的 Subscription，其 Kinds 字段与入参共享底层数组，
//     但本函数从不修改它；
//   - 空结果：返回非 nil 的空切片，调用方可无分支地遍历；
//   - 完全一致：是否命中完全由 Matches 决定。
func FilterSubscriptions(subs []Subscription, kind string, scopeID uint64, terminal bool) []Subscription {
	out := make([]Subscription, 0, len(subs))
	for _, s := range subs {
		if Matches(s, kind, scopeID, terminal) {
			out = append(out, s)
		}
	}
	return out
}

// 接线点（未实施，默认关闭）：
//
// 本文件不挂钩到任何现有入口，HTTP / SSE / RPC / ACP 的事件投递保持逐字节不变。
// 若将来要按 agent/scope 过滤事件，建议：
//
//  1. 在订阅时把 kind 列表、作用域 ID 与 terminalOnly 开关记入 Subscription；
//  2. EventHub.Publish 在向每个订阅者通道写入前，先用 Matches 判断该订阅是否
//     关心本次事件的 (kind, scopeID, terminal) 三元组；
//  3. 对需要一次性计算投递集合的调用方，可用 FilterSubscriptions 先筛出列表，
//     再按返回的稳定顺序逐个投递；
//  4. 作用域 ID 由 agent.Scope 的持有方在发布事件时填入，ScopeID==0 的订阅保持
//     兼容旧调用方——它们订阅全部作用域。
//
// 在没有明确的调用方与作用域编号来源之前不实施上述步骤，避免为一种尚无生产者
// 的上下文改动已冻结的事件投递与线协议。
