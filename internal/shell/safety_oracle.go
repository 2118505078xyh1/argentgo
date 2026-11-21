package shell

import "sync/atomic"

// 本文件提供进程级的安全咨询谓词注入点。背景：内建 shell 早已在
// Options.Safety 上支持"调用方注入谓词"，但生产接线从不设置它，导致
// 该防御在生产中形同虚设。shell 不能 import security（分层规则），因此
// 这里提供一个**包级注入点**：由上层（app/security）在启动时把真正的
// 安全判定函数注入进来，shell 自身只负责读取和调用，不 import 任何
// 安全策略包。
//
// 默认（未注入）时所有读取都得到 nil，CurrentSafety 一律放行——既有运行
// 语义逐字节保持不变。注入是纯增量行为，只有显式调用 SetSafetyOracle
// 才会改变判定结果。

// safetyOracle 存放当前注入的安全咨询谓词。
//
// atomic.Value 要求所有 Store 的具体类型完全一致，且不能存 nil 接口值。
// 因此这里始终保存 func(string) bool 类型的值：注入时存真实函数，
// 清除时存一个"类型化的 nil 函数"（concrete type 同为 func(string) bool，
// 但接口值非 nil，不会触发 atomic.Value 的 store-nil panic）。
// 读取方 safetyCheck 断言回 func(string) bool，类型化 nil 自然还原为 nil。
var safetyOracle atomic.Value

func init() {
	// 用一个类型化 nil 完成初始化，确立 atomic.Value 的具体类型，
	// 这样后续 Store(nil 函数) 与 Store(真实函数) 都满足类型一致性。
	var nilFn func(string) bool
	safetyOracle.Store(nilFn)
}

// SetSafetyOracle 注入或清除进程级安全咨询谓词。
//
//   - fn != nil：注入该函数，后续 CurrentSafety 在无显式谓词时会调用它；
//   - fn == nil：清除注入，恢复"默认放行"行为。
//
// 并发安全：可被多个 goroutine 同时调用，与 safetyCheck/CurrentSafety
// 并发读写也不会 panic 或产生数据竞争。注入的函数本身应是纯函数（至少不得
// 修改传入的 command），因为调用方可能在任意 goroutine 中调用它。
func SetSafetyOracle(fn func(string) bool) {
	if fn == nil {
		var nilFn func(string) bool
		safetyOracle.Store(nilFn)
		return
	}
	safetyOracle.Store(fn)
}

// safetyCheck 读取当前注入的安全咨询谓词；未注入或已清除时返回 nil。
// 返回的函数由调用方直接调用，本函数不复制、不修改它。
func safetyCheck() func(string) bool {
	v := safetyOracle.Load()
	if v == nil {
		return nil
	}
	fn, _ := v.(func(string) bool)
	return fn
}

// CurrentSafety 给出 command 的最终安全判定。
//
// 优先级：显式传入的 fn 高于包级 oracle。
//   - fn != nil：直接调用 fn(command)，忽略包级 oracle；
//   - fn == nil 且 oracle 未注入：返回 true（放行，默认行为不变）；
//   - fn == nil 且 oracle 已注入：返回 oracle(command)。
//
// 约定：本函数不修改 command（Go 字符串不可变），也不修改 fn/oracle；
// 它原样把 command 交给谓词，UTF-8/CJK 文本按字节透传，绝不在 rune 中间
// 截断。oracle 返回 false 即代表拒绝，CurrentSafety 原样返回该 false。
func CurrentSafety(fn func(string) bool, command string) bool {
	if fn == nil {
		fn = safetyCheck()
	}
	if fn == nil {
		return true
	}
	return fn(command)
}
