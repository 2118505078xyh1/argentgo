package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// pngDataURL 生成指定尺寸的全色 PNG data URL。
func pngDataURL(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{12, 34, 56, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestImageDimensionTokens(t *testing.T) {
	small := pngDataURL(t, 100, 100)
	large := pngDataURL(t, 2000, 2000)

	smallTokens := estimateImageTokens("high", small)
	largeTokens := estimateImageTokens("high", large)
	if smallTokens != imageTokensLow {
		t.Fatalf("100x100 high detail = %d, want %d", smallTokens, imageTokensLow)
	}
	if largeTokens <= smallTokens {
		t.Fatalf("2000x2000 high detail = %d, want > %d", largeTokens, smallTokens)
	}
	if largeTokens > imageTokensHigh {
		t.Fatalf("高估超过上界: %d", largeTokens)
	}
	if got := estimateImageTokens("low", large); got != imageTokensLow {
		t.Fatalf("low detail = %d, want %d", got, imageTokensLow)
	}
	// 坏 base64 与 non-data URL 回退旧的固定值。
	if got := estimateImageTokens("high", "data:image/png;base64,!!not-base64!!"); got != imageTokensHigh {
		t.Fatalf("bad base64 = %d, want %d", got, imageTokensHigh)
	}
	if got := estimateImageTokens("high", "/path/to/file.png"); got != imageTokensHigh {
		t.Fatalf("non data URL = %d, want %d", got, imageTokensHigh)
	}
}

func TestImageDimensionsWebPHeader(t *testing.T) {
	// VP8X 头：宽高各减一后按 24 位小端写入。
	b := make([]byte, 30)
	copy(b[0:4], "RIFF")
	copy(b[8:12], "WEBP")
	copy(b[12:16], "VP8X")
	b[24], b[25], b[26] = 99, 0, 0  // width = 100
	b[27], b[28], b[29] = 199, 0, 0 // height = 200
	w, h, ok := webpDimensions(b)
	if !ok || w != 100 || h != 200 {
		t.Fatalf("webpDimensions = %d,%d,%v", w, h, ok)
	}
}
