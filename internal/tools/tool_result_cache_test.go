package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCacheOversizedToolResult 锁定 #33：超大输出在默认路径上被换成
// argent://cache URI；read 在 resolve 之前解析该 URI 并取回正文。
func TestCacheOversizedToolResult(t *testing.T) {
	ctx := withToolResultCache(context.Background(), newSessionToolResultCache(toolResultCacheByteBudget))
	raw := strings.Repeat("0123456789", toolResultCacheThreshold/10+10)

	out, handled := cacheOversizedToolResult(ctx, raw)
	if !handled || out == "" {
		t.Fatal("超大输出未被缓存")
	}
	if !strings.Contains(out, toolResultCacheURIPrefix) ||
		!strings.Contains(out, toolResultCacheURISuffix) {
		t.Fatalf("缓存结果缺少 URI：%q", out)
	}
	start := strings.Index(out, toolResultCacheURIPrefix)
	rest := out[start:]
	end := strings.Index(rest, toolResultCacheURISuffix)
	uri := rest[:end+len(toolResultCacheURISuffix)]

	// 正常文件路径走原有读取逻辑；缓存 URI 在 resolve 之前被拦截。
	root := t.TempDir()
	garden := filepath.Join(root, "unrelated.txt")
	if err := os.WriteFile(garden, []byte("ordinary"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (readTool{}).Run(ctx, root, json.RawMessage(
		[]byte(`{"path":`+strconvQuote(uri)+`}`)))
	if err != nil {
		t.Fatalf("read cache URI: %v", err)
	}

	// 单行超长内容仍走 #38 的行截断，但必须来自缓存正文而非普通文件。
	if !strings.HasPrefix(result, "0123456789") ||
		!strings.Contains(result, lineTruncatedMarker) {
		t.Fatalf("read 未返回缓存正文：%q", result[:min(len(result), 120)])
	}

	// 过期/伪造 URI 返回"勿重放副作用"提示，而不是文件错误。
	missCtx := withToolResultCache(context.Background(), newSessionToolResultCache(toolResultCacheByteBudget))
	deadURI := toolResultCacheURIPrefix + "deadbeef" + toolResultCacheURISuffix
	miss, err := (readTool{}).Run(missCtx, root, json.RawMessage(
		[]byte(`{"path":`+strconvQuote(deadURI)+`}`)))
	if err != nil {
		t.Fatalf("read missing cache URI should not error: %v", err)
	}
	if !strings.Contains(miss, "DO NOT repeat side-effecting actions") {
		t.Fatalf("miss 提示缺少勿重放副作用文案：%q", miss)
	}
}

// strconvQuote 用标准库生成 JSON 字符串字面量，避免手工转义 URI。
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
