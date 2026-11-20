package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// 验证结果分类使用的稳定字面量。全部是小写 ASCII，不随语言、区域或错误文本变化，
// 便于日志检索与跨层传递。
const (
	verifyOutcomeCode    = "code"
	verifyOutcomeInfra   = "infra"
	verifyOutcomeTimeout = "timeout"
	verifyOutcomeUnknown = "unknown"
)

// classifyVerifyOutcome 把一次验证运行的失败归入 code / infra / timeout / unknown 四类之一，
// 用于把当前被折叠成单一 FAIL 的结论拆成"代码错误 / 基础设施故障 / 超时"。
//
// 参数含义：
//   - err 来自 exec.Cmd.Run；nil 表示进程正常结束；
//   - timedOut 由调用方根据 runCtx.Err() == context.DeadlineExceeded 判定。它优先于 err 的类型：
//     被截止时间杀掉的进程通常表现为 *exec.ExitError，若不先看这个标志就会被误报为代码错误；
//   - exitCode 是被执行进程的退出码；没有可用退出码（派生失败、被信号杀死等）时约定为负数。
//
// 本函数只依赖错误类型、哨兵错误与显式的布尔/整数参数，从不读取错误文本，
// 因此与 CJK 或其它任何语言的本地化消息无关，且对相同输入总是返回相同结果。
//
// 判定顺序（前者优先）：
//  1. timedOut 为真，或 err 包装了 context.DeadlineExceeded -> timeout；
//  2. err 为 nil -> unknown（成功运行没有失败类别可归）；
//  3. 进程派生/查找失败（exec.ErrNotFound、*exec.Error、*os.PathError、权限不足、取消）
//     -> infra；这些错误发生在验证代码真正运行之前，重试只会重复同样的故障；
//  4. *exec.ExitError 或 exitCode > 0 -> code（子进程确实运行并给出了非零结论）；
//  5. 其余 -> unknown。
func classifyVerifyOutcome(err error, timedOut bool, exitCode int) string {
	if timedOut || errors.Is(err, context.DeadlineExceeded) {
		return verifyOutcomeTimeout
	}
	if err == nil {
		return verifyOutcomeUnknown
	}
	if isVerifyInfraError(err) {
		return verifyOutcomeInfra
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) || exitCode > 0 {
		return verifyOutcomeCode
	}
	return verifyOutcomeUnknown
}

// isVerifyInfraError 判断错误是否发生在子进程执行验证代码之前。
// 只做类型与哨兵检查，刻意不检查错误字符串，以免本地化消息影响分类结果。
func isVerifyInfraError(err error) bool {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, os.ErrPermission) || errors.Is(err, context.Canceled) {
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return true
	}
	return false
}

// verifyOutcomeTag 把分类映射为可附加在失败输出前的短标记。
// 只有三类明确的失败才有标记；unknown（包括成功运行）返回空串，
// 使调用方在未获得有效分类时保持输出逐字节不变。
func verifyOutcomeTag(class string) string {
	switch class {
	case verifyOutcomeCode:
		return "[verify-outcome: code]"
	case verifyOutcomeInfra:
		return "[verify-outcome: infra]"
	case verifyOutcomeTimeout:
		return "[verify-outcome: timeout]"
	default:
		return ""
	}
}

// verifyOutcomeEnabled 判断结果分类标记是否选择性启用。默认关闭：env 为 nil、
// 未设置或值不为 "1" 时都返回 false，保证现有验证输出逐字节不变。
// 只有设置 ARGENT_VERIFY_OUTCOME=1 时才开启，与本包其它可选层的开关约定一致。
func verifyOutcomeEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_VERIFY_OUTCOME")) == "1"
}
