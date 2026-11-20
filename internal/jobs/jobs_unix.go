//go:build !windows

package jobs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// detach puts the job in its own session so it survives the shell that started
// it and can be killed as a group.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// alive reports whether pid is a running process.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to another user.
	return errors.Is(err, syscall.EPERM)
}

// sameProcess reports whether pid is alive AND was created at startedAt. A pid
// alone is not an identity: once a job is reloaded from jobs.json the OS may
// have recycled the number for an unrelated process.
//
// Linux exports the process start time as clock ticks since boot in
// /proc/<pid>/stat (field 22, in USER_HZ = 100 ticks/s) and the boot-relative
// wall clock in /proc/uptime. Other Unix systems have no /proc, so the check
// fails closed there: an unverifiable job is treated as stale and never killed.
func sameProcess(pid int, startedAt time.Time) bool {
	if pid <= 0 || startedAt.IsZero() {
		return false
	}
	ticks, ok := processStartTicks(pid)
	if !ok {
		return false
	}
	uptime, ok := systemUptime()
	if !ok {
		return false
	}
	// Derive the boot instant from the same "now" used to read uptime, then
	// add the process's own offset from boot. Both readings are within a few
	// milliseconds of each other; identityTolerance absorbs the skew.
	created := time.Now().Add(-uptime).Add(time.Duration(ticks) * (time.Second / 100))
	diff := created.Sub(startedAt)
	if diff < 0 {
		diff = -diff
	}
	return diff <= identityTolerance
}

// processStartTicks reads field 22 (starttime) from /proc/<pid>/stat.
func processStartTicks(pid int) (uint64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// comm (field 2) is parenthesized and may contain spaces or ')', so the
	// last ')' ends it. The fields after it begin at state (field 3), making
	// starttime (field 22) index 19.
	raw := string(data)
	end := strings.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) <= 19 {
		return 0, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	return ticks, err == nil
}

// systemUptime reads the boot-relative wall clock from /proc/uptime.
func systemUptime() (time.Duration, bool) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// killTree terminates pid and every descendant.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	// Setsid made the child a session leader, so -pid is its whole group.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
