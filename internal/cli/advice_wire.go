package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/auth"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// authStateFor 把 auth.Store.Status（面向存储的四种状态）映射到 advice 使用的
// auth.TokenState（面向用户的四种状态）。缺失令牌没有截止时间，按"状态未知"处理，
// 使用户得到一条可执行的检查命令，而不是沉默。
func authStateFor(s auth.Status) auth.TokenState {
	switch s {
	case auth.StatusExpired:
		return auth.TokenExpired
	case auth.StatusExpiring:
		return auth.TokenExpiring
	case auth.StatusValid:
		return auth.TokenValid
	default:
		return auth.TokenUnknown
	}
}

// appendAuthExpiryAdvice 在 401 / needs_auth 失败后追加一行更具体的凭证建议。
// 分类用 llm.ClassifyFailure 的封闭类别而非自由文本匹配；取值失败时静默跳过，
// 绝不因为读不到凭证存储而改变主错误输出或引入新的失败。
func (a App) appendAuthExpiryAdvice(w io.Writer, err error, provider string) {
	if err == nil || !cliHintsEnabled(a.IO.Env) {
		return
	}
	if llm.ClassifyFailure(err).Kind != llm.FailureAuth {
		return
	}
	store, openErr := authStore(a)
	if openErr != nil {
		return
	}
	advice := authExpiryAdvice(provider, authStateFor(store.Status(provider, time.Now())))
	if advice != "" {
		fmt.Fprintln(w, advice)
	}
}

// appendConfirmAdvice 在审批不可用/被拒、以及非交互确认失败后追加 confirmAdvice。
// isTTY 由调用方注入（生产路径用 hasTerminal），使测试无需真实终端即可覆盖两条分支。
func (a App) appendConfirmAdvice(w io.Writer, err error, isTTY bool) {
	if err == nil || !cliHintsEnabled(a.IO.Env) {
		return
	}
	lower := strings.ToLower(err.Error())
	if !errors.Is(err, security.ErrApprovalUnavailable) &&
		!strings.Contains(lower, "approval") &&
		!strings.Contains(lower, "interactive") {
		return
	}
	if advice := confirmAdvice("", isTTY); advice != "" {
		fmt.Fprintln(w, advice)
	}
}

// appendRuntimeAdvice 是 runPrompt 错误路径的统一接线点：在既有的
// writeActionableErrorEnv 之后调用，保持其字节级输出与单测 golden 不变。
func (a App) appendRuntimeAdvice(w io.Writer, err error, provider string, isTTY bool) {
	a.appendAuthExpiryAdvice(w, err, provider)
	a.appendConfirmAdvice(w, err, isTTY)
}
