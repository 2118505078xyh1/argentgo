package cli

import (
	"strconv"
	"strings"

	"example.com/argentgo/internal/tokenize"
)

// outputSizeHint 生成一条简短提示，告知用户即将把多少命令输出注入模型上下文。
//
// 它统计 content 的可视行数（以换行分隔；末尾换行不额外多算一行），并复用
// tokenize.Estimate 估算 token 成本。后者对中文、全角、emoji 等宽字符按每个
// 约一个 token 计，因此 CJK 文本不会被 chars/4 规则严重低估。
//
// 该函数是纯函数：只读参数，不产生 I/O、不访问全局状态，且不把 content 本身
// 嵌入返回值；返回的字符串长度有界（仅由两个十进制整数和固定文案构成，
// 与 content 大小无关）。因此它既能安全地用于表驱动测试，也能在将来由
// 命令输出注入路径 opt-in 调用，而不改变当前任何命令的字节级输出
// （当前尚未接线，见接线点报告）。
func outputSizeHint(content string) string {
	lines := 0
	if content != "" {
		lines = strings.Count(content, "\n") + 1
		if strings.HasSuffix(content, "\n") {
			lines--
		}
	}
	tokens := tokenize.Estimate(content)
	return "将把约 " + strconv.Itoa(lines) + " 行 / ~" + strconv.Itoa(tokens) + " tokens 的输出加入上下文"
}
