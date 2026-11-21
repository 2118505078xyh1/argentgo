package app

import (
	"strings"
	"sync"
)

// 本文件提供一个统一的 opt-in 开关注册表骨架。R10 接线学习发现 app 层目前没有任何
// SetXxx 开关装配点，这是所有 opt-in 接线的共同前置；本注册表为第 11 轮接线提供单一
// 装配点。它默认零调用方：不读取进程环境、不注册路由、不碰任何现有装配路径。

// OptInKey 标识一个默认关闭、需显式开启的可选行为。其字符串值是稳定的配置词汇表，
// 使调用方无需查找表就能把键映射到环境变量中的名字。
type OptInKey string

const (
	// OptInRefusalReason 控制是否在结果中暴露拒绝原因。
	OptInRefusalReason OptInKey = "refusal_reason"
	// OptInUntrustedResult 控制是否把工具结果标记为不可信。
	OptInUntrustedResult OptInKey = "untrusted_result"
	// OptInContextPressure 控制是否暴露上下文压力信号。
	OptInContextPressure OptInKey = "context_pressure"
	// OptInUntrustedBoundary 控制是否启用不可信内容边界标记。
	OptInUntrustedBoundary OptInKey = "untrusted_boundary"
	// OptInAttemptIsolation 控制是否在验证失败时把失败尝试分叉为旁支会话。
	// 它默认关闭：未显式开启时验证失败不触碰会话树，事件流逐字节不变。
	OptInAttemptIsolation OptInKey = "attempt_isolation"
	// OptInCompactionFidelity 控制是否在压缩摘要要素缺失时发出质量告警。
	// 它默认关闭：未显式开启时压缩完成点不会被挂接任何观察者，行为逐字节不变。
	OptInCompactionFidelity OptInKey = "compaction_fidelity"
	// OptInKeepImplicitContext 控制是否把压缩丢弃的隐式上下文（尾部消息与失败尝试）
	// 重新并入摘要。它默认关闭：未显式开启时摘要逐字节不变。
	OptInKeepImplicitContext OptInKey = "keep_implicit_context"
	// OptInPreserveSummarySections 控制是否在重建压缩摘要时保留原摘要中的关键章节
	// （如目标、工具活动）。它默认关闭：未显式开启时重建摘要逐字节不变。
	OptInPreserveSummarySections OptInKey = "preserve_summary_sections"
	// OptInApplyPatch 控制是否启用 apply_patch 类补丁工具行为。
	// 它默认关闭：未显式开启时工具行为逐字节不变。
	OptInApplyPatch OptInKey = "apply_patch"
	// OptInGrepEx 控制是否启用扩展 grep 搜索行为。
	// 它默认关闭：未显式开启时搜索结果逐字节不变。
	OptInGrepEx OptInKey = "grep_ex"
	// OptInCliHints 控制是否在输出中附带 CLI 提示。
	// 它默认关闭：未显式开启时输出逐字节不变。
	OptInCliHints OptInKey = "cli_hints"
	// OptInToolErrorReflection 控制是否在工具失败时向模型回灌结构化的错误反射
	// （修复指令 + 脱敏错误正文），帮助模型自我修正。它默认关闭：未显式开启时
	// 回灌给模型的错误串逐字节不变。
	OptInToolErrorReflection OptInKey = "tool_error_reflection"
	// OptInModelSummary 控制压缩时是否用模型生成增量叙事摘要。它默认关闭：
	// 未显式开启时不发起任何模型调用，压缩摘要逐字节不变。
	OptInModelSummary OptInKey = "model_summary"
	// OptInAutoVerify 控制编辑后是否自动派生已有的验证诊断回环（Go 项目为 go test ./...）。
	// 它默认关闭：未显式开启时验证接线返回 nil，默认运行逐字节不变。
	OptInAutoVerify OptInKey = "auto_verify"
)

// allOptInKeys 是全部受支持开关的清单，供 DefaultOptIns 与文档共用同一份事实来源。
var allOptInKeys = []OptInKey{
	OptInRefusalReason,
	OptInUntrustedResult,
	OptInContextPressure,
	OptInUntrustedBoundary,
	OptInAttemptIsolation,
	OptInCompactionFidelity,
	OptInKeepImplicitContext,
	OptInPreserveSummarySections,
	OptInApplyPatch,
	OptInGrepEx,
	OptInCliHints,
	OptInToolErrorReflection,
	OptInModelSummary,
	OptInAutoVerify,
}

// optInEnvVar 是 opt-in 开关列表所在的单个环境变量名。
const optInEnvVar = "ARGENT_OPT_INS"

// OptInRegistry 是线程安全的开关注册表。零值可用：首次 Set 时才分配内部映射。
type OptInRegistry struct {
	mu      sync.RWMutex
	enabled map[OptInKey]bool
}

// Set 设置某个开关。未设置过的键默认为关闭；重复设置以最后一次为准。
func (r *OptInRegistry) Set(key OptInKey, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enabled == nil {
		r.enabled = make(map[OptInKey]bool)
	}
	r.enabled[key] = enabled
}

// Enabled 报告某个开关是否开启。未知或未设置的键一律返回 false（fail-closed）。
func (r *OptInRegistry) Enabled(key OptInKey) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled[key]
}

// Snapshot 返回当前开关状态的副本。调用方修改返回的映射不会影响注册表；
// 零值或已 Reset 的注册表返回一个空的非 nil 映射。
func (r *OptInRegistry) Snapshot() map[OptInKey]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[OptInKey]bool, len(r.enabled))
	for key, value := range r.enabled {
		out[key] = value
	}
	return out
}

// Reset 清空所有开关，使注册表回到零值状态。
func (r *OptInRegistry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = nil
}

// DefaultOptIns 返回全部开关默认关闭的映射。返回的映射是新建副本，调用方可自由修改；
// 每个受支持的键都显式出现且值为 false。
func DefaultOptIns() map[OptInKey]bool {
	out := make(map[OptInKey]bool, len(allOptInKeys))
	for _, key := range allOptInKeys {
		out[key] = false
	}
	return out
}

// ParseOptInEnv 从单个环境变量 ARGENT_OPT_INS 解析开关列表：值为逗号分隔的键名，
// 两侧空白被忽略，未知键与空片段被忽略（fail-closed）。env 为 nil 时返回全 false。
// 它是纯函数：只读取一次 env，不修改入参，也不读取进程环境；非 ASCII 文本不会匹配
// 任何已知键，因此解析结果与地区无关。
func ParseOptInEnv(env func(string) string) map[OptInKey]bool {
	out := DefaultOptIns()
	if env == nil {
		return out
	}
	for _, field := range strings.Split(env(optInEnvVar), ",") {
		key := OptInKey(strings.TrimSpace(field))
		if _, known := out[key]; known {
			out[key] = true
		}
	}
	return out
}
