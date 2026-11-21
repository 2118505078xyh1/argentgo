package hooks

// HookDispatchState 描述钩子分发通道在执行与观察维度上的状态。
//
// 审计执行/观察路径时，仅用"是否空闲"不足以表达背压语义：调用方需要区分
// 通道仍在忙、正在终止（不再接收新工作）与已经关闭。HookDispatchState 提供
// 这四种互斥状态，便于据此实施显式背压，而不是把所有非空闲状态混为一谈。
//
// 其取值是稳定、与语言（含 CJK）无关的字符串，适合直接写入遥测或审计下游做
// 聚合；字面值不是对外的文本承诺，调用方不应解析其拼写。
type HookDispatchState string

const (
	// HookDispatchIdle 表示没有在途钩子，且通道既未关闭也未终止。
	HookDispatchIdle HookDispatchState = "idle"
	// HookDispatchBusy 表示仍有在途钩子执行，但通道尚未进入终止或关闭。
	HookDispatchBusy HookDispatchState = "busy"
	// HookDispatchTerminating 表示通道正在终止：不再接收新工作，尽管可能还有在途钩子。
	HookDispatchTerminating HookDispatchState = "terminating"
	// HookDispatchClosed 表示通道已关闭，不再接收任何工作。
	HookDispatchClosed HookDispatchState = "closed"
)

// String 返回状态的稳定字符串表示，约定与 Event.String 一致。
func (s HookDispatchState) String() string { return string(s) }

// classifyDispatchState 把三个相互独立的布尔信号归一到唯一的分发状态。
//
// 它是纯函数：不读写全局状态、不产生 I/O，对同一输入始终返回相同结果，
// 且不检查任何文本，因此完全与 CJK 等语言因素无关。
//
// 优先级从高到低为 closed > terminating > busy > idle：
//   - closed 为 true 时直接返回 HookDispatchClosed，忽略其余信号；
//   - 否则 terminated 为 true 时返回 HookDispatchTerminating（即便仍有在途工作）；
//   - 否则 inFlight 为 true 时返回 HookDispatchBusy；
//   - 三者皆为 false 时返回 HookDispatchIdle。
//
// 该优先级保证"终态压过过程态"：一旦通道关闭或进入终止，外部观察不会
// 因为在途工作仍存在而误判为普通的忙状态。
func classifyDispatchState(inFlight, terminated, closed bool) HookDispatchState {
	switch {
	case closed:
		return HookDispatchClosed
	case terminated:
		return HookDispatchTerminating
	case inFlight:
		return HookDispatchBusy
	default:
		return HookDispatchIdle
	}
}

// ClassifyDispatchState 是 classifyDispatchState 的导出接缝，供本包以外的
// 调用方（例如审计或遥测层）opt-in 使用：默认没有任何分发路径调用它，
// 因此现有分发行为完全不变；只有调用方主动引用才会启用状态分类。
func ClassifyDispatchState(inFlight, terminated, closed bool) HookDispatchState {
	return classifyDispatchState(inFlight, terminated, closed)
}
