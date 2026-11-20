package mcp

import (
	"os"
	"strconv"
	"strings"
)

// 本文件是对 blob.go 中"二进制资源黑洞"修复的增量补强，聚焦
// 音频/媒体资源：goose 多模态审计指出 MCP 的音频、视频等媒体
// 资源应安全落盘而非直接丢弃，而既有的 blobExtFromMIME 只覆盖
// 了部分类型。这里提供一个覆盖面更广、且严格白名单化的 MIME→
// 扩展名映射，并以"默认关闭"的选择加入方式接线，确保默认输出
// 与历史逐字节一致。

const (
	// mediaBlobExtEnv 是扩展媒体类型映射的选择加入开关。
	// 只有显式设为真值（如 "1"/"true"）时才启用 mediaBlobExt；
	// 未设置或设为假值时使用既有的 blobExtFromMIME，保持旧行为。
	mediaBlobExtEnv = "ARGENT_MCP_MEDIA_BLOB_EXT"
)

// mediaBlobExt 把服务器提供的 MIME 类型映射为安全、固定的文件
// 扩展名。它是纯函数：不执行 I/O、不读取全局状态，对同一输入
// 始终返回同一结果。
//
// 安全性来自白名单：返回值全部是本函数内写死的 ASCII 扩展名，
// 绝不从 mimeType 派生。因此无论输入是路径穿越（"../../evil"）、
// 控制字符、反斜杠还是 CJK 文本，都无法把路径分隔符、控制字符
// 或非 ASCII 字节混入扩展名——不匹配白名单时一律回退到 ".bin"。
// MIME 类型按 RFC 规定大小写不敏感，且可携带 "; charset=..." 之类
// 的参数，解析时会先去掉参数、去空白并转小写再匹配。
func mediaBlobExt(mimeType string) string {
	// 去掉 MIME 参数（分号之后的部分），再去首尾空白并转小写。
	base := mimeType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimSpace(base))

	// 以下 case 的返回值共同组成白名单，全部为写死的安全 ASCII 串。
	switch base {
	// 图片
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/bmp", "image/x-ms-bmp":
		return ".bmp"
	case "image/tiff", "image/tiff-x":
		return ".tiff"
	// 音频
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return ".wav"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg", "audio/x-ogg":
		return ".ogg"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/aac", "audio/x-aac":
		return ".aac"
	case "audio/mp4", "audio/x-m4a", "audio/m4a":
		return ".m4a"
	// 视频
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	// 文档 / 数据
	case "application/pdf":
		return ".pdf"
	case "application/json":
		return ".json"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

// mediaBlobExtEnabled 报告是否选择加入扩展媒体类型映射。
// 未设置、空值或无法解析为真值时返回 false（默认关闭），
// 因此默认行为与既有实现逐字节一致。
func mediaBlobExtEnabled() bool {
	on, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(mediaBlobExtEnv)))
	return err == nil && on
}

// resolveBlobExt 根据选择加入开关在扩展映射与既有映射之间选择。
// 开关默认关闭时退化为 blobExtFromMIME，保证历史输出与测试不变。
func resolveBlobExt(mime string) string {
	if mediaBlobExtEnabled() {
		return mediaBlobExt(mime)
	}
	return blobExtFromMIME(mime)
}
