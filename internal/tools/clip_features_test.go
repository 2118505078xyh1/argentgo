package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"example.com/argentgo/internal/tokenize"
)

// TestClipLossHeader 锁定 #36：默认 clip 路径的裁剪帧报告原始 token 数
// 与总行数，且保留旧的 [truncated] 子串以兼容既有调用方。
func TestClipLossHeader(t *testing.T) {
	raw := strings.Repeat("中文😀abc\n", 12000) // 约 132 KB，走 clip 而非缓存
	out := clip(context.Background(), raw)

	if !strings.Contains(out, "[truncated]") {
		t.Fatal("默认裁剪帧丢失了旧的 [truncated] 标记")
	}
	wantTokens := tokenize.Estimate(raw)
	wantLines := strings.Count(raw, "\n") + 1
	header := lossHeader(raw)
	if !strings.Contains(out, header) {
		t.Fatalf("裁剪帧缺少损失头 %q；实际输出前缀 %q", header, out[:min(len(out), 200)])
	}
	if !strings.Contains(header, "[truncated: original ~") ||
		!strings.Contains(header, " lines, kept head+tail]") {
		t.Fatalf("损失头格式不符：%q", header)
	}
	if wantTokens <= 0 || wantLines <= 1 {
		t.Fatalf("损失头的原始量不合理：tokens=%d lines=%d", wantTokens, wantLines)
	}
}

// TestClipJSONEscapedBudget 锁定 #37：全转义字符输出经 clip 后，
// json.Marshal 的长度严格不超过工具输出上限。
func TestClipJSONEscapedBudget(t *testing.T) {
	cases := map[string]string{
		"newline":   strings.Repeat("\n", 60000),
		"quote":     strings.Repeat("\"", 60000),
		"backslash": strings.Repeat("\\", 60000),
		"mixed":     strings.Repeat("\\\"\n", 20000),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			out := clip(context.Background(), raw)
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			// 1.2 序列化预留（codex with_serialization_allowance）。
			allowance := maxToolOutput + maxToolOutput/5
			if len(encoded) > allowance {
				t.Fatalf("marshal 后长度 %d 超过含预留的预算 %d", len(encoded), allowance)
			}
			if len(out) >= len(raw) {
				t.Fatal("转义密集输出未被裁剪")
			}
		})
	}
}
