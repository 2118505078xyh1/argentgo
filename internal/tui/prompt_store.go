package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// maxPromptRunes 是单条提示的字符上限。按 rune 计数而非字节，
	// 这样 CJK/emoji 不会因 UTF-8 多字节编码被误判为超长。
	maxPromptRunes = 4096

	// maxStoredEntries 是磁盘上的硬上限，防止历史无限增长。
	maxStoredEntries = 1000

	// frecencyHalfLife 是评分半衰期：24 小时前的计数折算为一半。
	frecencyHalfLife = 24 * time.Hour
)

// ErrInvalidPrompt 表示待追加的提示为空或超过单条长度上限。
var ErrInvalidPrompt = errors.New("tui: invalid prompt")

// promptEntry 是 JSONL 的一行。text 为提示正文，count 为累计次数，
// time 为最近一次的 UnixNano 时间戳（供 frecency 排序使用）。
type promptEntry struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
	Time  int64  `json:"time"`
}

// PromptStore 把提示历史持久化到单个 JSONL 文件。
// path 由调用方给定，本原语只在该路径及其所在目录的临时文件上读写，
// 绝不会根据提示内容派生路径，因此不存在路径逃逸的入口。
type PromptStore struct {
	path string
	mu   sync.Mutex
}

// 同一进程内可能有多个 PromptStore 指向同一路径。Windows 上替换重命名
// 会因目标被占用而失败，因此把发布步骤放在包级锁下串行化，
// 与 internal/tools 的 todoPublishMu 采用同一策略。
var promptStorePublishMu sync.Mutex

// NewPromptStore 返回一个绑定到 path 的持久化提示历史。
func NewPromptStore(path string) *PromptStore { return &PromptStore{path: path} }

// Append 把一条提示追加到历史末尾（写一行 JSONL）。
// 空提示或超过 maxPromptRunes 的提示会被拒绝且不触碰文件。
// 写入前容错读取已有内容并原子重写，因此历史里的坏行不会中断追加。
func (s *PromptStore) Append(text string) error {
	if !validPrompt(text) {
		return ErrInvalidPrompt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.readEntriesLocked()
	if err != nil {
		return err
	}
	entries = append(entries, promptEntry{Text: text, Count: 1, Time: time.Now().UnixNano()})
	if len(entries) > maxStoredEntries {
		entries = entries[len(entries)-maxStoredEntries:]
	}
	if err := s.writeEntriesLocked(entries); err != nil {
		return fmt.Errorf("tui: prompt append: %w", err)
	}
	return nil
}

// Load 容错解析文件：跳过空行、非法 JSON 以及空/超长提示，
// 并在发现坏行时用保留下来的有效行自愈重写（原子替换）。
func (s *PromptStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, bad, err := s.readEntriesLocked()
	if err != nil {
		return err
	}
	if !bad {
		return nil
	}
	if err := s.writeEntriesLocked(entries); err != nil {
		return fmt.Errorf("tui: prompt load heal: %w", err)
	}
	return nil
}

// Recent 返回去重后的最近 n 条提示，按时间从旧到新排列（最旧在前、
// 最新在后），便于直接灌入 Editor 的历史环。重复项只保留最近一次
// 出现的位置；空行与超长条目在读取阶段已被过滤。
func (s *PromptStore) Recent(n int) []string {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.readEntriesLocked()
	if err != nil {
		return nil
	}
	// 从新到旧收集，保证去重留下的是最近一次出现的位置。
	newestFirst := make([]string, 0, n)
	seen := make(map[string]bool, n)
	for i := len(entries) - 1; i >= 0 && len(newestFirst) < n; i-- {
		text := entries[i].Text
		if seen[text] {
			continue
		}
		seen[text] = true
		newestFirst = append(newestFirst, text)
	}
	// 翻转为从旧到新，与 Editor.Record 的追加顺序一致。
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	return newestFirst
}

// RecentByFrecency 返回按 frecency 从高到低排序的最近 n 条提示。
// 评分为各次记录的 count × 2^(-距今天数/半衰期) 之和；同分时更近的
// 条目排在前面，完全相同时按文本排序以保证结果确定。
func (s *PromptStore) RecentByFrecency(n int) []string {
	return s.recentByFrecencyAt(n, time.Now())
}

// recentByFrecencyAt 是 RecentByFrecency 的可注入时间版本，便于确定性测试。
func (s *PromptStore) recentByFrecencyAt(n int, now time.Time) []string {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.readEntriesLocked()
	if err != nil {
		return nil
	}
	type scored struct {
		text  string
		score float64
		last  int64
	}
	best := make(map[string]*scored, len(entries))
	for _, e := range entries {
		age := now.Sub(time.Unix(0, e.Time))
		if age < 0 {
			age = 0
		}
		decay := math.Pow(2, -float64(age)/float64(frecencyHalfLife))
		cur := best[e.Text]
		if cur == nil {
			cur = &scored{text: e.Text, last: e.Time}
			best[e.Text] = cur
		}
		cur.score += float64(e.Count) * decay
		if e.Time > cur.last {
			cur.last = e.Time
		}
	}
	ranked := make([]*scored, 0, len(best))
	for _, v := range best {
		ranked = append(ranked, v)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].last != ranked[j].last {
			return ranked[i].last > ranked[j].last
		}
		return ranked[i].text < ranked[j].text
	})
	if len(ranked) > n {
		ranked = ranked[:n]
	}
	out := make([]string, 0, len(ranked))
	for _, v := range ranked {
		out = append(out, v.text)
	}
	return out
}

// Cap 把历史封顶为最近 max 条（按 JSONL 行计）；max <= 0 表示清空。
// 未超过上限且没有坏行时不重写文件，避免无谓的磁盘写入。
func (s *PromptStore) Cap(max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, bad, err := s.readEntriesLocked()
	if err != nil {
		return
	}
	if max <= 0 {
		_ = s.writeEntriesLocked(nil)
		return
	}
	if len(entries) <= max && !bad {
		return
	}
	if len(entries) > max {
		entries = entries[len(entries)-max:]
	}
	_ = s.writeEntriesLocked(entries)
}

// validPrompt 判断提示是否可写入：先去空白，再按 rune 计数，
// 从而兼容 CJK/emoji 等多字节字符。
func validPrompt(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	return utf8.RuneCountInString(text) <= maxPromptRunes
}

// readEntriesLocked 容错读取所有有效行；坏行被跳过并通过 bad 标记，
// 供调用方决定是否自愈重写。调用方须持有 s.mu。
func (s *PromptStore) readEntriesLocked() (entries []promptEntry, bad bool, err error) {
	if s.path == "" {
		return nil, false, errors.New("tui: prompt store: empty path")
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("tui: prompt store read: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// 单行最坏情况：每个 rune 被 JSON 转义成代理对（约 12 字节），
	// 预留 1MB 远高于该上限，避免超长行触发 scanner 的 token 限制。
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e promptEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			bad = true
			continue
		}
		if !validPrompt(e.Text) {
			bad = true
			continue
		}
		if e.Count < 1 { // 兼容只写了 text 的历史行。
			e.Count = 1
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, bad, fmt.Errorf("tui: prompt store scan: %w", err)
	}
	return entries, bad, nil
}

// writeEntriesLocked 用同目录临时文件原子替换整个 JSONL 文件。
// 调用方持有 s.mu；发布步骤再由包级锁串行化以兼容 Windows。
func (s *PromptStore) writeEntriesLocked(entries []promptEntry) error {
	if s.path == "" {
		return errors.New("tui: prompt store: empty path")
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	// 提示可能包含敏感内容，沿用仅所有者可读写权限。
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	bw := bufio.NewWriter(tmp)
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := bw.Write(line); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	promptStorePublishMu.Lock()
	err = renamePromptTemp(name, s.path)
	promptStorePublishMu.Unlock()
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// renamePromptTemp 处理 Windows 上“目标被占用导致替换重命名失败”的情况：
// 仅在普通重命名失败且确认目标存在时才删除目标重试，且保留原始错误
// 以免把移除失败误报成成功。与 internal/tools 的 renameTodoTemp 一致。
func renamePromptTemp(tmp, path string) error {
	err := os.Rename(tmp, path)
	if err == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		return err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return err
	}
	if removeErr := os.Remove(path); removeErr != nil {
		return err
	}
	if retryErr := os.Rename(tmp, path); retryErr != nil {
		return retryErr
	}
	return nil
}
