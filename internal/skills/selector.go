package skills

import (
	"sort"
	"strings"
	"unicode"
)

// 与 toolrank 对齐的 BM25 参数：k1 控制词频饱和，b 控制长度归一。
const (
	selectorK1 = 1.2
	selectorB  = 0.75
	// fieldWeights 是 [名称, 短描述, 正文] 的字段权重（学自 codex fielded_bm25）。
	selectorNameWeight  = 8
	selectorShortWeight = 4
	selectorBodyWeight  = 1
	// selectorTopK 限制 BM25 补充进 active 集的技能数，避免长提示把所有技能都激活。
	selectorTopK = 3
)

// FieldedDoc 是技能的可检索字段。Short 通常是 description + when_to_use。
type FieldedDoc struct {
	Name  string
	Short string
	Body  string
}

// Ranked 是一条排序结果，Index 对应传入 docs 的下标。
type Ranked struct {
	Index int
	Score float64
}

// RankSkills 按与 query 的相关性对 docs 排序，返回最多 k 条正分结果。
// 主排序是 fielded BM25；当 BM25 全部为零（例如极短 CJK 查询没命中切词）时，
// 退化为字符 n-gram 重叠度兜底。结果稳定：同分按输入顺序。
func RankSkills(query string, docs []FieldedDoc, k int) []Ranked {
	q := tokenizeSelector(query)
	if len(q) == 0 || len(docs) == 0 {
		return nil
	}
	if k <= 0 || k > len(docs) {
		k = len(docs)
	}

	fields := make([][3][]string, len(docs))
	union := make([]map[string]bool, len(docs))
	df := make(map[string]int)
	totalLen := 0
	for i, doc := range docs {
		fields[i] = [3][]string{
			tokenizeSelector(doc.Name),
			tokenizeSelector(doc.Short),
			tokenizeSelector(doc.Body),
		}
		seen := make(map[string]bool)
		docLen := 0
		for _, field := range fields[i] {
			for _, tok := range field {
				seen[tok] = true
				docLen++
			}
		}
		union[i] = seen
		totalLen += docLen
	}
	for _, tok := range q {
		for _, seen := range union {
			if seen[tok] {
				df[tok]++
			}
		}
	}
	avgdl := float64(totalLen) / float64(len(docs))
	if avgdl <= 0 {
		avgdl = 1
	}
	n := float64(len(docs))
	weights := [3]float64{selectorNameWeight, selectorShortWeight, selectorBodyWeight}

	out := make([]Ranked, 0, len(docs))
	for i := range fields {
		docLen := len(fields[i][0]) + len(fields[i][1]) + len(fields[i][2])
		score := 0.0
		for fi, field := range fields[i] {
			tf := make(map[string]int, len(field))
			for _, tok := range field {
				tf[tok]++
			}
			for _, tok := range q {
				f := tf[tok]
				if f == 0 {
					continue
				}
				idf := 1 + (n-float64(df[tok])+0.5)/(float64(df[tok])+0.5)
				denom := float64(f) + selectorK1*(1-selectorB+selectorB*float64(docLen)/avgdl)
				score += weights[fi] * idf * (float64(f) * (selectorK1 + 1)) / denom
			}
		}
		if score > 0 {
			out = append(out, Ranked{Index: i, Score: score})
		}
	}

	// 字符 n-gram 兜底：BM25 没命中时，用查询与技能文本的字符 bigram 交集打分。
	if len(out) == 0 {
		qGrams := charNgrams(query)
		if len(qGrams) > 0 {
			for i := range docs {
				text := docs[i].Name + " " + docs[i].Short + " " + docs[i].Body
				grams := charNgrams(text)
				hit := 0
				for gram := range qGrams {
					if grams[gram] {
						hit++
					}
				}
				if hit > 0 {
					out = append(out, Ranked{Index: i, Score: float64(hit)})
				}
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// tokenizeSelector 切词：ASCII 单词（含 camelCase 边界）与 CJK 单字 + bigram。
func tokenizeSelector(s string) []string {
	var out []string
	var ascii strings.Builder
	var cjk []rune
	flushASCII := func() {
		if ascii.Len() == 0 {
			return
		}
		out = append(out, strings.ToLower(ascii.String()))
		ascii.Reset()
	}
	flushCJK := func() {
		for _, r := range cjk {
			out = append(out, string(r))
		}
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i])+string(cjk[i+1]))
		}
		cjk = cjk[:0]
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			if len(cjk) > 0 {
				flushCJK()
			}
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if ascii.Len() > 0 {
				prev := rune(ascii.String()[ascii.Len()-1])
				if unicode.IsLower(prev) && unicode.IsUpper(r) ||
					unicode.IsUpper(prev) && unicode.IsUpper(r) && unicode.IsLower(next) {
					flushASCII()
				}
			}
			ascii.WriteRune(r)
		case r >= 0x2e80 && r <= 0x9fff:
			if ascii.Len() > 0 {
				flushASCII()
			}
			if len(cjk) >= 256 {
				flushCJK()
			}
			cjk = append(cjk, r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

// charNgrams 返回文本的 Unicode 字符 bigram 集合，供 CJK 兜底使用。
func charNgrams(s string) map[string]bool {
	out := make(map[string]bool)
	runes := []rune(strings.ToLower(s))
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i])+string(runes[i+1])] = true
	}
	return out
}

// ParseMentions 从提示文本中提取显式技能引用，保留出现顺序并去重。
// 支持两种写法：
//   - `$skill-name`（不误触发代码块、路径变量与命令位置参数）
//   - `[label](argent://skill/skill-name)`
func ParseMentions(text string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		visible := maskInlineCode(line)
		for _, name := range parseSkillLinks(visible) {
			add(name)
		}
		for _, name := range parseDollarMentions(visible) {
			add(name)
		}
	}
	return out
}

// maskInlineCode 用空格替换行内 `...` 片段，使其中的 $foo 与链接不被解析。
func maskInlineCode(line string) string {
	var b strings.Builder
	inCode := false
	for _, r := range line {
		if r == '`' {
			inCode = !inCode
			b.WriteByte(' ')
			continue
		}
		if inCode {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseSkillLinks 解析 [label](argent://skill/name)。
func parseSkillLinks(line string) []string {
	const marker = "](argent://skill/"
	var out []string
	for {
		idx := strings.Index(line, marker)
		if idx < 0 {
			break
		}
		rest := line[idx+len(marker):]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			break
		}
		name := strings.TrimSpace(rest[:end])
		if isSkillName(name) {
			out = append(out, name)
		}
		line = rest[end+1:]
	}
	return out
}

// parseDollarMentions 解析 $name。排除命令位置参数（$N/${N}/$@/$ARGUMENTS）
// 与路径变量（前导 / 或 \ 之后的 $VAR）。
func parseDollarMentions(line string) []string {
	var out []string
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '$' || i+1 >= len(runes) {
			continue
		}
		// 路径变量（/home/$USER）与普通文本中的 $（a$b、foo$bar）都不算提及。
		if i > 0 && (runes[i-1] == '/' || runes[i-1] == '\\' || isSkillNameRune(runes[i-1])) {
			continue
		}
		start := i + 1
		j := start
		if runes[j] == '{' {
			continue // ${N} 是命令位置参数
		}
		if runes[j] >= '0' && runes[j] <= '9' || runes[j] == '@' {
			continue
		}
		for j < len(runes) && isSkillNameRune(runes[j]) {
			j++
		}
		if j > start {
			name := string(runes[start:j])
			if !strings.EqualFold(name, "ARGUMENTS") && isSkillName(name) {
				out = append(out, name)
			}
			i = j - 1
		}
	}
	return out
}

func isSkillNameRune(r rune) bool {
	return r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isSkillName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !isSkillNameRune(r) {
			return false
		}
	}
	return true
}

// SetTriggerContextWithPrompt 在显式触发器（路径/关键词/模型）基础上，把提示中
// 的显式提及与 BM25 补充的技能并入 active 集：显式优先，BM25 只补 top-k。
// 它返回本轮 active 子集（registry 顺序），语义与 SetTriggerContext 一致。
func (r *Registry) SetTriggerContextWithPrompt(path, keywords, modelFamily, prompt string) []Skill {
	if r == nil {
		return nil
	}
	active := r.SetTriggerContext(path, keywords, modelFamily)
	names := make(map[string]bool, len(active))
	for _, skill := range active {
		names[skill.Name] = true
	}
	if len(r.skills) == 0 || strings.TrimSpace(prompt) == "" {
		r.active = names
		return activeFromNames(r, names)
	}

	docs := make([]FieldedDoc, 0, len(r.skills))
	index := make(map[string]int, len(r.skills))
	for i, skill := range r.skills {
		docs = append(docs, FieldedDoc{
			Name:  skill.Name,
			Short: skill.Description + " " + skill.WhenToUse,
			Body:  skill.Body,
		})
		index[skill.Name] = i
	}
	for _, name := range ParseMentions(prompt) {
		if i, ok := index[name]; ok {
			names[r.skills[i].Name] = true
		}
	}
	for _, ranked := range RankSkills(prompt, docs, selectorTopK) {
		if ranked.Index >= 0 && ranked.Index < len(r.skills) {
			names[r.skills[ranked.Index].Name] = true
		}
	}
	r.active = names
	return activeFromNames(r, names)
}

func activeFromNames(r *Registry, names map[string]bool) []Skill {
	out := make([]Skill, 0, len(names))
	for _, skill := range r.skills {
		if names[skill.Name] {
			out = append(out, skill)
		}
	}
	return out
}
