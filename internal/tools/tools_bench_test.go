package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件记录工具热路径的性能基线：输出
// 截断、schema 投影和 repo 地图遍历。夹具是
// b.TempDir() 下的固定树或固定字符串；不涉及网络、模型或真实
// 墙上时钟依赖。不断言任何墙上时钟阈值。

var (
	benchToolsClipSink   string
	benchToolsSchemaSink json.RawMessage
	benchToolsRepoSink   string
)

// BenchmarkClipToSizes 测量感知 UTF-8 的头尾裁剪器在几种
// 输入大小和脚本上的表现（另一个大输入基线已在
// tools_clip_diff_test.go 中）。
func BenchmarkClipToSizes(b *testing.B) {
	cases := []struct {
		name  string
		text  string
		limit int
	}{
		{"ascii-1KB", strings.Repeat("abcdefghij", 100), 512},
		{"ascii-64KB", strings.Repeat("abcdefghij", 6400), 20000},
		{"cjk-24KB", strings.Repeat("中文裁剪测试工具", 1000), 12000},
		{"emoji-48KB", strings.Repeat("😀🚀", 6000), 16000},
		{"mixed-50KB", strings.Repeat("ab中😀e\u0301", 5000), 18000},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchToolsClipSink = clipTo(bc.text, bc.limit)
			}
		})
	}
}

// BenchmarkClip 测量通过 context 配置的输出上限进行的 clip，
// 即每个工具结果在跨越传输层前都会进行的调用。
func BenchmarkClip(b *testing.B) {
	ctx := context.Background()
	cases := []struct {
		name string
		text string
	}{
		{"ascii-1KB", strings.Repeat("abcdefghij", 100)},
		{"ascii-64KB", strings.Repeat("abcdefghij", 6400)},
		{"cjk-24KB", strings.Repeat("中文裁剪测试工具", 1000)},
		{"emoji-48KB", strings.Repeat("😀🚀", 6000)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchToolsClipSink = clip(ctx, bc.text)
			}
		})
	}
}

// benchSchemaParams 构建带 n 个字符串属性的工具参数 schema。
func benchSchemaParams(n int) json.RawMessage {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `"p%03d":{"type":"string","description":"field %03d"}`, i, i)
	}
	sb.WriteString(`},"required":["p000"]}`)
	return json.RawMessage(sb.String())
}

// BenchmarkInjectIntentSchema 测量在所有工具 schema 上公布
// `i` 意图属性的有序 JSON 重写。
func BenchmarkInjectIntentSchema(b *testing.B) {
	cases := []struct {
		name   string
		params json.RawMessage
	}{
		{"empty-object", json.RawMessage(`{"type":"object","properties":{}}`)},
		{"props-8", benchSchemaParams(8)},
		{"props-64", benchSchemaParams(64)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchToolsSchemaSink = InjectIntentSchema(bc.params)
			}
		})
	}
}

// benchSpecTool 是一个最小的 Tool，仅用于测试 Registry.Specs。
type benchSpecTool struct {
	name   string
	desc   string
	schema string
}

func (t benchSpecTool) Name() string        { return t.name }
func (t benchSpecTool) Description() string { return t.desc }
func (t benchSpecTool) Schema() string      { return t.schema }
func (t benchSpecTool) Mutating() bool      { return false }
func (t benchSpecTool) Run(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}

// BenchmarkRegistrySpecs 测量完整的公布 schema 构建：
// 描述截断加上对每个已注册工具的意图投影。
func BenchmarkRegistrySpecs(b *testing.B) {
	for _, n := range []int{8, 32, 64} {
		b.Run(fmt.Sprintf("tools=%d", n), func(b *testing.B) {
			r := &Registry{tools: make(map[string]Tool, n), order: make([]string, 0, n)}
			for i := 0; i < n; i++ {
				name := fmt.Sprintf("tool%03d", i)
				r.tools[name] = benchSpecTool{
					name:   name,
					desc:   strings.Repeat("tool description ", 32),
					schema: string(benchSchemaParams(16)),
				}
				r.order = append(r.order, name)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchToolsIntSink = len(r.Specs())
			}
		})
	}
}

var benchToolsIntSink int

// benchRepoTree 写入一个固定的仓库树，包含 n 个类 Go 源文件。
func benchRepoTree(b *testing.B, n int) string {
	b.Helper()
	root := b.TempDir()
	for i := 0; i < n; i++ {
		dir := root
		if i%3 == 0 {
			dir = filepath.Join(root, fmt.Sprintf("pkg%02d", i%12))
		}
		name := fmt.Sprintf("file%04d.go", i)
		body := fmt.Sprintf("package p\n\nfunc Fn%04d() int { return %d }\n", i, i)
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

// BenchmarkRepoMap 测量每次 repo_map 调用都会运行的目录遍历、
// 行数统计和符号区，直到 80 文件上限。
func BenchmarkRepoMap(b *testing.B) {
	for _, n := range []int{10, 60, 200} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			root := benchRepoTree(b, n)
			ctx := context.Background()
			args := json.RawMessage(`{}`)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := repoMapTool{}.Run(ctx, root, args)
				if err != nil {
					b.Fatal(err)
				}
				benchToolsRepoSink = out
			}
		})
	}
}

// BenchmarkRepoMapSymbolSection 单独测量符号区的排名和 token 预算渲染，
// 与文件系统遍历无关。
func BenchmarkRepoMapSymbolSection(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			syms := make([]repoSymbol, n)
			for i := range syms {
				syms[i] = repoSymbol{
					File:     fmt.Sprintf("pkg/file%04d.go", i%32),
					Name:     fmt.Sprintf("Fn%05d", i),
					Line:     i + 1,
					Kind:     "func",
					Exported: true,
				}
			}
			mentions := []string{"Fn00042", "pkg/file0007.go"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchToolsIntSink = len(repoMapSymbolSection(syms, mentions, repoMapSymbolTokenBudget))
			}
		})
	}
}
