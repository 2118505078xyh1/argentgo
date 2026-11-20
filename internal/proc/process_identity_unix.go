//go:build !windows

package proc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// procClockTicksPerSecond 是 USER_HZ，即 /proc/<pid>/stat 第 22 个字段
// （starttime）的单位。Linux 无论内核 HZ 设置如何都固定为 100。
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
	// starttime 是自启动以来的 tick 数，而非墙上时钟。根据此刻读取的
	// uptime 推导启动时刻，再加上该进程自身的偏移量。
	// 两次读取相差微秒级；之后比较记录值时，
	// DefaultStartTimeToleranceMS 会吸收这一偏差。
	boot := time.Now().Add(-uptime)
	started := boot.Add(time.Duration(ticks) * (time.Second / procClockTicksPerSecond))
	return started.UnixMilli(), nil
}

// readProcStartTicks 从 /proc/<pid>/stat 读取第 22 个字段（starttime）。
func readProcStartTicks(pid int) (uint64, error) {
	path := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("proc: read %s: %w", path, err)
	}
	raw := string(data)
	// comm（第 2 个字段）用括号括起来，且本身可能含空格或 ')'，
	// 因此以最后一个 ')' 结束。其后的字段从 state（第 3 个）开始，
	// 所以 starttime（第 22 个）的索引是 19。
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

// readProcUptime 从 /proc/uptime 读取相对启动时刻的墙上时钟。
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
