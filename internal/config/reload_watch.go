package config

// PollReload 是长生命周期调用方的热重载轮询原语：它
// 检查磁盘上的配置是否与 cfg 加载自（或上次重载进）的快照
// 不同，如果不同，则用相同的 cwd、env 和 flag 覆盖
// 重建配置。
//
// 它有意保持纯粹：没有定时器、没有后台 goroutine，除了
// Changed/Reload 已经执行的 I/O（文件 stat 和重新读取配置
// 文件）之外没有其他 I/O。调度由调用方负责——例如长生命周期引擎可以
// 在每个轮次边界调用一次。
//
// 返回约定：
//
//   - 无变化：(cfg, false, nil)。当前快照仍然有效；
//     调用方继续使用它，没有发生任何重载。
//   - 有变化且重载成功：(next, true, nil)。next 是新快照，
//     其 Generation 为 cfg.Generation()+1；把它采纳为新的当前配置。
//   - 有变化但重载失败：(cfg, false, err)。cfg 保持不变，仍是
//     最后一个已知的良好快照；err 描述重载失败原因。
//
// 由于成功的 Reload 会捕获新的指纹，对返回快照的重复轮询
// 是幂等的：一旦采纳 next，PollReload(next) 就会
// 报告无变化，直到磁盘再次改变。
//
// PollReload 只读取 cfg（Changed 和 Reload 都是值接收者，且
// 重载元数据在加载后不可变），因此同一个 *Config 可以被多个
// goroutine 并发轮询。共享可变"当前配置"字段的调用方
// 仍需串行化由谁来采纳返回的快照。
//
// nil Config 或手工构造的（非经 Load 的）Config 没有可比较的
// 指纹，会被原样返回且 changed=false。
func PollReload(cfg *Config) (*Config, bool, error) {
	if cfg == nil {
		return cfg, false, nil
	}
	if !cfg.Changed() {
		return cfg, false, nil
	}
	next, err := cfg.Reload()
	if err != nil {
		// 失败时保留最后一个已知的良好快照；是否采纳是
		// 调用方的决定，重建失败不得替换 cfg。
		return cfg, false, err
	}
	return next, true, nil
}
