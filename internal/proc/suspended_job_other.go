//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"strings"
)

// suspendedJobEnabled 在非 Windows 平台同样读取门控值，使文档守卫能在
// 源码中找到真实读点。本平台不实现挂起启动，startTracked 走原有路径。
func suspendedJobEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_SUSPENDED_JOB")) == "1"
}

// startTracked 在非 Windows 平台等价于 cmd.Start() + newPlatformProcess。
// 挂起启动与 Windows 作业对象是平台专有机制，交叉编译时保持原有行为。
func startTracked(cmd *exec.Cmd, killTree bool) (*platformProcess, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return newPlatformProcess(cmd.Process, killTree), nil
}
