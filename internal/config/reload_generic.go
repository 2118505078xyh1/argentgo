package config

// Snapshot 是泛型化的快照值：Value 是快照内容，Fingerprint 标识内容身份，
// Generation 是单调递增的版本号。
//
// 它是纯数据（struct value），不含 I/O、定时器或 goroutine；由调用方自己
// 构造、保存与传递。Fingerprint 约定为由内容推导（例如内容哈希），因此
// 内容一变指纹就变；空 Fingerprint 保留给"无新内容"的哨兵语义（见
// PollSnapshot）。Generation 由调用方在每次采纳新快照时加一。
type Snapshot[T any] struct {
	Value       T
	Fingerprint string
	Generation  int
}

// PollSnapshot 是 PollReload 的泛型对应物：一次"带校验的快照交换"步骤。
// 它把 PollReload 的纯函数原语（检测变化 → 加载 → 校验 → 交换）从具体的
// *Config 解放出来，可复用于任意可用指纹标识、可用 health 校验的快照类型。
//
// load 的契约（调用方用闭包实现，并在闭包内持有 last-good 状态）：
//
//   - 读取失败：返回 (lastGood, err)，lastGood 是上一次成功快照；
//   - 源内容未变化：返回指纹为空的快照（Snapshot[T]{} 即可），表示
//     没有新内容可供交换；
//   - 源内容已变化：返回候选快照，其 Fingerprint 标识新内容，
//     Generation 为 lastGood.Generation + 1。
//
// health 为 nil 时跳过校验；非 nil 且对候选值返回错误时视为拒绝该候选。
// health 只在有候选（指纹非空）时被调用，无变化或读取失败时不会运行。
//
// 返回约定：
//
//   - load 失败：(lastGood, false, err)。保留最后一次成功快照，err 描述
//     失败原因（与 PollReload 的失败分支一致）。
//   - 无变化：(-, false, nil)。changed=false，调用方继续持有当前快照。
//   - health 拒绝：(-, false, err)。changed=false，候选不会被采纳，
//     调用方因此保留 last-good；返回的 err 即 health 的错误。
//   - 成功交换：(候选, true, nil)。调用方应把返回的快照采纳为新的当前值。
//
// 本函数是纯函数：不读写磁盘、不访问全局状态、不启动 goroutine。因此对同一
// load 的并发调用本身是安全的（load 闭包若持有共享状态，需由闭包自行保证
// 并发安全；这与 PollReload 允许并发轮询同一个 *Config 的语义一致）。
func PollSnapshot[T any](load func() (Snapshot[T], error), health func(T) error) (cur Snapshot[T], changed bool, err error) {
	next, err := load()
	if err != nil {
		// 失败时保留 last-good；load 在失败路径上负责返回它，
		// 重建失败绝不能替换掉已知良好的快照。
		return next, false, err
	}
	// 空指纹表示源没有产生新快照（无变化）或是一个零值快照，
	// 两种情况都不触发交换，也不必运行 health。
	if next.Fingerprint == "" {
		return next, false, nil
	}
	if health != nil {
		if herr := health(next.Value); herr != nil {
			// health 拒绝：changed=false，候选不被采纳，
			// 调用方据此保留 last-good。
			return next, false, herr
		}
	}
	return next, true, nil
}
