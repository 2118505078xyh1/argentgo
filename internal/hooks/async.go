package hooks

import "context"

// planAsync 选择标记为 Async 的钩子进行后台执行。它从与 planRun 相同的
// claimedCommands walk 中抽取，因此一个命令在整个事件中只被认领一次，
// 停止边界在两条路径中都保护后面的钩子，
// 且后台集合与同步集合互不相交。异步钩子永不
// 进入 planRun 的层级，因此返回的决策不受异步工作影响。
func planAsync(commands []Command) []Command {
	claimed := claimedCommands(commands)
	async := make([]Command, 0, len(claimed))
	for _, command := range claimed {
		if command.Async {
			async = append(async, command)
		}
	}
	return async
}

// launchAsync 启动每个后台钩子并立即返回而不等待它。
//
// 生成的 context 通过
// 生成的 context 通过 WithoutCancel 与调用方的取消分离：
// 异步钩子是即发即忘的工作，应存活于触发它的请求之外，即使该请求的
// context 在 Run 返回后立刻被取消。值（会话 ID、调用 ID 等）被保留。
// 钩子自身的超时仍然生效，因此挂钟时间保持有界。
//
// panic 隔离、遥测和 runner 都来自 guardedRunCommand，
// 因此后台钩子的被观察方式与同步钩子完全一致。goroutine 中最后的
// 因此后台钩子的被观察方式与同步钩子完全一致。goroutine 中最后的
// recover 防止任何意外故障拖垮进程；Done 最后运行，使 WaitAsync 总能排空。
func (d *Dispatcher) launchAsync(ctx context.Context, async []Command, payload Payload) {
	for _, command := range async {
		d.asyncWG.Add(1)
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.timeoutFor(command))
		go func(command Command, runCtx context.Context, cancel context.CancelFunc) {
			defer d.asyncWG.Done()
			defer cancel()
			defer func() { _ = recover() }()
			_ = d.guardedRunCommand(runCtx, command, payload)
		}(command, runCtx, cancel)
	}
}

// WaitAsync 阻塞，直到 Run 在此 dispatcher 上启动的每个异步钩子
// 都已完成。Run 自身从不调用它，因此异步工作不会延迟
// 决策。仅在 Run 返回后、不会再添加钩子时调用它；测试与优雅关闭路径用它排空后台工作。
// 添加；测试与优雅关闭路径用它排空后台工作。
func (d *Dispatcher) WaitAsync() {
	d.asyncWG.Wait()
}
