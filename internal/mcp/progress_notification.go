package mcp

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode"
)

// 本文件提供把 MCP 服务器发来的 notifications/progress 通知解析为
// 结构化进度的纯函数。长工具执行期间服务器会持续推送进度，UI 需要
// 一个干净、有界、可安全直接显示的三元组（progress / total / message）。
//
// 设计要点：
//   - 输入 params 必须是 JSON 对象；非对象、空载荷、非法 JSON 一律失败；
//   - progress 是必填且必须是有限数字（MCP 规范要求），缺失或非法失败；
//   - total 与 message 是可选字段：缺省时返回零值，但一旦出现就必须是
//     正确类型且合法，否则整条通知视为非法，避免把半解析结果交给 UI；
//   - message 在进入显示层前做净化：控制字符替换为空格、去除首尾空白，
//     并按 rune 截断，保证 CJK 等多字节字符不会被切在中间，且长度有界。

// progressMessageMaxRunes 限制进度提示消息的最大 rune 数。
// 以 rune 而非字节计，既约束显示宽度，又天然保护 CJK 等宽字符不被切断。
const progressMessageMaxRunes = 128

// parseProgressNotification 把 notifications/progress 的 params 解析为
// 结构化的进度数据。它是纯函数：无 I/O、无全局状态，同一输入恒定返回
// 同一输出；任何失败路径都返回零值且 ok=false。
//
// 返回值的语义：
//   - progress：当前进度值，必填；
//   - total：总量，缺失时为 0（表示不确定总量）；
//   - message：人类可读的进度提示，缺失时为空串，净化后有界；
//   - ok：解析是否成功。
func parseProgressNotification(params json.RawMessage) (progress, total float64, message string, ok bool) {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		// 只接受 JSON 对象；null / 数组 / 标量都不是合法的 params。
		return 0, 0, "", false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return 0, 0, "", false
	}

	// progress 必填。
	progressRaw, present := fields["progress"]
	if !present {
		return 0, 0, "", false
	}
	progress, ok = decodeProgressNumber(progressRaw)
	if !ok {
		return 0, 0, "", false
	}

	// total 可选，缺省视为不确定总量（0）；出现则必须合法。
	if totalRaw, present := fields["total"]; present {
		total, ok = decodeProgressNumber(totalRaw)
		if !ok {
			return 0, 0, "", false
		}
	}

	// message 可选，缺省为空串；出现则必须是字符串并经过净化。
	if messageRaw, present := fields["message"]; present {
		message, ok = decodeProgressMessage(messageRaw)
		if !ok {
			return 0, 0, "", false
		}
	}

	return progress, total, message, true
}

// decodeProgressNumber 解码一个进度数值。它拒绝 null、非数字类型以及
// NaN / Inf（JSON 本身不允许这些字面量，但保留防线以覆盖手工构造的
// RawMessage），只接受有限的 float64。
func decodeProgressNumber(raw json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// decodeProgressMessage 解码并净化进度提示消息。null 或非字符串类型
// 视为非法；字符串内容中的控制字符被替换为空格，首尾空白被去掉，
// 再按 rune 截断到 progressMessageMaxRunes 以内。净化保证 CJK 等
// 多字节字符完整保留，且输出长度有界，可直接交给 UI 显示。
func decodeProgressMessage(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", false
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return "", false
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return truncateRunes(strings.TrimSpace(cleaned), progressMessageMaxRunes), true
}
