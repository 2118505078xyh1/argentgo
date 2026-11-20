package app

import "strings"

// verifyAutoRunHint 返回一段中文声明，告知模型编辑后验证命令会自动运行，
// 因此不必在回复中建议用户重复运行。hasVerify 为 false（未配置验证命令）时返回空串，
// 使未启用验证的运行在提示字节上与之前完全一致。该函数是纯函数，返回值稳定，
// 便于测试与复用。
func verifyAutoRunHint(hasVerify bool) string {
	if !hasVerify {
		return ""
	}
	return "编辑后会自动运行验证命令，不要在回复中建议用户重复运行"
}

// verifyAutoRunHintEnabled 判断验证自动运行声明是否选择性启用。
// 默认关闭（env 为 nil、未设置或值不为 "1"），从而保持字节精确的系统提示断言不变；
// 只有设置 ARGENT_VERIFY_AUTORUN_HINT=1 时才返回 true。
func verifyAutoRunHintEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_VERIFY_AUTORUN_HINT")) == "1"
}
