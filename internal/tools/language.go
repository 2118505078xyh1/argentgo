package tools

import (
	"path/filepath"
	"strings"
)

// languageByExtension 是文件扩展名到语言标识的统一检测表。
// 所有键都是小写且不带前导点；detectLanguage 会先把扩展名规范化后再查表。
// 把映射集中在这里，可以让不同子系统对同一扩展名得到一致的语言标识。
var languageByExtension = map[string]string{
	// 系统与脚本语言
	"go":     "go",
	"py":     "python",
	"pyw":    "python",
	"pyi":    "python",
	"rb":     "ruby",
	"php":    "php",
	"pl":     "perl",
	"pm":     "perl",
	"lua":    "lua",
	"r":      "r",
	"scala":  "scala",
	"groovy": "groovy",
	"gradle": "groovy",
	"kt":     "kotlin",
	"kts":    "kotlin",
	"swift":  "swift",
	"dart":   "dart",
	"ex":     "elixir",
	"exs":    "elixir",
	"erl":    "erlang",
	"hrl":    "erlang",
	"hs":     "haskell",
	"clj":    "clojure",
	"cljs":   "clojure",
	"cljc":   "clojure",
	"edn":    "clojure",
	"sh":     "shell",
	"bash":   "shell",
	"zsh":    "shell",
	"ksh":    "shell",
	"fish":   "shell",
	"ps1":    "powershell",
	"psm1":   "powershell",
	"psd1":   "powershell",
	"bat":    "batch",
	"cmd":    "batch",
	"mk":     "makefile",

	// 编译型语言
	"rs":   "rust",
	"c":    "c",
	"h":    "c",
	"cpp":  "cpp",
	"cc":   "cpp",
	"cxx":  "cpp",
	"hpp":  "cpp",
	"hh":   "cpp",
	"hxx":  "cpp",
	"java": "java",
	"cs":   "csharp",
	"asm":  "assembly",
	"s":    "assembly",

	// 前端与标记语言
	"js":       "javascript",
	"jsx":      "javascript",
	"mjs":      "javascript",
	"cjs":      "javascript",
	"ts":       "typescript",
	"tsx":      "typescript",
	"mts":      "typescript",
	"cts":      "typescript",
	"html":     "html",
	"htm":      "html",
	"css":      "css",
	"scss":     "css",
	"sass":     "css",
	"less":     "css",
	"vue":      "vue",
	"svelte":   "svelte",
	"xml":      "xml",
	"md":       "markdown",
	"markdown": "markdown",
	"mdx":      "markdown",

	// 数据与配置格式
	"json":       "json",
	"jsonc":      "json",
	"json5":      "json",
	"yaml":       "yaml",
	"yml":        "yaml",
	"toml":       "toml",
	"ini":        "ini",
	"cfg":        "ini",
	"conf":       "ini",
	"sql":        "sql",
	"proto":      "protobuf",
	"tf":         "terraform",
	"tfvars":     "terraform",
	"graphql":    "graphql",
	"gql":        "graphql",
	"dockerfile": "dockerfile",
	"patch":      "diff",
	"diff":       "diff",

	// 其他常见文本格式
	"txt": "text",
	"log": "text",
}

// detectLanguage 按文件扩展名返回语言标识。
// 扩展名匹配大小写不敏感，对包含 CJK 字符的文件名安全；
// 路径没有扩展名，或者扩展名不在检测表中时返回 "unknown"。
func detectLanguage(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return "unknown"
	}
	// filepath.Ext 返回的扩展名总是以 "." 开头，去掉前导点并统一大小写。
	key := strings.ToLower(ext[1:])
	if lang, ok := languageByExtension[key]; ok {
		return lang
	}
	return "unknown"
}
