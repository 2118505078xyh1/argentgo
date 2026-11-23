package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"  // 注册 GIF 解码器，供 image.DecodeConfig 使用
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"strings"
)

// 视觉 token 按分块公式计价：每个 512×512 的图像块记 imageTokensLow 个 token，
// 上下界分别保持为 85（low / 极小图）与 1000（high / 大图或尺寸未知）。
// 这样 100×100 截图不再与 4000×3000 大图同价，同时保留原有的保守上界。
const imageTileSize = 512

// imageDimensionTokens 根据像素宽高（按 detail）估算视觉 token。
// 使用不放大只分块的规则：tiles = ceil(w/512)*ceil(h/512)，
// tokens = clamp(imageTokensLow*tiles, imageTokensLow, imageTokensHigh)。
func imageDimensionTokens(w, h int, detail string) int {
	if strings.EqualFold(strings.TrimSpace(detail), "low") {
		return imageTokensLow
	}
	if w <= 0 || h <= 0 {
		return imageTokensHigh
	}
	tiles := ((w + imageTileSize - 1) / imageTileSize) * ((h + imageTileSize - 1) / imageTileSize)
	if tiles < 1 {
		tiles = 1
	}
	tokens := imageTokensLow * tiles
	if tokens < imageTokensLow {
		tokens = imageTokensLow
	}
	if tokens > imageTokensHigh {
		tokens = imageTokensHigh
	}
	return tokens
}

// imageDataFromURL 只解析 data URL 的前缀，并解码最多 64 KiB 的头，
// 避免为一次尺寸探测把整张图读入内存。非 data URL / 非法 base64 返回 false。
func imageDataFromURL(url string) ([]byte, bool) {
	if !strings.HasPrefix(strings.ToLower(url), "data:image/") {
		return nil, false
	}
	comma := strings.IndexByte(url, ',')
	if comma < 0 {
		return nil, false
	}
	payload := url[comma+1:]
	// 仅解码头部：base64 每 4 个字符表示 3 字节，64 KiB 头约需 87 KiB 字符。
	const maxEncoded = 88 * 1024
	if len(payload) > maxEncoded {
		payload = payload[:maxEncoded]
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		// 兼容无 padding 的变体。
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "="))
		if err != nil {
			return nil, false
		}
	}
	if len(decoded) == 0 {
		return nil, false
	}
	return decoded, true
}

// imageDimensions 用 stdlib image.DecodeConfig 读取 PNG/JPEG/GIF 宽高；
// WebP 不在标准库注册表中，按魔数头解析（VP8X / VP8L / 经典 VP8）。
func imageDimensions(data []byte) (w, h int, ok bool) {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return cfg.Width, cfg.Height, true
	}
	return webpDimensions(data)
}

// webpDimensions 从 WebP 头部读取宽高。RIFF 容器以 "RIFF....WEBP" 开头，
// 随后是四字符块标识。返回 (0,0,false) 表示无法识别。
func webpDimensions(b []byte) (int, int, bool) {
	if len(b) < 20 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, false
	}
	chunk := string(b[12:16])
	switch chunk {
	case "VP8X":
		// 24 位小端宽高各减一，位于负载的第 4..9 字节。
		w := 1 + int(b[24]) + int(b[25])<<8 + int(b[26])<<16
		h := 1 + int(b[27]) + int(b[28])<<8 + int(b[29])<<16
		return w, h, true
	case "VP8L":
		// 14 位宽高加一，压缩在 4 字节小端整数中。
		bits := uint32(b[21]) | uint32(b[22])<<8 | uint32(b[23])<<16 | uint32(b[24])<<24
		w := int(bits&0x3FFF) + 1
		h := int((bits>>14)&0x3FFF) + 1
		return w, h, true
	case "VP8 ":
		// 经典 VP8 帧头：同步字 0x9d012a 后跟 14 位宽高。
		if len(b) < 30 || b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, false
		}
		w := int(uint16(b[26])|uint16(b[27])<<8) & 0x3FFF
		h := int(uint16(b[28])|uint16(b[29])<<8) & 0x3FFF
		return w, h, true
	}
	return 0, 0, false
}

// estimateImageTokens 返回一个图像 part 的 token 估算：
// 尺寸可解析时按分块公式计价；解码失败或非 data URL 回退为原有的固定值
// （low=85，high=1000），宁高估不低估。
func estimateImageTokens(detail, url string) int {
	if strings.EqualFold(strings.TrimSpace(detail), "low") {
		return imageTokensLow
	}
	data, ok := imageDataFromURL(url)
	if !ok {
		return imageTokensHigh
	}
	w, h, ok := imageDimensions(data)
	if !ok {
		return imageTokensHigh
	}
	return imageDimensionTokens(w, h, detail)
}
