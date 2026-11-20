package app

import (
	"fmt"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// configReloader 是长生命周期引擎的热重载钩子。它保存当前的配置快照，
// 并在轮次边界通过 config.PollReload 轮询，当磁盘配置变化时原子地替换快照。
//
// 它刻意只做观察：采用新快照会记录最新配置（以及递增的 generation）并记下变更日志，
// 但从不在运行中途重新解析 Resolved 或重新接线 LLM 客户端。按设计，一次运行的 Resolved
// 在其生命周期内不可变，因此应用新配置意味着启动一次新运行。把该钩子保持为只读，
// 意味着安装它不会改变执行语义。
//
// 一次性执行从不构建它（见 runner.Prompt 中仅限长生命周期的守卫），
// 因此进程退出前不会做无意义的 stat/重载工作。
//
// 所有状态都由互斥锁保护：快照指针在锁下交换，
// 因此并发观察者看到的要么是完整的旧快照，要么是新快照，绝不会是半更新的值。
type configReloader struct {
	mu      sync.Mutex
	current *config.Config
	logf    func(string)
}

// newConfigReloader 返回一个以 cfg 为初始值的重载器。logf 可以为 nil，此时会静默变更/失败通知；
// 生产环境传入一个由进程诊断流支撑的写入器。
func newConfigReloader(cfg *config.Config, logf func(string)) *configReloader {
	return &configReloader{current: cfg, logf: logf}
}

// poll 把磁盘配置与当前快照比对，当 config.PollReload 报告重载成功时采用新快照。
// 重载失败会保留最后一个已知良好的快照，但仍记录错误，
// 使操作人员的错误编辑可见，而不是被静默忽略。布尔返回值报告是否采用了新快照；
// 它仅是提示性的，不驱动任何执行变化。
func (r *configReloader) poll() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	cur := r.current
	next, changed, err := config.PollReload(cur)
	if err != nil {
		if r.logf != nil {
			r.logf(fmt.Sprintf("reload failed (%s); keeping last known-good snapshot: %v", configLabel(cur), err))
		}
		return false
	}
	if !changed || next == cur {
		return false
	}
	r.current = next
	if r.logf != nil {
		r.logf(fmt.Sprintf("reloaded: %s -> %s", configLabel(cur), configLabel(next)))
	}
	return true
}

// snapshot 返回当前采用的快照。配置快照一经加载即不可变，调用方不得修改它。
func (r *configReloader) snapshot() *config.Config {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

func configLabel(cfg *config.Config) string {
	if cfg == nil {
		return "no config"
	}
	return fmt.Sprintf("generation %d, model %q", cfg.Generation(), cfg.Model)
}
