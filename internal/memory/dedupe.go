package memory

import "strings"

// 嵌套指令的「整段包含」去重。
//
// 嵌套仓库里父级 AGENTS.md 的规则常被子目录 AGENTS.md 原样抄一遍；
// 若两份都注入系统提示，同一内容会付两次 token 并稀释注意力。
// 本文件只比较「规范化后的段落序列」：段落是在空白行处切分、每行两端
// 去空白、段落内行序与文本必须逐字一致。不做模糊/近似匹配，也不改
// 任何文档的内容——只决定哪些文档不再重复注入。

// DedupeContainedDocs 丢弃那些正文段落序列被「更靠近 cwd 的文档」
// 作为连续子序列完整包含的文档。更靠近 cwd 由路径深度判定（pathDepth），
// 与输入顺序无关；同一目录下的兄弟文件（AGENTS.md 与 CLAUDE.md 并存）
// 深度相同，不会互相丢弃。
//
// 判定是保守的：空正文、被截断的正文（内容不完整，包含关系不可靠）都不
// 作为候选参与比较；只有两侧都未被截断时才可能丢弃。保留下来的文档保持
// 输入顺序与原始内容不变。nil/少于两份时原样返回。
func DedupeContainedDocs(docs []Doc) []Doc {
	if len(docs) < 2 {
		return docs
	}

	paras := make([][]string, len(docs))
	for i, doc := range docs {
		if doc.Truncated {
			continue
		}
		paras[i] = normalizeParagraphs(doc.Text)
	}

	drop := make([]bool, len(docs))
	for i := range docs {
		if drop[i] || len(paras[i]) == 0 {
			continue
		}
		for j := range docs {
			if i == j || drop[j] || len(paras[j]) == 0 {
				continue
			}
			// 只有当 j 比 i 更具体（路径更深）且其段落序列完整包含 i 时，
			// 才丢弃 i。更具体的那份是权威，留下它。
			if pathDepth(docs[j].Path) > pathDepth(docs[i].Path) &&
				containsParagraphs(paras[j], paras[i]) {
				drop[i] = true
				break
			}
		}
	}

	out := make([]Doc, 0, len(docs))
	for i, doc := range docs {
		if !drop[i] {
			out = append(out, doc)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeParagraphs 把正文转成可比较的段落序列：统一行尾、去掉每行两端
// 空白、在空白行处切段、丢弃空段。段落内部的行序与文本逐字保留。
func normalizeParagraphs(text string) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}
	var paras []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		paras = append(paras, strings.Join(cur, "\n"))
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return paras
}

// containsParagraphs 报告 sub 是否作为连续子序列出现在 super 中。
func containsParagraphs(super, sub []string) bool {
	if len(sub) == 0 || len(sub) > len(super) {
		return false
	}
	for start := 0; start+len(sub) <= len(super); start++ {
		match := true
		for k := range sub {
			if super[start+k] != sub[k] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
