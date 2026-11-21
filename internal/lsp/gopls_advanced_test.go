package lsp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
)

// TestIntegrationGoplsAdvancedBestEffort 对四个已实现的进阶方法做结构化验证。
// 它复用 gopls_integration_test.go 的 workspace/fixture/就绪门，但在断言上采取 best-effort：
// 数量类结果（token 数、hint 数、lens 数）与 RPC 错误只记日志，绝不使套件变红。
// 只有"已成功返回的结构"上的不变量才用 t.Errorf 报告——那才是真正的客户端解析 bug。
// 例如 gopls 默认关闭 inlay hint / semantic tokens，codeLens/resolve 也可能因不透明 data 报错，
// 这些都是合法结果，必须允许通过。
func TestIntegrationGoplsAdvancedBestEffort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}

	gopls := locateGopls(t) // 服务器缺失时该函数自行 t.Skip，CI 无 gopls 也保持绿色。
	// lsp.Start 用 exec.Command("gopls") 启动服务器，因此它必须能通过 PATH 被发现。
	t.Setenv("PATH", filepath.Dir(gopls)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOTOOLCHAIN", "local")

	workspace := t.TempDir()
	mainPath := writeIntegrationWorkspace(t, workspace, integrationSource)

	// initialize 握手单独设限：冷启动的 gopls 首次索引标准库可能较慢。
	startCtx, startCancel := context.WithTimeout(context.Background(), 60*time.Second)
	client, err := lsp.Start(startCtx, lsp.Server{
		Name:        "gopls",
		Command:     "gopls",
		LanguageIDs: []string{"go"},
	}, workspace)
	startCancel()
	if err != nil {
		t.Skipf("gopls could not be started (integration optional): %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	openCtx, openCancel := context.WithTimeout(context.Background(), 30*time.Second)
	uri, err := client.Open(openCtx, mainPath)
	openCancel()
	if err != nil {
		t.Skipf("gopls did not accept the document (integration optional): %v", err)
	}

	declPos := findToken(t, integrationSource, "RunService", 0)
	callPos := findToken(t, integrationSource, "RunService", 1)
	declLine := declPos.Line

	// 就绪门：definition 要求 gopls 已完成类型检查，这可能滞后于 didOpen。
	// 轮询直到调用点解析到声明行；未就绪则跳过（环境/时序问题，不是客户端 bug）。
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 40*time.Second)
	deadline := time.Now().Add(30 * time.Second)
	var defs []lsp.Location
	var defErr error
	ready := false
	for time.Now().Before(deadline) {
		defs, defErr = client.DefinitionAt(readyCtx, mainPath, callPos)
		if defErr == nil && definitionAtLine(defs, mainPath, declLine) {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	readyCancel()
	if !ready {
		t.Skipf("gopls did not resolve the call site to the RunService declaration "+
			"(integration optional: server too slow or unavailable); last result=%+v, err=%v", defs, defErr)
	}
	t.Logf("ready gate: DefinitionAt -> %d location(s)", len(defs))

	// 覆盖整个文档的范围，供内联提示查询使用。fixture 为 ASCII，行号即 UTF-16 行偏移。
	endLine := len(strings.Split(integrationSource, "\n")) - 1
	fullRange := lsp.Range{
		Start: lsp.Position{Line: 0, Character: 0},
		End:   lsp.Position{Line: endLine, Character: 0},
	}

	// SemanticTokens：非 nil 结果必须满足每 token 五个整数的编码不变量。
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tokens, err := client.SemanticTokens(ctx, uri)
		if err != nil {
			t.Logf("SemanticTokens error (best-effort): %v", err)
			return
		}
		if tokens == nil {
			t.Log("SemanticTokens: no content (best-effort)")
			return
		}
		if len(tokens.Data)%5 != 0 {
			t.Errorf("SemanticTokens.Data length = %d, want a multiple of 5", len(tokens.Data))
		}
		t.Logf("SemanticTokens -> %d token(s)", len(tokens.Data)/5)
	}()

	// InlayHints：非空项的 Position.Line 不得为负；提示数量只记日志。
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		hints, err := client.InlayHints(ctx, uri, fullRange)
		if err != nil {
			t.Logf("InlayHints error (best-effort): %v", err)
			return
		}
		for i, hint := range hints {
			if hint.Position.Line < 0 {
				t.Errorf("InlayHints[%d].Position.Line = %d, want >= 0", i, hint.Position.Line)
			}
		}
		t.Logf("InlayHints -> %d hint(s)", len(hints))
	}()

	// CodeLens：非空项的 Range.Start.Line 不得为负；lens 数量只记日志。
	// 把结果保留下来供 ResolveCodeLens 使用（无真实 lens 时退化为合成 lens，保证四方法各调用一次）。
	lenses := func() []lsp.CodeLens {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		got, err := client.CodeLens(ctx, uri)
		if err != nil {
			t.Logf("CodeLens error (best-effort): %v", err)
			return nil
		}
		for i, lens := range got {
			if lens.Range.Start.Line < 0 {
				t.Errorf("CodeLens[%d].Range.Start.Line = %d, want >= 0", i, lens.Range.Start.Line)
			}
		}
		t.Logf("CodeLens -> %d lens(es)", len(got))
		return got
	}()

	// ResolveCodeLens：若成功返回非 nil，则必须被标记为已解析；RPC 错误只记日志。
	func() {
		lens := lsp.CodeLens{Range: fullRange}
		if len(lenses) > 0 {
			lens = lenses[0]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resolved, err := client.ResolveCodeLens(ctx, lens)
		if err != nil {
			t.Logf("ResolveCodeLens error (best-effort): %v", err)
			return
		}
		if resolved == nil {
			t.Log("ResolveCodeLens: nil result (best-effort)")
			return
		}
		if !resolved.IsResolved {
			t.Errorf("ResolveCodeLens returned an unresolved lens")
		}
		t.Log("ResolveCodeLens -> resolved lens")
	}()
}
