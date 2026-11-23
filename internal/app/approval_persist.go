package app

import "strings"

// 本文件承载 TODO #127「可持久化审批/作用域规则」的 app 层 opt-in 接线。
// security 包只提供构件（FileBackedScopedApprover），默认不会被构造；
// 只有本开关开启时才把它接到审批链上，因此默认路径零文件 I/O、逐字节不变。

// approvalPersistEnv 是持久化审批规则的总开关；仅值为 "1"（允许首尾空白）时启用。
const approvalPersistEnv = "ARGENT_APPROVAL_PERSIST"

// approvalPersistEnabled 报告是否启用持久化审批规则。默认关闭。
func approvalPersistEnabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env(approvalPersistEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
