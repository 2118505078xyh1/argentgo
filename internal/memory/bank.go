package memory

// Persistent fact store. Unlike the instruction files in memory.go (which are
// project-owned, immutable input to the system prompt), this bank is written by
// the agent itself and survives across sessions. It is a single JSONL log:
// one Memory per line, appended and synced per write, rewritten through a
// temporary file for cap enforcement and deletion.

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

	"github.com/2118505078xyh1/argentgo/internal/security"
)

const (
	// memoryFileName is the append-only log backing a Bank.
	memoryFileName = "memory.jsonl"
	// defaultCap bounds retained entries: 2000 * 4KB is roughly 8MB on disk.
	defaultCap = 2000
	// maxTextBytes bounds the stored text of one entry (and of source/tags).
	maxTextBytes = 4096
	// maxTagBytes bounds a single tag so a malicious model cannot pad the log.
	maxTagBytes = 64
	// defaultRecallLimit applies when a caller passes a non-positive limit.
	defaultRecallLimit = 10
	// tagMatchBonus outweighs a single token hit so explicit tags act as a
	// strong filter rather than a faint relevance hint.
	tagMatchBonus = 3
	// scannerBufferSize accommodates a 4KB text plus tags and metadata.
	scannerBufferSize = 1024 * 1024
	// maxReflectBytes caps the total size of an extractive reflection, header
	// included. 4KB keeps the synthesis well inside a model's context budget.
	maxReflectBytes = 4096
	// reflectPositionBonus is the small head start given to sentences that
	// appear early in recall order. It keeps relevance (token overlap) as the
	// primary ranking signal while preserving a readable, lead-first summary.
	reflectPositionBonus = 2
	// skillDirName is the managed subdirectory under the bank dir where
	// promoted lessons are written as SKILL.md-compatible markdown.
	skillDirName = "skills"
)

// ErrEmptyText is returned when Retain is asked to store nothing.
var ErrEmptyText = errors.New("memory: text must not be empty")

// Memory is one durable fact. TS is stored in UTC.
type Memory struct {
	ID     string    `json:"id"`
	Text   string    `json:"text"`
	Tags   []string  `json:"tags,omitempty"`
	TS     time.Time `json:"ts"`
	Source string    `json:"source,omitempty"`
	// Invalidated, when non-empty, holds the reason a fact was invalidated and
	// excludes it from Recall and Reflect. omitempty means normal records never
	// carry the field, so existing logs and callers are byte-for-byte unchanged
	// when invalidation is unused.
	Invalidated string `json:"invalidated,omitempty"`
}

// Bank is a concurrency-safe persistent fact store backed by one JSONL file.
// Construct it with NewBank; the zero value is not usable.
type Bank struct {
	dir  string
	path string
	mu   sync.Mutex
	cap  int
}

// NewBank returns a bank rooted at dir, creating the directory when possible.
// An empty dir falls back to a private directory under os.TempDir().
func NewBank(dir string) *Bank {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "argent-memory")
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Bank{dir: dir, path: filepath.Join(dir, memoryFileName), cap: defaultCap}
}

// SetDir repoints the bank at a new directory. It is intended for setup, before
// concurrent use; the file itself is created lazily on the next write.
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

// Dir reports the directory currently backing the bank.
func (b *Bank) Dir() string { return b.dir }

// Retain stores a fact and returns its generated id. The text, tags and source
// are redacted with security.Redact before anything reaches disk, so the memory
// file never contains credentials. Stores over the cap drop the oldest entries.
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

	if err := b.appendLocked(mem); err != nil {
		return "", err
	}
	loaded, err := b.loadLocked()
	if err != nil {
		return "", err
	}
	if b.cap > 0 && len(loaded) > b.cap {
		// Keep the tail: the log is in insertion order, so the last cap entries
		// are the newest ones regardless of clock resolution.
		if err := b.writeAllLocked(loaded[len(loaded)-b.cap:]); err != nil {
			return "", err
		}
	}
	return id, nil
}

// Recall loads the bank and returns up to limit memories ranked by token
// overlap with query plus tag matches, ties broken newest-first. A non-positive
// limit uses defaultRecallLimit. With an empty query and no tags every memory
// is eligible, so the result is simply the newest first. Malformed lines are
// skipped leniently.
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
		// Invalidated facts are tombstones: kept on disk for provenance but
		// never surfaced by search. Normal records never carry the field, so
		// this filter is a no-op for banks that never call Invalidate.
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

// Forget removes the memory with id. It is idempotent: an unknown id is a no-op.
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
	removed := false
	for _, mem := range all {
		if mem.ID == id {
			removed = true
			continue
		}
		kept = append(kept, mem)
	}
	if !removed {
		return nil
	}
	return b.writeAllLocked(kept)
}

// All returns every valid memory in insertion order, skipping malformed lines.
// Invalidated memories remain here (and on disk) as tombstones; only Recall and
// Reflect hide them.
func (b *Bank) All() []Memory {
	b.mu.Lock()
	defer b.mu.Unlock()
	all, _ := b.loadLocked()
	if all == nil {
		return []Memory{}
	}
	return all
}

// Reflect produces a deterministic, EXTRACTIVE synthesis over the memories most
// relevant to query. It performs NO model call and uses only the standard
// library. Unlike plain concatenation, it is a real extractive synthesizer:
//
//  1. Recall returns the top `limit` memories (relevance first, then recency).
//  2. Each memory is split into sentences at .!?。！？ boundaries (decimal dots
//     such as "3.14" are left alone, and CJK text is kept intact).
//  3. Every sentence is scored by case-insensitive token overlap with the query
//     plus a small bonus for appearing early in recall order.
//  4. Near-identical sentences are deduplicated, keeping the first (best) copy.
//  5. The best `limit` sentences are emitted, ranked by score then original
//     order, one per "- " line under a header, capped at maxReflectBytes. The
//     byte cap is applied with the grapheme-safe clipper so combining marks,
//     emoji and flags are never cut in half.
//
// Output is fully stable for a given bank and query; no randomness and no model
// call are involved. A bank with no eligible sentences returns a short notice.
func (b *Bank) Reflect(query string, limit int) string {
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	hits := b.Recall(query, nil, limit)
	if len(hits) == 0 {
		return "no memories to reflect on"
	}

	// Unique query tokens, lowercased; duplicates in the query must not inflate
	// a sentence's score.
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
			// Earlier sentences get a small, bounded head start.
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
		// Fold embedded newlines so one sentence stays one reflected line.
		text := strings.Join(strings.Fields(c.text), " ")
		room := maxReflectBytes - used - 3 // "- " prefix and trailing "\n"
		if room <= 0 {
			break
		}
		clipped := clipGraphemeBytes(text, room)
		// Defensive fallback: if no whole cluster fits, the rune-safe clipper
		// produces the same (empty or rune-aligned) result.
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

// dedupeKey normalises a sentence into a comparison key so near-identical texts
// (differing only in case, whitespace, CJK spacing or punctuation) collapse to a
// single reflected line. Each token is NUL-delimited so "ab c" and "a bc" do
// not collide.
func dedupeKey(text string) string { return strings.Join(reflectTokens(text), "\x00") }

// splitSentences splits text at .!?。！？ boundaries, keeping the terminating
// punctuation attached to its sentence and absorbing runs of it (e.g. "?!").
// A "." between two ASCII digits is treated as a decimal point, not a boundary.
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

// reflectTokens lowercases text and splits it into comparison tokens. Latin and
// other non-CJK letters/digits stay whole ("quick," -> "quick"), while Han,
// Hiragana, Katakana and Hangul are emitted one rune at a time so a CJK query
// can overlap a sentence without whitespace-delimited words. Punctuation and
// symbols are dropped.
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

// Learn retains a reusable lesson and, when promote is true, also writes a
// lightweight SKILL.md-compatible markdown file under <bank dir>/skills/<id>.md.
// It deliberately does NOT import internal/skills (layering): the file simply
// follows the frontmatter + body format the skills loader already understands.
// Skill writing is best-effort — a failure still leaves the lesson retained and
// returns the id with an empty path plus a wrapped error, so a lesson is never
// lost just because its optional skill file could not be written.
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

// writeSkill emits one SKILL.md-compatible document for a promoted lesson.
// 0600 is best-effort on Windows, where POSIX file modes are largely ignored;
// the explicit mode documents intent and is correct on Unix.
func (b *Bank) writeSkill(id, text string, tags []string) (string, error) {
	// Mirror the stored record: use the same redaction + cap Retain applied so a
	// credential can never reach the skill file through the raw argument.
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

// skillDescription builds a single-line frontmatter description from tags or,
// when none are present, a clipped first line of the lesson. Newlines are
// flattened so the value cannot break the YAML-ish frontmatter block.
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

// Invalidate marks the memory with id as invalidated, recording reason, and
// removes it from Recall and Reflect. The record stays on disk (and in All) as
// a tombstone so provenance is preserved. Implementation: set the omitempty
// `invalidated` field on the record via the same atomic rewrite used by Forget.
// It is idempotent: an unknown id is a no-op.
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

// appendLocked writes one record with a single write + Sync so a crash cannot
// leave a partial line in the log. The caller holds b.mu.
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

// loadLocked reads the whole log. Bad lines are skipped rather than failing the
// whole read, so one corrupt record cannot hide the rest. The caller holds b.mu.
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

// writeAllLocked replaces the log atomically from a temporary file in the same
// directory. Windows may refuse to rename over an existing file, so that case
// falls back to removing the target first. The caller holds b.mu.
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

// boundedText trims, redacts and caps a stored string. Redaction happens before
// truncation so a clipped credential can never leave a fragment on disk.
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

// cleanTags normalises, redacts and deduplicates tags.
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

// tokenize lowercases text into comparison tokens. Punctuation is removed so
// "quick," and "quick" match. CJK text has no whitespace-delimited words, so
// each CJK rune becomes its own token; otherwise a query such as "喜欢" could
// never overlap a Chinese sentence, and Reflect would have nothing to synthesise.
func tokenize(s string) []string { return reflectTokens(s) }

// newID combines a nanosecond timestamp with random bytes so ids stay unique
// even when many facts are retained in the same tick.
func newID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("memory: generate id: %w", err)
	}
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(raw[:])), nil
}
