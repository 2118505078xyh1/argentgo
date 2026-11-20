package tools

import "errors"

// errBinaryFile 在 read 工具拒绝明显是二进制的文件时返回。
// 它被包装进带路径的错误里，调用方可用 errors.Is 匹配；
// 错误信息本身就是给模型的可读提示，引导其换用合适的工具。
var errBinaryFile = errors.New("binary file detected: use an appropriate tool (e.g. a hex/byte viewer) instead of read")

// looksLikeBinary 判断一组字节是否更像二进制而非 UTF-8 文本。
//
// utf8.Valid 只能排除非法编码，无法识别“合法 UTF-8 的二进制”：
// 例如含 NUL 字节或整片 ASCII 控制字符的数据。这里的判据有两条：
//
//  1. 出现 NUL（0x00）即刻判为二进制——任何文本都不会包含 NUL，
//     而大量二进制格式都以它为结构字节（PNG、ELF、zip 等）。
//  2. 非空白 ASCII 控制字符（以及 DEL 0x7F）占比超过 10% 判为二进制。
//     换行、制表、回车、换页、垂直制表在文本里合法，明确排除，
//     因此 CJK、emoji（均是多字节高位序列）和正常文本不会被误判。
//
// 空数据不是二进制。
func looksLikeBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	control := 0
	for _, b := range data {
		if b == 0 {
			// NUL 是决定性的二进制标记，无需再统计比例。
			return true
		}
		if b < 0x20 {
			switch b {
			case '\t', '\n', '\v', '\f', '\r':
				// 文本中合法的控制字符（空白），不计入比例。
			default:
				control++
			}
		} else if b == 0x7f {
			control++
		}
	}
	// 用整数比较避免大文件上的乘法溢出；等价于 control/len(data) > 10%。
	return control > len(data)/10
}
