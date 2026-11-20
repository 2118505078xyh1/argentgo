//go:build windows

package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"unsafe"
)

const (
	createNoWindow        = 0x0800_0000
	createNewProcessGroup = 0x0000_0200
	detachedProcess       = 0x0000_0008

	processSetQuota   = 0x0100
	processTerminate  = 0x0001
	killOnJobClose    = 0x2000
	extendedLimitInfo = 9
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
)

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IOInfo                jobIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// configure 请求隐藏的控制台，并在调用方需要进程树终止时，
// 创建一个新的进程组，使误入的控制台 Ctrl+C 不会传到子进程。
func configure(cmd *exec.Cmd, killTree, noWindow bool) {
	flags := uint32(0)
	if noWindow {
		flags |= createNoWindow
	}
	if killTree {
		flags |= createNewProcessGroup
	}
	if flags != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	}
}

// platformProcess 负责单个子进程的终止策略。
//
// Windows 没有终止整棵子树的调用：作业对象是可靠地
// 关闭 shell 的孙进程的唯一方式，因此子进程被分配到一个作业对象，
// 关闭（或终止）该对象即为终止请求。
type platformProcess struct {
	proc    *os.Process
	job     syscall.Handle
	killAll bool
	// mu 保护 job：等待 goroutine 在 close() 中清除它，而调用方
	// 此时可能仍在终止进程。没有它，Kill 与自然退出竞争就是
	// 数据竞争，并可能在句柄关闭后向其发信号。
	mu sync.Mutex
}

func newPlatformProcess(p *os.Process, killTree bool) *platformProcess {
	pp := &platformProcess{proc: p, killAll: killTree}
	if !killTree || p == nil {
		return pp
	}
	job, err := createJob()
	if err != nil {
		return pp
	}
	if err := assignJob(job, p.Pid); err != nil {
		// 旧版 Windows 可能拒绝嵌套；回退到 taskkill。
		syscall.CloseHandle(job)
		return pp
	}
	pp.job = job
	return pp
}

func (p *platformProcess) killTree() {
	// 在作业对象调用期间持有锁，使 close() 不会在
	// 读取句柄与使用句柄之间将其关闭。
	p.mu.Lock()
	job := p.job
	if job != 0 {
		_, _, _ = procTerminateJobObject.Call(uintptr(job), 1)
	}
	p.mu.Unlock()

	if p.proc != nil {
		_ = p.proc.Kill()
	}
	// 无法创建（或分配）的作业对象会让 shell 的后代
	// 继续存活；taskkill /T 与 Rust 代码使用的最后手段相同。
	if job == 0 && p.killAll && p.proc != nil {
		killViaTaskkill(p.proc.Pid)
	}
}

func (p *platformProcess) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		// KILL_ON_JOB_CLOSE 意味着这也会回收仍在运行的一切。
		syscall.CloseHandle(p.job)
		p.job = 0
	}
}

// configureDetached 给子进程自己的进程组且不分配控制台，
// 因此关闭启动终端不会把它一起关掉。
func configureDetached(cmd *exec.Cmd, noWindow bool) {
	flags := uint32(detachedProcess | createNewProcessGroup)
	if noWindow {
		flags |= createNoWindow
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}

func createJob() (syscall.Handle, error) {
	handle, _, err := procCreateJobObjectW.Call(0, 0)
	if handle == 0 {
		return 0, err
	}
	job := syscall.Handle(handle)
	var info jobExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = killOnJobClose
	ok, _, setErr := procSetInformationJobObject.Call(
		uintptr(job),
		extendedLimitInfo,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ok == 0 {
		syscall.CloseHandle(job)
		return 0, setErr
	}
	return job, nil
}

func assignJob(job syscall.Handle, pid int) error {
	process, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(process)
	ok, _, assignErr := procAssignProcessToJobObject.Call(uintptr(job), uintptr(process))
	if ok == 0 {
		return assignErr
	}
	return nil
}

// killViaTaskkill 移除作业对象分配失败的整棵子树。
func killViaTaskkill(pid int) {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	_ = cmd.Run()
}

// shellCandidates 列出值得探测的 POSIX shell，最优者在前。
//
// Windows 上 bash 常常不在 PATH 中，因此 Git for Windows 的位置
// 从 PATH 上的 git.exe 以及常见安装根目录推导。
func shellCandidates() []string {
	var out []string
	if custom := os.Getenv("ARGENT_BASH"); custom != "" {
		out = append(out, custom)
	}
	if found, err := Which("bash"); err == nil {
		out = append(out, found)
	}
	if git, err := Which("git"); err == nil {
		// <root>\cmd\git.exe -> <root>\bin\bash.exe
		root := filepath.Dir(filepath.Dir(git))
		out = append(out,
			filepath.Join(root, "bin", "bash.exe"),
			filepath.Join(root, "usr", "bin", "bash.exe"),
		)
	}
	roots := []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs"),
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		out = append(out, filepath.Join(root, "Git", "bin", "bash.exe"))
	}
	out = append(out, `C:\Program Files\Git\bin\bash.exe`)
	return out
}

func fallbackShell() (string, []string) {
	if found, err := Which("cmd.exe"); err == nil {
		return found, []string{"/C"}
	}
	if found, err := Which("powershell"); err == nil {
		return found, []string{"-NoProfile", "-Command"}
	}
	return "cmd.exe", []string{"/C"}
}
