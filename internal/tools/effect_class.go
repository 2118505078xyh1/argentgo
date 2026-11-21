package tools

import "strings"

// EffectClass 描述一次工具调用对外部世界的影响类别。
// 它比 Tool.Mutating() 的二值判断更精细，供推测执行等需要在运行前
// 判断"效果是否安全"的调用方使用。
//
// 本文件是一组纯函数、零调用方的分类原语：它不改变任何现有工具的
// Specs()/Run() 行为，只提供统一且保守的分级标准。
type EffectClass string

const (
	// EffectLocalRead 表示只读取本地工作区、无外部依赖的纯读。
	// 这是唯一允许推测执行的类别。
	EffectLocalRead EffectClass = "local-read"
	// EffectExternalRead 表示读取外部数据源，或具备时间、记忆等
	// 非确定性依赖；它不写状态，但结果无法在离线环境中重现。
	EffectExternalRead EffectClass = "external-read"
	// EffectIdempotentWrite 表示重复执行效果相同的写入。
	// 它是分类体系中的一类，当前白名单不产出它，保留给调用方使用。
	EffectIdempotentWrite EffectClass = "idempotent-write"
	// EffectSideEffect 表示会改变状态且效果不可忽略的写入。
	EffectSideEffect EffectClass = "side-effect"
	// EffectForbidden 表示效果不可预测或不可回滚，绝不参与推测执行。
	EffectForbidden EffectClass = "forbidden"
)

// localReadNames 是本地纯读工具白名单。
// 只有这些名字可判为 EffectLocalRead，清单与声明为 shared 的本地
// 只读工具一致。匹配是精确且大小写敏感的：刻意不做 trim 或小写化，
// 否则形如 "read " 的冒名工具会混入可信白名单而被推测执行。
var localReadNames = map[string]bool{
	"read":       true,
	"ls":         true,
	"glob":       true,
	"grep":       true,
	"find":       true,
	"git_status": true,
	"repo_map":   true,
	"diff":       true,
}

// externalReadNames 是外部读工具白名单。
var externalReadNames = map[string]bool{
	"web_fetch":    true,
	"web_search":   true,
	"current_time": true,
}

// forbiddenNames 是危险工具黑名单。
// 任意命令执行的效果既不可预测也不可回滚，必须优先于 mutating 标志。
var forbiddenNames = map[string]bool{
	"bash":       true,
	"powershell": true,
	"shell":      true,
	"exec":       true,
}

// ClassifyEffect 按 name 与 mutating 把工具分级为 EffectClass。
//
// 判定次序（前者优先）：
//  1. 命中黑名单 → forbidden；
//  2. 命中本地纯读白名单且非变更 → local-read；
//  3. 命中外部读白名单或 memory_ 前缀且非变更 → external-read；
//  4. mutating 为真 → side-effect；
//  5. 其余（未知名且非变更）→ 保守当作 external-read。
//
// 未知名永远得不到 local-read，因此不会误入推测执行。
func ClassifyEffect(name string, mutating bool) EffectClass {
	if forbiddenNames[name] {
		return EffectForbidden
	}
	if !mutating {
		if localReadNames[name] {
			return EffectLocalRead
		}
		if externalReadNames[name] || strings.HasPrefix(name, "memory_") {
			return EffectExternalRead
		}
		// 未知名保守处理：无法证明是本地纯读，就当作外部读。
		return EffectExternalRead
	}
	// 变更性写入一律视为有副作用（黑名单已在前面拦截）。
	return EffectSideEffect
}

// SpeculativeAllowed 报告某个类别是否允许推测执行。
// 只有 local-read 返回 true；其余类别及任何未知取值一律 false（fail-closed）。
func SpeculativeAllowed(c EffectClass) bool {
	return c == EffectLocalRead
}
