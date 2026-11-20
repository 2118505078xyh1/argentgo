package lsp

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// PathToURI 把文件系统路径转换为 file:// URI。Windows 路径变成
// PathToURI 把文件系统路径转换为 file:// URI。Windows 路径变成 file:///C:/x/y；URI 安全集之外的字符被百分号编码。
func PathToURI(path string) string {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	var out strings.Builder
	out.Grow(len(cleaned) + 8)
	out.WriteString("file://")
	for i := 0; i < len(cleaned); i++ {
		ch := cleaned[i]
		switch {
		case ch >= 'A' && ch <= 'Z',
			ch >= 'a' && ch <= 'z',
			ch >= '0' && ch <= '9',
			ch == '/', ch == '-', ch == '_', ch == '.', ch == '~', ch == ':':
			out.WriteByte(ch)
		default:
			fmt.Fprintf(&out, "%%%02X", ch)
		}
	}
	return out.String()
}

// URIToPath 把 file:// URI 转回文件系统路径。
func URIToPath(uri string) string {
	rest := strings.TrimPrefix(uri, "file://")
	decoded := make([]byte, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		if rest[i] == '%' && i+2 < len(rest) {
			if value, err := strconv.ParseUint(rest[i+1:i+3], 16, 8); err == nil {
				decoded = append(decoded, byte(value))
				i += 2
				continue
			}
		}
		decoded = append(decoded, rest[i])
	}
	path := string(decoded)
	// /C:/x 是 C:\x 的 URI 形式。
	if len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}
