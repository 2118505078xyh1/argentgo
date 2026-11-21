package server

// TurnStateEmit 是事件观测回调的签名，参数与 Reduce / Store.Apply 完全一致：
// kind 为规范化后的事件种类，turn 仅用于错误详情，startedMS 为事件起点的
// Unix 毫秒（0 表示无计时），seq 为事件源分配的单调序号。
//
// 该类型只使用标量与字符串，刻意不依赖 agent.Event，使状态投影保持可单独
// 注入；事件源负责把自身的事件结构翻译成这四个参数。
type TurnStateEmit func(kind string, turn int, startedMS, seq uint64)

// TurnStateObserverSink 是事件源可实现的可选能力：在既有的事件投递之外，
// 额外接受一个观测回调，把事件投影到 TurnState Store。
//
// 接线方用类型断言探测该接口（与 selectHub 探测可选 EventHub() 能力同一
// 范式），未实现的事件源行为逐字节不变。EventHub 本身不实现它，其字段与
// Publish 签名均不改动——观测完全是 opt-in 的。
type TurnStateObserverSink interface {
	SetTurnStateObserver(TurnStateEmit)
}

// NewTurnStateObserver 返回一个纯观测回调：把事件源投递的四元组折叠进 store。
//
// 回调内部只调用 Store.Apply，不发布事件、不写任何输出，因此对事件序列没有
// 任何影响；未注册回调时（默认零调用方）事件序列与今日完全一致。观测绝不应
// 中断事件流，所以 Apply 返回的 ErrIllegalTransition 被就地吞掉；需要检查
// 错误的调用方应直接使用 Store.Apply。store 为 nil 时返回空回调，不会 panic；
// 零值 Store 可直接使用。
func NewTurnStateObserver(store *Store) TurnStateEmit {
	if store == nil {
		return func(string, int, uint64, uint64) {}
	}
	return func(kind string, turn int, startedMS, seq uint64) {
		_, _ = store.Apply(kind, turn, startedMS, seq)
	}
}

// AttachTurnStateObserver 把由 store 构造的观测回调注入事件源。
// sink 实现 TurnStateObserverSink 且 store 非 nil 时返回 true；否则返回 false，
// 不产生任何副作用。它只做类型断言，不改变 sink 的动态类型或任何现有字段，
// 因此不满足接口的事件源与未注入时的字节序列完全一致。
func AttachTurnStateObserver(sink any, store *Store) bool {
	if store == nil {
		return false
	}
	typed, ok := sink.(TurnStateObserverSink)
	if !ok {
		return false
	}
	typed.SetTurnStateObserver(NewTurnStateObserver(store))
	return true
}
