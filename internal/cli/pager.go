package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// pagerMinBytes 是交互式分页器开始有价值的输出大小阈值；更短的输出
// 应当直接滚动而过，无需额外按键。
const pagerMinBytes = 4096

// shouldPage 报告是否值得使用可选的分页器路径：配置了分页器、stdout 是
// 交互式的，且输出足够长需要滚动。它是分页器的决策钩子，在这个保守的
// 实现中故意不真正启动分页器（见 writePaged）。把决策保持为纯函数，
// 使得无需真实控制台就能轻松测试非 TTY 的保证。
func shouldPage(pager, content string, isTTY bool) bool {
	return strings.TrimSpace(pager) != "" && isTTY && len(content) >= pagerMinBytes
}

// writePaged 是 `sessions show` 和 `logs` 等长输出子命令的输出网关。它把
// content 写入 w，并把下游读取方提前关闭（例如 `argentgo sessions show X | head`）
// 视为正常停止，而非命令失败：broken-pipe 错误会被吞掉并返回 nil。
//
// pager 和 isTTY 参数描述可选的分页路径（见 shouldPage）：后续实现可以在
// stdout 是终端时把长输出通过 $PAGER 管道传输。这个保守的实现刻意避免
// 启动子进程（不引入新的 OS 依赖），因此内容总是直接写入，默认的 stdout
// 输出逐字节保持不变。现在接受这些参数，是为了启用分页时调用点无需改动。
func writePaged(w io.Writer, content string, pager string, isTTY bool) error {
	// 计算可选的分页决策，但暂不执行：不会启动分页器子进程，
	// 因此两种结果都会落到同样的写入。
	_ = shouldPage(pager, content, isTTY)
	return writeIgnoringBrokenPipe(w, content)
}

// writeIgnoringBrokenPipe 把 content 写入 w，当唯一的失败是消费方关闭了
// 管道时返回 nil。
func writeIgnoringBrokenPipe(w io.Writer, content string) error {
	_, err := io.WriteString(w, content)
	if isBrokenPipe(err) {
		return nil
	}
	return err
}

// isBrokenPipe 识别下游管道或 stdout 句柄在所有输出被消费完之前关闭时
// 产生的错误。
func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// 在 Windows 上，关闭的管道表现为 ERROR_BROKEN_PIPE，它的 errno 不会
	// 映射到可移植的 syscall.EPIPE 哨兵；因此回退到错误消息，使 `| head`
	// 和关闭的 os.File 在这两种平台上都被覆盖。
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "pipe has been ended") ||
		strings.Contains(msg, "pipe is being closed")
}

// writeLongOut 把长输出命令的输出经 writePaged 路由，从环境读取可选的
// 分页器，从 App 接缝读取 TTY 标志。HasTerminal 为 nil（测试、嵌入式传输）
// 表示没有终端，这正是保守的无分页路径。
func (a App) writeLongOut(content string) error {
	return writePaged(a.IO.Out, content, a.env("PAGER"), a.hasTerminal())
}

// reportLongOut 写入内容，并且只把真正的写入失败映射为退出码 1。
// broken pipe 会被 writePaged 吞掉，因此 `... | head` 仍然算成功。
func reportLongOut(app App, content string) int {
	if err := app.writeLongOut(content); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	return 0
}
