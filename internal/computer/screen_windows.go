//go:build windows

package computer

import (
	"errors"
	"image"
	"unsafe"
)

// Win32 GDI 位图头（BITMAPINFOHEADER，40 字节）。
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
}

// Capture 抓取整个虚拟桌面，返回 32bpp RGBA 图像。
// 纯 Win32 GDI + stdlib image，无 cgo、无第三方依赖。
func Capture() (*image.RGBA, error) {
	// SM_XVIRTUALSCREEN/ Y/ CX/ CY = 76/77/78/79，覆盖多显示器。
	x, _, _ := procGetSystemMetrics.Call(76)
	y, _, _ := procGetSystemMetrics.Call(77)
	w, _, _ := procGetSystemMetrics.Call(78)
	h, _, _ := procGetSystemMetrics.Call(79)
	if w == 0 || h == 0 {
		// 退化到主显示器（SM_CXSCREEN/SM_CYSCREEN = 0/1）。
		x, y = 0, 0
		w, _, _ = procGetSystemMetrics.Call(0)
		h, _, _ = procGetSystemMetrics.Call(1)
	}
	if w == 0 || h == 0 {
		return nil, errors.New("computer: invalid screen dimensions")
	}
	// 防御异常大的虚拟桌面尺寸，避免一次分配巨量内存。
	const maxDim = 16384
	if w > maxDim || h > maxDim {
		return nil, errors.New("computer: screen dimensions out of range")
	}

	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return nil, errors.New("computer: GetDC failed")
	}
	defer procReleaseDC.Call(0, hdc)

	memdc, _, _ := procCreateCompatibleDC.Call(hdc)
	if memdc == 0 {
		return nil, errors.New("computer: CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memdc)

	hbmp, _, _ := procCreateCompatibleBitmap.Call(hdc, w, h)
	if hbmp == 0 {
		return nil, errors.New("computer: CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hbmp)

	old, _, _ := procSelectObject.Call(memdc, hbmp)
	defer procSelectObject.Call(memdc, old)

	// SRCCOPY = 0x00CC0020；源坐标用虚拟桌面原点。
	if ok, _, _ := procBitBlt.Call(memdc, 0, 0, w, h, hdc, x, y, 0x00CC0020); ok == 0 {
		return nil, errors.New("computer: BitBlt failed")
	}

	bi := bitmapInfo{}
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = int32(w)
	bi.Header.Height = -int32(h) // 负数 => top-down DIB，免去逐行翻转
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = 0 // BI_RGB

	pixels := make([]byte, w*h*4)
	got, _, _ := procGetDIBits.Call(memdc, hbmp, 0, h,
		uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&bi)), 0)
	if got == 0 {
		return nil, errors.New("computer: GetDIBits failed")
	}

	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < int(w*h); i++ {
		b, g, r, a := pixels[i*4], pixels[i*4+1], pixels[i*4+2], pixels[i*4+3]
		img.Pix[i*4] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = a
	}
	return img, nil
}
