package hooks

import (
	"unicode"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

const (
	// telemetryTagMaxRunes 是单个遥测标签允许的最大字符数（按 rune 计）。
	// 超过此长度即在 rune 边界截断，避免高基数标签无限增长。
	telemetryTagMaxRunes = 256
	// telemetryTagOther 是所有不符合白名单的标签值的统一归属桶。把异常值折叠到
	// 一个固定取值，既压平基数，也阻止凭据、路径等原始内容进入遥测后端。
	telemetryTagOther = "other"
)

// sanitizeTelemetryTag 把任意字符串规约成低基数、可安全上报的遥测标签。
//
// 规约规则（全部为纯函数，不读写任何全局状态）：
//
//  1. 空值直接归入 "other"；
//  2. security.Redact 作为凭据兜底：只要值里出现任何凭据形态就整体丢弃；
//  3. 按 rune 截断到 telemetryTagMaxRunes 个字符，且不切开 UTF-8 序列；
//  4. 剩下的每个字符必须落在白名单内（Unicode 字母/数字及 '-' '_' '.' ':'），
//     否则整个值折叠为 "other"。
//
// 白名单采用 Unicode 字母/数字分类而非纯 ASCII，因此 CJK 字符被当作合法文字
// 原样保留，不会被拆成乱码字节；包含 CJK 的长串同样在 rune 边界安全截断。
func sanitizeTelemetryTag(value string) string {
	if value == "" {
		return telemetryTagOther
	}
	// 凭据兜底：命中任意凭据形态即整体丢弃，不泄漏半个密钥片段。
	if security.Redact(value) != value {
		return telemetryTagOther
	}

	runes := []rune(value)
	if len(runes) > telemetryTagMaxRunes {
		runes = runes[:telemetryTagMaxRunes]
	}
	for _, r := range runes {
		if !isTelemetryTagRune(r) {
			return telemetryTagOther
		}
	}
	return string(runes)
}

// isTelemetryTagRune 报告 r 是否属于标签白名单字符集。
// 使用 Unicode 分类使 CJK 等表意文字被视为合法字符，而不是被误判为乱码。
func isTelemetryTagRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) ||
		r == '-' || r == '_' || r == '.' || r == ':'
}

// telemetrySafeKeys 是允许进入脱敏遥测映射的字段白名单。这里只放分类、计数、
// 耗时和布尔标记；任何可能携带原始内容的键（prompt/output/args/path/command 等）
// 都不在表内，因此会被自动丢弃。
var telemetrySafeKeys = map[string]struct{}{
	"event":       {},
	"name":        {},
	"hook":        {},
	"tool":        {},
	"category":    {},
	"status":      {},
	"outcome":     {},
	"reason":      {},
	"error_code":  {},
	"result":      {},
	"phase":       {},
	"count":       {},
	"total":       {},
	"elapsed_ms":  {},
	"duration_ms": {},
	"size_bytes":  {},
	"length":      {},
	"slow":        {},
	"blocked":     {},
	"failed":      {},
	"timed_out":   {},
	"exit_code":   {},
}

// telemetrySafe 把任意事件映射投影为 trace-safe 的遥测映射。
//
// 仅保留 telemetrySafeKeys 中的键：字符串值先经 security.Redact 兜底脱敏，
// 再经 sanitizeTelemetryTag 规约为有界的低基数分类；整数、浮点与布尔标量原样
// 保留（它们只表示长度、计数或状态）；其余一切（原始内容、嵌套 map/slice、
// nil、未知类型）全部丢弃。函数不修改输入，返回全新的映射；nil 或空输入返回
// 一个非 nil 的空映射，便于调用方继续写入字段。
func telemetrySafe(event map[string]any) map[string]any {
	out := make(map[string]any, len(telemetrySafeKeys))
	for key, value := range event {
		if _, ok := telemetrySafeKeys[key]; !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			// Redact 兜底在前，标签规约在后：即便凭据形态恰好由白名单字符组成，
			// 也已被替换成含空格/方括号的占位符，继而被折叠为 "other"。
			out[key] = sanitizeTelemetryTag(security.Redact(v))
		case bool:
			out[key] = v
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			out[key] = v
		default:
			// 原始内容、嵌套结构与 nil 等一律丢弃。
		}
	}
	return out
}

// TelemetrySafeFunc 消费一份已脱敏的低基数遥测映射。与 TelemetryFunc 一样，
// 实现必须并发安全。它默认不会被分发路径调用，只有调用方主动接线才生效。
type TelemetrySafeFunc func(map[string]any)

// SafeMap 把一条钩子观察转成脱敏的遥测映射。
//
// 命令字符串被规约为分类标签，耗时/退出码/状态位是安全标量；原始命令、输出、错误
// 文本和 payload 内容根本不会进入映射，因此即使观察本身来自不可信钩子也不会
// 泄漏内容或抬高标签基数。
func (r HookTelemetry) SafeMap() map[string]any {
	return telemetrySafe(map[string]any{
		"event":      string(r.Event),
		"name":       r.Name,
		"elapsed_ms": r.Elapsed.Milliseconds(),
		"exit_code":  r.ExitCode,
		"slow":       r.Slow,
		"blocked":    r.Blocked,
		"timed_out":  r.TimedOut,
		"failed":     r.Failed(),
	})
}

// SafeTelemetryAdapter 把 TelemetrySafeFunc 适配成可赋给 Dispatcher.Telemetry
// 的 TelemetryFunc。这是一条默认关闭的可选接线：调用方主动设置
//
//	dispatcher.Telemetry = SafeTelemetryAdapter(sink)
//
// 才会启用；未接线时现有分发与遥测行为完全不变。sink 为 nil 时返回 nil。
// 与 reportTelemetry 一致，sink 内部的 panic 被隔离，绝不破坏分发。
func SafeTelemetryAdapter(sink TelemetrySafeFunc) TelemetryFunc {
	if sink == nil {
		return nil
	}
	return func(record HookTelemetry) {
		defer func() { _ = recover() }()
		sink(record.SafeMap())
	}
}
