package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// 审批事件基于 EventNotice 构建（见 events.go）：事件种类集合由该文件掌管，
// 因此这里不新增种类，而是保留一个稳定的文本前缀，并把结构化的 JSON 负载放进 Event.Text。
// 这样这些条目仍走普通的 agent 流，意味着每一种传输方式——进程内的 Emit 回调、
// --rpc 以及原生桥接（仅转发文本增量/通知）——都无需改动协议即可投递它们。
//
// 供原生桥接（以及任何其他纯文本消费者）使用的线上契约：
//
//	approval:request  {"v":1,"tool":"<tool>","digest":"<hash>","id":"<id>"}
//	approval:resolved {"v":1,"tool":"<tool>","digest":"","id":"<id>","granted":true|false}
//
// 前缀后面紧跟一个 JSON 对象。"v" 是 schema 版本（当前为 1）。"id" 用于把已解决事件
// 与其请求关联起来（调用方通常传入发起该请求的工具调用 id）。"digest" 是对确切参数的
// 稳定哈希，且始终经过脱敏/限长。"granted" 只出现在已解决事件中。ParseApprovalEvent
// 是唯一的解码器；对无关通知、错误前缀和格式错误的负载它会返回 ok=false，
// 这样消费者就能安全地忽略其他一切内容。
const (
	approvalRequestPrefix  = "approval:request "
	approvalResolvedPrefix = "approval:resolved "
	approvalWireVersion    = 1

	// approvalFieldMaxRunes 限制调用方提供的工具名、参数摘要和关联 id 的长度。
	// 十六进制 sha256 摘要为 64 个 rune，因此正常输入会原样通过；
	// 该上限只对畸形/恶意的值起作用。
	approvalFieldMaxRunes = 128
)

// approvalWire 是两个事件构造函数和解析器共用的 JSON 负载。
// Granted 是指针，这样解码器就能区分"拒绝"（false）与字段缺失（nil => 格式错误）。
type approvalWire struct {
	V       int    `json:"v"`
	Tool    string `json:"tool"`
	Digest  string `json:"digest"`
	ID      string `json:"id"`
	Granted *bool  `json:"granted,omitempty"`
}

// ApprovalRequestedEvent 构造一条通知，宣告正在请求对 tool 的审批。
// argsDigest 应当是对确切参数的稳定哈希（例如十六进制 sha256）；它会被防御性地限长
// 并剥离控制字节，因此恶意值无法注入额外的一行记录。id 用于把之后的已解决事件
// 关联回本次请求（通常是工具调用 id）。
func ApprovalRequestedEvent(tool, argsDigest, id string) Event {
	// 对仅含字符串/布尔/整数的结构体做序列化不会失败。
	payload, _ := json.Marshal(approvalWire{
		V:      approvalWireVersion,
		Tool:   redactApprovalField(tool),
		Digest: redactApprovalField(argsDigest),
		ID:     redactApprovalField(id),
	})
	return Event{Kind: EventNotice, Text: approvalRequestPrefix + string(payload)}
}

// ApprovalResolvedEvent 构造一条通知，记录由 id 标识的审批请求的结果。
// tool 和 id 必须与请求事件一致，这样消费者才能把两者配成一条记录项。
func ApprovalResolvedEvent(tool, id string, granted bool) Event {
	// 对仅含字符串/布尔/整数的结构体做序列化不会失败。
	payload, _ := json.Marshal(approvalWire{
		V:       approvalWireVersion,
		Tool:    redactApprovalField(tool),
		ID:      redactApprovalField(id),
		Granted: &granted,
	})
	return Event{Kind: EventNotice, Text: approvalResolvedPrefix + string(payload)}
}

// ParseApprovalEvent 解码由 ApprovalRequestedEvent 或 ApprovalResolvedEvent 产生的事件。
// kind 为 "request" 或 "resolved"；granted 仅对 "resolved" 有意义。
// 对任何其他事件种类、未知前缀或格式错误的负载都会返回 ok=false，
// 这样消费者就能忽略无关通知。
func ParseApprovalEvent(e Event) (kind, tool, id string, granted bool, ok bool) {
	if e.Kind != EventNotice {
		return "", "", "", false, false
	}
	switch {
	case strings.HasPrefix(e.Text, approvalRequestPrefix):
		var p approvalWire
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e.Text, approvalRequestPrefix)), &p); err != nil {
			return "", "", "", false, false
		}
		if p.V != approvalWireVersion || p.Tool == "" || p.ID == "" {
			return "", "", "", false, false
		}
		return "request", p.Tool, p.ID, false, true
	case strings.HasPrefix(e.Text, approvalResolvedPrefix):
		var p approvalWire
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e.Text, approvalResolvedPrefix)), &p); err != nil {
			return "", "", "", false, false
		}
		if p.V != approvalWireVersion || p.Tool == "" || p.ID == "" || p.Granted == nil {
			return "", "", "", false, false
		}
		return "resolved", p.Tool, p.ID, *p.Granted, true
	default:
		return "", "", "", false, false
	}
}

// redactApprovalField 丢弃控制字节和 ANSI 转义序列，使一个值永远无法注入额外的
// 记录行或转义码，然后在 rune 边界处用省略号标记截断，使总长度保持在
// approvalFieldMaxRunes 之内。（线上负载是 JSON 编码的，换行已被转义；
// 剥离控制字符对纯文本消费者而言是纵深防御。）
func redactApprovalField(s string) string {
	s = stripApprovalControls(s)
	if utf8.RuneCountInString(s) <= approvalFieldMaxRunes {
		return s
	}
	return truncateApprovalRunes(s, approvalFieldMaxRunes-1) + "…"
}

// stripApprovalControls 移除 ANSI CSI/OSC/裸 ESC 序列及控制字节，保留可打印文本。
// 它绝不会输出换行符或 NUL。
func stripApprovalControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC：消耗整个转义序列。
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI：直到 0x40..0x7e 范围内的终止字节。
				i++
				for i < len(s) {
					last := s[i]
					i++
					if last >= 0x40 && last <= 0x7e {
						break
					}
				}
			case ']': // OSC：直到 BEL（0x07）或 ST（ESC '\'）。
				i++
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default: // 其他转义：丢弃 ESC 及这一个字节。
				i++
			}
			continue
		}
		if c < 0x20 || c == 0x7f { // ASCII 控制字节，包括 \n 和 \r。
			i++
			continue
		}
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x80 && r <= 0x9f { // 以 UTF-8 编码的 C1 控制字节。
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// truncateApprovalRunes 返回 s 的前 max 个 rune，绝不会切断 UTF-8 序列。
func truncateApprovalRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	kept := 0
	for pos := range s {
		if kept == max {
			return s[:pos]
		}
		kept++
	}
	return s
}
