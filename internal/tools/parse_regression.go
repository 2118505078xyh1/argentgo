package tools

import (
	"go/parser"
	"os"
	"strings"

	"example.com/argentgo/internal/astcache"
)

// parseRegressionEnv 是「写入前 Go 语法劣化检查」的独立门控变量。
const parseRegressionEnv = "ARGENT_PARSE_REGRESSION_GUARD"

func parseRegressionEnabled() bool {
	return strings.TrimSpace(os.Getenv(parseRegressionEnv)) == "1"
}

// goParseErrorCount 返回源码的语法错误数（解析到首个致命错误即止的保守计数）。
func goParseErrorCount(src []byte) int {
	// 内容寻址缓存：写入前的语法劣化检查常对同一文件反复解析。
	_, _, err := astcache.ParseDefault("", "go", src, parser.SkipObjectResolution)
	if err == nil {
		return 0
	}
	// parser 的错误是 scanner.ErrorList；其 Len 即错误条数。
	if list, ok := err.(interface{ Len() int }); ok {
		return list.Len()
	}
	return 1
}

// checkParseRegression 在 opt-in 开启且目标为 .go 文件时，比较写入前后的语法错误数：
// 原本能解析、改后不能解析则拒绝写入（模型可见错误），引导其修正语法。
// 原本就有错误的文件不做劣化判断（避免阻止模型修复其它错误）。
func checkParseRegression(path string, before, after []byte) error {
	if !parseRegressionEnabled() || !strings.HasSuffix(strings.ToLower(path), ".go") {
		return nil
	}
	base := goParseErrorCount(before)
	if base != 0 {
		return nil // 原文件已有语法问题，交给后续验证处理
	}
	if goParseErrorCount(after) > 0 {
		return &ParseRegressionError{Path: path}
	}
	return nil
}

// ParseRegressionError 表示本次编辑会把原本合法的 Go 文件改成语法错误。
type ParseRegressionError struct{ Path string }

func (e *ParseRegressionError) Error() string {
	return "edit rejected: the result is not valid Go syntax; fix the edit so the file still parses (path: " + e.Path + ")"
}
