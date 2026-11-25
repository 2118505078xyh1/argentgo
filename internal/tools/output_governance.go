package tools

import (
	"strconv"

	"example.com/argentgo/internal/tokenize"
)

// OutputBudget 是对一次工具输出的预算诊断结果。它只回答"按当前配置处理会
// 发生什么"，不携带原始输出，也不触发任何裁剪或落盘。
type OutputBudget struct {
	// Limit 是生效的输出上限（字节）。非正入参会被归一化为 maxToolOutput，
	// 与 clipTo 的行为保持一致；调用方应读取本字段而非记住传入的 limit。
	Limit int
	// RawBytes 是原始输出的字节数，与 clip/spill 使用的字节口径一致。
	RawBytes int
	// Clipped 表示原始输出超过生效上限，需要裁剪。
	Clipped bool
	// Spilled 表示在启用溢出时，原始输出超过默认落盘阈值，因而"可能落盘"。
	// 它只是预测：本原语不写文件，真正是否落盘取决于运行时目录与自定义阈值。
	Spilled bool
	// TokenHint 是 tokenize.Estimate 给出的估计 token 成本（CJK/emoji 感知）。
	TokenHint int
}

// PlanOutput 是纯函数：只读取 raw，不修改任何入参、不产生 I/O，也不读取进程级
// 配置。Clipped 采用与 clipTo 相同的字节口径，并按同样规则把非正 limit 归一化
// 到 maxToolOutput；Spilled 用包内默认阈值 DefaultOutputSpillThreshold 判断
// "是否可能落盘"——本原语刻意不接收目录与自定义阈值，那属于 WithOutputSpill
// 的运行时配置。len 只做字节计数、不做切片，因此 CJK 文字与 emoji 不会被切坏；
// token 估算交给 CJK 感知的 tokenize.Estimate。
func PlanOutput(raw string, limit int, spillEnabled bool) OutputBudget {
	if limit <= 0 {
		limit = maxToolOutput
	}
	rawBytes := len(raw)
	return OutputBudget{
		Limit:     limit,
		RawBytes:  rawBytes,
		Clipped:   rawBytes > limit,
		Spilled:   spillEnabled && rawBytes > DefaultOutputSpillThreshold,
		TokenHint: tokenize.Estimate(raw),
	}
}

// Describe 返回预算的稳定中文摘要。输出只由 OutputBudget 的字段决定，不含原始
// 输出内容，也不访问 map、时间或环境，因此同一预算永远得到同一字符串；长度有界
// （仅几个十进制整数加固定文案），可安全写进日志或诊断信息。
func (b OutputBudget) Describe() string {
	clip := "无需裁剪"
	if b.Clipped {
		clip = "需要裁剪"
	}
	spill := "不落盘"
	if b.Spilled {
		spill = "可落盘"
	}
	return "原始 " + strconv.Itoa(b.RawBytes) + " 字节 / 上限 " + strconv.Itoa(b.Limit) +
		" 字节，约 " + strconv.Itoa(b.TokenHint) + " tokens，" + clip + "，" + spill
}
