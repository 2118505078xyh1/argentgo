package app

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// tuiLogFile 是 TUI 运行期间结构化日志的落盘位置（相对 storage 根）。
const tuiLogFile = "logs/tui.log"

// logFileEnv 允许操作者把 TUI 日志改到别处（调试时可指向固定文件）。
const logFileEnv = "ARGENT_LOG_FILE"

// tuiLogPath 决定 TUI 日志写到哪里：优先 ARGENT_LOG_FILE，其次 storage/logs/tui.log。
// 两者都拿不到时返回空串，调用方据此退化为丢弃。
func tuiLogPath(storage string, env func(string) string) string {
	if env != nil {
		if p := strings.TrimSpace(env(logFileEnv)); p != "" {
			return p
		}
	}
	if strings.TrimSpace(storage) == "" {
		return ""
	}
	return filepath.Join(storage, filepath.FromSlash(tuiLogFile))
}

// redirectTUILogs 把标准库 log 的输出改到文件。
//
// agent 的逐轮计时/工具轨迹走 slog.Default()，而 slog 的默认 handler 经标准库
// log 写 stderr；TUI 独占全屏时这些行会直接画进帧里，把输入框顶乱（用户看到
// 输入框里冒出 INFO 行）。这里在 TUI 运行期间改道，退出时恢复；文件不可用时
// 退化为 io.Discard，绝不回落到终端。返回的恢复函数可安全被 defer 调用。
func redirectTUILogs(storage string, env func(string) string) func() {
	prev := log.Writer()
	restore := func() { log.SetOutput(prev) }

	path := tuiLogPath(storage, env)
	if path == "" {
		log.SetOutput(io.Discard)
		return restore
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.SetOutput(io.Discard)
			return restore
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.SetOutput(io.Discard)
		return restore
	}
	log.SetOutput(file)
	return func() {
		log.SetOutput(prev)
		_ = file.Close()
	}
}
