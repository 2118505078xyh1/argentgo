package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

// call_index_test.go 把门禁的"生产调用方"查询从"每个新原语扫一遍全仓"改为
// "全仓只解析一次、建索引后 O(1) 查询"。原实现对每个新增导出函数都重新
// collectGoFiles + ReadFile + ParseFile + ast.Inspect，符号数与仓库文件数相乘，
// 是门禁耗时的主要来源。本文件的索引保持原有判定语义不变：
//   - 只看非测试 .go 文件（构建标签文件仍参与，多认调用方=少误报）；
//   - 直接调用、选择器调用、泛型调用都按目标名字计数；
//   - 字符串字面量中出现同名按 RPC/反射注册处理；
//   - 声明自身区间内的调用视为递归，不算外部调用方。

// callSite 是一次调用的位置，用于排除声明自身的递归调用。
type callSite struct {
	rel  string
	line int
}

// callIndex 是全仓生产代码的调用名索引。
type callIndex struct {
	calls   map[string][]callSite // 调用名 -> 所有出现位置
	strings map[string]bool       // 字符串字面量值
}

// buildCallIndex 一次遍历 root 下全部非测试 Go 文件建立调用索引。
// 解析失败或读取失败的文件被跳过，与旧实现"跳过坏文件"的保守策略一致。
func buildCallIndex(root string) (*callIndex, error) {
	files, err := collectGoFiles(root)
	if err != nil {
		return nil, err
	}
	idx := &callIndex{
		calls:   make(map[string][]callSite),
		strings: make(map[string]bool),
	}
	for _, abs := range files {
		rel := relPath(root, abs)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if name := callExprName(x.Fun); name != "" {
					idx.calls[name] = append(idx.calls[name], callSite{
						rel:  rel,
						line: fset.Position(x.Pos()).Line,
					})
				}
			case *ast.BasicLit:
				if value, ok := stringLitValue(x); ok {
					idx.strings[value] = true
				}
			}
			return true
		})
	}
	return idx, nil
}

// hasCaller 报告 sym 是否有生产调用方。语义与旧的 hasProductionCaller 完全一致。
func (idx *callIndex) hasCaller(sym primitiveSymbol) bool {
	if idx.strings[sym.Name] {
		return true
	}
	for _, site := range idx.calls[sym.Name] {
		if site.rel == sym.RelPath && site.line >= sym.StartLine && site.line <= sym.EndLine {
			continue // 声明自身的递归调用不算外部调用方
		}
		return true
	}
	return false
}
