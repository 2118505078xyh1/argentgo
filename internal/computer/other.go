//go:build !windows

package computer

import (
	"errors"
	"image"
)

// 非 Windows 平台的 stub。保留完整导出 API，使工具层可以无条件注册，
// 只在真正调用时报告不可用——与 DAP/browser 的「公布但不启动」语义一致。
// 同时保证 GOOS=linux go build ./... 交叉编译通过。
var errUnsupported = errors.New("computer: desktop control is only available on Windows")

func Capture() (*image.RGBA, error) { return nil, errUnsupported }

func Click(_, _ int) error { return errUnsupported }

func Scroll(_, _, _ int) error { return errUnsupported }

func Type(_ string) error { return errUnsupported }

func Key(_ string) error { return errUnsupported }
