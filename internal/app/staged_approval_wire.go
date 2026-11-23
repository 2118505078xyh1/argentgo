package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// stagedApprovalEnv 是分阶段审批状态机的独立门控变量。
const stagedApprovalEnv = "ARGENT_STAGED_APPROVAL"

func stagedApprovalEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(stagedApprovalEnv)) == "1"
}

// ensure the staged approver satisfies the security contract at compile time.
var _ security.StagedApprover = (*stagedApprover)(nil)
