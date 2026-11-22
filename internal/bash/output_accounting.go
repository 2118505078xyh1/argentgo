package bash

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// outputAccounting 控制是否在静默输出时附加对账标注。默认关闭：关闭时
// bash 工具的 "(no output)" 分支逐字节保持现状，不改变任何既有 run 的
// 可观察输出。这是进程级 opt-in，由调用方在启动时按需开启。
// 底层用 atomic.Bool，确保开启/关闭与并发的工具调用之间无数据竞争。
var outputAccounting atomic.Bool

// SetOutputAccounting 打开或关闭输出对账标注。
// 关闭（默认）时 OutputIntegrityNote 恒返回空串，调用方据此保持原输出不变。
func SetOutputAccounting(enabled bool) {
	outputAccounting.Store(enabled)
}

// OutputIntegrityNote 在原始输出有字节、却因清洗而归零时生成一条简短标注，
// 揭示"原始有 N 字节但送达模型的内容为空"这一损失。R31 的教训是静默缺输出
// 代价高：合法零输出与"清洗吞掉一切"必须可区分，但又不重试副作用命令。
//
// raw 是原始输出，rawBytes 是调用方按字节口径测得的原始长度（len(output)），
// delivered 是清洗/裁剪后即将送达模型的正文。开关关闭时恒返回空串，调用方
// 据此不改任何输出字节。开启时：
//   - rawBytes == 0：合法零输出，返回中性标注 [output: 0 bytes]；
//   - rawBytes > 0 且 delivered 为空（或仅空白）：清洗归零，返回
//     [output: N bytes raw → 0 lines delivered, sanitized empty]；
//   - 其它情况（delivered 非空）：返回空串——超长裁剪已有独立 marker，不重复。
//
// 本函数是纯函数：不修改入参、不产生 I/O。标注由固定文案、十进制整数与一个
// 合法多字节字符（→，U+2192）拼成，长度有界且始终是合法 UTF-8，绝不吞掉
// 任何原始输出字节。
func OutputIntegrityNote(raw, delivered string, rawBytes int) string {
	if !outputAccounting.Load() {
		return ""
	}
	if rawBytes == 0 {
		return "[output: 0 bytes]"
	}
	if strings.TrimSpace(delivered) == "" {
		return "[output: " + strconv.Itoa(rawBytes) + " bytes raw → 0 lines delivered, sanitized empty]"
	}
	return ""
}

// bashNoOutput 在 bash 工具即将返回 "(no output)" 前调用。开关关闭时
// 逐字节返回现状的 "(no output)"；开启且对账标注非空时，把标注附在其后。
// raw 是原始输出，delivered 是清洗后的正文，字节数按原始输出计算。
func bashNoOutput(raw, delivered string) string {
	note := OutputIntegrityNote(raw, delivered, len(raw))
	if note == "" {
		return "(no output)"
	}
	return "(no output)\n" + note
}
