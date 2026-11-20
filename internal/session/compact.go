package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ArchiveRange is a half-open byte range [Start, End) locating one message in
// a compaction archive. A reader can seek to Start and read End-Start bytes to
// recover that message verbatim, without loading the whole archive. ID/Role are
// copied from the original entry so a range can be matched to a summary bullet
// without decoding the payload.
type ArchiveRange struct {
	ID    string `json:"id,omitempty"`
	Role  string `json:"role,omitempty"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

// ArchiveRef is an optional pointer to the lossless archive of the messages a
// compaction dropped. Path is relative to the session store directory (the
// same dir that holds the <id>.jsonl transcripts), so a record can carry it
// without embedding a machine-specific absolute path. Bytes is the size of the
// archive file; Ranges maps each dropped message, in order, to its byte range
// inside that file.
type ArchiveRef struct {
	Path   string         `json:"path"`
	Bytes  int64          `json:"bytes"`
	Ranges []ArchiveRange `json:"ranges,omitempty"`
}

// Compaction is the payload of a kind:"compaction" record. The summary
// replaces the dropped part of the conversation; Keep is how many of the most
// recent messages still follow it verbatim.
//
// Archive is additive and optional (omitempty): older records have no archive
// field and are read exactly as before. Existing compaction behaviour never
// sets it; writers opt in by calling WriteCompactionArchive / WriteCompactionArchiveRef.
type Compaction struct {
	Summary   string      `json:"summary"`
	Dropped   int         `json:"dropped"`
	Keep      int         `json:"keep"`
	Files     []string    `json:"files,omitempty"`
	Failures  []string    `json:"failures,omitempty"`
	NextSteps []string    `json:"nextSteps,omitempty"`
	Archive   *ArchiveRef `json:"archive,omitempty"`
}

// Compact replaces the older part of the conversation with one summary record
// and keeps the last keep messages. It is a no-op when the history already fits.
func (s *Session) Compact(keep int) error {
	if keep < 0 {
		return fmt.Errorf("session: keep must be >= 0, got %d", keep)
	}
	entries := s.Messages()
	if len(entries) <= keep {
		return nil
	}
	start := adjustCut(entries, len(entries)-keep)
	if start <= 0 {
		return nil
	}
	return s.appendCompaction(entries, start)
}

// CompactBudget compacts the older part of the conversation so the kept suffix
// fits an estimated token budget. The original Compact(keep) signature is
// unchanged; this is the token-budget overload used by mid-turn compaction,
// where a fixed message count is meaningless for variable-length tool output.
func (s *Session) CompactBudget(budget int) error {
	if budget <= 0 {
		return fmt.Errorf("session: compaction budget must be > 0, got %d", budget)
	}
	entries := s.Messages()
	start := CutForBudget(entries, budget)
	if start <= 0 {
		return nil
	}
	return s.appendCompaction(entries, start)
}

// appendCompaction writes the summary record and then re-emits any
// retention-flagged records from the dropped prefix so they survive
// (SelectForCompaction). A flagged tool call/result is re-emitted together with
// its partner, so compaction never reintroduces an orphaned result or a call
// without its result. When nothing is flagged this is byte-for-byte the old
// behaviour: one compaction record and nothing else.
func (s *Session) appendCompaction(entries []Entry, start int) error {
	restored := retainedForCompaction(entries, start)
	compaction := Summarize(entries[:start], len(entries)-start)
	if err := s.Append(KindCompaction, compaction); err != nil {
		return err
	}
	for _, record := range restored {
		if _, err := s.AppendRecord(record.Kind, record.Data); err != nil {
			return err
		}
	}
	return nil
}

// retainedForCompaction returns the flagged records from the dropped prefix in
// original order, expanded to whole tool pairs. Only Record.Data is inspected
// by SelectForCompaction, so projecting Entry onto a minimal Record is enough.
func retainedForCompaction(entries []Entry, start int) []Record {
	dropped := entries[:start]
	records := make([]Record, len(dropped))
	for i, entry := range dropped {
		records[i] = Record{ID: entry.ID, Data: entry.Data}
	}
	// No cap: a retention hint must win over keeping compaction cheap.
	flagged := SelectForCompaction(records, 0)
	if len(flagged) == 0 {
		return nil
	}
	flaggedIDs := make(map[string]bool, len(flagged))
	for _, record := range flagged {
		flaggedIDs[record.ID] = true
	}

	// Index tool pairs inside the dropped prefix so a flagged half pulls in
	// the other half rather than leaving an orphan in the survived context.
	calls := make(map[string][]int)
	results := make(map[string][]int)
	for i, entry := range dropped {
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				calls[id] = append(calls[id], i)
			}
		case "tool":
			if entry.ToolCallID != "" {
				results[entry.ToolCallID] = append(results[entry.ToolCallID], i)
			}
		}
	}

	keep := make([]bool, len(dropped))
	for i, entry := range dropped {
		if !flaggedIDs[entry.ID] {
			continue
		}
		keep[i] = true
		if entry.Role == "tool" {
			for _, ci := range calls[entry.ToolCallID] {
				keep[ci] = true
			}
		}
		if entry.Role == "assistant" {
			for _, id := range toolCallIDs(entry) {
				for _, ri := range results[id] {
					keep[ri] = true
				}
			}
		}
	}

	out := make([]Record, 0, len(dropped))
	for i, keepIt := range keep {
		if keepIt {
			out = append(out, Record{Kind: entryKind(dropped[i]), Data: dropped[i].Data})
		}
	}
	return out
}

// entryKind maps a conversation role back to the record kind used to re-emit
// it, so messageEntry projects the restored record onto the same Entry.
func entryKind(entry Entry) string {
	switch entry.Role {
	case "user":
		return KindUser
	case "assistant":
		return KindAssistant
	case "tool":
		return KindTool
	case "system":
		return KindSummary
	default:
		return KindMessage
	}
}

// CutForBudget returns the first index to keep for a conversation whose kept
// suffix should fit budget. Zero means there is nothing worth compacting: the
// whole history already fits, or the budget is too small to keep any pair.
func CutForBudget(entries []Entry, budget int) int {
	keep := KeepForBudget(entries, budget)
	if keep <= 0 || keep >= len(entries) {
		return 0
	}
	return adjustCut(entries, len(entries)-keep)
}

// KeepForBudget reports how many trailing entries fit within budget. It walks
// from the newest entry backwards, so the most recent context survives.
func KeepForBudget(entries []Entry, budget int) int {
	if budget <= 0 {
		return 0
	}
	total, keep := 0, 0
	for i := len(entries) - 1; i >= 0; i-- {
		cost := estimateEntryTokens(entries[i])
		if total+cost > budget {
			break
		}
		total += cost
		keep++
	}
	return keep
}

// adjustCut makes a proposed cut tool-pairing safe: a tool call and its result
// must end up on the same side of the cut. It first preserves the original
// behaviour of backing up over a trailing run of tool messages, then uses the
// tool-call ids carried in assistant payloads and tool results to back up over
// any remaining call/result pair straddling the cut. Backing up keeps more
// messages verbatim (the pair moves into the kept suffix); it never moves the
// cut forward, so a call can never be summarised while its result is kept (or
// vice versa). The same helper is used by Compact(keep) and CompactBudget.
func adjustCut(entries []Entry, start int) int {
	// Legacy, id-less case: never cut inside a trailing run of tool results.
	for start > 0 && start < len(entries) && entries[start].Role == "tool" {
		start--
	}

	// Map every tool-call id to the assistant indices that declared it. The
	// transcript may contain retries with the same id, so keep all positions.
	callIndices := make(map[string][]int)
	for i, entry := range entries {
		if entry.Role != "assistant" {
			continue
		}
		for _, id := range toolCallIDs(entry) {
			callIndices[id] = append(callIndices[id], i)
		}
	}

	// For each pair (call, result) the cut must not fall strictly between them,
	// i.e. in (call, result]. Lowering start to the call index resolves that
	// split; repeat because the new start may sit inside another pair's range.
	for {
		moved := false
		for i, entry := range entries {
			if entry.Role != "tool" || entry.ToolCallID == "" {
				continue
			}
			call := lastCallBefore(callIndices[entry.ToolCallID], i)
			if call >= 0 && call < start && start <= i {
				start = call
				moved = true
				break
			}
		}
		if !moved {
			break
		}
	}
	return start
}

// toolCallIDs extracts the ids of the tool calls declared by an assistant
// entry. Assistant records persist the full llm.Message payload, whose key is
// "tool_calls" (some producers use "toolCalls"); older records may instead
// carry a single "tool_call_id". Entries without any id contribute no pairs,
// which preserves the id-less trailing-run behaviour of adjustCut.
func toolCallIDs(entry Entry) []string {
	if len(entry.Data) == 0 {
		if entry.ToolCallID != "" {
			return []string{entry.ToolCallID}
		}
		return nil
	}
	var payload struct {
		Snake []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		Camel []struct {
			ID string `json:"id"`
		} `json:"toolCalls"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		if entry.ToolCallID != "" {
			return []string{entry.ToolCallID}
		}
		return nil
	}
	ids := make([]string, 0, len(payload.Snake)+len(payload.Camel))
	for _, call := range payload.Snake {
		if call.ID != "" {
			ids = append(ids, call.ID)
		}
	}
	for _, call := range payload.Camel {
		if call.ID != "" {
			ids = append(ids, call.ID)
		}
	}
	if len(ids) == 0 && entry.ToolCallID != "" {
		ids = append(ids, entry.ToolCallID)
	}
	return ids
}

// lastCallBefore returns the greatest call index strictly before before, or -1
// when the id has no call on that side of the transcript.
func lastCallBefore(indices []int, before int) int {
	last := -1
	for _, index := range indices {
		if index >= before {
			break
		}
		last = index
	}
	return last
}

// estimateEntryTokens is a conservative, allocation-free estimate used only
// for compaction cuts. Wide (CJK) runes cost one token each; other runes follow
// the usual four-chars-per-token rule.
func estimateEntryTokens(entry Entry) int {
	cost := 4 + estimateRunes(entry.Content) + estimateRunes(entry.Name) + estimateRunes(entry.ToolCallID)
	cost += len(entry.Data) / 4
	return cost
}

func estimateRunes(text string) int {
	wide, narrow := 0, 0
	for _, r := range text {
		if (r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) ||
			(r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE30 && r <= 0xFE6F) ||
			(r >= 0xFF00 && r <= 0xFF60) || (r >= 0x20000 && r <= 0x3FFFD) {
			wide++
		} else {
			narrow++
		}
	}
	return wide + (narrow+3)/4
}

// Summarize turns the dropped entries into a compaction payload that keeps the
// structure a later turn needs: goal, touched files, failures and next steps.
func Summarize(dropped []Entry, keep int) Compaction {
	var goals, files, failures, next []string
	seenFiles := make(map[string]bool)

	for _, entry := range dropped {
		switch entry.Role {
		case "user":
			if goal := firstLine(entry.Content, 200); goal != "" {
				goals = append(goals, goal)
			}
		case "tool":
			if reason, ok := failureReason(entry.Content); ok {
				failures = append(failures, failureBullet(entry.Name, reason))
			}
		}
		if entry.Role == "assistant" || entry.Role == "system" {
			next = append(next, nextStepLines(entry.Content, 200)...)
		}
		for _, path := range pathsFromData(entry.Data) {
			if !seenFiles[path] {
				seenFiles[path] = true
				files = append(files, path)
			}
		}
	}
	// files are collected in chronological order; keep the most recent twelve
	// before sorting, otherwise "recent" would mean lexicographically last.
	files = tailItems(files, 12)
	sort.Strings(files)
	goals = tailItems(goals, 5)
	failures = tailItems(failures, 8)
	next = tailItems(next, 5)

	var summary strings.Builder
	summary.WriteString("## Compaction\n")
	fmt.Fprintf(&summary, "- dropped %d message(s), kept the last %d\n", len(dropped), keep)
	writeSection(&summary, "Goal", goals)
	writeSection(&summary, "Files changed", files)
	writeSection(&summary, "Failures", failures)
	writeSection(&summary, "Next steps", next)
	// Deterministic, zero-LLM account of the reads/edits/commands that were
	// dropped, so a resumed turn does not repeat them or forget a change.
	if activity := summarizeToolActivity(dropped); activity != "" {
		summary.WriteString(activity)
	}

	return Compaction{
		Summary:   summary.String(),
		Dropped:   len(dropped),
		Keep:      keep,
		Files:     files,
		Failures:  failures,
		NextSteps: next,
	}
}

func writeSection(builder *strings.Builder, title string, items []string) {
	fmt.Fprintf(builder, "\n## %s\n", title)
	if len(items) == 0 {
		builder.WriteString("- (none)\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(builder, "- %s\n", item)
	}
}

// nextStepMarkers are the short explicit cues a model uses to record
// unfinished work. Matching is intentionally loose (substring, case-folded):
// a false positive costs one extra bullet in the summary, while a miss would
// drop the plan a resumed run needs.
var nextStepMarkers = []string{
	"next step", "next:", "todo", "follow up", "follow-up", "remaining", "下一步", "后续", "待办",
}

// nextStepLines keeps only the lines that actually carry a next-step marker.
// A plan is often written after a summary line, so inspecting just the first
// line of an entry loses it.
func nextStepLines(content string, limit int) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		marked := false
		for _, marker := range nextStepMarkers {
			if strings.Contains(lower, marker) {
				marked = true
				break
			}
		}
		if !marked {
			continue
		}
		if step := truncateRunes(trimmed, limit); step != "" {
			out = append(out, step)
		}
	}
	return out
}

// tailItems keeps the most recent limit entries.
func tailItems(items []string, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

// failureReason recognizes a tool result that represents a failure.
func failureReason(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if index := strings.LastIndex(lower, "[exit "); index >= 0 {
		tail := trimmed[index:]
		code := 0
		digits := 0
		rest := strings.TrimPrefix(tail, "[exit ")
		for _, ch := range rest {
			if ch < '0' || ch > '9' {
				break
			}
			code = code*10 + int(ch-'0')
			digits++
		}
		if digits > 0 && code != 0 {
			return firstLine(tail, 160), true
		}
	}
	if strings.Contains(lower, "timed out") {
		return "timed out", true
	}
	if reason := failureLine(trimmed, 160); reason != "" {
		return reason, true
	}
	return "", false
}

// failureMarkers are the cues that identify a failed tool result. Matching is
// case-folded substring matching on a single line so a failure buried after
// progress output ("compiling...", "building") is still captured.
var failureMarkers = []string{
	"error", "failed", "panic", "traceback", "fatal", "错误", "失败", "异常", "崩溃",
}

// failureLine returns the first line of content that reports a failure. The
// whole result is scanned rather than just its first line, then the offending
// line is clipped to limit runes.
func failureLine(content string, limit int) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, marker := range failureMarkers {
			if strings.Contains(lower, marker) {
				return truncateRunes(trimmed, limit)
			}
		}
	}
	return ""
}

func failureBullet(name, reason string) string {
	if strings.TrimSpace(name) == "" {
		name = "tool"
	}
	return name + " — " + reason
}

// pathsFromData collects file paths mentioned by a record payload, including
// tool-call arguments that are themselves JSON strings.
func pathsFromData(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	collectPaths(value, &out, seen)
	return out
}

func collectPaths(value any, out *[]string, seen map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "path", "file":
				if text, ok := child.(string); ok {
					addPath(out, seen, text)
				}
			case "files":
				switch list := child.(type) {
				case []any:
					for _, item := range list {
						if text, ok := item.(string); ok {
							addPath(out, seen, text)
						}
					}
				case string:
					addPath(out, seen, list)
				}
			case "arguments", "args", "input":
				if text, ok := child.(string); ok {
					var inner any
					if json.Unmarshal([]byte(text), &inner) == nil {
						collectPaths(inner, out, seen)
					}
					continue
				}
				collectPaths(child, out, seen)
			default:
				collectPaths(child, out, seen)
			}
		}
	case []any:
		for _, item := range typed {
			collectPaths(item, out, seen)
		}
	}
}

func addPath(out *[]string, seen map[string]bool, path string) {
	path = strings.TrimSpace(path)
	if path == "" || seen[path] {
		return
	}
	seen[path] = true
	*out = append(*out, path)
}

// ---------------------------------------------------------------------------
// Lossless compaction archive.
//
// A compaction only adds a summary record; the original JSONL transcript is
// never rewritten, so "lossless" already holds at the file level. These helpers
// add the second half of the gptme audit item: a model-friendly, byte-addressable
// copy of the dropped messages. They are deliberately pure (no wiring into
// Compact/appendCompaction) so existing compaction stays byte-for-byte identical.
// Archives live under <session-store-dir>/compactions/, a subdirectory that
// Container.List already skips because it only considers *.jsonl files at the
// top level. Nothing is written under .argent/.
// ---------------------------------------------------------------------------

const compactionArchiveDir = "compactions"

// compactionArchiveID validates an archive id the same way Container.path
// validates a session id, so an id can never escape the compactions directory.
// A trailing ".jsonl" is accepted and stripped for convenience.
func compactionArchiveID(id string) (string, error) {
	clean := strings.TrimSpace(id)
	if strings.HasSuffix(strings.ToLower(clean), fileExt) {
		clean = clean[:len(clean)-len(fileExt)]
	}
	if clean == "" || clean == "." || clean == ".." ||
		strings.ContainsAny(clean, `/\:*?"<>|`) {
		return "", fmt.Errorf("session: invalid compaction archive id %q", id)
	}
	return clean, nil
}

// CompactionArchivePath returns the on-disk path of the archive for id:
// <dir>/compactions/<id>.jsonl. The id is validated for path safety.
func CompactionArchivePath(dir, id string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("session: empty compaction archive directory")
	}
	clean, err := compactionArchiveID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, compactionArchiveDir, clean+fileExt), nil
}

// compactionArchiveRelPath is the path stored in an ArchiveRef: relative to the
// session store directory, with forward slashes for stable records on any OS.
func compactionArchiveRelPath(id string) string {
	return filepath.ToSlash(filepath.Join(compactionArchiveDir, id+fileExt))
}

// writeCompactionArchive is the shared implementation. It writes messages as
// JSONL atomically (temp file + rename), computes a byte range for every
// message, and returns both the absolute path and an ArchiveRef whose Path is
// relative to dir. No temp file is left behind on any error path.
func writeCompactionArchive(dir, id string, messages []Entry) (path string, ref *ArchiveRef, err error) {
	full, err := CompactionArchivePath(dir, id)
	if err != nil {
		return "", nil, err
	}
	clean, _ := compactionArchiveID(id) // already validated by CompactionArchivePath

	archiveDir := filepath.Dir(full)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("session: create %s: %w", archiveDir, err)
	}

	tmp, err := os.CreateTemp(archiveDir, ".compact-*.tmp")
	if err != nil {
		return "", nil, fmt.Errorf("session: archive temp in %s: %w", archiveDir, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep original text byte-faithful (no \u003c etc.)

	ranges := make([]ArchiveRange, 0, len(messages))
	var offset int64
	for _, entry := range messages {
		buf.Reset()
		if err := enc.Encode(entry); err != nil {
			return "", nil, fmt.Errorf("session: encode archived message %q: %w", entry.ID, err)
		}
		start := offset
		n, err := tmp.Write(buf.Bytes())
		if err != nil {
			return "", nil, fmt.Errorf("session: write archive %s: %w", full, err)
		}
		offset += int64(n)
		ranges = append(ranges, ArchiveRange{
			ID:    entry.ID,
			Role:  entry.Role,
			Start: start,
			End:   offset,
		})
	}
	if err := tmp.Close(); err != nil {
		return "", nil, fmt.Errorf("session: close archive %s: %w", full, err)
	}
	// os.Rename does not overwrite an existing file on Windows, so drop a
	// previous archive of the same id first. The id is expected to be unique
	// per compaction; this only matters when a caller deliberately rewrites.
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("session: replace archive %s: %w", full, err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return "", nil, fmt.Errorf("session: rename archive to %s: %w", full, err)
	}
	committed = true

	return full, &ArchiveRef{
		Path:   compactionArchiveRelPath(clean),
		Bytes:  offset,
		Ranges: ranges,
	}, nil
}

// WriteCompactionArchive writes messages to the lossless archive
// <dir>/compactions/<id>.jsonl and returns the on-disk path. The write is
// atomic and leaves no temporary files behind.
func WriteCompactionArchive(dir, id string, messages []Entry) (path string, err error) {
	path, _, err = writeCompactionArchive(dir, id, messages)
	return path, err
}

// WriteCompactionArchiveRef is WriteCompactionArchive plus the byte ranges and
// store-relative path needed to embed a reference in a Compaction record.
func WriteCompactionArchiveRef(dir, id string, messages []Entry) (*ArchiveRef, error) {
	_, ref, err := writeCompactionArchive(dir, id, messages)
	return ref, err
}

// resolveArchivePath turns a stored ref.Path into a filesystem path. Absolute
// paths are used as-is; relative paths are resolved under dir. A ref.Path using
// forward slashes (as written by compactionArchiveRelPath) works on Windows too.
func resolveArchivePath(dir, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("session: empty compaction archive path")
	}
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		// Normalize a stored store-relative path (forward slashes) for this OS.
		candidate = filepath.Join(dir, filepath.FromSlash(candidate))
	}
	// The path may come from a persisted record, so never let it escape the
	// store directory (matching the invariant for every other filesystem path).
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("session: resolve archive dir %s: %w", dir, err)
	}
	full, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("session: resolve archive path %s: %w", path, err)
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("session: compaction archive path %q escapes store dir", path)
	}
	return full, nil
}

// ReadCompactionArchive reads an archive back into messages, preserving every
// field (including the original json.RawMessage Data). Blank and malformed
// lines are skipped, matching the leniency of readRecords. path may be the
// absolute path returned by WriteCompactionArchive or the store-relative Path
// stored in an ArchiveRef.
func ReadCompactionArchive(dir, path string) ([]Entry, error) {
	full, err := resolveArchivePath(dir, path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("session: read archive %s: %w", full, err)
	}
	defer file.Close()

	entries := make([]Entry, 0, 16)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("session: read archive %s: %w", full, err)
	}
	return entries, nil
}

// ReadArchiveRange reads exactly one byte range from an archive, the primitive
// that turns an ArchiveRange in a Compaction record into the original message
// bytes. Bounds are checked against the file so a corrupt offset cannot read
// past the archive.
func ReadArchiveRange(dir, path string, rng ArchiveRange) ([]byte, error) {
	full, err := resolveArchivePath(dir, path)
	if err != nil {
		return nil, err
	}
	if rng.Start < 0 || rng.End < rng.Start {
		return nil, fmt.Errorf("session: invalid archive range [%d,%d)", rng.Start, rng.End)
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("session: stat archive %s: %w", full, err)
	}
	if rng.End > info.Size() {
		return nil, fmt.Errorf("session: archive range [%d,%d) exceeds file size %d", rng.Start, rng.End, info.Size())
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("session: read archive %s: %w", full, err)
	}
	defer file.Close()

	buf := make([]byte, rng.End-rng.Start)
	if _, err := file.ReadAt(buf, rng.Start); err != nil {
		return nil, fmt.Errorf("session: read archive range [%d,%d): %w", rng.Start, rng.End, err)
	}
	return buf, nil
}

// CompactionArchiveNotice renders a short, model-readable reference that can
// be appended to a compaction summary/notice so a resumed turn knows the exact
// original text is recoverable and how to address it. It is a pure helper:
// compression does not call it unless a caller opts in.
func CompactionArchiveNotice(ref *ArchiveRef) string {
	if ref == nil || strings.TrimSpace(ref.Path) == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Lossless archive\n")
	fmt.Fprintf(&b, "- The verbatim text of the dropped messages is preserved in `%s` (%d bytes).\n", ref.Path, ref.Bytes)
	fmt.Fprintf(&b, "- To recover one message, read the byte range `start..end` for its index from the `archive.ranges` field of this compaction record.\n")
	return b.String()
}
