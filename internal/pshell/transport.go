package pshell

import (
	"io"
	"os/exec"

	"github.com/2118505078xyh1/argentgo/internal/proc"
)

// transport 是长驻 shell 的字节通道。PID/StartTime 供身份校验：进程退出后
// PID 可能被复用，只有身份仍匹配才允许杀进程，避免误杀无辜进程。
type transport interface {
	io.ReadWriteCloser
	PID() int
	StartTime() int64
}

// spawn 创建一个长驻 shell。shellKind 决定 bash 还是 PowerShell，workspace
// 作为子进程工作目录。它是可注入的：测试用内存 fake，不依赖真实 bash。
type spawn func(shellKind, workspace string) (transport, error)

// cmdTransport 是 transport 的真实实现，持有一个 *exec.Cmd 及其 stdin/stdout
// 管道。stderr 被丢弃，以免混入 stdout 污染哨兵切分（阶段1限制）。
type cmdTransport struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	pid       int
	startedAt int64
}

func (t *cmdTransport) PID() int                    { return t.pid }
func (t *cmdTransport) StartTime() int64            { return t.startedAt }
func (t *cmdTransport) Read(p []byte) (int, error)  { return t.stdout.Read(p) }
func (t *cmdTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }

// Close 关闭管道并杀掉直接 shell。
//
// 阶段1只用 cmd.Process.Kill()：没有为子进程配置进程组/作业对象，在 Unix 上
// 用负 pid 杀整组会误伤 agent 自身，因此不做进程树清理。后代进程可能在 shell
// 被杀后短暂残留并写入已关闭的管道，这是阶段1的限制（见报告）。
// 真正的"是否仍是我们自己的进程"由 Manager 通过 proc.IsSameProcess 判定后才
// 调用本方法，因此这里不再重复校验身份。
func (t *cmdTransport) Close() error {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait() // 回收僵进程
	}
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.stdout != nil {
		_ = t.stdout.Close()
	}
	return nil
}

// defaultSpawn 启动真实的长驻 shell。非交互参数保证 shell 不打印提示符
// （管道模式下也没有 TTY 提示符），从而stdout 流里只有命令输出和探针。
func defaultSpawn(shellKind, workspace string) (transport, error) {
	var cmd *exec.Cmd
	switch shellKind {
	case "powershell", "pwsh":
		program := "pwsh"
		if _, err := exec.LookPath(program); err != nil {
			program = "powershell"
		}
		cmd = exec.Command(program, "-NoProfile", "-NonInteractive", "-NoLogo")
	default: // bash / sh / zsh 等 POSIX shell
		cmd = exec.Command("bash", "--noprofile", "--norc")
	}
	cmd.Dir = workspace
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}

	pid := cmd.Process.Pid
	// 启动时间作为身份令牌；查询失败时记为 0（未知），IsSameProcess 仍会
	// 接受存活的 pid，与 proc 包的约定一致。
	startedAt, _ := proc.ProcessStartTime(pid)
	return &cmdTransport{cmd: cmd, stdin: stdin, stdout: stdout, pid: pid, startedAt: startedAt}, nil
}
