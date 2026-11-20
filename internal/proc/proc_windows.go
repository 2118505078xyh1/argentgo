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

// configure asks for a hidden console and, when the caller wants tree kills, a
// new process group so a stray console Ctrl+C does not reach the child.
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

// platformProcess owns the kill strategy for one child.
//
// Windows has no kill-the-subtree call: a job object is the only reliable way
// to take down a shell's grandchildren, so the child is assigned to one whose
// close (or terminate) is the kill request.
type platformProcess struct {
	proc    *os.Process
	job     syscall.Handle
	killAll bool
	// mu guards job: the wait goroutine clears it in close() while a caller
	// can still be killing the process. Without it a Kill racing a natural
	// exit is a data race and can signal a handle after it was closed.
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
		// Nesting can be refused on old Windows; fall back to taskkill.
		syscall.CloseHandle(job)
		return pp
	}
	pp.job = job
	return pp
}

func (p *platformProcess) killTree() {
	// Hold the lock across the job call so close() cannot close the handle
	// between reading it and using it.
	p.mu.Lock()
	job := p.job
	if job != 0 {
		_, _, _ = procTerminateJobObject.Call(uintptr(job), 1)
	}
	p.mu.Unlock()

	if p.proc != nil {
		_ = p.proc.Kill()
	}
	// A job that could not be created (or assigned) leaves the descendants
	// of a shell alive; taskkill /T is the same last resort the Rust code uses.
	if job == 0 && p.killAll && p.proc != nil {
		killViaTaskkill(p.proc.Pid)
	}
}

func (p *platformProcess) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		// KILL_ON_JOB_CLOSE means this also reaps anything still running.
		syscall.CloseHandle(p.job)
		p.job = 0
	}
}

// configureDetached gives the child its own process group and no console, so
// closing the launching terminal does not take it down.
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

// killViaTaskkill removes a subtree whose job-object assignment failed.
func killViaTaskkill(pid int) {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	_ = cmd.Run()
}

// shellCandidates lists the POSIX shells worth probing, best first.
//
// bash is often not on PATH on Windows, so the Git for Windows locations are
// derived from the git.exe that is on PATH and from the usual install roots.
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
