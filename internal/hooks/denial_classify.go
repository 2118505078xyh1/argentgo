package hooks

import "strings"

// 拒绝原因的结构化分类标签。它们是稳定、与语言无关的取值，供审计/遥测
// 下游按类别聚合，而不传播原始的原因文本。
const (
	// denialCategoryPolicy 表示由配置的策略或权限规则拒绝。
	denialCategoryPolicy = "policy"
	// denialCategoryCondition 表示由运行环境/工作区等上下文条件拒绝（越界、只读、需批准等）。
	denialCategoryCondition = "condition"
	// denialCategoryEngine 表示引擎自身故障（无法执行、超时、不可用、解析失败等）。
	denialCategoryEngine = "engine"
	// denialCategoryUser 表示操作员/用户明确否决或未授予批准。
	denialCategoryUser = "user"
	// denialCategoryUnknown 表示原因无法归入上述任何类别（含空值）。
	denialCategoryUnknown = "unknown"
)

// denialKeywordRule 把一组关键词映射到一个稳定分类。
// 关键词以小写英文或原样 CJK 书写；匹配前对原因做 strings.ToLower，
// 因此英文大小写不敏感，而 CJK 不受大小写影响。
type denialKeywordRule struct {
	category string
	keywords []string
}

// denialRules 按优先级从高到低排列；第一条命中的规则即决定分类。
// 优先级体现"先排除系统性故障，再区分人为与规则性拒绝"：
//
//  1. engine：凡出现无法执行/超时/不可用/解析失败等字样，先归为引擎故障。
//     这能正确捕获 "could not be canonicalized for policy checking" 这类
//     虽含 "policy" 但本质是故障的原因。
//  2. user：操作员明确拒绝或批准未授予。
//  3. policy：配置的策略、权限规则、黑白名单。
//  4. condition：工作区边界、只读、需要批准等上下文条件。
//
// 关键词采用具名短语而非泛化单词（例如 "operator refused" 而非单独的
// "denied"），避免 "denied by policy" 被误判为操作员否决。
var denialRules = []denialKeywordRule{
	{
		category: denialCategoryEngine,
		keywords: []string{
			"could not", "cannot", "can't", "couldn't", "unavailable",
			"timed out", "timeout", "failed", "failure", "panic",
			"canonicaliz", "not a json", "internal error", "unexpected",
			"故障", "超时", "失败", "无法", "不能", "不可用", "异常", "错误", "崩溃",
		},
	},
	{
		category: denialCategoryUser,
		keywords: []string{
			"operator refused", "operator rejected", "refused the tool",
			"rejected the tool", "denied by the user", "user denied",
			"user refused", "user rejected", "not granted",
			"用户拒绝", "操作员拒绝", "操作员否决", "用户否决", "否决",
		},
	},
	{
		category: denialCategoryPolicy,
		keywords: []string{
			"denied by policy", "deny list", "allow list", "permissions table",
			"denied by the permissions", "not allowed", "forbidden", "prohibited",
			"denied path", "deny path", "policy", "rule",
			"策略", "禁止", "不允许", "黑名单", "白名单", "规则", "权限表",
		},
	},
	{
		category: denialCategoryCondition,
		keywords: []string{
			"workspace root", "escapes the workspace", "outside the allowed",
			"not writable", "needs approval", "needs operator approval",
			"requires approval", "sandbox", "read-only", "readonly", "condition",
			"工作区", "越界", "逃逸", "沙箱", "不可写", "只读",
			"需要批准", "需要审批", "需要操作员批准", "需要操作员审批", "边界", "条件",
		},
	},
}

// classifyDenialReason 把任意拒绝原因归一到一个稳定的结构化分类。
//
// 它是纯函数：不读写全局状态、不产生 I/O，对同一输入始终返回相同标签。
// 行为要点：
//
//   - 先做 strings.TrimSpace 与 strings.ToLower，因此首尾空白与英文大小写
//     都不影响结果；CJK 字符在大小写转换中保持不变。
//   - 按 denialRules 的优先级顺序做子串匹配，首条命中即返回；未命中或空值
//     返回 "unknown"。
//   - 子串匹配基于作用在合法 UTF-8 上的 strings.Contains，CJK 原因按整段
//     语义匹配关键词，不会被拆成半个 rune；任意非法/混合文本都只会命中某条
//     规则或落到 "unknown"，不会 panic。
//
// 该函数默认不被分发路径调用，因此不改变现有审计输出；它是可供 opt-in
// 的分类能力，调用方可自行决定是否为原因附加分类标签。
func classifyDenialReason(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	if normalized == "" {
		return denialCategoryUnknown
	}
	for _, rule := range denialRules {
		for _, keyword := range rule.keywords {
			if strings.Contains(normalized, keyword) {
				return rule.category
			}
		}
	}
	return denialCategoryUnknown
}

// ClassifyDenialReason 是 classifyDenialReason 的导出接缝，供本包以外的
// 调用方（例如审计或遥测层）opt-in 使用：默认没有任何分发路径调用它，
// 因此现有审计输出与行为完全不变；只有调用方主动引用才会启用分类。
func ClassifyDenialReason(reason string) string {
	return classifyDenialReason(reason)
}
