package proc

import (
	"os"
	"os/exec"
)

// ConfigureGroup 为尚未启动的 cmd 配置进程组，使 KillGroup 能终止整棵子进程树。
// 它必须与 Command.KillTree 使用同一套底层配置：Unix 上设置 Setpgid，
// Windows 上设置 CREATE_NEW_PROCESS_GROUP。应在 cmd.Start / cmd.Run 之前调用。
//
// 这个辅助供那些需要自己驱动 os/exec（而不是走 proc.Run）的调用方使用，
// 例如 hooks 需要保留自己的输出捕获、超时分类与退出码判定。
func ConfigureGroup(cmd *exec.Cmd) {
	configure(cmd, true, false)
}

// KillGroup 终止由 ConfigureGroup 配置的进程及其后代。
// p 是已启动进程的 cmd.Process；重复调用是安全的。
//
// Unix 上通过负 pid 向整个进程组发 SIGKILL；Windows 上优先用作业对象，
// 失败时回退到 taskkill /T。底层实现与 Command.KillTree 完全一致。
func KillGroup(p *os.Process) {
	if p == nil {
		return
	}
	handle := newPlatformProcess(p, true)
	defer handle.close()
	handle.killTree()
}
