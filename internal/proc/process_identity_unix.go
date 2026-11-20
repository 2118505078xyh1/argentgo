//go:build !windows

package proc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// procClockTicksPerSecond is USER_HZ, the unit of /proc/<pid>/stat field 22
// (starttime). Linux fixes it at 100 regardless of the kernel's HZ setting.
const procClockTicksPerSecond = 100

func processStartTime(pid int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("proc: invalid pid %d", pid)
	}
	ticks, err := readProcStartTicks(pid)
	if err != nil {
		return 0, err
	}
	uptime, err := readProcUptime()
	if err != nil {
		return 0, err
	}
	// starttime is ticks since boot, not wall-clock. Derive the boot instant
	// from the uptime read at this moment, then add the process's own offset.
	// Both reads happen microseconds apart; DefaultStartTimeToleranceMS absorbs
	// the skew when a recorded value is compared later.
	boot := time.Now().Add(-uptime)
	started := boot.Add(time.Duration(ticks) * (time.Second / procClockTicksPerSecond))
	return started.UnixMilli(), nil
}

// readProcStartTicks reads field 22 (starttime) from /proc/<pid>/stat.
func readProcStartTicks(pid int) (uint64, error) {
	path := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("proc: read %s: %w", path, err)
	}
	raw := string(data)
	// comm (field 2) is parenthesized and may itself contain spaces or ')', so
	// the final ')' ends it. Fields after it start at state (field 3), making
	// starttime (field 22) index 19.
	end := strings.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, fmt.Errorf("proc: parse %s: missing comm field", path)
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) <= 19 {
		return 0, fmt.Errorf("proc: parse %s: starttime field missing", path)
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("proc: parse %s starttime: %w", path, err)
	}
	return ticks, nil
}

// readProcUptime reads the boot-relative wall clock from /proc/uptime.
func readProcUptime() (time.Duration, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("proc: read /proc/uptime: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("proc: parse /proc/uptime: empty")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0, fmt.Errorf("proc: parse /proc/uptime: invalid uptime %q", fields[0])
	}
	return time.Duration(secs * float64(time.Second)), nil
}
