package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// 本文件把 decision_judge.go 的纯函数 DecidePrompt 以 opt-in 方式接入 Execute。
// 设计约束（与 decision_judge.go 的安全边界一致）：
//   - 只有 ARGENT_AUTO_DECIDE=1 才会改变任何行为；默认关闭时 Execute 的每一行
//     产物（提示、系统消息、事件、会话记录、模型）都逐字节不变。
//   - 判定结果只影响"性能旋钮"：思考档位建议（写入动态上下文）与模型选择。
//   - 绝不触碰 Approve / Policy / SandboxMode / MutatingTools 等安全字段：
//     不构造策略、不替换审批器、不改变沙箱、不启用或禁用任何工具。

// autoDecideEnabled 报告按提示自动决策的 opt-in 门是否开启。
// 与 inspectionEnabled 同风格：只有精确值 "1"（允许首尾空白）开启，
// 其他任何值（包括空值、"true"、"yes"）都保持历史行为不变。
func autoDecideEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_AUTO_DECIDE")) == "1"
}

// promptHasCode 用轻量启发式判断提示是否包含代码：围栏代码块或行内反引号。
// 它只作为 DecidePrompt 的"含代码"复杂度信号，不参与任何安全决策。
func promptHasCode(prompt string) bool {
	return judgeFencedBacktick.MatchString(prompt) ||
		judgeFencedTilde.MatchString(prompt) ||
		judgeInlineCode.MatchString(prompt)
}

// autoDecideMutationMarkers 是"提示看起来涉及修改"的轻量启发式信号。
// 注意：这只是文本复杂度信号，与 Resolved.Mutating、沙箱模式、策略或工具可用性
// 完全无关——开启本门不会因此放宽任何授权或修改类工具的集合。
var autoDecideMutationMarkers = []string{
	"修改", "编辑", "删除", "重写", "重构", "修复", "新增", "创建", "写入",
	"edit", "modify", "delete", "rewrite", "refactor", "fix", "change", "create", "write", "remove",
}

// promptMutatingSignal 返回提示是否带有"涉及修改"的文本信号。
// 分析前复用 stripPromptNonUserText 剥离注入块，避免附件文本操纵判定。
func promptMutatingSignal(prompt string) bool {
	text := strings.ToLower(stripPromptNonUserText(prompt))
	for _, marker := range autoDecideMutationMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// autoDecideThinking 在用户未显式指定思考档位时返回 judge 的建议；
// 用户已指定的档位始终优先，避免 opt-in 覆盖操作人员的显式选择。
func autoDecideThinking(userThinking, suggested string) string {
	if strings.TrimSpace(userThinking) != "" {
		return userThinking
	}
	return suggested
}

// applyAutoDecideModel 在 judge 建议使用次级模型时切换主模型，并保持回退链的顺序：
//   - FallbackModels 非空时，其第 0 项即主模型，因此只替换首项，
//     其余回退模型按原顺序保留（secondary -> 原回退序列）；
//   - FallbackModels 为空时，直接替换旧的 Model 字段。
//
// 次级模型名为空时不做任何改动。此函数只动"模型"相关的两个字段，
// 与 Approve / Policy / SandboxMode / MutatingTools 无关。
func applyAutoDecideModel(opts *agent.Options, secondary string) {
	if opts == nil {
		return
	}
	secondary = strings.TrimSpace(secondary)
	if secondary == "" {
		return
	}
	if len(opts.FallbackModels) > 0 {
		// 复制一份再改首项，避免改动可能与其它运行共享的切片底层数组。
		fallbacks := append([]string(nil), opts.FallbackModels...)
		fallbacks[0] = secondary
		opts.FallbackModels = fallbacks
		return
	}
	opts.Model = secondary
}
