package proc

import (
	"fmt"
	"strings"
)

// secretKeyIndicators 是 KEY=VALUE 令牌的键中不区分大小写的子串，
// 其值绝不能出现在日志行中。匹配前键中的分隔符会被
// 规范化为 '_'，因此 API-KEY、--api-key 和 API_KEY 都会
// 被识别。故意保守：过度脱敏一条诊断行的代价很低，
// 而把凭据泄露到日志中则不然。
var secretKeyIndicators = []string{
	"password", "passwd", "pwd", "secret", "token",
	"credential", "api_key", "access_key", "private_key", "session_key",
}

const redactedValue = "***"

// CommandSummary 返回 c 的紧凑、单行、日志安全的描述：
// 进程路径和参数（凭据类值已脱敏）、超时时间和进程树终止模式。
// 它是用于日志和调试输出的纯诊断辅助函数；从不修改 c，也不启动任何东西。
// debug output; it never mutates c or spawns anything.
//
// 格式为：
//
//	cmd="<path> <arg1> <arg2>..." timeout=<duration|none> killTree=<bool> env=<count>
//
// 每个 KEY=VALUE 或 --KEY=VALUE 形式的参数，当 KEY 看起来像凭据时
// 会被脱敏为 KEY=***；其他令牌原样通过。%q 让
// CJK 和其他可打印 Unicode 在带引号的命令行中保持可读。Env
// 条目只计数、不打印，因此 c.Env 中的密钥也不会通过
// 该摘要泄露。nil 命令摘要为 "<nil>"。
func CommandSummary(c *Command) string {
	if c == nil {
		return "<nil>"
	}

	parts := make([]string, 0, 1+len(c.Args))
	parts = append(parts, c.Path)
	for _, arg := range c.Args {
		parts = append(parts, redactToken(arg))
	}

	timeout := "none"
	if c.Timeout > 0 {
		timeout = c.Timeout.String()
	}
	return fmt.Sprintf("cmd=%q timeout=%s killTree=%t env=%d",
		strings.Join(parts, " "), timeout, c.KillTree, len(c.Env))
}

// 当 KEY 看起来像凭据时，redactToken 对 KEY=VALUE / --KEY=VALUE 令牌
// 的值进行脱敏。没有 '='、值为空或键普通的令牌
// 原样返回。
func redactToken(token string) string {
	key, value, ok := strings.Cut(token, "=")
	if !ok || value == "" {
		return token
	}
	if looksSecret(strings.TrimLeft(key, "-")) {
		return key + "=" + redactedValue
	}
	return token
}

// looksSecret 报告 key 是否表示凭据。比较不区分大小写，
// 且把 '-' 和 '.' 视为 '_'，因此 --api-key、API-KEY 和
// api.key 都会规范化为 api_key 并匹配同一指示符。
func looksSecret(key string) bool {
	normalised := strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(key))
	for _, indicator := range secretKeyIndicators {
		if strings.Contains(normalised, indicator) {
			return true
		}
	}
	return false
}
