//go:build !windows && !linux && !darwin

package tui

// 没有终端后端支持平台的桩。UI 仍能构建且
// 测试仍能运行；Run 只是不触碰终端。

func enableRaw() (func(), error) { return func() {}, nil }

func terminalSize() (int, int) { return 0, 0 }

func watchResize() (<-chan size, func()) {
	out := make(chan size)
	return out, func() {}
}
