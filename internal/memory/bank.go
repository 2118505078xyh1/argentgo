package memory

// 持久化事实存储。与 memory.go 中的指令文件（它们是
// 项目拥有的、系统提示的不可变输入）不同，这个 bank 由
// agent 自己写入并跨会话保留。它是单个 JSONL 日志：
// 每行一条 Memory，每次写入都追加并同步，通过
// 临时文件重写以执行容量上限和删除操作。

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"example.com/argentgo/internal/security"
)

const (
	// memoryFileName 是支撑 Bank 的仅追加日志。
	memoryFileName = "memory.jsonl"
	// defaultCap 限制保留的条目数：2000 * 4KB 在磁盘上约 8MB。
	defaultCap = 2000
	// maxTextBytes 限制单条存储文本（以及 source/tags）的长度。
	maxTextBytes = 4096
	// maxTagBytes 限制单个标签，使恶意模型无法填充日志。
	maxTagBytes = 64
	// defaultRecallLimit 在调用方传入非正数的 limit 时生效。
	defaultRecallLimit = 10
	// tagMatchBonus 超过单个标记命中，使显式标签起到
	// 强过滤作用，而不是微弱的相关性提示。
	tagMatchBonus = 3
	// scannerBufferSize 容纳 4KB 文本加标签和元数据。
	scannerBufferSize = 1024 * 1024
	// maxReflectBytes 限制抽取式反射的总大小（含头部）。
	// 4KB 使合成结果远在模型的上下文预算之内。
	maxReflectBytes = 4096
	// reflectPositionBonus 是给在召回顺序中较早出现的句子的
	// 小幅领先。它让相关性（标记重叠）保持
	// 主要排序信号，同时保留可读的、重点在前的摘要。
	reflectPositionBonus = 2
	// skillDirName 是 bank 目录下受管理的子目录，
	// 被提升的经验教训以 SKILL.md 兼容的 markdown 写入其中。
	skillDirName = "skills"
)

// 当 Retain 被要求不存储任何内容时返回 ErrEmptyText。
var ErrEmptyText = errors.New("memory: text must not be empty")

// Memory 是一条持久化事实。TS 以 UTC 存储。
type Memory struct {
	ID     string    `json:"id"`
	Text   string    `json:"text"`
	Tags   []string  `json:"tags,omitempty"`
	TS     time.Time `json:"ts"`
	Source string    `json:"source,omitempty"`
	// Invalidated 非空时保存事实被作废的原因，
	// 并将其排除在 Recall 和 Reflect 之外。omitempty 表示正常记录
	// 永不携带该字段，因此在未使用作废功能时，现有日志和调用方
	// 保持逐字节不变。
	Invalidated string `json:"invalidated,omitempty"`
	// SpillURI 非空时表示 Text 只是预览，全文存放于该 URI 指向的
	// 落盘文件，可用 Recover 取回。omitempty 保证未启用落盘时记录
	// 保持逐字节不变。
	SpillURI string `json:"spill_uri,omitempty"`
	// SpillBytes 是落盘全文的字节数，0 表示未落盘。
	SpillBytes int `json:"spill_bytes,omitempty"`
}

// Bank 是由单个 JSONL 文件支撑的并发安全持久化事实存储。
// 用 NewBank 构造；零值不可用。
type Bank struct {
	dir   string
	path  string
	mu    sync.Mutex
	cap   int
	spill *SpillOptions // nil 表示关闭大条目落盘（默认）
}

// NewBank 返回一个以 dir 为根的 bank，尽可能创建该目录。
// 空 dir 会回退到 os.TempDir() 下的私有目录。
func NewBank(dir string) *Bank {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "argent-memory")
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Bank{dir: dir, path: filepath.Join(dir, memoryFileName), cap: defaultCap}
}

// SetDir 将 bank 指向新目录。它用于初始化，即在
// 并发使用之前；文件本身在下次写入时懒创建。
func (b *Bank) SetDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dir = dir
	b.path = filepath.Join(dir, memoryFileName)
	_ = os.MkdirAll(dir, 0o755)
}

// Dir 报告当前支撑 bank 的目录。
func (b *Bank) Dir() string { return b.dir }

// Retain 存储一条事实并返回其生成的 id。文本、标签和来源
// 在写入磁盘前都用 security.Redact 脱敏，因此 memory
// 文件永不包含凭据。超出上限的存储会丢弃最旧的条目。
func (b *Bank) Retain(text string, tags []string, source string) (string, error) {
	body := boundedText(text)
	if body == "" {
		return "", ErrEmptyText
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	mem := Memory{
		ID:     id,
		Text:   body,
		Tags:   cleanTags(tags),
		TS:     time.Now().UTC(),
		Source: boundedText(source),
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// 大条目落盘是可选功能。默认关闭时 maybeSpill 是空操作，
	// mem 与日志字节和本功能加入之前完全一致。
	b.maybeSpill(&mem, text)

	if err := b.appendLocked(mem); err != nil {
		return "", err
	}
	loaded, err := b.loadLocked()
	if err != nil {
		return "", err
	}
	if b.cap > 0 && len(loaded) > b.cap {
		// 保留尾部：日志按插入顺序排列，因此最后 cap 条
		// 无论时钟分辨率如何都是最新的。
		evicted := loaded[:len(loaded)-b.cap]
		if err := b.writeAllLocked(loaded[len(loaded)-b.cap:]); err != nil {
			return "", err
		}
		// 被淘汰记录若曾落盘，其全文文件一并清理（尽力而为）。
		b.removeSpillFiles(evicted)
	}
	return id, nil
}

// Recall 加载 bank，并返回最多 limit 条按与 query 的标记
// 重叠度加标签匹配排序的 memory，平局时新的优先。非正数
// limit 使用 defaultRecallLimit。空 query 且无标签时每条 memory
// 都符合条件，因此结果就是简单地按最新优先排列。格式错误的行会被
// 宽松跳过。
func (b *Bank) Recall(query string, tags []string, limit int) []Memory {
	b.mu.Lock()
	all, err := b.loadLocked()
	b.mu.Unlock()
	if len(all) == 0 {
		return nil
	}
	_ = err // partial results are still useful; a truncated tail is ignored

	if limit <= 0 {
		limit = defaultRecallLimit
	}
	queryTokens := tokenize(query)
	wantTags := cleanTags(tags)
	filtered := len(queryTokens) > 0 || len(wantTags) > 0

	type hit struct {
		mem   Memory
		score int
		idx   int // file order, used as a stable newest-first tie breaker
	}
	results := make([]hit, 0, len(all))
	for i, mem := range all {
		// 已作废的事实是墓碑：为溯源而保留在磁盘上，但
		// 从不出现在搜索结果中。正常记录永不携带该字段，因此
		// 对从不调用 Invalidate 的 bank 来说，这个过滤是空操作。
		if mem.Invalidated != "" {
			continue
		}
		score := 0
		if len(queryTokens) > 0 {
			textTokens := tokenize(mem.Text)
			for _, q := range queryTokens {
				for _, tok := range textTokens {
					if q == tok {
						score++
						break
					}
				}
			}
		}
		if len(wantTags) > 0 {
			for _, want := range wantTags {
				for _, tag := range mem.Tags {
					if strings.EqualFold(want, tag) {
						score += tagMatchBonus
						break
					}
				}
			}
		}
		if filtered && score == 0 {
			continue
		}
		results = append(results, hit{mem: mem, score: score, idx: i})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		if results[i].mem.TS.Equal(results[j].mem.TS) {
			return results[i].idx > results[j].idx
		}
		return results[i].mem.TS.After(results[j].mem.TS)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	out := make([]Memory, len(results))
	for i, r := range results {
		out[i] = r.mem
	}
	return out
}

// Forget 删除 id 对应的 memory。它是幂等的：未知 id 是空操作。
func (b *Bank) Forget(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("memory: id must not be empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	all, err := b.loadLocked()
	if err != nil {
		return err
	}
	kept := all[:0]
	var removedMems []Memory
	removed := false
	for _, mem := range all {
		if mem.ID == id {
			removed = true
			removedMems = append(removedMems, mem)
			continue
		}
		kept = append(kept, mem)
	}
	if !removed {
		return nil
	}
	if err := b.writeAllLocked(kept); err != nil {
		return err
	}
	// 删除记录后同步清理其落盘全文（尽力而为）。
	b.removeSpillFiles(removedMems)
	return nil
}

// All 按插入顺序返回每条有效 memory，跳过格式错误的行。
// 已作废的 memory 在这里（以及磁盘上）作为墓碑保留；只有 Recall 和
// Reflect 隐藏它们。
func (b *Bank) All() []Memory {
	b.mu.Lock()
	defer b.mu.Unlock()
	all, _ := b.loadLocked()
	if all == nil {
		return []Memory{}
	}
	return all
}

// Reflect 对与 query 最相关的 memory 产生确定性的、抽取式
// 合成。它不调用任何模型，只使用标准
// 库。与普通拼接不同，它是真正的抽取式合成器：
//
//  1. Recall 返回前 `limit` 条 memory（先按相关性，再按新近度）。
//  2. 每条 memory 在 .!?。！？ 边界处拆分为句子（小数点
//     如 "3.14" 保持不变，CJK 文本保持完整）。
//  3. 每个句子按与 query 的不区分大小写的标记重叠度打分，
//     并对在召回顺序中较早出现给予小幅加分。
//  4. 近乎相同的句子去重，保留第一个（最佳的）副本。
//  5. 输出最佳的 `limit` 个句子，先按分数再按原始
//     顺序排序，在标题下每行一个 "- "，上限为 maxReflectBytes。字
//     节上限通过字形安全的裁剪器施加，因此组合标记、
//     emoji 和旗帜从不被切掉一半。
//
// 对于给定的 bank 和 query，输出完全稳定；不涉及随机性或模型
// 调用。没有符合条件句子的 bank 返回一条简短通知。
func (b *Bank) Reflect(query string, limit int) string {
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	hits := b.Recall(query, nil, limit)
	if len(hits) == 0 {
		return "no memories to reflect on"
	}

	// 唯一的 query 标记，已转小写；query 中的重复项不得抬高
	// 句子的分数。
	queryTokens := reflectTokens(query)
	querySet := make(map[string]bool, len(queryTokens))
	uniqueQuery := queryTokens[:0]
	for _, tok := range queryTokens {
		if !querySet[tok] {
			querySet[tok] = true
			uniqueQuery = append(uniqueQuery, tok)
		}
	}
	queryTokens = uniqueQuery

	type candidate struct {
		text  string
		score int
		order int // collection order; stable tie breaker
	}
	candidates := make([]candidate, 0, len(hits))
	seen := make(map[string]bool)
	ordinal := 0
	for _, mem := range hits {
		for _, sentence := range splitSentences(mem.Text) {
			pos := ordinal
			ordinal++
			key := dedupeKey(sentence)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true

			score := 0
			sentenceTokens := reflectTokens(sentence)
			sentenceSet := make(map[string]bool, len(sentenceTokens))
			for _, tok := range sentenceTokens {
				sentenceSet[tok] = true
			}
			for _, q := range queryTokens {
				if sentenceSet[q] {
					score++
				}
			}
			// 较早的句子获得小幅、有界的领先。
			if bonus := reflectPositionBonus - pos; bonus > 0 {
				score += bonus
			}
			candidates = append(candidates, candidate{
				text:  sentence,
				score: score,
				order: len(candidates),
			})
		}
	}
	if len(candidates) == 0 {
		return "no memories to reflect on"
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].order < candidates[j].order
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	header := "Reflection (extractive synthesis):\n"
	var sb strings.Builder
	sb.WriteString(header)
	used := len(header)
	for _, c := range candidates {
		// 折叠内嵌换行，使一个句子保持为一行反射结果。
		text := strings.Join(strings.Fields(c.text), " ")
		room := maxReflectBytes - used - 3 // "- " prefix and trailing "\n"
		if room <= 0 {
			break
		}
		clipped := clipGraphemeBytes(text, room)
		// 防御性回退：若没有完整的字簇放得下，按 rune 安全的裁剪器
		// 会产生相同（空或 rune 对齐）的结果。
		if clipped == "" && text != "" {
			clipped = clipRunes(text, room)
		}
		clipped = strings.TrimSpace(clipped)
		if clipped == "" {
			continue
		}
		line := "- " + clipped + "\n"
		sb.WriteString(line)
		used += len(line)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// dedupeKey 将句子规范化为比较键，使近乎相同的文本
// （仅在大小写、空白、CJK 间距或标点上不同）折叠为
// 一行反射结果。每个标记以 NUL 分隔，因此 "ab c" 和 "a bc" 不
// 会冲突。
func dedupeKey(text string) string { return strings.Join(reflectTokens(text), "\x00") }

// splitSentences 在 .!?。！？ 边界处拆分文本，将结尾
// 标点附加到其句子上，并吸收连续的标点（例如 "?!"）。
// 两个 ASCII 数字之间的 "." 视为小数点，而非边界。
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if isSentenceEnd(r) && !isDecimalDot(text, i) {
			end := i + size
			for end < len(text) {
				next, nsz := utf8.DecodeRuneInString(text[end:])
				if !isSentenceEnd(next) {
					break
				}
				end += nsz
			}
			if seg := strings.TrimSpace(text[start:end]); seg != "" {
				out = append(out, seg)
			}
			start = end
			i = end
			continue
		}
		i += size
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	}
	return false
}

func isDecimalDot(text string, i int) bool {
	return i > 0 && i+1 < len(text) &&
		text[i-1] >= '0' && text[i-1] <= '9' &&
		text[i+1] >= '0' && text[i+1] <= '9'
}

// reflectTokens 将文本转为小写并拆分为比较标记。拉丁字母及
// 其他非 CJK 字母/数字保持完整（"quick," -> "quick"），而汉字、
// 平假名、片假名和韩文逐 rune 输出，这样 CJK query
// 无需空白分隔的词也能与句子重叠。标点和
// 符号被丢弃。
func reflectTokens(text string) []string {
	text = strings.ToLower(text)
	tokens := make([]string, 0, len(text)/2)
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for _, r := range text {
		if isCJK(r) {
			flush()
			tokens = append(tokens, string(r))
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			word.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// Learn 保留一条可复用的经验教训，并在 promote 为 true 时，另外在
// <bank dir>/skills/<id>.md 下写入一个轻量的 SKILL.md 兼容 markdown 文件。
// 它刻意不导入 internal/skills（分层考虑）：该文件只是
// 遵循 skills 加载器已能理解的 frontmatter + 正文格式。
// 技能写入是尽力而为的 —— 失败仍会保留经验教训，
// 并返回 id 加空路径以及包装后的错误，因此经验不会仅仅因为
// 其可选技能文件无法写入而丢失。
func (b *Bank) Learn(text string, tags []string, promote bool) (string, string, error) {
	id, err := b.Retain(text, tags, "learn")
	if err != nil {
		return "", "", err
	}
	if !promote {
		return id, "", nil
	}
	path, err := b.writeSkill(id, text, tags)
	if err != nil {
		return id, "", fmt.Errorf("memory: learn: retained %s but skill write failed: %w", id, err)
	}
	return id, path, nil
}

// writeSkill 为被提升的经验教训输出一个 SKILL.md 兼容文档。
// 0600 在 Windows 上是尽力而为的，POSIX 文件模式基本被忽略；
// 显式模式记录了意图，且在 Unix 上是正确的。
func (b *Bank) writeSkill(id, text string, tags []string) (string, error) {
	// 镜像存储的记录：使用 Retain 所施加的相同脱敏 + 上限，使
	// 凭据绝不可能通过原始参数进入技能文件。
	body := boundedText(text)
	dir := filepath.Join(b.dir, skillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("memory: skill mkdir: %w", err)
	}
	path := filepath.Join(dir, id+".md")
	content := "---\n" +
		"name: " + id + "\n" +
		"description: " + skillDescription(body, tags) + "\n" +
		"---\n\n" +
		body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("memory: skill write: %w", err)
	}
	return path, nil
}

// skillDescription 根据标签构建单行 frontmatter 描述，若
// 没有标签则取经验教训裁剪后的第一行。换行会被
// 展平，使该值不会破坏类 YAML 的 frontmatter 块。
func skillDescription(body string, tags []string) string {
	var desc string
	if clean := cleanTags(tags); len(clean) > 0 {
		desc = strings.Join(clean, ", ")
	} else {
		desc = strings.TrimSpace(strings.SplitN(body, "\n", 2)[0])
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if desc == "" {
		desc = "reusable lesson"
	}
	if len(desc) > maxTagBytes*4 {
		desc = strings.TrimSpace(clipRunes(desc, maxTagBytes*4))
	}
	return desc
}

// Invalidated 将 id 对应的 memory 标记为已作废，记录原因，并
// 将其从 Recall 和 Reflect 中移除。记录作为墓碑留在磁盘上（以及 All 中），
// 以保留溯源。实现方式：通过 Forget 使用的相同原子重写，
// 设置记录上的 omitempty `invalidated` 字段。
// 它是幂等的：未知 id 是空操作。
func (b *Bank) Invalidate(id string, reason string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("memory: id must not be empty")
	}
	reason = boundedText(reason)
	if reason == "" {
		reason = "invalidated"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	all, err := b.loadLocked()
	if err != nil {
		return err
	}
	changed := false
	for i := range all {
		if all[i].ID == id {
			all[i].Invalidated = reason
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return b.writeAllLocked(all)
}

// appendLocked 通过单次写入 + Sync 写入一条记录，使崩溃
// 不会在日志中留下半行。调用方持有 b.mu。
func (b *Bank) appendLocked(mem Memory) error {
	line, err := json.Marshal(mem)
	if err != nil {
		return fmt.Errorf("memory: encode: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("memory: append: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("memory: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("memory: close: %w", err)
	}
	return nil
}

// loadLocked 读取整个日志。坏行会被跳过而非导致
// 整个读取失败，因此一条损坏记录不会隐藏其余内容。调用方持有 b.mu。
func (b *Bank) loadLocked() ([]Memory, error) {
	f, err := os.Open(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: open: %w", err)
	}
	defer f.Close()

	var out []Memory
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), scannerBufferSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var mem Memory
		if err := json.Unmarshal([]byte(line), &mem); err != nil {
			continue
		}
		if mem.ID == "" || mem.Text == "" {
			continue
		}
		out = append(out, mem)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("memory: scan: %w", err)
	}
	return out, nil
}

// writeAllLocked 用同目录下的临时文件原子替换日志。
// Windows 可能拒绝覆盖重命名现有文件，因此这种情况
// 会回退为先删除目标。调用方持有 b.mu。
func (b *Bank) writeAllLocked(mems []Memory) error {
	tmp, err := os.CreateTemp(b.dir, "memory-*.jsonl.tmp")
	if err != nil {
		return fmt.Errorf("memory: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	bw := bufio.NewWriter(tmp)
	for _, mem := range mems {
		line, err := json.Marshal(mem)
		if err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: encode: %w", err)
		}
		if _, err := bw.Write(line); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: write: %w", err)
		}
		if err := bw.WriteByte('\n'); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: write: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: flush: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: close: %w", err)
	}

	if err := os.Rename(tmpName, b.path); err != nil {
		if rmErr := os.Remove(b.path); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("memory: replace: %w", err)
		}
		if err2 := os.Rename(tmpName, b.path); err2 != nil {
			return fmt.Errorf("memory: replace: %w", err2)
		}
	}
	return nil
}

// boundedText 对存储字符串进行裁剪、脱敏和限幅。脱敏在
// 截断之前进行，因此被裁剪的凭据绝不会在磁盘上留下碎片。
func boundedText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = security.Redact(s)
	if len(s) > maxTextBytes {
		s = clipRunes(s, maxTextBytes)
	}
	return strings.TrimSpace(s)
}

// cleanTags 规范化、脱敏并去重标签。
func cleanTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = boundedText(tag)
		if tag == "" {
			continue
		}
		if len(tag) > maxTagBytes {
			tag = clipRunes(tag, maxTagBytes)
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tokenize 将文本转为小写并拆分为比较标记。标点被移除，因此
// "quick," 和 "quick" 匹配。CJK 文本没有空白分隔的词，所以
// each CJK rune becomes its own token; otherwise a query such as "喜欢" could
// 无法与中文句子重叠，Reflect 也就无可合成的内容。
func tokenize(s string) []string { return reflectTokens(s) }

// newID 将纳秒时间戳与随机字节结合，使 id 保持唯一，
// 即使同一时刻保留了大量事实。
func newID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("memory: generate id: %w", err)
	}
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(raw[:])), nil
}
