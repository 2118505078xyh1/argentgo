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

// This file records performance baselines for the tools hot paths: output
// clipping, schema projection and the repo map walk. Fixtures are fixed trees
// under b.TempDir() or fixed strings; no network, model or real wall-clock
// dependency is involved. No wall-clock thresholds are asserted.

var (
	benchToolsClipSink   string
	benchToolsSchemaSink json.RawMessage
	benchToolsRepoSink   string
)

// BenchmarkClipToSizes measures the UTF-8-aware head/tail clipper over several
// input sizes and scripts (a separate large-input baseline already lives in
// tools_clip_diff_test.go).
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

// BenchmarkClip measures clip through the context-configured output limit,
// i.e. the call every tool result makes before crossing the wire.
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

// benchSchemaParams builds a tool parameter schema with n string properties.
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

// BenchmarkInjectIntentSchema measures the ordered JSON rewrite that advertises
// the `i` intent property on every tool schema.
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

// benchSpecTool is a minimal Tool used only to exercise Registry.Specs.
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

// BenchmarkRegistrySpecs measures the full advertised-schema build: description
// capping plus intent projection for every registered tool.
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

// benchRepoTree writes a fixed repository tree with n Go-ish source files.
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

// BenchmarkRepoMap measures the directory walk, line counting and symbol
// section that run on every repo_map call, up to the 80-file cap.
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

// BenchmarkRepoMapSymbolSection isolates the ranking and token-budgeted render
// of the symbol section, independent of filesystem walking.
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
