//go:build windows

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

const (
	// createSuspended 让 CreateProcessW 创建进程后主线程保持挂起，
	// 直到把它纳入作业对象后才显式恢复，从而封住孙进程逃逸窗口。
	createSuspended = 0x0000_0004

	threadSuspendResume = 0x0002
	dwordError          = ^uint32(0)
)

var (
	procOpenThread       = kernel32.NewProc("OpenThread")
	procResumeThread     = kernel32.NewProc("ResumeThread")
	procThread32First    = kernel32.NewProc("Thread32First")
	procThread32Next     = kernel32.NewProc("Thread32Next")
	procTerminateProcess = kernel32.NewProc("TerminateProcess")
)

// suspendedThreadEntry 与 Windows THREADENTRY32 布局逐字对应。
type suspendedThreadEntry struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePriority   int32
	DeltaPriority  int32
	Flags          uint32
}

// suspendedJobEnabled 读取独立门控；只有修剪后精确等于 "1" 才开启，默认关闭。
// 该函数在非 Windows 构建中有同名的桩，文档守卫据此确认真实源码读点。
func suspendedJobEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_SUSPENDED_JOB")) == "1"
}

// shouldSuspendedStart 决定是否使用挂起启动。
// 只有既要求整树终止、又显式打开门控时才付出挂起/恢复的开销。
func shouldSuspendedStart(killTree bool) bool {
	return killTree && suspendedJobEnabled()
}

// markSuspendedStart 在门控生效时给 cmd 追加 CREATE_SUSPENDED。
// os/exec 会把它原样传给 CreateProcessW，因此主线程在 CreateProcessW
// 返回时仍未运行，不可能在分配作业对象之前派生孙进程。
func markSuspendedStart(cmd *exec.Cmd, killTree bool) {
	if !shouldSuspendedStart(killTree) {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended
}

// resumeSuspendedProcess 找到目标进程的主线程并恢复它。
// CreateProcessW 的 CREATE_SUSPENDED 只挂起主线程，恢复这一条即可。
func resumeSuspendedProcess(pid int) error {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("thread snapshot: %w", err)
	}
	defer syscall.CloseHandle(snapshot)

	var te suspendedThreadEntry
	te.Size = uint32(unsafe.Sizeof(te))
	ret, _, err := procThread32First.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&te)))
	if ret == 0 {
		return fmt.Errorf("thread32 first: %w", err)
	}
	for {
		if te.OwnerProcessID == uint32(pid) {
			return resumeThread(te.ThreadID)
		}
		te.Size = uint32(unsafe.Sizeof(te))
		ret, _, err = procThread32Next.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&te)))
		if ret == 0 {
			return fmt.Errorf("thread %d missing in snapshot: %w", pid, err)
		}
	}
}

func resumeThread(threadID uint32) error {
	handle, _, err := procOpenThread.Call(threadSuspendResume, 0, uintptr(threadID))
	if handle == 0 {
		return fmt.Errorf("open thread %d: %w", threadID, err)
	}
	defer syscall.CloseHandle(syscall.Handle(handle))
	ret, _, err := procResumeThread.Call(handle)
	if uint32(ret) == dwordError {
		return fmt.Errorf("resume thread %d: %w", threadID, err)
	}
	return nil
}

// terminateSuspendedProcess 终止一个从未运行过的挂起进程。
// 仅在入作业/恢复失败时调用，确保它不会脱离作业继续执行。
func terminateSuspendedProcess(pid int) {
	handle, err := syscall.OpenProcess(processTerminate, false, uint32(pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(handle)
	_, _, _ = procTerminateProcess.Call(uintptr(handle), 1)
}

// attachSuspended 把已挂起的进程纳入作业对象并恢复主线程。
// 任一环节失败都终止那个从未运行过的进程，绝不让它脱离作业继续执行。
// 依赖注入的函数参数让失败路径可在不启动真实进程的测试中覆盖。
func attachSuspended(
	pid int,
	create func() (syscall.Handle, error),
	assign func(syscall.Handle, int) error,
	resume func(int) error,
	terminate func(int),
	closeJob func(syscall.Handle),
) (syscall.Handle, error) {
	job, err := create()
	if err != nil {
		terminate(pid)
		return 0, fmt.Errorf("create job: %w", err)
	}
	if err := assign(job, pid); err != nil {
		closeJob(job)
		terminate(pid)
		return 0, fmt.Errorf("assign job: %w", err)
	}
	if err := resume(pid); err != nil {
		closeJob(job)
		terminate(pid)
		return 0, fmt.Errorf("resume thread: %w", err)
	}
	return job, nil
}

// closeJobHandle 把 syscall.CloseHandle 的返回值丢弃，便于作为注入函数传入。
func closeJobHandle(job syscall.Handle) {
	_ = syscall.CloseHandle(job)
}

// startTracked 启动子进程，并在门控开启且需要整树终止时改走挂起路径：
// CreateProcessW 带 CREATE_SUSPENDED → 分配作业对象 → 分配成功才 ResumeThread。
// 门控关闭或 killTree 为假时，与原有 cmd.Start() + newPlatformProcess
// 路径逐字节一致。
func startTracked(cmd *exec.Cmd, killTree bool) (*platformProcess, error) {
	if !shouldSuspendedStart(killTree) {
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return newPlatformProcess(cmd.Process, killTree), nil
	}

	markSuspendedStart(cmd, killTree)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	job, err := attachSuspended(pid, createJob, assignJob, resumeSuspendedProcess, terminateSuspendedProcess, closeJobHandle)
	if err != nil {
		// 进程已被终止但从未运行；回收它的句柄，避免泄漏一个悬挂的子进程。
		_ = cmd.Wait()
		return nil, fmt.Errorf("suspended job pid %d: %w", pid, err)
	}
	return &platformProcess{proc: cmd.Process, job: job, killAll: true}, nil
}
