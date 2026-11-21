//go:build !windows

package tui

// writeClipboard 在非 Windows 平台是空操作：复制后端只在 Windows 上
// 通过 CF_UNICODETEXT 实现，其余平台保持无副作用。这样 keys.go 不再
// 需要 runtime.GOOS 判断，平台差异完全由 build tag 隔离。
func writeClipboard(string) error { return nil }
