package tools

import "strings"

// 本文件提供两个只读谓词，供无法 import tools 的调用方（例如
// agent / app 层）在会话压缩、裁剪时判断一条工具结果是否携带
// output-spill 恢复标记。两者都是纯函数：不写文件、不改入参、
// 不改变任何现有工具的行为。

// HasSpillMarker 报告 s 中是否出现了 output-spill 恢复标记。
//
// 这是一次有意的"过包含"判断：它只匹配标记前缀 spillMarkerOpen，
// 并不验证标记是否完整、是否指向真实写出的产出。不可信工具输出里
// 恰好出现该前缀时同样会返回 true。对调用方（会话压缩/裁剪）而言，
// 过包含只会让本可裁剪的一条消息多保留一次，安全；反过来若漏判了
// 真实标记，则可能把恢复信息裁掉，令被省略的输出再无法取回。
// 因此这里宁可过包含，绝不可漏包含。
func HasSpillMarker(s string) bool {
	return strings.Contains(s, spillMarkerOpen)
}

// SpillMarkerPath 返回 s 中第一个完整 output-spill 标记公布的恢复
// 路径；没有完整标记（或标记未携带路径）时返回空串。
//
// 它复用 spillResultArtifacts，因而与溢出幂等守卫共用同一套标记
// 结构校验：省略计数必须是纯数字、必须带 "; full output: " 链接、
// 必须以 "]\n" 闭合。形似标记的伪造文本不会产出路径。
// 注意本函数只解析标记字面量，不做文件存在性或目录边界校验
// （那需要 dir 上下文）；调用方若要真正读取，应再走 SpillRecovery
// 的校验链。
func SpillMarkerPath(s string) string {
	paths := spillResultArtifacts(s)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}
