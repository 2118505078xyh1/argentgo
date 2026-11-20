package app

import "strings"

// parseTrustEnv 把 ARGENT_TRUST_PROJECT 的值解释为对项目级指令、MCP 服务和插件的布尔信任决定。
//
// 可识别的假值为 0、false、no 和 off；可识别的真值为 1、true、yes 和 on。
// 匹配不区分大小写且忽略两端空白。未设置（空）的值默认为 true，
// 使现有项目在其系统提示中保留 AGENTS.md/CLAUDE.md。任何其他值都会安全失败（不信任），
// 与 Resolve 历史上对 Resolved.TrustProject 使用的严格 `== "1"` 检查一致。
//
// 提示路径（trustFromEnv）和 Resolve 的 Resolved.TrustProject 决定都应调用这个辅助函数，
// 使同一个 ARGENT_TRUST_PROJECT 值不会在两条路径上产生相反的信任结论。
func parseTrustEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return false
	}
}
