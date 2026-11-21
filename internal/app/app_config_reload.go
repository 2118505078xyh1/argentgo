package app

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
		r.logf(reloadLogLine(cur, next))
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

// reloadLogLine 生成采纳新快照后的单行日志。默认路径返回原有的
// "reloaded: ... -> ..." 行，行为逐字保持不变；仅当 ARGENT_CONFIG_RELOAD_DIFF=1
// 时改用基于键路径差异的中文摘要。
func reloadLogLine(cur, next *config.Config) string {
	baseline := fmt.Sprintf("reloaded: %s -> %s", configLabel(cur), configLabel(next))
	if !configReloadDiffEnabled() {
		return baseline
	}
	if summary := formatConfigReloadDiff(cur, next); summary != "" {
		return summary
	}
	return baseline
}

// configReloadDiffEnabled 报告是否开启热重载的变更摘要。默认关闭，
// 仅当 ARGENT_CONFIG_RELOAD_DIFF=1 时开启；在调用时读取，便于测试动态调整。
func configReloadDiffEnabled() bool {
	return os.Getenv("ARGENT_CONFIG_RELOAD_DIFF") == "1"
}

// reloadDiffRedacted 是敏感键在摘要中的固定占位值，与 internal/config 的脱敏占位一致。
const reloadDiffRedacted = "<redacted>"

// formatConfigReloadDiff 基于两份配置映射的键差异生成单行摘要，例如
// "config reloaded: model, providers.x.apiKey=<redacted>"。
//
// 安全要点：它只消费 config.DiffConfigMaps 产出的键路径，绝不接触配置值——
// 因此 apiKey/token 等原文从源头就不会进入摘要。对看起来像凭据的键追加
// <redacted> 占位符，让操作人员知道"该键的值变了但被隐藏"。敏感判定规则与
// internal/config 的敏感键判定保持一致（归一化后精确匹配或敏感后缀匹配）；
// 该判定只决定是否显示占位符，不影响"从不打印值"的安全性质。
func formatConfigReloadDiff(cur, next *config.Config) string {
	if cur == nil || next == nil {
		return ""
	}
	diff := config.DiffConfigMaps(cur.Raw, next.Raw)
	if !diff.Changed {
		return ""
	}

	seen := make(map[string]bool, len(diff.AddedKeys)+len(diff.RemovedKeys)+len(diff.ModifiedKeys))
	keys := make([]string, 0, len(seen))
	addKeys := func(list []string) {
		for _, key := range list {
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	addKeys(diff.AddedKeys)
	addKeys(diff.RemovedKeys)
	addKeys(diff.ModifiedKeys)
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if reloadDiffSensitive(key) {
			parts = append(parts, key+"="+reloadDiffRedacted)
		} else {
			parts = append(parts, key)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "config reloaded: " + strings.Join(parts, ", ")
}

// reloadDiffSecretNames / reloadDiffSecretSuffixes 与 internal/config 的敏感键
// 词汇表保持一致：只收录明确表示凭据的段名，不含过于宽泛的裸 "key"。
var (
	reloadDiffSecretNames = map[string]bool{
		"apikey":        true,
		"token":         true,
		"secret":        true,
		"password":      true,
		"passwd":        true,
		"authorization": true,
		"credential":    true,
		"credentials":   true,
		"accesskey":     true,
		"privatekey":    true,
	}
	reloadDiffSecretSuffixes = []string{
		"apikey", "token", "secret", "password", "passwd", "credential", "credentials",
	}
)

// reloadDiffSensitive 判断点分隔路径中是否有任一段看起来是凭据键。
// 每段先归一化（转小写、去掉 '_' 和 '-'），再做精确名匹配或敏感后缀匹配；
// 按段匹配使 "providers.openai.apiKey" 能被识别。CJK 段名归一化是空操作，
// 自然不会命中英文敏感词。
func reloadDiffSensitive(path string) bool {
	for _, segment := range strings.Split(path, ".") {
		normalized := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(segment), "_", ""), "-", "")
		if normalized == "" {
			continue
		}
		if reloadDiffSecretNames[normalized] {
			return true
		}
		for _, suffix := range reloadDiffSecretSuffixes {
			if strings.HasSuffix(normalized, suffix) {
				return true
			}
		}
	}
	return false
}
