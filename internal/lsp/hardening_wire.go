package lsp

import (
	"os"
	"strings"
)

// LSP 客户端进程硬化 opt-in。默认关闭，保持原有环境拼装与单进程 Kill 行为。
const (
	lspSanitizeEnvEnv = "ARGENT_LSP_SANITIZE_ENV"
	lspKillTreeEnv    = "ARGENT_LSP_KILL_TREE"
)

func lspSanitizeEnvEnabled() bool {
	return strings.TrimSpace(os.Getenv(lspSanitizeEnvEnv)) == "1"
}

func lspKillTreeEnabled() bool {
	return strings.TrimSpace(os.Getenv(lspKillTreeEnv)) == "1"
}
