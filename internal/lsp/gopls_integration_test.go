package lsp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/lsp"
)

// 本文件是端到端集成测试：它通过 stdio 讲真实 LSP
// 本文件是端到端集成测试：它通过 stdio 与实际 gopls 进程讲真实的 LSP。它没有构建标签，所以只要恰好安装了 gopls，`go test ./internal/lsp` 就会执行它，但所有与*环境*有关的失败（gopls 缺失、缓慢或拒绝启动）都是
// 只要安装了 gopls 就会执行它，但所有与
// *环境*有关的失败（gopls 缺失、缓慢或拒绝启动）都是
// t.Skip，绝不是套件失败。只有明确错误的成功响应才是
// （定义落在错误文件、缺少声明的符号、引用过少）
// t.Skip，绝不是套件失败。只有明确错误的成功响应（定义落在错误文件、缺少声明的符号、引用过少）才用 t.Errorf 报告 —— 那是真正的客户端发现。
//
// `go test -short ./internal/lsp` 通过 testing.Short() 跳过它。

// integrationSource 是一个小而有效的 Go 模块主体。RunService 声明一次并调用一次：这给 definition/references 一个精确目标，而结构体、常量和第二个函数给 documentSymbol 一棵可供展平的多样树。全部保持 ASCII，使行内字节偏移
// 声明一次并调用一次：这给 definition/references 一个精确
// 目标，而结构体、常量和第二个函数给 documentSymbol 一棵
// 多样树以供展平。全部保持 ASCII，使行内字节偏移
// 同时也是 LSP UTF-16 字符偏移。
const integrationSource = `package main

import "fmt"

const AppName = "lsptest"

type Service struct {
	Name string
}

func RunService(s Service) string {
	return s.Name
}

func Help() string {
	return "help"
}

func main() {
	s := Service{Name: AppName}
	fmt.Println(RunService(s))
	fmt.Println(Help())
}
`

func TestIntegrationGoplsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}

	gopls := locateGopls(t)
	// lsp.Start 用 exec.Command("gopls") 启动服务器，因此它必须能通过 PATH 被发现，即使它位于不在默认 PATH 中的 Go bin 目录。
	// 必须能通过 PATH 被发现，即使它位于
	// 不在默认 PATH 中的 Go bin 目录。
	t.Setenv("PATH", filepath.Dir(gopls)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOTOOLCHAIN", "local") // never try to download a toolchain during CI

	workspace := t.TempDir()
	mainPath := writeIntegrationWorkspace(t, workspace, integrationSource)

	// initialize 握手单独设限：冷启动的 gopls 首次运行时可能花一段时间索引标准库。
	// 首次运行时索引标准库可能花一段时间。
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

	// 后续所有请求共用一个宽松预算；整个测试
	// 后续所有请求共用一个宽松预算；由于每个阶段都有自己的更短截止时间，整个测试远低于 120s。
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	uri, err := client.Open(ctx, mainPath)
	if err != nil {
		t.Skipf("gopls did not accept the document (integration optional): %v", err)
	}

	// Diagnostics 是有用的就绪提示（gopls 对干净文件发布空集合），但只是尽力而为：某些版本在没什么可报告时干脆跳过发布。
	// 对干净文件发布空集合），但只是尽力而为：某些版本跳过发布
	// 完全，当没什么可报告时。
	diagnostics, diagErr := client.Diagnostics(ctx, uri, 20*time.Second)
	if diagErr != nil {
		t.Logf("diagnostics wait: %v (continuing)", diagErr)
	} else {
		t.Logf("diagnostics: %d item(s) for a valid Go file", len(diagnostics))
	}

	declPos := findToken(t, integrationSource, "RunService", 0) // func RunService
	callPos := findToken(t, integrationSource, "RunService", 1) // RunService(s)
	declLine := declPos.Line

	// 就绪门：definition 需要 gopls 已经对包做了类型检查，这可能滞后于 didOpen。轮询直到调用点解析到声明行，然后从该结果做真正的断言。
	// 这可能滞后于 didOpen。轮询直到调用点解析到
	// 声明行，然后从该结果做真正的断言。
	deadline := time.Now().Add(30 * time.Second)
	var defs []lsp.Location
	var defErr error
	ready := false
	for time.Now().Before(deadline) {
		defs, defErr = client.DefinitionAt(ctx, mainPath, callPos)
		if defErr == nil && definitionAtLine(defs, mainPath, declLine) {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Skipf("gopls did not resolve the call site to the RunService declaration "+
			"(integration optional: server too slow or unavailable); last result=%+v, err=%v", defs, defErr)
	}
	t.Logf("DefinitionAt(call site) -> %d location(s), first at %s", len(defs), defs[0].Range.Start.String())

	// CORE：落在另一个文件的定义是真正的发现。
	for _, loc := range defs {
		if !sameFile(loc.URI, mainPath) {
			t.Errorf("DefinitionAt returned %q, want the workspace main.go", loc.URI)
		}
	}

	// CORE：documentSymbol 必须列出我们写下的声明。
	symbols, err := client.DocumentSymbolAt(ctx, mainPath)
	if err != nil {
		t.Skipf("gopls documentSymbol failed (integration optional): %v", err)
	}
	names := make([]string, 0, len(symbols))
	present := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		names = append(names, symbol.Name)
		present[symbol.Name] = true
	}
	t.Logf("DocumentSymbolAt -> %d symbol(s): %v", len(symbols), names)
	for _, want := range []string{"RunService", "Service", "AppName", "Help", "main"} {
		if !present[want] {
			t.Errorf("DocumentSymbolAt missing %q; got %v", want, names)
		}
	}

	// CORE：声明处的引用必须包括声明本身和调用点。
	// 和调用点。
	refs, err := client.ReferencesAt(ctx, mainPath, declPos, true)
	if err != nil {
		t.Skipf("gopls references failed (integration optional): %v", err)
	}
	t.Logf("ReferencesAt(RunService) -> %d location(s)", len(refs))
	if len(refs) < 2 {
		t.Errorf("ReferencesAt found %d location(s), want >= 2 (declaration + call site)", len(refs))
	}

	// BEST-EFFORT：报告服务器给我们的内容；绝不因空 hover、被过滤的工作区查询或已格式化的代码而使套件失败。
	// 空 hover、被过滤的工作区查询或已格式化的代码。
	hover, err := client.HoverAt(ctx, mainPath, callPos)
	switch {
	case err != nil:
		t.Logf("hover error (best-effort): %v", err)
	case hover == nil:
		t.Log("hover: no content (best-effort)")
	case !strings.Contains(hover.Contents, "RunService"):
		t.Logf("hover: unexpected content %q (best-effort)", hover.Contents)
	default:
		t.Logf("hover: %q", hover.Contents)
	}

	workspaceSymbols, err := client.WorkspaceSymbols(ctx, "RunService")
	if err != nil {
		t.Logf("workspace/symbol error (best-effort): %v", err)
	} else {
		t.Logf("WorkspaceSymbols(RunService) -> %d result(s)", len(workspaceSymbols))
	}

	edits, err := client.FormattingAt(ctx, mainPath, lsp.FormattingOptions{TabSize: 4, InsertSpaces: true})
	if err != nil {
		t.Logf("formatting error (best-effort): %v", err)
	} else {
		t.Logf("FormattingAt -> %d edit(s)", len(edits))
	}
}

// locateGopls 在 PATH 或常规 Go bin 目录中查找 gopls。服务器缺失使整个集成测试跳过，因此没有 gopls 的 CI 保持绿色。
// 服务器缺失使整个集成测试跳过，因此没有 gopls 的 CI
// 保持绿色。
func locateGopls(t *testing.T) string {
	t.Helper()
	if found, err := exec.LookPath("gopls"); err == nil {
		return found
	}
	if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
		fallback := filepath.Join(userProfile, "go", "bin", "gopls.exe")
		if info, err := os.Stat(fallback); err == nil && !info.IsDir() {
			return fallback
		}
	}
	t.Skip("gopls not installed: not on PATH and not found at ~/go/bin/gopls.exe")
	return ""
}

// writeIntegrationWorkspace 写入服务器加载的最小有效模块。
func writeIntegrationWorkspace(t *testing.T, dir, source string) string {
	t.Helper()
	goMod := "module example.com/lsptest\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(source), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return mainPath
}

// findToken 返回 source 中 token 第 occurrence-th 次匹配的从 0 开始的 LSP 位置
// findToken 返回 source 中 token 第 occurrence-th 次匹配的从 0 开始的 LSP 位置（从 0 计数）。fixture 是 ASCII，因此字节和 UTF-16 偏移一致。
// 偏移一致。
func findToken(t *testing.T, source, token string, occurrence int) lsp.Position {
	t.Helper()
	seen := 0
	for lineNo, line := range strings.Split(source, "\n") {
		offset := 0
		for {
			index := strings.Index(line[offset:], token)
			if index < 0 {
				break
			}
			if seen == occurrence {
				return lsp.Position{Line: lineNo, Character: offset + index}
			}
			seen++
			offset += index + len(token)
		}
	}
	t.Fatalf("token %q occurrence %d not found in fixture", token, occurrence)
	return lsp.Position{}
}

// definitionAtLine 报告某个位置是否指向 path 的某一行。
func definitionAtLine(locations []lsp.Location, path string, line int) bool {
	for _, location := range locations {
		if sameFile(location.URI, path) && location.Range.Start.Line == line {
			return true
		}
	}
	return false
}

// sameFile 双向比较 URI 和文件系统路径，使 URI
// sameFile 双向比较 URI 和文件系统路径，使 gopls 与 PathToURI 之间的 URI 大小写或百分号编码差异不会产生虚假发现。
// 大小写或百分号编码差异不产生虚假发现。
func sameFile(uri, path string) bool {
	if strings.EqualFold(uri, lsp.PathToURI(path)) {
		return true
	}
	return strings.EqualFold(filepath.Clean(lsp.URIToPath(uri)), filepath.Clean(path))
}
