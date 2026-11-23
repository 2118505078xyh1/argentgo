package uri

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// DocsFS 是由调用方注入的 docs/ 文件系统（通常用 go:embed）。
// 本包不直接 embed，避免把 docs 目录绑死在库包上——native 等二进制可自行 embed
// 并注入，实现"二进制自带手册"。
type DocsFS struct {
	FS fs.FS
}

// docIndexEntry 是索引第一行的条目：文件名 + 正文在 dag 中的偏移。
type docIndexEntry struct {
	Name string `json:"name"`
	Off  int    `json:"off"`
}

// DocsHandler 是 argent://docs/<path> 的解析器。
// 构造时一次性把 embed.FS 里的 .md 压成两行索引，正文惰性解压（首次读取后缓存）。
type DocsHandler struct {
	mu      sync.Mutex
	index   []docIndexEntry // 第一行解码结果，按 name 排序
	blobs   []string        // 第二行按 base64 分号的 gzip 正文
	decoded map[string]string
	names   []string
}

// NewDocsHandler 从 fs.FS 构建文档索引。只索引 .md 文件，路径统一为正斜杠。
func NewDocsHandler(fsys fs.FS) (*DocsHandler, error) {
	if fsys == nil {
		return nil, errors.New("uri: nil docs filesystem")
	}
	h := &DocsHandler{decoded: make(map[string]string)}
	var names []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(path.Ext(p), ".md") {
			return nil
		}
		clean := strings.TrimPrefix(path.Clean(p), "./")
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data); err != nil {
			zw.Close()
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		h.blobs = append(h.blobs, base64.StdEncoding.EncodeToString(buf.Bytes()))
		h.index = append(h.index, docIndexEntry{Name: clean, Off: len(h.blobs) - 1})
		names = append(names, clean)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("uri: build docs index: %w", err)
	}
	sort.Strings(names)
	h.names = names
	return h, nil
}

// EncodeIndex 把索引编码成两行文本：第一行是 JSON 条目数组，第二行是 base64
// gzip 正文（按分号分隔）。二进制可把这两行作为常量嵌入，省去运行时 gzip。
func (h *DocsHandler) EncodeIndex() (string, error) {
	first, err := json.Marshal(h.index)
	if err != nil {
		return "", err
	}
	return string(first) + "\n" + strings.Join(h.blobs, ";"), nil
}

// NewDocsHandlerFromIndex 从两行索引恢复 handler（配合 EncodeIndex 使用）。
func NewDocsHandlerFromIndex(indexText string) (*DocsHandler, error) {
	lines := strings.SplitN(indexText, "\n", 2)
	if len(lines) != 2 {
		return nil, errors.New("uri: docs index needs two lines")
	}
	h := &DocsHandler{decoded: make(map[string]string)}
	if err := json.Unmarshal([]byte(lines[0]), &h.index); err != nil {
		return nil, fmt.Errorf("uri: decode docs index: %w", err)
	}
	h.blobs = strings.Split(lines[1], ";")
	for _, e := range h.index {
		h.names = append(h.names, e.Name)
	}
	sort.Strings(h.names)
	return h, nil
}

// Resolve 实现 Handler。path 可以是完整文件名，也支持省略 .md 后缀。
func (h *DocsHandler) Resolve(docPath string) (Resource, error) {
	docPath = strings.TrimPrefix(strings.TrimSpace(docPath), "/")
	if docPath == "" || docPath == "/" {
		docPath = "README.md"
	}
	name := docPath
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if content, ok := h.decoded[name]; ok {
		return Resource{Scheme: "docs", Path: name, Content: content}, nil
	}
	idx := -1
	for i, e := range h.index {
		if strings.EqualFold(e.Name, name) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Resource{}, fmt.Errorf("%w: docs/%s", ErrNotFound, docPath)
	}
	blob := h.blobs[h.index[idx].Off]
	compressed, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return Resource{}, fmt.Errorf("uri: decode docs blob: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return Resource{}, fmt.Errorf("uri: docs gzip: %w", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		zr.Close()
		return Resource{}, fmt.Errorf("uri: decompress docs: %w", err)
	}
	zr.Close()
	content := out.String()
	h.decoded[name] = content
	return Resource{Scheme: "docs", Path: name, Content: content}, nil
}

// List 实现 Handler。
func (h *DocsHandler) List() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.names))
	copy(out, h.names)
	return out
}
