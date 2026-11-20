package security

import "regexp"

// injectionPattern 描述一类"人类可读"的提示注入载荷。description 用中文说明
// 命中的注入类型，调用方可以直接把它放进警告里。每条模式都要求祈使语境（动作
// 动词 + 指令类名词，或明确的角色切换句式），而不是匹配孤立单词；这是避免误报
// 的关键：正常讨论 "system prompt"、"disregard" 这类词不会被标记。
type injectionPattern struct {
	description string
	re          *regexp.Regexp
}

// promptInjectionPatterns 是全部可见注入检测模式。所有正则都带 (?i)，因此英文
// 大小写不敏感；CJK 文本本身不分大小写。结果按这里的顺序返回，每个模式最多
// 出现一次。
var promptInjectionPatterns = []injectionPattern{
	// 英文：要求忽略/无视先前的指令、规则或上文。要求动词后跟随指令类名词，
	// 所以 "ignore previous test failures" 这类正常句子不会命中。
	{
		"英文：要求忽略先前的指令或规则",
		regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override|bypass)\s+(?:(?:all|any|the|your|these|those|previous|prior|earlier|above|preceding)\s+){0,3}(?:instructions?|rules?|prompts?|directions?|guidelines?|messages?|commands?|constraints?|restrictions?|everything|above)\b`),
	},
	// 英文：经典越狱开场 "you are now a developer"。必须跟着角色词，
	// 所以 "you are now free to go" 这种正常句子不会命中。
	{
		"英文：要求模型切换角色（you are now）",
		regexp.MustCompile(`(?i)\byou are now\s+(?:(?:a|an|the|in)\s+)?(?:developer|admin(?:istrator)?|assistant|dan|superuser|root|unrestricted|unfiltered|jailbroken|debug(?:ger)?|engineer|persona|expert|owner|god)\b`),
	},
	// 英文：要求泄露、打印或覆盖系统提示词。必须有动作动词开头，
	// 因此 "the system prompt defines behavior" 这类讨论不会命中。
	{
		"英文：要求泄露或覆盖系统提示词",
		regexp.MustCompile(`(?i)\b(?:reveal|print|show|display|ignore|override|bypass|disclose|dump|repeat|output|tell\s+me|write\s+out)\s+(?:me\s+)?(?:(?:the|your|full|entire|original|hidden|exact)\s+){0,3}system\s+(?:prompt|message|instructions?)\b`),
	},
	// 中文：要求忽略/无视先前的指令。动词前必须是句首、空白或标点（或"请"），
	// 因此 "不要忽略指令"、"讨论忽略指令的危害" 不会命中。
	{
		"中文：要求忽略先前的指令或规则",
		regexp.MustCompile(`(?s)(?:^|[\s，。！？；：、\n])请?\s*(?:忽略|无视|忘记|跳过).{0,10}?(?:指令|指示|规则|提示词|提示|命令|要求|约束|限制)`),
	},
	// 中文：越狱式角色切换 "你现在是开发者"。必须跟着明确的角色词，
	// 正常句子 "你现在是产品经理" 不会命中。
	{
		"中文：要求模型切换角色",
		regexp.MustCompile(`(?i)(?:你现在是|从现在起你是|从现在开始你是|你现在扮演)(?:一个?|一位)?\s*(?:开发者|开发模式|管理员|调试|无限制|未过滤|超级用户|助手|专家|老板|主人|root|dan|系统)`),
	},
	// 中文：要求泄露/输出系统提示词，同样要求前方出现动作动词。
	{
		"中文：要求泄露或输出系统提示词",
		regexp.MustCompile(`(?i)(?:泄露|输出|打印|显示|忽略|绕过|重复|告诉我).{0,6}?(?:系统提示词|系统提示|系统指令|system\s+prompt)`),
	},
}

// detectPromptInjection 返回 s 中命中的可见提示注入模式描述。它是纯函数：不修改
// 输入、不发射事件、不做任何 I/O；是否警告、拒绝或记录完全由调用方决定。没有命中
// 时返回 nil。
//
// 精度边界：它只识别"人类可读"的注入措辞。把载荷藏在不可见 Unicode Tag 字符中
// 会打断字节序列而无法在此匹配，这类文本应先用 StripUnicodeTags 清洗后再检测；
// 相反地，逐字引用注入短语本身（例如在文章里讨论这句话）仍会被标记，因此结果
// 应当作为告警线索而不是自动判决。
func detectPromptInjection(s string) []string {
	if s == "" {
		return nil
	}
	var hits []string
	for _, pattern := range promptInjectionPatterns {
		if pattern.re.MatchString(s) {
			hits = append(hits, pattern.description)
		}
	}
	return hits
}

// DetectPromptInjection 是 detectPromptInjection 的导出包装，供包外调用方在
// opt-in 的警告路径中使用。默认没有任何自动接线：调用方必须自行决定是否把结果
// 注入工具输出（这会改变字节），或仅写入审计告警。
func DetectPromptInjection(s string) []string {
	return detectPromptInjection(s)
}
