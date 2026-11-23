package mcp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 本文件堵上 MCP"二进制资源黑洞"：tools/call 结果可以嵌入
// 一个 "resource" 内容块，其资源携带 base64 的 `blob` 数据
// （二进制、音频、图片、PDF 等）。旧的 CallTool 解析器只读取
// `resource.text`，因此只有 blob 的资源什么也贡献不了。
//
// 设计约束（来自审计）：
//   - 默认行为逐字节不变。只有当显式通过
//     ARGENT_MCP_BLOB_ARTIFACT_DIR 配置了产物目录时，才写入 blob 产物。
//     该变量未设置时，CallTool
//     走与以往完全相同的代码路径。
//   - 不引入新依赖；仅用标准库，仿照 internal/tools/output_spill*.go 中的
//     产物约定（0700 目录、0600 O_EXCL 文件、随机十六进制
//     名称、尽力而为且绝不让原工具调用失败的写入）。
//   - 不可信输入是服务器提供的 blob 和 mimeType。两者在影响内存或
//     文件系统之前，都会被限制/净化。

const (
	// maxResourceBlobBytes 限制单个嵌入资源 blob 解码后的大小。
	// 该上限在解码*之前*检查，使恶意服务器无法通过
	// base64 放大（4 个编码字符 -> 3 字节）强制大量分配。
	maxResourceBlobBytes = 16 << 20 // 16 MiB

	// blobArtifactDirEnv 让 CallTool 的 blob 处理选择写入产物。
	// 为空/未设置保持旧行为（忽略 blob 资源）。
	blobArtifactDirEnv = "ARGENT_MCP_BLOB_ARTIFACT_DIR"

	blobArtifactPrefix = "mcp-blob-"
	blobIDBytes        = 8  // -> 16 个小写十六进制字符
	maxArtifactExtLen  = 12 // 对 ".svg"、".webp"、".jpeg" 等足够宽松
)

// 哨兵错误让调用方区分 blob 无法解码的原因。
var (
	ErrBlobEmpty     = errors.New("mcp: resource blob is empty")
	ErrBlobTooLarge  = errors.New("mcp: resource blob exceeds 16 MiB limit")
	ErrBlobBadBase64 = errors.New("mcp: resource blob is not valid base64")
)

// ResourceContent 是 tools/call 结果中 "resource" 内容块的嵌入资源。
// 根据 MCP，资源携带 `text` 或 base64 的 `blob`；
// 两者可同时存在，但格式良好的块只使用其中一个。
type ResourceContent struct {
	URI      string `json:"uri,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// DecodeResourceBlob 解码嵌入 MCP 资源的 base64 `blob`。
// 它是纯函数：无 I/O、无全局状态。它返回解码后的字节
// 以及服务器宣告的（去空白后的）MIME 类型。
//
// 解码后的载荷受 maxResourceBlobBytes 限制。编码长度
// 会先检查，使来自不可信服务器的超大字符串在 base64.StdEncoding
// 分配输出缓冲区之前就被拒绝。带填充的标准 base64 和
// 不带填充的原始标准 base64 都可接受。
func DecodeResourceBlob(resource ResourceContent) (data []byte, mime string, err error) {
	mime = strings.TrimSpace(resource.MIMEType)
	encoded := strings.TrimSpace(resource.Blob)
	if encoded == "" {
		return nil, mime, ErrBlobEmpty
	}

	// 16 MiB 载荷的编码字符上限，加上少量
	// 填充/原始编码的余量。在此限制可防止内存
	// 放大：我们绝不会对任意大的字符串调用 DecodeString。
	maxEncoded := base64.StdEncoding.EncodedLen(maxResourceBlobBytes) + 4
	if len(encoded) > maxEncoded {
		return nil, mime, fmt.Errorf("%w: encoded length %d", ErrBlobTooLarge, len(encoded))
	}

	data, err = base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// 有些服务器省略填充；作为回退接受原始标准 base64。
		var rawErr error
		data, rawErr = base64.RawStdEncoding.DecodeString(encoded)
		if rawErr != nil {
			return nil, mime, fmt.Errorf("%w: %v", ErrBlobBadBase64, err)
		}
	}
	if len(data) > maxResourceBlobBytes {
		return nil, mime, fmt.Errorf("%w: decoded %d bytes", ErrBlobTooLarge, len(data))
	}
	return data, mime, nil
}

// blobArtifactDir 报告配置的产物目录。为空表示
// blob 功能禁用，CallTool 必须保持旧行为。
func blobArtifactDir() string {
	return strings.TrimSpace(os.Getenv(blobArtifactDirEnv))
}

// saveBlobAsArtifact 是 CallTool 侧的适配器：解码、写入 dir 下、
// 并渲染模型看到的标记。它有意尽力而为——
// 任何失败都返回空字符串，因此解码或写入问题绝不会让
// 本已成功的工具调用失败（blob 保持与本次改动之前
// 一样不可见）。
func saveBlobAsArtifact(dir string, resource ResourceContent) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	data, mime, err := DecodeResourceBlob(resource)
	if err != nil {
		return ""
	}
	// 扩展名映射默认使用扩展媒体类型映射；
	// 只有 ARGENT_MCP_MEDIA_BLOB_EXT=0 时才回退到既有映射。
	path, err := SaveBlobArtifact(dir, data, resolveBlobExt(mime))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("[Binary resource saved to %s]", path)
}

// SaveBlobArtifact 把 data 写入 dir 下一个全新的、安全命名的文件，
// 并返回其路径。它仿照 internal/tools 的产物溢出约定，但不
// 导入或修改那个包（它由另一位 agent 负责）。
//
// 安全：`ext` 受服务器影响（源自 mimeType），因此经过
// 严格净化——任何路径分隔符、".." 或非字母数字字符
// 都会被拒绝，从而堵上路径逃逸途径。最终路径还会
// 验证保持在 dir 之下。文件以 0600、O_EXCL 创建在 0700
// 目录中，并使用随机十六进制名称，因此现有文件绝不会被覆盖，
// 且名称不含 shell 或路径元字符。
func SaveBlobArtifact(dir string, data []byte, ext string) (path string, err error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("mcp: artifact directory is empty")
	}
	cleanExt, err := sanitizeArtifactExt(ext)
	if err != nil {
		return "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return "", err
	}

	const attempts = 8
	for i := 0; i < attempts; i++ {
		id, err := randomBlobID()
		if err != nil {
			return "", err
		}
		name := blobArtifactPrefix + id + cleanExt
		candidate := filepath.Join(absDir, name)
		// 纵深防御：即使 name 是生成的，也要确认拼接后的
		// 路径不会逃逸出产物目录。
		if !pathWithinDir(absDir, candidate) {
			return "", fmt.Errorf("mcp: artifact path escapes directory: %s", candidate)
		}

		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // 极其不可能；换一个新 id 再试
		}
		if err != nil {
			return "", err
		}
		if _, werr := f.Write(data); werr != nil {
			f.Close()
			os.Remove(candidate) // 绝不留下不完整的产物
			return "", werr
		}
		if cerr := f.Close(); cerr != nil {
			os.Remove(candidate)
			return "", cerr
		}
		return candidate, nil
	}
	return "", fmt.Errorf("mcp: could not create a unique blob artifact in %s", absDir)
}

// sanitizeArtifactExt 规范化可选的文件扩展名。它最多接受
// maxArtifactExtLen 个 ASCII 字母数字，可带一个前导点，并
// 返回带前导点的小写形式。其他任何内容（分隔符、".."、
// 空白、标点）都是错误，以免改变目标路径。
func sanitizeArtifactExt(ext string) (string, error) {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return "", nil // 无扩展名的产物也可以
	}
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" || len(ext) > maxArtifactExtLen {
		return "", fmt.Errorf("mcp: invalid artifact extension %q", ext)
	}
	for _, r := range ext {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z') {
			return "", fmt.Errorf("mcp: invalid artifact extension %q", ext)
		}
	}
	return "." + strings.ToLower(ext), nil
}

// pathWithinDir 报告 p 是否解析到 dir 内部的位置。它与
// internal/tools 对其产物溢出使用的包含规则相同。
func pathWithinDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// randomBlobID 从 crypto/rand 返回 blobIDBytes*2 个小写十六进制字符。
func randomBlobID() (string, error) {
	var b [blobIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// blobExtFromMIME 把（服务器提供的）MIME 类型映射为安全、固定的文件
// 扩展名。未知类型回退到 ".bin"；结果总能被
// sanitizeArtifactExt 接受，因此 mimeType 永远无法影响路径。
func blobExtFromMIME(mime string) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	switch base {
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
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
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
