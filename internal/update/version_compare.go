package update

import "strings"

// compareVersions 按语义化版本优先级比较两个版本字符串，返回 -1、0 或 1
// （分别表示 left 小于、等于、大于 right）。比较遵循 "主版本.次版本.补丁"
// 顺序，支持可选的 v/V 前缀、一到三段数字核心以及点分隔的预发布标识符；
// 预发布版本的优先级低于其对应的正式版本，'+' 之后的构建元数据不参与比较。
//
// 与 CompareVersions 不同：只要任一字符串不是合法版本，本函数就退化为对
// 原始输入做字符串比较（strings.Compare），而不是把非法版本当作 0.0.0。
// 这样非语义化的标签也能得到稳定的全序。
//
// 本函数为纯函数：只依赖参数，不读写全局状态。解析与比较仅以 ASCII 字符
// 作为分隔符和字节序依据，不会在 UTF-8 多字节字符中间切分，因此对 CJK
// 输入是安全的。
func compareVersions(left, right string) int {
	first, firstErr := ParseVersion(left)
	second, secondErr := ParseVersion(right)
	if firstErr != nil || secondErr != nil {
		// 任一输入非法时退化为原始字符串比较，得到确定的字节序全序。
		return strings.Compare(left, right)
	}
	return first.Compare(second)
}
