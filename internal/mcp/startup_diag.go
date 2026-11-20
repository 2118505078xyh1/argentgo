package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// startupReplayTimeout 限制一次诊断重放。发布值为 2 秒；
// 之所以是变量，只是为了让测试能缩短它。重放仅用于诊断：
// 它绝不能拖慢调用方的启动路径。
var startupReplayTimeout = 2 * time.Second

// startupReplayGrace 是截止时间之后的一个短暂窗口，用于从遵守 context 的
// 运行中收集结果，使其部分输出不会因 ctx.Done() 与结果通道之间的
// 竞态而丢失。
const startupReplayGrace = 25 * time.Millisecond

// stderrDrainWait 限制 stdio 错误丰富化等待异步 stderr 排空
// goroutine 追上一个刚退出服务器的时间。打印启动错误后死亡的
// 服务器会在 stderr 排空保证运行之前就关闭 stdout（唤醒 readLoop）；
// 一个短的有界等待使诊断具有确定性，同时
// 绝不拖慢调用方的错误路径。
// 它是变量，以便测试能缩短它。
var stderrDrainWait = 50 * time.Millisecond

// maxStartupReplayRunes 限制把多少子进程输出折叠进一行错误信息。
// 话痨服务器不得把失败的启动变成一行巨大的日志。
const maxStartupReplayRunes = 4096

// startupOutcome 是一次诊断重放产生的结果。
type startupOutcome struct {
	out []byte
	err error
}

// EnrichStartupError 把不透明的 stdio 启动失败（裸 EOF 或
// 连接断开）转换为一个错误，该错误还携带服务器命令实际打印
// 的内容。原始错误总是用 %w 保留，因此使用 errors.Is 分类的
// 调用方不受影响。
//
// 重放仅用于**诊断**，有意不接入 Connect：
// 启动服务器两次有副作用，因此应用层必须通过提供 run 来选择加入
// （通常是 exec.CommandContext(ctx, cmd, args...).CombinedOutput）。
// 当 run 为 nil、错误不是连接错误或命令为空时，错误
// 原样返回——默认情况下错误处理逐字节相同。
//
// run 收到一个带 2 秒截止时间的 context。这里也用 select 强制
// 该超时，使忽略 context 的 run 无法拖慢 EnrichStartupError；
// 超时的重放会被改写为 "timed out after 2s"。把重放留在
// 注入的函数之后，使本包保持仅依赖标准库，且无需启动进程
// 即可完全测试。
func EnrichStartupError(err error, cmd string, args []string, run func(context.Context, string, []string) ([]byte, error)) error {
	if !isStartupConnectionError(err) || run == nil || strings.TrimSpace(cmd) == "" {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), startupReplayTimeout)
	defer cancel()

	results := make(chan startupOutcome, 1)
	go func() {
		out, runErr := run(ctx, cmd, args)
		results <- startupOutcome{out: out, err: runErr}
	}()

	var outcome startupOutcome
	timedOut := false
	select {
	case outcome = <-results:
		// 行为良好的 run 会返回其 context 错误而不是让我们
		// 等待截止时间；那仍是超时，因此按超时报告。
		if errors.Is(outcome.err, context.DeadlineExceeded) || errors.Is(outcome.err, os.ErrDeadlineExceeded) {
			timedOut = true
		}
	case <-ctx.Done():
		timedOut = true
		// 遵守 context 的 run 会在截止时间附近返回。
		// 给它一个短暂宽限期交出部分输出；
		// 忽略 context 的 run 也受该窗口限制。通道是带缓冲的，
		// 因此 goroutine 绝不会在发送时阻塞。
		select {
		case outcome = <-results:
		case <-time.After(startupReplayGrace):
		}
	}

	return enrichStartupOutcome(err, cmd, args, outcome, timedOut)
}

// isStartupConnectionError 报告 err 是否是值得为诊断重放的
// 那种不透明失败：裸 EOF（包括 errors.New("EOF")）、
// 被包装的 EOF，或连接断开/被拒绝。它有意排除
// 普通错误，使绝大多数失败原样通过。
func isStartupConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	// 传输层可能把原因压平为文本：stdio.readLoop 把 io.EOF
	// 转为 "server closed the connection" 而不包装它，这正是
	// 审计指出的裸 EOF 报告。也匹配这些措辞。
	text := strings.ToLower(err.Error())
	for _, needle := range []string{
		"eof",
		"connection closed",
		"closed the connection",
		"connection refused",
		"connection reset",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// enrichStartupOutcome 把一次重放结果折叠进原始错误。当
// 重放没有产生新信息时，原样返回原始错误。
func enrichStartupOutcome(err error, cmd string, args []string, outcome startupOutcome, timedOut bool) error {
	output := trimStartupOutput(outcome.out)
	label := startupCommandLabel(cmd, args)

	var detail string
	switch {
	case timedOut:
		detail = "timed out after " + startupReplayTimeout.String()
		if output != "" {
			detail += "; partial output: " + output
		}
	case output != "" && outcome.err != nil:
		detail = fmt.Sprintf("replay error: %v; output: %s", outcome.err, output)
	case output != "":
		detail = "output: " + output
	case outcome.err != nil:
		detail = "replay error: " + outcome.err.Error()
	default:
		return err
	}
	return fmt.Errorf("%w (startup diagnostic replay of %s: %s)", err, label, detail)
}

// appendStderrContext 把服务器捕获的 stderr 折叠进一个不透明的启动
// 连接错误。除非 err 是审计指出的裸 EOF/连接断开
// 这类错误且 tail 带有有用内容，否则它是空操作，因此
// 绝大多数错误保持其逐字节的原始文本。原始错误总是用 %w 保留，
// 使 errors.Is 分类
// 不受影响。
func appendStderrContext(err error, tail string) error {
	if !isStartupConnectionError(err) {
		return err
	}
	trimmed := trimDiagnosticOutput([]byte(tail), "[server stderr truncated]")
	if trimmed == "" {
		return err
	}
	return fmt.Errorf("%w (server stderr: %s)", err, trimmed)
}

// enrichStderr 把 stdio 服务器已经捕获的 stderr 附加到
// 连接错误，**不重放进程且无副作用**。这是
// EnrichStartupError 的包内对应物：因为 newStdioTransport 保留了
// 有界的 stderr 环形缓冲区，应用现有的"打印错误"路径变得
// 自给自足——既不需要重跑命令，也不需要任何代码改动。
// 非 stdio 传输、非连接错误和空 stderr 保持 err
// 不变。
func (c *client) enrichStderr(err error) error {
	if c == nil || !isStartupConnectionError(err) {
		return err
	}
	stdio, ok := c.tr.(*stdioTransport)
	if !ok {
		return err
	}
	return appendStderrContext(err, stdio.stderrWithDrain())
}

// trimStartupOutput 为单行日志规范化重放输出：去除外围
// 空白并限制长度。截断按字符进行，因此 CJK 字符串
// 绝不会在 UTF-8 序列中间被切断。
func trimStartupOutput(out []byte) string {
	return trimDiagnosticOutput(out, "[startup replay output truncated]")
}

// trimDiagnosticOutput 是带调用方提供的截断标记的 trimStartupOutput，
// 使实时 stderr 丰富化可以标记自己的截断，而不改变
// 现有调用方所依赖的重放措辞。
func trimDiagnosticOutput(out []byte, marker string) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	if runes := []rune(s); len(runes) > maxStartupReplayRunes {
		s = string(runes[:maxStartupReplayRunes]) + "\n... " + marker + " ..."
	}
	return s
}

// startupCommandLabel 把重放的命令渲染为一个带引号的字符串。%q
// 使可打印的 Unicode（如 CJK 路径）保持可读。
func startupCommandLabel(cmd string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, cmd)
	parts = append(parts, args...)
	return fmt.Sprintf("%q", strings.Join(parts, " "))
}
