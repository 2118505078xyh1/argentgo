// Package provider 动态模型目录抓取。
//
// catalog_cache.go 提供纯策略（ShouldRefresh/Merge/FingerprintStatic），
// catalog_source.go 提供原子落盘与严格加载，但两者都不碰网络——此前用户看到的
// 模型目录是 20 个编译期静态模板，provider 上新/下架/改名模型无法感知。
// 本文件补上网络边界：GET {baseURL}/models，带 If-None-Match / If-Modified-Since，
// 304 视为未变；解析 OpenAI 风格 {"data":[{"id","name"}]}；校验 origin 防止把
// gateway OAuth 凭据发到跨域目录 URL；错误信息清空 url/headers/body 防泄露。
//
// 纯标准库、无 cgo、无后台 goroutine（由调用方决定是否异步刷新）。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ModelsRefreshTimeout 是单次目录抓取的独立超时（对齐 codex MODELS_REFRESH_TIMEOUT）。
	ModelsRefreshTimeout = 5 * time.Second
	// modelsPath 是 OpenAI 兼容的目录端点。
	modelsPath = "/models"
	// maxCatalogBody 限制响应体大小，防恶意服务端撑爆内存。
	maxCatalogBody = 8 << 20 // 8 MiB
)

// FetchResult 是一次抓取的结果。
type FetchResult struct {
	// Models 是解析出的模型列表（已校验 ID 非空且唯一）。
	Models []Model
	// ETag / LastModified 原样保留，供下次条件请求回传。
	ETag         string
	LastModified string
	// NotModified 为 true 表示服务端返回 304，Models 为空但 ETag/LastModified 可能更新。
	NotModified bool
}

// FetchOptions 配置一次抓取。
type FetchOptions struct {
	// BaseURL 是 provider 的 API 根（如 https://api.openai.com/v1）。
	BaseURL string
	// AuthHeader / AuthValue 是可选鉴权头（如 Authorization: Bearer xxx）。
	// 空值时视为匿名抓取。
	AuthHeader string
	AuthValue  string
	// ETag / LastModified 是上次的缓存校验器，非空时作为条件请求头回传。
	ETag         string
	LastModified string
	// Client 可注入自定义 HTTP 客户端（测试/代理场景）；nil 使用默认客户端。
	Client *http.Client
	// Timeout 覆盖默认超时；<=0 使用 ModelsRefreshTimeout。
	Timeout time.Duration
}

// ErrCatalogOriginMismatch 表示目录 URL 与 baseURL 不同源（防止凭据跨域泄露）。
var ErrCatalogOriginMismatch = errors.New("provider: catalog URL origin mismatch")

// ErrCatalogURLInvalid 表示 baseURL 无法解析为绝对 http(s) URL。
var ErrCatalogURLInvalid = errors.New("provider: invalid catalog base URL")

// catalogURL 从 baseURL 派生目录端点。要求绝对 http(s) URL，且路径以 /models 结尾。
func catalogURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", fmt.Errorf("%w: empty", ErrCatalogURLInvalid)
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrCatalogURLInvalid, base)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: scheme %q", ErrCatalogURLInvalid, u.Scheme)
	}
	// 拼 /models，保留 base 的路径前缀（如 /v1）。
	p := strings.TrimRight(u.Path, "/") + modelsPath
	return u.Scheme + "://" + u.Host + p, nil
}

// sameOrigin 比较 base 与 target 的 scheme+host（大小写不敏感）。
func sameOrigin(base, target string) bool {
	a, err := url.Parse(base)
	if err != nil || a.Scheme == "" || a.Host == "" {
		return false
	}
	b, err := url.Parse(target)
	if err != nil || b.Scheme == "" || b.Host == "" {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// FetchCatalog 抓取一次模型目录。它不发后台请求，也不写磁盘——调用方负责
// 用 Merge + WriteCatalogAtomic 落盘。
func FetchCatalog(ctx context.Context, opts FetchOptions) (FetchResult, error) {
	endpoint, err := catalogURL(opts.BaseURL)
	if err != nil {
		return FetchResult{}, err
	}
	// origin 校验：目录端点必须来自 baseURL 本身，不接受外部重定向到跨域 URL。
	if !sameOrigin(opts.BaseURL, endpoint) {
		return FetchResult{}, fmt.Errorf("%w: %s", ErrCatalogOriginMismatch, redactedURL(endpoint))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return FetchResult{}, sanitizeFetchErr(err)
	}
	if opts.AuthHeader != "" && opts.AuthValue != "" {
		req.Header.Set(opts.AuthHeader, opts.AuthValue)
	}
	if opts.ETag != "" {
		req.Header.Set("If-None-Match", opts.ETag)
	}
	if opts.LastModified != "" {
		req.Header.Set("If-Modified-Since", opts.LastModified)
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = ModelsRefreshTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := client.Do(req.WithContext(callCtx))
	if err != nil {
		return FetchResult{}, sanitizeFetchErr(err)
	}
	defer resp.Body.Close()

	result := FetchResult{
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	if resp.StatusCode == http.StatusNotModified {
		result.NotModified = true
		return result, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 不清空错误体原文里的敏感信息——这里根本不读 body。
		return FetchResult{}, fmt.Errorf("provider: catalog fetch status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBody+1))
	if err != nil {
		return FetchResult{}, sanitizeFetchErr(err)
	}
	if len(body) > maxCatalogBody {
		return FetchResult{}, errors.New("provider: catalog response too large")
	}

	var payload struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return FetchResult{}, errors.New("provider: catalog response is not valid JSON")
	}

	seen := make(map[string]bool, len(payload.Data))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			// 服务端返回空 ID 或重复 ID：整份目录不可信，拒绝。
			return FetchResult{}, errors.New("provider: catalog contains empty or duplicate model id")
		}
		seen[id] = true
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = id
		}
		result.Models = append(result.Models, Model{ID: id, Name: name})
	}
	return result, nil
}

// redactedURL 只保留 scheme+host，路径折叠，避免凭据出现在错误里。
func redactedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	return u.Scheme + "://" + u.Host + "/<redacted>"
}

// sanitizeFetchErr 去掉底层错误里的 URL 与 header 片段（http.Client 的 error
// 常带完整 URL，可能含查询串里的 key）。
func sanitizeFetchErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// 任何 http(s) URL 都折叠为 scheme://host/<redacted>。
	for _, scheme := range []string{"https://", "http://"} {
		if i := strings.Index(msg, scheme); i >= 0 {
			rest := msg[i+len(scheme):]
			end := strings.IndexAny(rest, " \t\"'")
			if end < 0 {
				end = len(rest)
			}
			msg = msg[:i] + scheme + redactedURL(scheme+rest[:end]) + msg[i+len(scheme)+end:]
		}
	}
	return errors.New("provider: catalog fetch failed: " + msg)
}
