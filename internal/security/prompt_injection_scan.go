package security

import (
	"regexp"
	"sort"
)

// 严重级别取值。调用方可以用字符串相等或 switch 比较；这里刻意用稳定的小写
// 英文标识符，而不是中文文案，方便写入审计日志和 JSON。
const (
	InjectionSeverityHigh   = "high"   // 明确的指令覆盖 / 系统提示泄露 / 越权角色切换。
	InjectionSeverityMedium = "medium" // 角色扮演类软性诱导，措辞可能出现在正常讨论中。
	InjectionSeverityLow    = "low"    // 预留给弱信号模式；当前没有模式使用它。
)

// InjectionFinding 是一条注入模式命中记录。
type InjectionFinding struct {
	Pattern  string // 稳定模式标识，例如 "en:ignore-previous-instructions"，便于上层聚合/抑制。
	Offset   int    // 命中在原文本中的字节偏移（从左数，非负）。见下方"CJK"说明。
	Severity string // InjectionSeverityHigh / Medium / Low 之一。
}

// injectionScanPattern 描述一类"人类可读"的注入措辞。所有正则都带 (?i)，
// 因此英文大小写不敏感；CJK 文本本身不分大小写，(?i) 对它无副作用。
type injectionScanPattern struct {
	name     string
	severity string
	re       *regexp.Regexp
}

// promptInjectionScanPatterns 是全部可见注入模式。这里与 prompt_injection.go
// 的检测器刻意保持独立：本原语返回 Offset/Severity 的结构化命中，供上层自行决策，
// 不复用也不影响既有的警告路径。
//
// 误报边界（重要）：正则只匹配"动作动词 + 指令类名词 / 角色词"这类祈使语境，
// 而不是孤立关键词。例如单独出现的 "system prompt"、"忽略"、"扮演医生" 都不会命中，
// 藉此降低对正常讨论的误报。即便如此，逐字引用注入短语（如安全文章里举例
// "ignore previous instructions"）仍会被标记；并且中文模式不带句首锚点，
// "不要忽略上述指令" 这类否定句也可能命中。本原语只提供线索，不做语义过滤，
// 是否告警/拒绝完全由调用方结合上下文判断。
var promptInjectionScanPatterns = []injectionScanPattern{
	// 英文：要求忽略/无视先前的指令。动词后跟限定词 + 指令类名词，
	// 所以 "ignore previous test failures" 不会命中。
	{
		"en:ignore-previous-instructions",
		InjectionSeverityHigh,
		regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override|bypass|skip)\s+(?:(?:all|any|the|your|these|those|previous|prior|earlier|above|preceding)\s+){0,3}(?:instructions?|rules?|prompts?|directions?|guidelines?|commands?|constraints?|restrictions?)\b`),
	},
	// 英文：要求泄露或覆盖系统提示词。必须有动作动词开头，
	// 因此 "the system prompt defines behavior" 这类讨论不会命中。
	{
		"en:system-prompt-disclosure",
		InjectionSeverityHigh,
		regexp.MustCompile(`(?i)\b(?:reveal|print|show|display|disclose|dump|repeat|output|write\s+out|tell\s+me|ignore|override|bypass)\s+(?:me\s+)?(?:(?:the|your|full|entire|original|hidden|exact)\s+){0,3}system\s+(?:prompt|message|instructions?)\b`),
	},
	// 英文：经典越狱开场 "you are now a developer"。必须跟着角色词，
	// 所以 "you are now free to go" 不会命中。
	{
		"en:role-escalation-you-are-now",
		InjectionSeverityHigh,
		regexp.MustCompile(`(?i)\byou are now\s+(?:(?:a|an|the|in)\s+)?(?:developer|admin(?:istrator)?|assistant|dan|superuser|root|unrestricted|unfiltered|jailbroken|debug(?:ger)?|engineer|persona|expert|owner|god|mode)\b`),
	},
	// 英文：角色扮演式越权 "act as DAN" / "pretend you are unrestricted"。
	// 角色词受限，因此 "act as a doctor" 不会命中。
	{
		"en:roleplay-escalation",
		InjectionSeverityMedium,
		regexp.MustCompile(`(?i)\b(?:act\s+as|pretend\s+(?:you\s+are|to\s+be)|roleplay\s+as|play\s+the\s+role\s+of)\s+(?:(?:a|an|the)\s+)?(?:unrestricted|unfiltered|jailbroken|dan|developer|admin(?:istrator)?|root|superuser|system|god|evil|amoral)\b`),
	},
	// 中文：要求忽略/无视上述指令。要求出现指代词（上述/之前/以上…）或
	// 数量词 + 指令类名词，所以 "忽略这个拼写错误"、"讨论忽略指令" 不会命中。
	{
		"zh:ignore-above-instructions",
		InjectionSeverityHigh,
		regexp.MustCompile(`(?:忽略|无视|忘记|跳过)\s*(?:(?:上述|以上|前面|之前|先前|上面|刚刚|这段|本段)(?:\s*的)?\s*(?:(?:所有|全部|一切)\s*)?(?:指令|指示|规则|提示词|提示|命令|要求|约束|限制|内容|消息)?|(?:所有|全部|一切)\s*(?:的)?\s*(?:指令|指示|规则|提示词|提示|命令|要求|约束|限制|内容|消息))`),
	},
	// 中文：要求泄露/输出系统提示词，前方必须出现动作动词。
	{
		"zh:system-prompt-disclosure",
		InjectionSeverityHigh,
		regexp.MustCompile(`(?i)(?:泄露|输出|打印|显示|展示|给出|忽略|绕过|重复|告诉我|写出|公布)\s*(?:(?:你|完整|全部|隐藏|原始|系统)的?)?\s*(?:系统提示词|系统提示|系统指令|系统消息|system\s+prompt|system\s+message)`),
	},
	// 中文：越狱式角色切换 "你现在是开发者"。角色词受限，
	// 正常句子 "你现在是产品经理" 不会命中。
	{
		"zh:role-escalation",
		InjectionSeverityHigh,
		regexp.MustCompile(`(?i)(?:你现在是|从现在起你是|从现在开始你是|现在你是|你现在扮演|你扮演)\s*(?:一个?|一位)?\s*(?:开发者|开发模式|管理员|调试模式?|无限制|未过滤|超级用户|助手|专家|老板|主人|root|dan|系统|越权|god)`),
	},
	// 中文：角色扮演式越权 "扮演管理员" / "假装你是 DAN"。
	{
		"zh:roleplay-escalation",
		InjectionSeverityMedium,
		regexp.MustCompile(`(?i)(?:扮演|假装你是|假装自己是|假设你是|模拟你是)\s*(?:一个?|一位)?\s*(?:管理员|开发者|无限制(?:的)?(?:助手|模式|AI)?|未过滤|dan|root|超级用户|系统|越权|god|jailbroken)`),
	},
}

// ScanPromptInjection 报告 text 中命中的可见提示注入模式。它只检测、不做语义
// 过滤：不修改输入、不发射事件、不做任何 I/O，也不改变任何字节；是否告警、
// 拒绝或仅记录完全由调用方决定。无命中时返回 nil。
//
// 返回值按 Offset 升序、同 Offset 按 Pattern 升序排列，并按 (Pattern, Offset,
// Severity) 去重，因此对同一输入调用多次结果完全一致（确定性）。
//
// CJK 与 Offset：Offset 是原字符串的字节偏移（与 strings.Index、regexp 的
// FindStringIndex 一致），不是 rune 序号。Go 的 regexp 按 UTF-8 解码，
// 正则中的 .{n} 以及上面的 \s* 都按 rune 计数，因此多字节的中文/emoji 不会
// 被切断，也不会影响命中；只是偏移值按字节计。上层若要定位到 rune，可自行
// 用 utf8.RuneCountInString(text[:f.Offset]) 换算。
//
// 精度边界：只识别"人类可读"的注入措辞。把载荷藏在不可见 Unicode Tag 字符中
// 会打断字节序列而无法在此匹配，这类文本应先用 StripUnicodeTags 清洗后再检测；
// 反之逐字引用注入短语仍会被标记（见 promptInjectionScanPatterns 的注释）。
// 结果应作为告警线索，而不是自动判决。
func ScanPromptInjection(text string) []InjectionFinding {
	if text == "" {
		return nil
	}
	var findings []InjectionFinding
	for _, pattern := range promptInjectionScanPatterns {
		for _, loc := range pattern.re.FindAllStringIndex(text, -1) {
			findings = append(findings, InjectionFinding{
				Pattern:  pattern.name,
				Offset:   loc[0],
				Severity: pattern.severity,
			})
		}
	}
	if len(findings) == 0 {
		return nil
	}

	// 去重：同一 (Pattern, Offset, Severity) 只保留一条。
	seen := make(map[InjectionFinding]struct{}, len(findings))
	deduped := findings[:0]
	for _, f := range findings {
		if _, exists := seen[f]; exists {
			continue
		}
		seen[f] = struct{}{}
		deduped = append(deduped, f)
	}

	// 排序：先 Offset 再 Pattern，得到全序，保证确定性。
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Offset != deduped[j].Offset {
			return deduped[i].Offset < deduped[j].Offset
		}
		return deduped[i].Pattern < deduped[j].Pattern
	})
	return deduped
}
