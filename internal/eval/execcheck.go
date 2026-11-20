package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultExecTimeout = 30 * time.Second
	maxExecTimeout     = 120 * time.Second
	execOutputCap      = 64 * 1024
	execTruncMarker    = "...[output truncated]..."
	// execShellMeta 列出命令分隔符、重定向、替换和
	// 控制字符。这里不使用 shell，因此它们被拒绝作为纵深防御：未来的重构不得意外使它们生效。
	// 纵深防御：未来的重构不得意外使它们生效。
	execShellMeta = ";|&<>()$`'\";\n\r\x00^"
)

// 哨兵错误，使调用方能用 errors.Is 分类被拒绝的 fixture。
var (
	ErrExecEmptyCommand    = errors.New("eval: exec check: empty command")
	ErrExecCommandDenied   = errors.New("eval: exec check: command is not allowlisted")
	ErrExecGoSubcommand    = errors.New("eval: exec check: go subcommand must be test or build")
	ErrExecBadPath         = errors.New("eval: exec check: executable path must be relative and stay inside the workspace")
	ErrExecShellMeta       = errors.New("eval: exec check: shell metacharacters are not allowed")
	ErrExecDangerousGoFlag = errors.New("eval: exec check: go flag can run arbitrary binaries")
)

// ExecCheck 通过在种入的工作区内运行白名单命令来评分场景。它是
// ExecCheck 通过在种入的工作区内运行白名单命令来评分场景。它是严格选择加入的：eval harness 中没有任何内容会自行调用
// RunExecCheck，因此 fixture 必须显式指定一个。
//
// 安全模型：这是本地的、确定性的 harness，不是沙箱。没有
// OS 隔离、没有网络限制也没有用户切换。安全性来自四个边界：
// 来自四个边界：
//
//  1. Validate 必须接受该命令（go test/build 或相对 .exe）。
//  2. 不涉及 shell；argv 直接传给 os/exec。
//  3. Dir 始终是一次性临时工作区，绝不是真实仓库。
//  4. 进程在短暂且有上限的超时后被杀死。
//
// 把启用了 exec 检查的 fixture 当作可信代码，与
// 把启用了 exec 检查的 fixture 当作可信代码，与仓库已经运行的 Go 测试文件完全一样。它用于本地确定性 fixture 上的 pass@1-by-running，
// 而非对不可信提交评分。
type ExecCheck struct {
	// Command 是 "go" 或以 .exe 结尾的 fixture 本地相对路径。
	Command string `json:"command"`
	// Args 原样传给进程；对 "go" 必须以
	// Args 原样传给进程；对 "go" 必须以 "test" 或 "build" 开头。
	Args []string `json:"args"`
	// TimeoutMS 是毫秒截止时间：0 表示 30s，大于 120000 的值被限制在 120s。
	// 120000 的值被限制在 120s。
	TimeoutMS int `json:"timeout_ms"`
	// WantExitCode 是算作通过的退出状态。
	WantExitCode int `json:"want_exit_code"`
	// WantStdoutContains 设置后必须出现在捕获的 stdout 尾部。
	WantStdoutContains string `json:"want_stdout_contains,omitempty"`
}

// Validate 拒绝任何未明确列入白名单的命令。它被
// Validate 拒绝任何未明确列入白名单的命令。它被 RunExecCheck 再次调用，因此即使调用方忘记先验证，
// 畸形检查也无法绕过边界。
func (c ExecCheck) Validate() error {
	command := strings.TrimSpace(c.Command)
	if command == "" {
		return ErrExecEmptyCommand
	}
	if strings.ContainsAny(command, execShellMeta) {
		return fmt.Errorf("%w: command %q", ErrExecShellMeta, command)
	}

	if command == "go" {
		if len(c.Args) == 0 {
			return fmt.Errorf("%w: missing subcommand", ErrExecGoSubcommand)
		}
		switch c.Args[0] {
		case "test", "build":
		default:
			return fmt.Errorf("%w: %q", ErrExecGoSubcommand, c.Args[0])
		}
		return validateExecArgs(c.Args)
	}

	if err := validateFixtureExe(command); err != nil {
		return err
	}
	return validateExecArgs(c.Args)
}

// validateFixtureExe 只接受指向 fixture 本地 .exe 的相对路径。
// 绝对路径（包括 Windows 盘符相对路径）、".." 遍历以及
// 绝对路径（包括 Windows 盘符相对路径）、".." 遍历以及 sh/bash/curl 等未知裸命令都被拒绝。
func validateFixtureExe(command string) error {
	// 单靠 filepath.IsAbs 会漏掉像 "\tool.exe" 这样无盘符的根路径 Windows 路径。
	// 单靠 filepath.IsAbs 会漏掉像 "\tool.exe" 这样无盘符的根路径 Windows 路径。
	// Windows 将其相对于 *cmd.Dir 的盘符* 解析，而 RunExecCheck 把 cmd.Dir 设为一次性工作区，因此该命令
	// 实际上会从盘符根目录运行（例如 D:\tool.exe）—— 在四个边界承诺保持其中的
	// 工作区之外。显式拒绝前导分隔符；在 Unix 上前导 "/" 已经是绝对的，但
	// 工作区之外。显式拒绝前导分隔符；在 Unix 上前导 "/" 已经是绝对的，但
	// 检查两种拼写使规则在所有平台上一致。
	if filepath.IsAbs(command) || strings.Contains(command, ":") ||
		strings.HasPrefix(command, "/") || strings.HasPrefix(command, string(os.PathSeparator)) {
		return fmt.Errorf("%w: %q", ErrExecBadPath, command)
	}
	for _, part := range strings.FieldsFunc(command, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return fmt.Errorf("%w: %q", ErrExecBadPath, command)
		}
	}
	if !strings.EqualFold(filepath.Ext(command), ".exe") {
		return fmt.Errorf("%w: %q (expected \"go\" or a relative .exe path)", ErrExecCommandDenied, command)
	}
	return nil
}

// validateExecArgs 筛查 argv。即使不使用 shell，shell 元字符也被拒绝；
// -exec/-toolexec 被拒绝，因为它们让 `go test` 或
// 让 `go build` 启动任意二进制，绕过命令白名单。
func validateExecArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, execShellMeta) {
			return fmt.Errorf("%w: argument %q", ErrExecShellMeta, arg)
		}
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "-exec", "--exec", "-toolexec", "--toolexec":
			return fmt.Errorf("%w: %q", ErrExecDangerousGoFlag, arg)
		}
	}
	return nil
}

// ExecResult 是一次 exec 检查观察到的结果。StdoutTail 和
// StderrTail 最多保存最后 64KB 输出，被截断时带有前缀标记（标记保持在 rune 边界上）。
// 被截断时（标记保持在 rune 边界上）。
type ExecResult struct {
	ExitCode   int
	StdoutTail string
	StderrTail string
	TimedOut   bool
	Err        error
}

// RunExecCheck 在 workspace 内运行 c。该工作区预期是由 eval harness 种入的
// 由 eval harness 种入的一次性临时目录；此函数从不
// 一次性临时目录；此函数从不自行切换目录，也从不启动 shell。
func RunExecCheck(workspace string, c ExecCheck) ExecResult {
	res := ExecResult{}
	if err := c.Validate(); err != nil {
		res.Err = err
		res.ExitCode = -1
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), execDeadline(c.TimeoutMS))
	defer cancel()

	// 在应用 Dir 之前，先在父级的上下文中把 "go" 解析为绝对路径。
	// 应用。否则工作区中被种入的 go.exe 可能被选中。
	// 否则，工作区中被种入的 go.exe 可能在 Windows 上被相对的 PATH 条目选中。
	name := c.Command
	if c.Command == "go" {
		if found, err := exec.LookPath("go"); err == nil {
			if abs, err := filepath.Abs(found); err == nil {
				name = abs
			}
		}
	}

	cmd := exec.CommandContext(ctx, name, c.Args...)
	cmd.Dir = workspace
	cmd.Env = execEnvironment()

	var stdout, stderr tailBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res.StdoutTail = stdout.String()
	res.StderrTail = stderr.String()
	res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	if err != nil {
		res.Err = err
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			// 启动/chdir 失败：命令从未运行，因此绝不能评分成
			// 启动/chdir 失败：命令从未运行，因此绝不能评分成成功的退出 0。
			res.ExitCode = -1
		}
	}
	return res
}

// execDeadline 将 fixture 的 TimeoutMS 解析为有界的挂钟截止时间。小于等于零的值使用默认值；超过上限的值
// 截止时间。小于等于零的值使用默认值；超过上限的值
// 被限制到上限。
//
// 上限在转换为 time.Duration 之前先以毫秒应用。
// 先用大整数乘以 time.Millisecond 可能使 int64 溢出并回绕成负时长，那会立即触发 context 截止时间
// 回绕成负时长，那会立即触发 context 截止时间
// 而非应用文档记载的 120s 上限。
func execDeadline(timeoutMS int) time.Duration {
	if timeoutMS <= 0 {
		return defaultExecTimeout
	}
	maxMS := int(maxExecTimeout / time.Millisecond)
	if timeoutMS > maxMS {
		return maxExecTimeout
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

// GradeExecCheck 是纯评分函数：退出码必须匹配，且在被要求时
// GradeExecCheck 是纯评分函数：退出码必须匹配，且在被要求时 stdout 必须包含 needle。超时或启动失败
// 总是失败。非零退出恰好是检查想要的结果时是可以的。
func GradeExecCheck(res ExecResult, c ExecCheck) bool {
	if res.TimedOut {
		return false
	}
	if res.Err != nil {
		var exitErr *exec.ExitError
		if !errors.As(res.Err, &exitErr) {
			return false
		}
	}
	if res.ExitCode != c.WantExitCode {
		return false
	}
	return c.WantStdoutContains == "" || strings.Contains(res.StdoutTail, c.WantStdoutContains)
}

// execEnvironment 去掉 GOFLAGS，使 fixture 无法通过环境把额外标志（例如 -toolexec）偷偷带入白名单的 go 命令。
// 例如 -toolexec）通过环境带入白名单的 go 命令。
func execEnvironment() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// tailBuffer 只保留写入它的最后 execOutputCap 字节。裁剪
// tailBuffer 只保留写入它的最后 execOutputCap 字节。裁剪会前进到下一个 rune 边界，使尾部从不从 UTF-8 rune 中间开始。
type tailBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *tailBuffer) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.buf.Len() > execOutputCap {
		data := w.buf.Bytes()
		drop := w.buf.Len() - execOutputCap
		// 前进到 rune 边界可能比 execOutputCap 本身丢弃更多字节，因此保留的尾部短于 cap。
		// 本身丢弃更多字节，因此保留的尾部短于 cap。
		for drop < len(data) && !utf8.RuneStart(data[drop]) {
			drop++
		}
		keep := len(data) - drop
		copy(data, data[drop:])
		// 截断到恰好复制过去的字节。在这里截断到 cap
		// 会在尾部留下复制前的陈旧字节（可能
		// 截断到恰好复制过去的字节。在这里截断到 cap 会在尾部留下复制前的陈旧字节（可能是半个 rune），String() 随后会暴露它们。
		w.buf.Truncate(keep)
		w.truncated = true
	}
	return n, err
}

func (w *tailBuffer) String() string {
	if !w.truncated {
		return w.buf.String()
	}
	return execTruncMarker + w.buf.String()
}
