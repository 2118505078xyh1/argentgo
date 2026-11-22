// Package pshell 实现阶段1的持久化 shell 内核：通过长驻管道 shell 逐次执行
// 命令，并用带随机 nonce 的哨兵行从字节流中切分每次命令的输出。
// 本包刻意不依赖 PTY：管道模式没有回显，哨兵切分因此可靠。
package pshell

import (
	"strconv"
	"strings"
)

// markerPrefix 是哨兵行前缀。完整哨兵形如 __A_<nonce>|<cwd>|<venv>|<code>\n，
// nonce 由 crypto/rand 生成，命令无法预测，因此伪造的哨兵不会被尾锚定命中。
const markerPrefix = "__A_"

// splitProbe 从长驻 shell 的累积输出里切出一次命令的 stdout 与探针负载。
//
// 尾锚定：用 strings.LastIndex 定位最后一次出现的哨兵（真正的探针总是最后
// 打印的），这样命令先前输出的伪造哨兵不会干扰切分。定位后向前退到行首取
// stdout，向后到 \n 取负载，再按 "|" 拆成 4 段并解析退出码。
//
// 校验点：nonce 必须完整、必须凑齐 4 段、行尾必须有完整换行；任一不满足即
// 视为输出被截断，返回 ok=false，调用方应继续读取或重建会话。
//
// 残余风险：cwd/venv 采用 "|" 分隔，若路径本身含 "|" 会被误切分——阶段1
// 接受此风险（工作区路径由上层约束），后续版本可改用长度前缀编码。
func splitProbe(out, nonce string) (stdout, cwd, venv string, code int, ok bool) {
	marker := markerPrefix + nonce + "|"
	idx := strings.LastIndex(out, marker)
	if idx < 0 {
		return "", "", "", 0, false
	}

	// 向后找行尾：没有完整换行说明输出被截断。
	rest := out[idx+len(marker):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return "", "", "", 0, false
	}
	line := out[idx : idx+len(marker)+nl]
	line = strings.TrimSuffix(line, "\r") // Windows 下 CRLF，去掉 \r 才能解析退出码

	// 向前退到行首：行首之前的字节才是命令真正的 stdout。
	// 若命令最后一行未以换行结尾，该残行会随哨兵一起被丢弃——这是管道
	// 模式无前导换行时的已知边界，阶段1不做特殊处理。
	lineStart := idx
	for lineStart > 0 && out[lineStart-1] != '\n' {
		lineStart--
	}
	stdout = out[:lineStart]

	parts := strings.SplitN(line, "|", 4)
	if len(parts) != 4 {
		return "", "", "", 0, false
	}
	// nonce 必须完整：第一段恰是前缀+nonce（多一个或少一个字符都拒绝）。
	if parts[0] != markerPrefix+nonce {
		return "", "", "", 0, false
	}
	cwd, venv = parts[1], parts[2]
	code, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", "", "", 0, false
	}
	return stdout, cwd, venv, code, true
}
