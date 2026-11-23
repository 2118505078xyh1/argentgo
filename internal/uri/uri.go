// Package uri 为 argentgo 提供统一的 argent://<scheme>/<path> 引用解析。
//
// 背景（学自 oh-my-pi internal-urls）：技能、命令、文档、工具结果缓存等资源
// 都需要稳定的引用地址。此前 argent:// 只有 cache 一个特例（散落在 agent/
// tools 包），skill 链接又是另一套解析——没有统一注册表，新增 scheme 要改多处，
// 也无法校验引用是否有效。本包提供一个线程安全的scheme 注册表与统一 Resolve。
//
// 纯标准库、无 cgo、无 I/O 副作用（handler 自行决定）。所有 scheme 通过
// Register 注入；未注册的 scheme 返回明确的 ErrUnknownScheme。
package uri

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

const schemeName = "argent"

var (
	// ErrUnknownScheme 表示没有为该 scheme 注册 handler。
	ErrUnknownScheme = errors.New("uri: unknown argent scheme")
	// ErrInvalidURI 表示 URI 格式不合法。
	ErrInvalidURI = errors.New("uri: invalid argent uri")
	// ErrNotFound 表示 handler 找不到对应资源。
	ErrNotFound = errors.New("uri: resource not found")
)

// Resource 是一次解析的结果。
type Resource struct {
	// Scheme 是资源类别（docs/skill/...）。
	Scheme string
	// Path 是 scheme 内的规范化路径。
	Path string
	// Content 是解析出的文本内容（二进制资源由 handler 自行编码）。
	Content string
}

// Handler 解析一个 scheme 内的路径。
// path 已去掉前导斜杠并做 Clean；未找到资源应返回 ErrNotFound。
type Handler interface {
	Resolve(path string) (Resource, error)
	// List 返回该 scheme 下可见的路径列表（供 doctor/补全使用）。
	List() []string
}

// HandlerFunc 是只实现 Resolve 的便捷适配器（List 返回空）。
type HandlerFunc func(path string) (Resource, error)

func (f HandlerFunc) Resolve(path string) (Resource, error) { return f(path) }
func (f HandlerFunc) List() []string                        { return nil }

// Registry 是 scheme→handler 的线程安全注册表。
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	order    []string // 注册顺序，供 List 稳定输出
}

// New 创建空注册表。
func New() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

var defaultRegistry = New()

// Default 返回进程级默认注册表。
func Default() *Registry { return defaultRegistry }

// Register 注册一个 scheme。重复注册同名 scheme 会返回错误（避免静默覆盖）。
func (r *Registry) Register(scheme string, h Handler) error {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" || h == nil {
		return fmt.Errorf("%w: empty scheme or nil handler", ErrInvalidURI)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[scheme]; exists {
		return fmt.Errorf("uri: scheme %q already registered", scheme)
	}
	r.handlers[scheme] = h
	r.order = append(r.order, scheme)
	return nil
}

// RegisterDefault 注册到默认注册表。
func RegisterDefault(scheme string, h Handler) error { return defaultRegistry.Register(scheme, h) }

// Parse 解析一个 argent:// URI，返回 scheme 与规范化 path（不调用 handler）。
func Parse(raw string) (scheme, path string, err error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, schemeName+"://") {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidURI, raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != schemeName {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidURI, raw)
	}
	scheme = strings.ToLower(u.Host)
	if scheme == "" {
		return "", "", fmt.Errorf("%w: missing scheme in %q", ErrInvalidURI, raw)
	}
	path = strings.TrimPrefix(u.Path, "/")
	if path == "" {
		path = "/"
	}
	// 防路径逃逸：不允许 .. 段。
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return "", "", fmt.Errorf("%w: path escape in %q", ErrInvalidURI, raw)
		}
	}
	return scheme, path, nil
}

// Resolve 解析 URI 并返回资源内容。
func (r *Registry) Resolve(raw string) (Resource, error) {
	scheme, path, err := Parse(raw)
	if err != nil {
		return Resource{}, err
	}
	r.mu.RLock()
	h := r.handlers[scheme]
	r.mu.RUnlock()
	if h == nil {
		return Resource{}, fmt.Errorf("%w: %q", ErrUnknownScheme, scheme)
	}
	res, err := h.Resolve(path)
	if err != nil {
		return Resource{}, err
	}
	if res.Scheme == "" {
		res.Scheme = scheme
	}
	if res.Path == "" {
		res.Path = path
	}
	return res, nil
}

// ResolveDefault 使用默认注册表解析。
func ResolveDefault(raw string) (Resource, error) { return defaultRegistry.Resolve(raw) }

// Schemes 返回默认注册表中已注册的 scheme 列表（排序稳定）。
func Schemes() []string { return defaultRegistry.Schemes() }

// Schemes 返回已注册的 scheme 列表（按注册顺序）。
func (r *Registry) Schemes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	sort.Strings(out)
	return out
}

// List 列出某 scheme 下的所有路径；未注册返回错误。
func (r *Registry) List(scheme string) ([]string, error) {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	r.mu.RLock()
	h := r.handlers[scheme]
	r.mu.RUnlock()
	if h == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownScheme, scheme)
	}
	return h.List(), nil
}

// IsURI 报告字符串是否是一个 argent:// URI。
func IsURI(s string) bool {
	_, _, err := Parse(s)
	return err == nil
}
