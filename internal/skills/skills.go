// Package skills 发现磁盘上的 SKILL.md 流程并按需加载。
//
// 技能是包含 `SKILL.md` 的目录（也接受裸 `<name>.md`）。
// 文件以 `---` frontmatter 块开头，至少携带 `name`
// 和 `description`；其余部分是流程本身。
//
// 系统提示中只放紧凑索引（每个技能一行）；正文通过 `use_skill` 工具获取。二十个安装技能则
// 系统提示中只放紧凑索引（每个技能一行）；正文通过 `use_skill` 工具获取。这样安装二十个技能只耗费二十行而非二十篇文档。
// 只耗费二十行而非二十篇文档。
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// DefaultProjectSubdir 是仓库存放自有技能的位置。
const DefaultProjectSubdir = ".argent/skills"

// DefaultHomeSubdir 是写入新技能时用户主目录下的目标位置。
// 它只供 author.go 使用；发现路径由 DefaultUserSubdirs 决定，二者刻意解耦。
const DefaultHomeSubdir = ".argentgo/skills"

// DefaultUserSubdirs 是发现技能时按顺序搜索的用户主目录子路径。
// 越靠前优先级越高；同名技能由先出现的目录胜出。
var DefaultUserSubdirs = []string{".argentgo/skills", ".agents/skills", ".claude/skills"}

// Load 在没有技能携带请求的名称时返回 ErrNotFound。
var ErrNotFound = errors.New("skill not found")

// Skill 是磁盘上的一个流程。
type Skill struct {
	// Name 是模型传给工具的内容。
	Name string
	// Description 是索引中显示的一行触发摘要。
	Description string
	// WhenToUse 是较长的"何时加载我…"提示，也显示在索引中。
	WhenToUse string
	// ArgumentHint 是来自 frontmatter `argument-hint` 字段的用量提示。
	// 它携带由 SkillArgumentHints 使用的可选枚举候选项。
	ArgumentHint string
	// Body 是不含 frontmatter 的流程。
	Body string
	// Path 是流程来源的 SKILL.md 文件。
	Path string
	// Tools 命名技能携带的脚本工具（frontmatter `tools`）。
	Tools []string
	// AllowedTools 是技能公布的工具白名单。
	AllowedTools []string
	// Warnings 报告在声明性内容中发现的不可见字符。
	// 它仅是元数据：解析过程永不因它改变，也永不阻塞。
	Warnings []string
	// Triggers 保存可选的自动激活规则。它仅是元数据：
	// Triggers 保存可选的自动激活规则。它仅是元数据：这里没有任何内容改变技能通过 use_skill 按名称加载的方式。
	Triggers Triggers
}

// Entry 是索引和 Match 使用的技能紧凑形式。
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	WhenToUse   string `json:"when_to_use,omitempty"`
	// Score 只由 Match 设置；从不被序列化。
	Score int `json:"-"`
}

// Entry 返回技能的紧凑索引形式。
func (s Skill) Entry() Entry {
	return Entry{Name: s.Name, Description: s.Description, WhenToUse: s.WhenToUse}
}

// Parse 读取一个技能文档。path 用于诊断并作为回退名称（目录名）。
// 并作为回退名称（`.../foo/SKILL.md` 的目录名）。
func Parse(text, path string) (Skill, error) {
	frontmatter, body, ok := SplitFrontmatter(text)
	if !ok {
		return Skill{}, fmt.Errorf("%s: skill file must start with a '---' frontmatter block", path)
	}
	fields := ParseFields(frontmatter)

	name := fields.Get("name")
	if name == "" {
		name = skillNameFromPath(path)
	}
	if name == "" {
		name = "unnamed"
	}
	return Skill{
		Name:         name,
		Description:  fields.Get("description"),
		WhenToUse:    fields.Get("when_to_use"),
		ArgumentHint: fields.Get("argument-hint"),
		Body:         strings.TrimSpace(body),
		Path:         path,
		Tools:        fields.List("tools"),
		AllowedTools: fields.List("allowed-tools"),
		Warnings:     security.ScanArtifact(text).Warnings(),
		Triggers: Triggers{
			Paths:    fields.List("paths"),
			Keywords: fields.List("keywords"),
			Models:   fields.List("models"),
		},
	}, nil
}

func skillNameFromPath(path string) string {
	base := filepath.Base(path)
	if strings.EqualFold(base, "SKILL.md") || strings.EqualFold(base, "SKILL.markdown") {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// DefaultRoots 返回一个会话要搜索的技能目录，按优先级：先是项目，然后是用户主目录。
// 优先级：先是项目，然后是用户主目录。
func DefaultRoots(cwd, home string) []string {
	var roots []string
	if cwd != "" {
		roots = append(roots, filepath.Join(cwd, filepath.FromSlash(DefaultProjectSubdir)))
	}
	if home != "" {
		for _, subdir := range DefaultUserSubdirs {
			roots = append(roots, filepath.Join(home, filepath.FromSlash(subdir)))
		}
	}
	return roots
}

// Registry 是一个会话可用的技能集合。
type Registry struct {
	skills []Skill
}

// New 返回空 registry。
func New() *Registry { return &Registry{} }

// Discover 加载 roots 下的所有技能。
//
// Roots 按顺序搜索，较后的 root 从不替换较早的，因此项目技能会遮蔽同名的用户技能。解析失败的目录
// 因此项目技能会遮蔽同名的用户技能。解析失败的目录
// 或文件被跳过：一个损坏的技能不得隐藏其他技能。
// rest.
func Discover(roots []string) *Registry {
	r := New()
	for _, root := range roots {
		for _, candidate := range skillFiles(root) {
			text, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			skill, err := Parse(string(text), candidate)
			if err != nil {
				continue
			}
			r.addIfAbsent(skill)
		}
	}
	sort.Slice(r.skills, func(i, j int) bool { return r.skills[i].Name < r.skills[j].Name })
	return r
}

func skillFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			file := filepath.Join(path, "SKILL.md")
			if info, err := os.Stat(file); err == nil && !info.IsDir() {
				files = append(files, file)
			}
			continue
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

// Add 注册一个技能，替换同名的已有技能。
func (r *Registry) Add(skill Skill) {
	for i := range r.skills {
		if r.skills[i].Name == skill.Name {
			r.skills[i] = skill
			return
		}
	}
	r.skills = append(r.skills, skill)
}

func (r *Registry) addIfAbsent(skill Skill) {
	for _, existing := range r.skills {
		if existing.Name == skill.Name {
			return
		}
	}
	r.skills = append(r.skills, skill)
}

// All 返回所有技能，按名称排序。
func (r *Registry) All() []Skill {
	out := make([]Skill, len(r.skills))
	copy(out, r.skills)
	return out
}

// Len 返回已安装技能的数量。
func (r *Registry) Len() int { return len(r.skills) }

// Get 按精确名称查找一个技能。
func (r *Registry) Get(name string) (Skill, bool) {
	name = strings.TrimSpace(name)
	for _, skill := range r.skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

// Index 返回系统提示的紧凑条目。
func (r *Registry) Index() []Entry {
	entries := make([]Entry, 0, len(r.skills))
	for _, skill := range r.skills {
		entries = append(entries, skill.Entry())
	}
	return entries
}

// skillIndexFieldRunes 限制索引/抬头中单个字段（名称、描述、触发提示、来源路径）
// 的 rune 上限。声明性元数据不可信，超长字段会在系统提示的单个技能行里撑爆预算，
// 因此按字素簇边界裁剪到固定长度。
const skillIndexFieldRunes = 200

// skillIndexTotalRunes 限制整段索引（含头部与换行）的 rune 上限，作为字段上限之外的
// 第二道防线。按整行追加，保证被保留的技能仍然是一行一条，且头部永远完整。
const skillIndexTotalRunes = 4096

// IndexText 把索引渲染为追加到系统提示的块。未安装技能时为空。
func (r *Registry) IndexText() string {
	if len(r.skills) == 0 {
		return ""
	}
	const header = "Skills: procedures you can load by name with the use_skill tool. " +
		"Load one when the task matches its description; do not load all of them.\n"
	lines := make([]string, 0, len(r.skills))
	for _, skill := range r.skills {
		// 声明性元数据不可信：可能来自块标量或带引号的 \n 转义，
		// 也可能携带 Unicode tag / bidi 不可见字符或超长内容。clipIndexField
		// 统一做脱敏、折叠与裁剪，使每个技能恰好占据一行索引且无法向系统提示注入条目。
		name := clipIndexField(skill.Name)
		desc := clipIndexField(skill.Description)
		when := clipIndexField(skill.WhenToUse)
		switch {
		case desc == "" && when == "":
			lines = append(lines, fmt.Sprintf("- %s\n", name))
		case when == "":
			lines = append(lines, fmt.Sprintf("- %s: %s\n", name, desc))
		case desc == "":
			lines = append(lines, fmt.Sprintf("- %s (when: %s)\n", name, when))
		default:
			lines = append(lines, fmt.Sprintf("- %s: %s (when: %s)\n", name, desc, when))
		}
	}
	var b strings.Builder
	b.WriteString(header)
	used := utf8.RuneCountInString(header)
	for _, line := range lines {
		n := utf8.RuneCountInString(line)
		// 整行追加：一旦超过总预算就停止，宁可少列几个技能，
		// 也绝不截断某个技能的行或丢弃头部。
		if used+n > skillIndexTotalRunes {
			break
		}
		b.WriteString(line)
		used += n
	}
	return b.String()
}

// oneLine 折叠内嵌空白，使单个技能在纯文本渲染中不会输出
// oneLine 折叠内嵌空白，使单个技能在纯文本渲染中不会输出超过一行。Parse 刻意保留块标量中的换行，因此折叠属于渲染边界的职责。
// 超过一行。Parse 刻意保留块标量中的换行，因此折叠属于渲染边界的职责。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipIndexField 对声明性元数据字段做统一的渲染边界处理：先脱敏不可见字符
// （Unicode tag / bidi 控制符），再折叠为单行，最后按 skillIndexFieldRunes 在
// 字素簇边界裁剪，绝不切坏 CJK、emoji 或组合符。索引与 use_skill 抬头共用本函数，
// 保证“索引里看到的名称”与“加载后抬头里的名称”逐字节一致。
func clipIndexField(s string) string {
	return textutil.ClipRunes(oneLine(security.RedactArtifactContent(s)), skillIndexFieldRunes)
}

// Load 返回完整技能，含正文。
func (r *Registry) Load(name string) (Skill, error) {
	if skill, ok := r.Get(name); ok {
		return skill, nil
	}
	wanted := strings.TrimSpace(name)
	if suggestions := r.Suggest(wanted); len(suggestions) > 0 {
		return Skill{}, fmt.Errorf("%w: %q (did you mean: %s)", ErrNotFound, wanted, strings.Join(suggestions, ", "))
	}
	return Skill{}, fmt.Errorf("%w: %q", ErrNotFound, wanted)
}

// Match 按与提示的关键词重叠度对技能排序。
//
// 分数统计同时出现在名称（权重 4）、when_to_use 提示（权重 3）或
// Match 按与提示的关键词重叠度对技能排序。分数统计同时出现在名称（权重 4）、when_to_use 提示（权重 3）或描述（权重 2）中的不同提示词。只返回正分的技能，最佳优先，然后按名称。
// 只返回正分的技能，最佳优先，然后按名称。
func (r *Registry) Match(prompt string) []Entry {
	words := keywords(prompt)
	if len(words) == 0 {
		return nil
	}
	var matched []Entry
	for _, skill := range r.skills {
		score := overlap(words, keywords(skill.Name))*4 +
			overlap(words, keywords(skill.WhenToUse))*3 +
			overlap(words, keywords(skill.Description))*2
		if score == 0 {
			continue
		}
		entry := skill.Entry()
		entry.Score = score
		matched = append(matched, entry)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Score != matched[j].Score {
			return matched[i].Score > matched[j].Score
		}
		return matched[i].Name < matched[j].Name
	})
	return matched
}

// Suggest 列出与 name 松散相关的技能名，用于拼写错误提示。
func (r *Registry) Suggest(name string) []string {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return nil
	}
	var names []string
	for _, skill := range r.skills {
		lower := strings.ToLower(skill.Name)
		haystack := lower + " " + strings.ToLower(skill.Description)
		if strings.Contains(haystack, needle) || strings.Contains(needle, lower) {
			names = append(names, skill.Name)
		}
	}
	sort.Strings(names)
	return names
}

// stopwords 防止填充词单独产生匹配。
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "do": true, "for": true, "from": true,
	"how": true, "i": true, "if": true, "in": true, "into": true, "is": true,
	"it": true, "me": true, "my": true, "of": true, "on": true, "or": true,
	"so": true, "that": true, "the": true, "then": true, "this": true, "to": true,
	"use": true, "when": true, "with": true, "you": true,
}

// keywords 把 s 转为小写并拆分为不同的词标记，去掉填充词和单字符。
// 去掉填充词和单字符。
func keywords(s string) map[string]bool {
	words := make(map[string]bool)
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		word := strings.ToLower(current.String())
		current.Reset()
		if len(word) < 2 || stopwords[word] {
			return
		}
		words[word] = true
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return words
}

func overlap(a, b map[string]bool) int {
	count := 0
	for word := range a {
		if b[word] {
			count++
		}
	}
	return count
}
