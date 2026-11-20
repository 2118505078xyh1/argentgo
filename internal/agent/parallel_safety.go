package agent

import "sync/atomic"

// parallelDeclaredOnly 控制并发分类是否切换到“仅白名单”模式，默认关闭。
// 关闭时沿用既有 Mutating() 代理判定，所有运行语义逐字节保持不变；
// 打开后，除既有的变更型/委派/重复防护否决外，还要求每个工具名都被显式声明为并发安全。
// 这是进程级开关，由 app 层在启动时统一设置，避免把语义变更悄悄引入既有运行。
var parallelDeclaredOnly atomic.Bool

// parallelSafeDeclared 存放已声明并发安全的工具名白名单。
// 其中保存的 map 一旦构建便只读，通过 atomic.Value 整体发布，
// 使并发派发的 worker goroutine 可以安全读取而无需加锁。
var parallelSafeDeclared atomic.Value

func init() {
	parallelSafeDeclared.Store(map[string]bool{})
}

// SetParallelDeclaredOnly 打开或关闭“仅白名单”并发分类模式。
// 关闭（默认）时完全走既有的 Mutating() 判定；打开后只会让批次更容易退化为串行，
// 绝不会让原本串行的批次变成并行。
func SetParallelDeclaredOnly(enabled bool) {
	parallelDeclaredOnly.Store(enabled)
}

// parallelDeclaredOnlyEnabled 报告当前是否启用了“仅白名单”并发分类。
func parallelDeclaredOnlyEnabled() bool {
	return parallelDeclaredOnly.Load()
}

// SetParallelSafeTools 设置允许并行的工具名白名单。
// nil 或空切片表示没有任何工具被声明安全（在该模式下所有批次都会串行）。
// 每次调用都构建全新的 map 并整体替换，已发布的 map 不会被就地修改。
func SetParallelSafeTools(names []string) {
	safe := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			safe[name] = true
		}
	}
	parallelSafeDeclared.Store(safe)
}

// declaredParallelSafe 返回当前的并发安全白名单（调用方不得修改）。
func declaredParallelSafe() map[string]bool {
	v := parallelSafeDeclared.Load()
	if v == nil {
		return nil
	}
	safe, _ := v.(map[string]bool)
	return safe
}

// canRunParallel 报告 toolName 是否被显式声明为可安全并发执行。
//
// 这是不读取任何全局状态的纯函数，只依据传入的 declaredSafe 判定：
//   - declaredSafe[toolName] 明确为 true 时返回 true；
//   - 未声明（键不存在）、显式声明为 false、空工具名或 nil 白名单一律返回 false。
//
// 判定只做精确的字符串键查找，不进行大小写折叠或字节切片，
// 因此对包含 CJK 字符的工具名同样安全（不会在 UTF-8 字符中间切开）。
func canRunParallel(toolName string, declaredSafe map[string]bool) bool {
	if toolName == "" || declaredSafe == nil {
		return false
	}
	return declaredSafe[toolName]
}
