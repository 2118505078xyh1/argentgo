package agent

// shouldStopTurn 把散布在模型/工具循环各处的停止判断收敛为一次纯决策。
// 它不读取外部状态、不访问时钟、不构造事件，也不修改任何输入；给定相同的参数，
// 总是返回相同的结果。调用方拿到决定后自行负责事件投递与记录收尾等副作用。
//
// 四个输入信号按固定的优先级参与判定（从高到低）：
//
//	取消(ctxCanceled) > 预算突破(budgetBreach) > 轮次上限(turns/maxTurns) > 既有原因(stopReason)
//
// 高优先级信号会覆盖低优先级信号所选出的原因。各原因字符串要么是既有的 Stop* 常量，
// 要么是调用方原样传入的 stopReason；函数本身从不解析、比较或改写字符串内容，
// 因此对 CJK 等非 ASCII 原因同样逐字节透传（与地区、语言无关）。
//
// maxTurns <= 0 表示不限轮次。stopReason 为空表示当前没有既定的停止原因。
// 当没有任何信号触发时返回 (false, "")；只要判定为停止，返回的原因必为非空。
//
// 预算突破的具体原因（例如 StopMaxCost、StopMaxTotalTokens）由调用方通过 stopReason
// 传入，使统一的决策仍能保留原有的细分原因；仅在调用方未给出原因时才回退到累计
// token 上限这一最通用的预算原因，以免产生空的停止原因。
func shouldStopTurn(turns, maxTurns int, budgetBreach bool, ctxCanceled bool, stopReason string) (bool, string) {
	// 取消拥有最高优先级：一旦上下文被取消，无论其他条件如何都必须立即停止。
	if ctxCanceled {
		return true, StopCancelled
	}
	// 预算突破优先于轮次上限与既有原因。具体原因由调用方通过 stopReason 提供，原样透传。
	if budgetBreach {
		if stopReason != "" {
			return true, stopReason
		}
		// 调用方漏传预算原因时的兜底：累计 token 上限是最通用的预算维度，
		// 它保证"判定停止 => 原因非空"这一不变式成立。
		return true, StopMaxTotalTokens
	}
	// 轮次上限优先于既有原因；maxTurns 非正表示不限制。
	if maxTurns > 0 && turns >= maxTurns {
		return true, StopMaxTurns
	}
	// 既有原因（错误反射、压缩后收尾等已决定的停止）作为最低优先级的兜底。
	if stopReason != "" {
		return true, stopReason
	}
	return false, ""
}
