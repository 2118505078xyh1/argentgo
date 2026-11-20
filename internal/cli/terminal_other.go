//go:build !linux && !darwin && !windows

package cli

// 在没有专门探测手段的平台上，isTerminal 保守地假设存在终端，
// 保留守卫引入前的行为，而不是为所有这类平台禁用 TUI。
func isTerminal(uintptr) bool { return true }
