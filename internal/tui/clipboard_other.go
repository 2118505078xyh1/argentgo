//go:build !windows

package tui

import (
	"io"
	"os"
	"strings"
	"sync"
)

// OSC 52 剪贴板后端（非 Windows）。Windows 用 CF_UNICODETEXT（见 clipboard_windows.go），
// 其余平台通过终端的 OSC 52 协议写入剪贴板：ESC ] 52 ; c ; <base64> BEL。
// 纯标准库实现，无新依赖；不支持 OSC 52 的终端会忽略该序列，复制静默降级为无操作。

var (
	osc52Mu     sync.Mutex
	osc52Writer io.Writer = os.Stdout // 由 Run 在启动时设为 TUI 的输出流
)

// SetClipboardOutput 设置 OSC 52 序列的写入目标（TUI 的输出流）。
// Run 在启动时调用；nil 时回退到 os.Stdout。它只在非 Windows 生效。
func SetClipboardOutput(w io.Writer) {
	osc52Mu.Lock()
	defer osc52Mu.Unlock()
	if w != nil {
		osc52Writer = w
	}
}

// osc52Disabled 报告是否应跳过 OSC 52：TERM=dumb 时终端无剪贴板能力。
func osc52Disabled(env func(string) string) bool {
	if env == nil {
		env = os.Getenv
	}
	return strings.EqualFold(strings.TrimSpace(env("TERM")), "dumb")
}

func writeClipboard(text string) error {
	if osc52Disabled(os.Getenv) {
		return nil
	}
	// OSC 52 的负载是 base64(UTF-8 字节)，终端据此还原 Unicode 文本（含 CJK）。
	seq := osc52Sequence(text)

	osc52Mu.Lock()
	w := osc52Writer
	osc52Mu.Unlock()
	_, err := io.WriteString(w, seq)
	return err
}
