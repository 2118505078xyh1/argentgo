package hooks

import (
	"encoding/json"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Verdict 表示单个钩子（或一组钩子）表达的权限裁决。
//
// 三个取值的大小关系经过刻意设计：VerdictNone < VerdictAllow < VerdictDeny。
// 因此"取最大值"天然满足"deny 绝对优先"的聚合语义，聚合函数无需分支判断。
// 该类型是不透明的状态无关值：同一个 Verdict 在任何上下文都含义一致。
type Verdict uint8

const (
	// VerdictNone 表示钩子没有给出任何权限裁决（默认、无信号、信号无法解析）。
	VerdictNone Verdict = iota
	// VerdictAllow 表示钩子明确批准了被拦截的操作。
	VerdictAllow
	// VerdictDeny 表示钩子明确否决/阻断了被拦截的操作。它优先于其它一切裁决。
	VerdictDeny
)

const (
	// maxAdditionalContextBytes 限制 additionalContext 注入文本的最终字节上限，
	// 防止不可信钩子输出占用过多模型上下文（默认 8192 字节）。
	maxAdditionalContextBytes = 8192
	// additionalContextClipLimit 为省略号（3 字节）预留空间：传给 clipRunes 后，
	// 可保证结果的字节数不超过 maxAdditionalContextBytes。
	additionalContextClipLimit = maxAdditionalContextBytes - len("…") + 1
	// rewriteClipBytes 限制单条输入/输出改写的字节数，超长改写直接忽略。
	rewriteClipBytes = 8192
)

// VerdictFromOutcome 把一次钩子执行结果解析为权限裁决。
//
// 这是纯函数：不读写全局状态、不产生 I/O、不改动传入的 Outcome；给定相同输入
// 总是返回相同结果。它综合两条互相独立的信号通道，并按"deny > allow > none"
// 取优先级最高者：
//
//  1. 退出码通道：
//     - 2：传统的"阻断错误"码，直接判为 VerdictDeny；
//     - 49：约定的"结构化决策码"，表示决策由 stdout 的 permissionDecision 承载，
//     统一交由下方的 JSON 通道解析，因此它本身不直接产出裁决；
//     - 其它退出码（0、1、-1 等）不产出退出码裁决。
//  2. stdout JSON 通道：始终尝试解析（便于钩子以正常退出码携带结构化决策）。
//     在每个 '{' 处尝试解码一个 JSON 对象，识别两种等价写法：顶层的
//     permissionDecision，以及 Claude 风格的 hookSpecificOutput.permissionDecision。
//     值为 allow / deny（大小写与首尾空白不敏感）时分别产出 VerdictAllow / VerdictDeny；
//     缺失、畸形、类型不符或取值未知一律视为无信号。
//
// 超时（TimedOut）的结果直接返回 VerdictNone：超时是执行故障而非权限裁决，
// 其 stdout 也不可信。解析全程只做 JSON 解码与 ASCII '{' 定界符扫描，从不按
// 固定字节数切割文本，因此 CJK 等本地化原因文本会被原样保留，既不会被截断成
// 半个 rune，也不会因非法 UTF-8 而 panic（encoding/json 会安全替换为 U+FFFD）。
func VerdictFromOutcome(o Outcome) Verdict {
	if o.TimedOut {
		return VerdictNone
	}

	v := VerdictNone
	switch o.ExitCode {
	case 2:
		// 阻断错误（传统约定）→ deny。即便 stdout 同时声明 allow，
		// 下方的取优先级操作也会让 deny 保持胜出。
		v = VerdictDeny
	case 49:
		// 结构化决策码：决策由 stdout 的 permissionDecision 承载，
		// 在下方 JSON 通道统一解析，此处不直接赋值。
	}

	if d := permissionDecisionVerdict(o.Stdout); d != VerdictNone {
		v = higherVerdict(v, d)
	}
	return v
}

// permissionDecisionVerdict 在 stdout 中扫描所有可能的 JSON 对象并取其中最强的
// permissionDecision 裁决。逐 '{' 解码既能跳过钩子常见的"先打日志后打 JSON"前缀，
// 也能覆盖嵌套对象；对同一文本中的多个决策仍遵守 deny 绝对优先。
func permissionDecisionVerdict(stdout string) Verdict {
	v := VerdictNone
	for i := 0; i < len(stdout); i++ {
		// '{' 是 ASCII 字节，不可能出现在 UTF-8 多字节序列内部，
		// 因此该定界符扫描不会切开 CJK rune。
		if stdout[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(stdout[i:]))
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			// 该 '{' 并非合法 JSON 的起点（例如日志里的花括号），继续扫描。
			continue
		}
		if d, ok := verdictFromDecisionObject(obj); ok {
			v = higherVerdict(v, d)
		}
	}
	return v
}

// verdictFromDecisionObject 从单个已解码的 JSON 对象中提取权限决策，
// 先查顶层 permissionDecision，再查 hookSpecificOutput.permissionDecision。
func verdictFromDecisionObject(obj map[string]any) (Verdict, bool) {
	if v, ok := decisionFromValue(obj["permissionDecision"]); ok {
		return v, true
	}
	if nested, ok := obj["hookSpecificOutput"].(map[string]any); ok {
		if v, ok := decisionFromValue(nested["permissionDecision"]); ok {
			return v, true
		}
	}
	return VerdictNone, false
}

// rewritesFromStdout 从钩子 stdout 的 JSON 对象中提取 hookSpecificOutput 下的
// updatedInput / updatedOutput。值必须是合法 JSON（对象或字符串），且先经脱敏与
// 长度上限处理，避免钩子把超长或含凭据的内容注入工具执行。
func rewritesFromStdout(stdout string) (json.RawMessage, json.RawMessage) {
	for i := 0; i < len(stdout); i++ {
		if stdout[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(stdout[i:]))
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			continue
		}
		nested, _ := obj["hookSpecificOutput"].(map[string]any)
		var in, out json.RawMessage
		if nested != nil {
			in = sanitizeRewrite(nested["updatedInput"])
			out = sanitizeRewrite(nested["updatedOutput"])
		}
		if in == nil {
			in = sanitizeRewrite(obj["updatedInput"])
		}
		if out == nil {
			out = sanitizeRewrite(obj["updatedOutput"])
		}
		if len(in) > 0 || len(out) > 0 {
			return in, out
		}
	}
	return nil, nil
}

// sanitizeRewrite 把原始 JSON 值规约为可安全使用的改写载荷：必须是合法对象或
// 字符串，序列化后按字节上限在 rune 边界截断，并做基本凭据脱敏。
func sanitizeRewrite(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	switch value.(type) {
	case map[string]any, string:
	default:
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	if len(raw) > rewriteClipBytes {
		return nil // 超长改写直接忽略，避免截断出非法 JSON
	}
	return json.RawMessage(raw)
}

// decisionFromValue 把 permissionDecision 的字符串值归一为裁决，
// 仅接受 allow / deny（忽略大小写与首尾空白），其它取值均视为无信号。
func decisionFromValue(value any) (Verdict, bool) {
	text, ok := value.(string)
	if !ok {
		return VerdictNone, false
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "allow":
		return VerdictAllow, true
	case "deny":
		return VerdictDeny, true
	default:
		return VerdictNone, false
	}
}

// higherVerdict 返回两个裁决中优先级更高者。借助 Verdict 的数值顺序，
// 它等价于取较大值，从而保证 deny > allow > none。
func higherVerdict(a, b Verdict) Verdict {
	if a > b {
		return a
	}
	return b
}

// AggregateVerdict 聚合一组裁决，返回优先级最高者：只要出现 VerdictDeny 就返回
// VerdictDeny，否则出现 VerdictAllow 返回 VerdictAllow，其余（含 nil）返回 VerdictNone。
//
// 这是纯函数：不读写状态、不改动切片。由于聚合等价于"取最大值"，它与元素的
// 顺序无关（无序）、对重复元素不敏感（幂等），且多次分批聚合与一次性聚合结果一致。
func AggregateVerdict(vs []Verdict) Verdict {
	v := VerdictNone
	for _, x := range vs {
		if x > v {
			v = x
		}
	}
	return v
}

// AdditionalContextFromOutcome 从钩子输出中提取要注入下一轮模型的附加上下文。
//
// 这是纯函数：不读写全局状态、不产生 I/O、不改动传入的 Outcome；给定相同输入
// 总是返回相同结果。它扫描 stdout 中每个可能的 JSON 对象，识别两种等价写法：
// 顶层的 additionalContext，以及 Claude 风格的
// hookSpecificOutput.additionalContext；同一对象内顶层优先，多个对象中取第一个
// 非空且合法的结果（扫描顺序与 stdout 中出现顺序一致）。
//
// 安全约束（防注入 / 防超长）：
//   - 值必须是字符串，缺失、类型不符或非法 JSON 一律返回空串；
//   - 先经 security.Redact 脱敏（剥离凭据形态与不可见 Unicode Tag），再去空白，
//     最后按字节上限在 rune 边界截断，故 CJK 文本不会被切成半个 rune；
//   - 超时（TimedOut）的结果直接返回空串：超时是执行故障，其 stdout 不可信。
//
// 明确不解析 systemMessage / suppressOutput 等其它字段。
func AdditionalContextFromOutcome(o Outcome) string {
	if o.TimedOut {
		return ""
	}
	return additionalContextFromStdout(o.Stdout)
}

// additionalContextFromStdout 在 stdout 中逐 '{' 扫描 JSON 对象，返回第一个
// 非空且合法的 additionalContext 文本。逐 '{' 解码既能跳过钩子常见的
// "先打日志后打 JSON" 前缀，也能覆盖嵌套对象。
func additionalContextFromStdout(stdout string) string {
	for i := 0; i < len(stdout); i++ {
		// '{' 是 ASCII 字节，不可能出现在 UTF-8 多字节序列内部，
		// 因此该定界符扫描不会切开 CJK rune。
		if stdout[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(stdout[i:]))
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			// 该 '{' 并非合法 JSON 的起点（例如日志里的花括号），继续扫描。
			continue
		}
		if text, ok := additionalContextFromObject(obj); ok && text != "" {
			return text
		}
	}
	return ""
}

// additionalContextFromObject 从单个已解码的 JSON 对象中提取附加上下文，
// 先查顶层 additionalContext，再查 hookSpecificOutput.additionalContext。
// 顶层值非法（缺失或类型不符）时才回退到嵌套形态。
func additionalContextFromObject(obj map[string]any) (string, bool) {
	if text, ok := sanitizeAdditionalContext(obj["additionalContext"]); ok {
		return text, true
	}
	if nested, ok := obj["hookSpecificOutput"].(map[string]any); ok {
		if text, ok := sanitizeAdditionalContext(nested["additionalContext"]); ok {
			return text, true
		}
	}
	return "", false
}

// sanitizeAdditionalContext 把 additionalContext 的原始 JSON 值规约为可安全
// 注入的文本：仅接受非空字符串，先脱敏再去空白，最后按字节上限在 rune 边界
// 截断。非字符串值、空值或全空白值视为无信号。
func sanitizeAdditionalContext(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(security.Redact(text))
	if text == "" {
		return "", false
	}
	return clipRunes(text, additionalContextClipLimit), true
}
