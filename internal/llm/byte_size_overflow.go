package llm

import "regexp"

// byteSizeOverflowREs 识别"按字节计数的请求载荷超过网关/服务端上限"的
// 明确文案。每条模式都要求同时出现"字节级请求主体"（body / entity /
// payload / size / 请求体 / 载荷 等）与"溢出语义"（too large / exceed /
// limit / 413 / 过大 / 超限 等），或是像 "request body too large" 这样
// 不可歧义的完整短语。
//
// 这样刻意收紧是为了不误报：token 数上限、输出长度、响应体过大、
// "max_tokens must be" 等都不含字节级请求主体，因此不会命中。
// 状态码 413 也必须与大小/主体语义同现，裸 413 不算。
var byteSizeOverflowREs = []*regexp.Regexp{
	// HTTP 413 的标准措辞：请求体/实体/载荷过大。
	regexp.MustCompile(`(?i)request[_\s-]?(?:body|entity|payload)[^\n]{0,32}?too[_\s-]+(?:large|big)`),
	// 字节级主体后跟 exceed / over the limit / size limit / too large。
	regexp.MustCompile(`(?i)(?:payload|request[_\s-]?(?:body|entity|size))[^\n]{0,32}?(?:exceed(?:s|ed|ing)?|over[_\s-]+(?:the[_\s-]+)?limit|size[_\s-]*limit|too[_\s-]+(?:large|big))`),
	// RequestSize ... Limit 形式的网关错误（含下划线/连字符变体）。
	regexp.MustCompile(`(?i)request[_\s-]?size[^\n]{0,32}?(?:too[_\s-]+(?:large|big)|exceed|limit|\b413\b)`),
	// 状态码 413：必须与大小/主体语义同现，裸 413 不算溢出。
	regexp.MustCompile(`(?i)\b413\b[^\n]{0,80}?(?:request[_\s-]?(?:body|entity|size|payload)|payload|size[_\s-]*limit|too[_\s-]+(?:large|big)|exceed|larger[^\n]{0,12}?than|limit|size)`),
	regexp.MustCompile(`(?i)(?:request[_\s-]?(?:body|entity|size|payload)|payload|size[_\s-]*limit|limit|size)[^\n]{0,80}?\b413\b`),
	// 中文：请求体/正文/内容/大小/尺寸 过大、超限或超过上限。
	regexp.MustCompile(`请求(?:体|正文|内容|大小|尺寸)[^\n]{0,16}?(?:过大|太大|超限|超出(?:大小|尺寸|长度|上限|限制)|超过(?:大小|尺寸|长度|上限|限制))`),
	// 中文：载荷/报文/包体 过大或超限。
	regexp.MustCompile(`(?:载荷|报文|包体)[^\n]{0,16}?(?:过大|太大|超限)`),
}

// isByteSizeOverflow 判断一段错误信息是否明确表示"请求体按字节计超过
// 上限"。它是纯函数：大小写不敏感、支持中文，空串与非溢出文案一律
// 返回 false。本函数只做文本识别，不改变任何分类或重试行为；是否
// 接入 contextOverflowRE 由调用方依据产品策略决定。
func isByteSizeOverflow(msg string) bool {
	if msg == "" {
		return false
	}
	for _, re := range byteSizeOverflowREs {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}
