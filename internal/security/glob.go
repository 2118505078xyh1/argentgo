package security

import "strings"

// GlobMatch 用路径模式匹配一个路径。
//
// `*` 在单个段内匹配，`**` 跨段匹配，`?` 匹配一个字符。不含 `/` 的模式
// （或不从根开始的模式）会与路径的每个尾部匹配，因此 `*.env` 匹配
// `project/src/.env`，`src/**` 匹配 `src/a/b.rs`。不含任何 glob 元字符的
// 模式被视为前缀：`~/.ssh` 拒绝 `~/.ssh/id_rsa`。
func GlobMatch(pattern, path string) bool {
	pat := normalize(expandHome(pattern))
	text := normalize(path)
	if pat == "" {
		return false
	}
	// 以 `/` 结尾的模式表示“该目录及其下的全部内容”。若不加处理，字面量模式会在
	// 比较时再拼出一个 `/`（"src/" + "/" = "src//"），导致 "src/file" 永远匹配不上；
	// 通配符模式则因为末尾多出一个空段而漏掉目录内的文件。统一补成 `/**`，让两类
	// 模式都能匹配目录本身以及它下面的任意深度路径。
	if strings.HasSuffix(pat, "/") {
		pat = strings.TrimRight(pat, "/") + "/**"
	}
	if !strings.ContainsAny(pat, "*?[") {
		// 前缀语义：在完整路径以及每个尾部上都进行检查。
		for _, tail := range tails(text) {
			if tail == pat || strings.HasPrefix(tail, pat+"/") {
				return true
			}
		}
		return false
	}
	for _, tail := range tails(text) {
		if matchSegments(strings.Split(pat, "/"), strings.Split(tail, "/")) {
			return true
		}
	}
	return false
}

// normalize 把路径转为小写，并把反斜杠转换为正斜杠。
// 前导斜杠会被去掉，使绝对路径匹配和尾部匹配对齐。
func normalize(path string) string {
	text := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	for strings.HasPrefix(text, "/") {
		text = text[1:]
	}
	return text
}

// tails 列出路径本身以及它在段边界处的每个后缀。
func tails(path string) []string {
	segments := strings.Split(path, "/")
	out := make([]string, 0, len(segments))
	for start := range segments {
		out = append(out, strings.Join(segments[start:], "/"))
	}
	return out
}

func matchSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(path); skip++ {
			if matchSegments(pattern[1:], path[skip:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if !matchChars([]rune(pattern[0]), []rune(path[0])) {
		return false
	}
	return matchSegments(pattern[1:], path[1:])
}

func matchChars(pattern, text []rune) bool {
	if len(pattern) == 0 {
		return len(text) == 0
	}
	switch pattern[0] {
	case '*':
		for skip := 0; skip <= len(text); skip++ {
			if matchChars(pattern[1:], text[skip:]) {
				return true
			}
		}
		return false
	case '?':
		return len(text) > 0 && matchChars(pattern[1:], text[1:])
	default:
		return len(text) > 0 && text[0] == pattern[0] && matchChars(pattern[1:], text[1:])
	}
}
