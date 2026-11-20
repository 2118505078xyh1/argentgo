package app

import "strings"

// repoMapReadOnlyHint 返回一段中文只读声明，用于告知模型 repo map 仅作只读参考。
// 核心语义：文件仅仅因为出现在 repo map 中，并不构成对它的编辑授权；编辑任何文件前，
// 必须先读取该文件并把内容加入上下文。该函数是纯函数，返回值稳定，便于测试与复用。
func repoMapReadOnlyHint() string {
	return "repo map 仅供参考：文件仅因出现在 repo map 中，并不构成对它的编辑授权；编辑任何文件前，必须先读取该文件并把内容加入上下文。"
}

// repoMapHintEnabled 判断 repo map 只读声明是否选择性启用。
// 默认关闭（未设置或值不为 "1"），从而保持字节精确的系统提示断言不变；
// 只有设置 ARGENT_REPO_MAP_HINT=1 时才返回 true。
func repoMapHintEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_REPO_MAP_HINT")) == "1"
}
