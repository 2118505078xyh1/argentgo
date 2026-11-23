package app

import "strings"

// repoMapReadOnlyHint 返回一段中文只读声明，用于告知模型 repo map 仅作只读参考。
// 核心语义：文件仅仅因为出现在 repo map 中，并不构成对它的编辑授权；编辑任何文件前，
// 必须先读取该文件并把内容加入上下文。该函数是纯函数，返回值稳定，便于测试与复用。
func repoMapReadOnlyHint() string {
	return "repo map 仅供参考：文件仅因出现在 repo map 中，并不构成对它的编辑授权；编辑任何文件前，必须先读取该文件并把内容加入上下文。"
}

// repoMapHintEnabled 判断 repo map 只读声明是否启用。
// 默认开启：env 为 nil、未设置或值不为 "0" 时都返回 true；
// 只有值（首尾修剪后）显式为 "0" 时才关闭。
func repoMapHintEnabled(env func(string) string) bool {
	if env == nil {
		return true
	}
	return strings.TrimSpace(env("ARGENT_REPO_MAP_HINT")) != "0"
}
