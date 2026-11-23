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
// 扩展名映射，并默认开启；只有显式设为 "0" 时才回退到既有映射。

const (
	// mediaBlobExtEnv 是扩展媒体类型映射的开关。
	// 默认开启：取值空或不为 "0" 时启用 mediaBlobExt；
	// 只有显式设为 "0" 时才回退到既有的 blobExtFromMIME。
	mediaBlobExtEnv = "ARGENT_MCP_MEDIA_BLOB_EXT"

	// imageArtifactEnv 是 image 内容块落盘的选择加入开关。
	// 默认关闭，因此 CallTool 对 image 块的输出与历史逐字节一致。
	// 只有显式设为真值时，才会把 image 的 base64 数据写入
	// ARGENT_MCP_BLOB_ARTIFACT_DIR 指向的目录，并在占位符里附上可读路径。
	imageArtifactEnv = "ARGENT_MCP_IMAGE_ARTIFACT"
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

// mediaBlobExtEnabled 报告是否启用扩展媒体类型映射。
// 取值空或不为 "0" 时返回 true（默认开启），只有显式设为 "0" 才返回 false。
func mediaBlobExtEnabled() bool {
	return strings.TrimSpace(os.Getenv(mediaBlobExtEnv)) != "0"
}

// resolveBlobExt 根据开关在扩展映射与既有映射之间选择。
// 默认使用扩展映射；只有开关显式设为 "0" 时才退化为 blobExtFromMIME。
func resolveBlobExt(mime string) string {
	if mediaBlobExtEnabled() {
		return mediaBlobExt(mime)
	}
	return blobExtFromMIME(mime)
}

// callToolBlock 在 ContentBlock 之上额外携带 MCP image 内容块的 base64
// `data` 字段。ContentBlock 定义于 content_filter.go（本任务不改该文件），
// 故用内嵌方式扩展：原有字段的名称、类型与 JSON 解码行为完全不变，
// 新增的 Data 仅在 image 落盘时使用。
type callToolBlock struct {
	ContentBlock
	Data string `json:"data"`
}

// filterCallToolBlocks 按可见性过滤，同时保留每个块附带的 `data`。
// 行为与 filterContentBlocks 一致（复用同一个 contentBlockVisible 判定），
// 仅用于 CallTool 中 minPriority>0 的场景。它是纯函数：不改入参切片
// 及其元素，全部保留时直接返回原切片，避免零拷贝语义变化。
func filterCallToolBlocks(blocks []callToolBlock, minPriority int) []callToolBlock {
	if len(blocks) == 0 {
		return blocks
	}
	kept := 0
	for _, block := range blocks {
		if contentBlockVisible(block.ContentBlock, minPriority) {
			kept++
		}
	}
	if kept == len(blocks) {
		return blocks
	}
	out := make([]callToolBlock, 0, kept)
	for _, block := range blocks {
		if contentBlockVisible(block.ContentBlock, minPriority) {
			out = append(out, block)
		}
	}
	return out
}

// imageArtifactEnabled 报告是否选择加入 image 落盘。
// 未设置、空值或无法解析为真值时返回 false（默认关闭），
// 因此 CallTool 的默认输出与既有实现逐字节一致。
func imageArtifactEnabled() bool {
	on, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(imageArtifactEnv)))
	return err == nil && on
}

// imageBlockPlaceholder 渲染 image 内容块的占位符。
//
// 默认（开关关闭、未配置产物目录、无可解码数据、落盘失败任一情形成立）
// 逐字节返回旧占位符 "[image: <mime>]"。只有开关开启且配置了产物目录、
// 数据可解码且成功落盘时，才把占位符增强为
// "[image: <mime> → <path>]"，让模型知道可以用 read 工具读取该文件。
//
// mime 的显示与旧代码完全一致（空值回退 "unknown"），不在此处做净化，
// 保证开关关闭时占位符逐字节不变；路径由 SaveBlobArtifact 生成，
// 本身已做白名单/逃逸防护，因此特殊或 CJK mime 不会影响落盘安全。
func imageBlockPlaceholder(block callToolBlock) string {
	mime := block.MIMEType
	if mime == "" {
		mime = "unknown"
	}
	base := "[image: " + mime + "]"
	if !imageArtifactEnabled() {
		return base
	}
	dir := blobArtifactDir()
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(block.Data) == "" {
		return base
	}
	path := saveImageAsArtifact(dir, block.MIMEType, block.Data)
	if path == "" {
		return base
	}
	return "[image: " + mime + " → " + path + "]"
}

// saveImageAsArtifact 把 image 块的 base64 数据解码后写入 dir 下的全新
// 产物文件，返回其路径。它复用 DecodeResourceBlob 与 SaveBlobArtifact，
// 因此享有同样的大小上限、扩展名白名单、0700 目录、0600 O_EXCL 文件、
// 随机唯一名称与防逃逸校验。任何失败都返回空字符串，保持尽力而为语义：
// 坏数据绝不让本已成功的工具调用失败。
func saveImageAsArtifact(dir, mime, encoded string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	resource := ResourceContent{MIMEType: mime, Blob: encoded}
	data, _, err := DecodeResourceBlob(resource)
	if err != nil {
		return ""
	}
	path, err := SaveBlobArtifact(dir, data, resolveBlobExt(mime))
	if err != nil {
		return ""
	}
	return path
}
