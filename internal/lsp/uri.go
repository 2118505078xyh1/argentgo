package lsp

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// PathToURI converts a filesystem path to a file:// URI. Windows paths become
// file:///C:/x/y; characters outside the URI safe set are percent-encoded.
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

// URIToPath turns a file:// URI back into a filesystem path.
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
	// /C:/x is the URI form of C:\x.
	if len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}
