package security

// redactToolOutput 在工具输出进入模型上下文之前对其中的凭据做脱敏。
//
// 它刻意只做一件事：把输出交给 Redact。Redact 的模式表已经覆盖
// API key（sk-*/sk_live_* 等前缀）、token（ghp_*、github_pat_*、JWT、
// Authorization/Bearer）、password/passwd、URL userinfo 中的口令、PEM
// 私钥，以及 `KEY=value` 形式的环境变量值（如 OPENAI_API_KEY=、
// DB_PASSWORD=、AWS_SECRET_ACCESS_KEY=）。复用同一份模式表意味着这里
// 不会出现第二套与之漂移的规则；将来 Redact 新增形态，工具输出自动受益。
//
// 替换是逐字节拼接的：只有命中的片段被占位符取代，其余内容（包括
// ANSI 转义、换行、CJK 与 emoji）原样保留。Redact 还会剥离 Unicode Tag
// 字符，因此藏在工具输出里的不可见提示注入指令同样被清除。
//
// 该函数是纯函数：不读取全局状态，不修改输入，对同一输入始终返回相同结果。
func redactToolOutput(output string) string {
	return Redact(output)
}

// RedactToolOutput 是 redactToolOutput 的导出入口，供需要把工具输出
// 送入模型上下文的调用方在 opt-in 分支中接入。接线默认关闭：调用方
// 只有在明确启用脱敏时才应调用它，否则工具输出保持逐字节不变。
func RedactToolOutput(output string) string {
	return redactToolOutput(output)
}
