package bash

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// maxForegroundSleep 是前台 sleep 的拒绝阈值（秒）。达到该值的首条
// 前台 sleep 会在工具派发前被拒绝，避免模型用 `sleep 270; tail log`
// 空等一整轮工具时间才等来超时。
const maxForegroundSleep = 30

// sleepGuardEnabled 报告前台长 sleep 守卫是否开启。默认开启；仅当
// ARGENT_SLEEP_GUARD=0（首尾空白修剪后）时紧急关闭，回到旧的直接
// 执行路径。它不是 opt-in：无需用户选择，=0 只是回退阀。
func sleepGuardEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_SLEEP_GUARD")) != "0"
}

// literalWord 仅在单词恰好由单个未加引号的字面片段组成时返回其文本。
// 含变量展开、引号、命令替换的单词一律返回 false，使守卫对这些复杂
// 形式保守放行——它们可能在运行期展开成任意秒数，静态判断不可靠。
func literalWord(w *Word) (string, bool) {
	if w == nil || len(w.Parts) != 1 {
		return "", false
	}
	lit, ok := w.Parts[0].(*LitPart)
	if !ok {
		return "", false
	}
	return lit.Text, true
}

// leadingForegroundSleep 沿 AST 左脊找到第一条会被执行的简单命令，
// 当它恰为字面 `sleep N` 且 N 为非负整数时返回秒数。
//
// 只沿 OpSeq(;)/OpAnd(&&)/OpOr(||) 的左分支下探：这些操作符的左命令
// 确实先执行，因此 `sleep 270; tail log` 会被命中。管道(OpPipe)、
// 后台(&，解析期打为 UnsupportedCmd)、命令替换与变量一律保守放行。
func leadingForegroundSleep(f *File) (int, bool) {
	if f == nil || f.Stmt == nil {
		return 0, false
	}
	stmt := f.Stmt
	for {
		bin, ok := stmt.Cmd.(*BinaryCmd)
		if !ok {
			break
		}
		if bin.Op == OpPipe {
			return 0, false
		}
		stmt = bin.X
	}
	call, ok := stmt.Cmd.(*CallExpr)
	if !ok || len(call.Args) < 2 || len(call.Assigns) != 0 {
		return 0, false
	}
	prog, ok := literalWord(call.Args[0])
	if !ok || prog != "sleep" {
		return 0, false
	}
	secsText, ok := literalWord(call.Args[1])
	if !ok {
		return 0, false
	}
	secs, err := strconv.Atoi(secsText)
	if err != nil || secs < 0 {
		return 0, false
	}
	return secs, true
}

// sleepGuardFor 判断命令是否应被前台长 sleep 守卫拒绝。它只使用自研
// shell 已有的词法/语法结果（ParseForRun）；解析失败即保守放行，
// 后台形式因含 Unsupported 构造而天然落在此分支。
func sleepGuardFor(command string) (int, bool) {
	parsed, ok := ParseForRun(command)
	if !ok {
		return 0, false
	}
	secs, guarded := leadingForegroundSleep(parsed)
	if !guarded || secs < maxForegroundSleep {
		return 0, false
	}
	return secs, true
}

// sleepGuardMessage 给出模型可读的拒绝文案：以 `Not run:` 开头，并列出
// 两种等价替代（后台执行 + task_output/wait 轮询，或 until 循环轮询）。
func sleepGuardMessage(secs int) string {
	return fmt.Sprintf("Not run: leading foreground `sleep %d` (>= %ds) would waste a whole tool round waiting; "+
		"run it in the background and poll with task_output/wait, "+
		"or poll a condition with `until <check>; do sleep 5; done`.",
		secs, maxForegroundSleep)
}

// rejectLongForegroundSleep 在命令被派发前执行守卫；开启且命中时返回
// 一个以 `Not run:` 开头的错误，调用方把它直接作为工具错误回报模型。
func rejectLongForegroundSleep(command string) error {
	if !sleepGuardEnabled() {
		return nil
	}
	if secs, guarded := sleepGuardFor(command); guarded {
		return errors.New(sleepGuardMessage(secs))
	}
	return nil
}
