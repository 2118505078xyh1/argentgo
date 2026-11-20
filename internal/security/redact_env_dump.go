package security

import "strings"

// redactEnvDump 针对 `env` / `printenv` 风格的逐行 `KEY=value` 输出做脱敏：
// 键名被判定为机密的整行值会被占位符替换，其余内容逐字节保留。
//
// 不直接复用 Redact 的原因：Redact 里 keyValueNames 的规则只会把“具有凭据
// 形状”的片段换掉，而环境变量的值可能是任意口令（例如 DB_PASSWORD=myP@ss!），
// 没有 sk-/ghp_ 之类前缀就会漏过去。这里改为按整行替换值，并使用
// secretKeyKind 作为键名判定：它是 keyValueNames 的超集（凡是被 keyValueNames
// 命中的键名都会被它命中），因此不会引入第二套会漂移的规则。
//
// 只按第一个等号切分，值里自带的 `=` 既不改变键名判定，也不会被误当作分隔符。
// 只读写 ASCII 字节分隔符（'='、'\n'、'\r'），它们永远不会出现在 UTF-8 多字节
// 序列中，所以 CJK/emoji 值不会被从中间截断。函数不访问全局可变状态，对同一
// 输入始终返回相同结果；对已脱敏输出再次调用是空操作（幂等）。
func redactEnvDump(output string) string {
	if output == "" {
		return output
	}
	var b strings.Builder
	b.Grow(len(output))
	rest := output
	for rest != "" {
		nl := strings.IndexByte(rest, '\n')
		line := rest
		ending := ""
		if nl >= 0 {
			line = rest[:nl]
			ending = rest[nl : nl+1]
			rest = rest[nl+1:]
		} else {
			rest = ""
		}
		// `\r\n` 里的 `\r` 属于行尾，不能算进值里。
		body := line
		cr := ""
		if strings.HasSuffix(body, "\r") {
			body = body[:len(body)-1]
			cr = "\r"
		}
		b.WriteString(redactEnvLine(body))
		b.WriteString(cr)
		b.WriteString(ending)
	}
	return b.String()
}

// redactEnvLine 处理单行 `KEY=value`：只按第一个等号切分，机密键名的值整体替换
// 为带种类的占位符；非机密行（包括没有等号或键名为空的行）原样返回。
func redactEnvLine(line string) string {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return line
	}
	key := line[:eq]
	kind := secretKeyKind(key)
	if kind == "" {
		return line
	}
	return key + "=" + placeholder(kind)
}

// RedactEnvDump 是 redactEnvDump 的导出 opt-in 入口，供需要在把命令输出送入
// 模型上下文前清洗 `env`/`printenv` 结果的调用方使用。默认不接线：调用方只有
// 在明确启用时才应调用它，否则输出逐字节不变。若同时需要通用凭据形态脱敏与
// Unicode Tag 剥离，可在调用方把本函数与 RedactToolOutput 组合使用。
func RedactEnvDump(output string) string {
	return redactEnvDump(output)
}
