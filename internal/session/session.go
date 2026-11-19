// Package session stores a conversation as JSONL files whose records form a tree.
//
// One file lives under a directory per session. Every line is a record
// `{"id","parent","ts","kind","data"}`; a record's parent is the id of the
// record it follows, so branching never rewrites history. The conversation the
// model sees is the path from the root to the active leaf, and a compaction
// record replaces the older part of that path with a summary.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Record kinds written by the store. Kinds are open: an unknown kind is kept
// verbatim and ignored by the conversation view.
const (
	KindHeader     = "header"
	KindMessage    = "message"
	KindUser       = "user"
	KindAssistant  = "assistant"
	KindTool       = "tool"
	KindSummary    = "summary"
	KindCompaction = "compaction"
	KindCheckpoint = "checkpoint"
	KindAttempt    = "attempt"
	KindTitle      = "title"
)

const (
	fileExt    = ".jsonl"
	titleLimit = 60
	maxLineLen = 16 << 20

	// keepAppendFD is false on Windows: os.OpenFile there does not request
	// FILE_SHARE_DELETE, so a held handle blocks os.Remove/os.Rename of the
	// session file (and breaks t.TempDir cleanup). The id index still removes
	// the O(n^2) re-read on every platform; Windows just repeats the cheap
	// open/write/close trio per append.
	keepAppendFD = runtime.GOOS != "windows"
)

// Record is one line of a session file.
type Record struct {
	ID     string          `json:"id"`
	Parent string          `json:"parent"`
	TS     time.Time       `json:"ts"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`
}

// Entry is one message on the linear conversation.
type Entry struct {
	ID         string
	Parent     string
	Role       string // system | user | assistant | tool
	Content    string
	Name       string // tool name, when the record carries one
	ToolCallID string
	TS         time.Time
	Data       json.RawMessage
}

// Header is the first record of every session.
type Header struct {
	Title   string    `json:"title,omitempty"`
	Created time.Time `json:"created"`
}

// Session is an append-only handle to one session file.
type Session struct {
	mu     sync.Mutex
	dir    string
	id     string
	active string

	// Append state is built lazily on the first AppendRecord and reused while
	// the backing file is untouched. All fields below are guarded by mu.
	// Rebuilding it means one full JSONL read; steady-state appends only write
	// one line and update these fields, avoiding O(n^2) re-parsing.
	ids      map[string]struct{} // ids of the records currently in the file
	lastID   string              // id of the last record in file order
	nextSeq  int                 // next n%06d candidate to mint
	fd       *os.File            // long-lived O_CREATE|O_WRONLY|O_APPEND handle (nil on Windows)
	fdSize   int64               // file size at last sync
	fdMTime  time.Time           // modtime at last sync
	lastInfo os.FileInfo         // path identity at last sync (inode/volume on Windows)

	// readRecordsFn, when set by tests, overrides the full read used to build
	// or rebuild the index. Records() deliberately stays on readRecords.
	readRecordsFn func(string) ([]Record, error)
}

// ID is the session id, which is also its file name without the extension.
func (s *Session) ID() string { return s.id }

// Dir reports the directory that holds the session file.
func (s *Session) Dir() string { return s.dir }

// Append writes one record as a child of the active leaf and makes it the new
// active leaf. The payload is encoded as JSON; nil is stored as `null`.
func (s *Session) Append(kind string, payload any) error {
	_, err := s.AppendRecord(kind, payload)
	return err
}

// AppendRecord writes one record like Append but also returns the stored
// record, so callers can link later records (checkpoints, retries, attempts)
// to the exact id that was minted.
func (s *Session) AppendRecord(kind string, payload any) (Record, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return Record{}, errors.New("session: empty record kind")
	}
	data, err := encodePayload(payload)
	if err != nil {
		return Record{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	parent, id, err := s.prepareAppend()
	if err != nil {
		return Record{}, err
	}
	record := Record{
		ID:     id,
		Parent: parent,
		TS:     time.Now().UTC(),
		Kind:   kind,
		Data:   data,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return Record{}, fmt.Errorf("session: encode record: %w", err)
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if err := s.writeAppend(record.ID, buf); err != nil {
		return Record{}, err
	}
	s.active = record.ID
	return record, nil
}

// prepareAppend returns the parent and a fresh id for the next record. It
// lazily builds the in-memory index, and rebuilds it whenever the append fd is
// missing or stale. The id rule is the same as nextID: the lowest unused
// n%06d, so external records with gaps cannot cause a collision.
func (s *Session) prepareAppend() (parent, id string, err error) {
	if !s.appendStateValid() {
		if err := s.rebuildAppendState(); err != nil {
			return "", "", err
		}
	}
	parent = s.active
	if _, ok := s.ids[parent]; !ok {
		// Same fallback as the old full-read path: an active id that is not
		// in the file (forks, external rewrites) links to the last record.
		parent = s.lastID
	}
	id = fmt.Sprintf("n%06d", s.nextSeq)
	for {
		if _, used := s.ids[id]; !used {
			break
		}
		s.nextSeq++
		id = fmt.Sprintf("n%06d", s.nextSeq)
	}
	s.nextSeq++
	return parent, id, nil
}

// appendStateValid checks that the cached index still describes the file on
// disk: same inode/volume, same size, same modtime. A replacement by rename, a
// truncation, or an edit by another writer fails one of these checks, so the
// caller falls back to the full-read rebuild path. On Windows there is no held
// fd, so the path identity recorded at last sync is used for the inode check.
func (s *Session) appendStateValid() bool {
	if s.ids == nil {
		return false
	}
	pathInfo, err := os.Stat(s.path())
	if err != nil {
		return false
	}
	if pathInfo.Size() != s.fdSize || !pathInfo.ModTime().Equal(s.fdMTime) {
		return false
	}
	if s.fd != nil {
		fdInfo, err := s.fd.Stat()
		if err != nil {
			return false
		}
		if !os.SameFile(fdInfo, pathInfo) || fdInfo.Size() != s.fdSize {
			return false
		}
	} else if s.lastInfo == nil || !os.SameFile(pathInfo, s.lastInfo) {
		return false
	}
	return true
}

// rebuildAppendState is the full-read fallback: read the transcript once,
// rebuild the id index, then open a fresh append fd. It retries when the file
// changes between the read and the fstat, so the index can never lag behind
// bytes already in the file.
func (s *Session) rebuildAppendState() error {
	s.closeAppendFD()
	for attempt := 0; attempt < 3; attempt++ {
		pathInfo, err := os.Stat(s.path())
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("session: stat %s: %w", s.path(), err)
		}
		var expectedSize int64
		if err == nil {
			expectedSize = pathInfo.Size()
		}

		records, err := s.readAll()
		if err != nil {
			return err
		}
		s.ids = make(map[string]struct{}, len(records)+8)
		s.lastID = ""
		for _, record := range records {
			s.ids[record.ID] = struct{}{}
			s.lastID = record.ID
		}
		s.nextSeq = 1
		for {
			if _, used := s.ids[fmt.Sprintf("n%06d", s.nextSeq)]; !used {
				break
			}
			s.nextSeq++
		}

		fd, err := s.openAppendFile()
		if err != nil {
			return err
		}
		info, err := fd.Stat()
		if err != nil {
			fd.Close()
			return fmt.Errorf("session: stat %s: %w", s.path(), err)
		}
		if info.Size() != expectedSize {
			// Another writer moved the file between the read and the open;
			// start over instead of trusting a stale index.
			fd.Close()
			continue
		}
		// Stat the path after the open: O_CREATE may have just created the
		// file, so this is the first reliable identity/modtime snapshot.
		postInfo, err := os.Stat(s.path())
		if err != nil {
			fd.Close()
			return fmt.Errorf("session: stat %s: %w", s.path(), err)
		}
		if !os.SameFile(info, postInfo) || postInfo.Size() != expectedSize {
			fd.Close()
			continue
		}
		if !keepAppendFD {
			// See keepAppendFD: never leave a Windows handle open.
			fd.Close()
			fd = nil
		}
		s.fd = fd
		s.fdSize = expectedSize
		s.fdMTime = postInfo.ModTime()
		s.lastInfo = postInfo
		return nil
	}
	return fmt.Errorf("session: %s kept changing while rebuilding append state", s.path())
}

func (s *Session) openAppendFile() (*os.File, error) {
	fd, err := os.OpenFile(s.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", s.path(), err)
	}
	return fd, nil
}

// writeAppend writes one JSONL line and updates the index in memory. On Unix
// the long-lived fd is reused; on Windows a short-lived fd is opened per append
// (see keepAppendFD). Any anomaly drops the cached state so the next append
// takes the safe full-read path.
func (s *Session) writeAppend(id string, buf []byte) error {
	fd := s.fd
	temporary := fd == nil
	if temporary {
		var err error
		fd, err = s.openAppendFile()
		if err != nil {
			return err
		}
	}
	n, err := fd.Write(buf)
	if temporary {
		fd.Close()
	}
	if err != nil {
		s.closeAppendFD()
		s.ids = nil
		return fmt.Errorf("session: write %s: %w", s.path(), err)
	}
	if n != len(buf) {
		s.closeAppendFD()
		s.ids = nil
		return fmt.Errorf("session: write %s: short write", s.path())
	}
	s.ids[id] = struct{}{}
	s.lastID = id

	expected := s.fdSize + int64(len(buf))
	pathInfo, err := os.Stat(s.path())
	if err != nil || s.lastInfo == nil ||
		!os.SameFile(pathInfo, s.lastInfo) || pathInfo.Size() != expected {
		// The file was replaced or written to behind our back during the
		// append. The line reached a handle that is no longer current; drop
		// the cache so the next append rebuilds from what is really on disk.
		s.invalidateAppendState()
		return nil
	}
	if !temporary {
		if fdInfo, err := fd.Stat(); err != nil || !os.SameFile(fdInfo, pathInfo) {
			s.invalidateAppendState()
			return nil
		}
	}
	s.fdSize = expected
	s.fdMTime = pathInfo.ModTime()
	s.lastInfo = pathInfo
	return nil
}

// invalidateAppendState drops both the fd and the id index.
func (s *Session) invalidateAppendState() {
	s.closeAppendFD()
	s.ids = nil
	s.lastID = ""
}

func (s *Session) closeAppendFD() {
	if s.fd != nil {
		s.fd.Close()
		s.fd = nil
	}
	s.fdSize = 0
	s.fdMTime = time.Time{}
	s.lastInfo = nil
}

// readAll goes through the overridable reader so tests can count full reads.
func (s *Session) readAll() ([]Record, error) {
	if s.readRecordsFn != nil {
		return s.readRecordsFn(s.path())
	}
	return readRecords(s.path())
}

// invalidateAppendCacheAfterRewrite drops cached append state. Code that
// rewrites the session file (compaction, repair tools) should call this so the
// next append re-reads the file. Validation normally detects such rewrites via
// inode/size/modtime, but this makes it explicit and immediate.
func (s *Session) invalidateAppendCacheAfterRewrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateAppendState()
}

// Records returns every record in file order.
func (s *Session) Records() ([]Record, error) {
	return readRecords(s.path())
}

// Messages returns the linear conversation on the active branch. The latest
// compaction record replaces everything before it with one system summary.
func (s *Session) Messages() []Entry {
	records, err := s.Records()
	if err != nil {
		return nil
	}
	path := s.activePath(records)

	latest := -1
	for i := range path {
		if path[i].Kind == KindCompaction {
			latest = i
		}
	}
	if latest >= 0 {
		var compaction Compaction
		if err := json.Unmarshal(path[latest].Data, &compaction); err == nil {
			out := []Entry{{
				ID:      path[latest].ID,
				Parent:  path[latest].Parent,
				Role:    "system",
				Content: compaction.Summary,
				TS:      path[latest].TS,
				Data:    path[latest].Data,
			}}
			head := messageEntries(path[:latest])
			if compaction.Keep >= 0 && compaction.Keep < len(head) {
				head = head[len(head)-compaction.Keep:]
			}
			out = append(out, head...)
			out = append(out, messageEntries(path[latest+1:])...)
			return out
		}
	}
	return messageEntries(path)
}

// Path returns the records from the session root down to entryID, oldest first.
func (s *Session) Path(entryID string) ([]Record, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	byID := indexByID(records)
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := entryID; current != ""; {
		record, ok := byID[current]
		if !ok {
			return nil, fmt.Errorf("session: unknown entry %q", current)
		}
		if seen[current] {
			return nil, fmt.Errorf("session: cycle at entry %q", current)
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// Fork copies the path to entryID into a new session and returns it. The
// original session is untouched, and the copy keeps the original entry ids so
// the history it points at is unchanged.
func Fork(dir, id, entryID string) (*Session, error) {
	store, err := Open(dir)
	if err != nil {
		return nil, err
	}
	source, err := store.OpenSession(id)
	if err != nil {
		return nil, err
	}
	return source.Fork(entryID)
}

// Fork copies the path to entryID into a new session in the same directory.
func (s *Session) Fork(entryID string) (*Session, error) {
	path, err := s.Path(entryID)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("session: entry %q is not on a path", entryID)
	}
	// A fork taken from a mid-turn record would otherwise lose the header and
	// its explicit title. Copy the original header when the path omits it, and
	// carry the effective title across as a fresh title record.
	all, err := s.Records()
	if err != nil {
		return nil, err
	}
	hasHeader, hasTitle := false, false
	for _, record := range path {
		if record.Kind == KindHeader {
			hasHeader = true
		}
		if record.Kind == KindTitle {
			hasTitle = true
		}
	}
	if !hasHeader {
		for _, record := range all {
			if record.Kind == KindHeader {
				path = append([]Record{record}, path...)
				break
			}
		}
	}
	if !hasTitle {
		if title := titleFromRecords(s.ID(), all); title != "" {
			data, err := encodePayload(TitleRecord{Title: title})
			if err != nil {
				return nil, err
			}
			path = append(path, Record{
				ID:     nextID(path),
				Parent: path[len(path)-1].ID,
				TS:     time.Now().UTC(),
				Kind:   KindTitle,
				Data:   data,
			})
		}
	}
	container := &Container{dir: s.dir}
	if err := os.MkdirAll(container.dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", container.dir, err)
	}
	id, target, err := container.newID()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("session: create %s: %w", target, err)
	}
	writer := bufio.NewWriter(file)
	for _, record := range path {
		line, err := json.Marshal(record)
		if err != nil {
			file.Close()
			os.Remove(target)
			return nil, fmt.Errorf("session: encode fork record: %w", err)
		}
		if _, err := writer.Write(append(line, '\n')); err != nil {
			file.Close()
			os.Remove(target)
			return nil, fmt.Errorf("session: write %s: %w", target, err)
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(target)
		return nil, fmt.Errorf("session: write %s: %w", target, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(target)
		return nil, fmt.Errorf("session: close %s: %w", target, err)
	}
	return &Session{dir: container.dir, id: id, active: path[len(path)-1].ID}, nil
}

// path is the session file location.
func (s *Session) path() string {
	return filepath.Join(s.dir, s.id+fileExt)
}

// activePath walks from the active leaf to the root and returns the records
// oldest first. Records that are not reachable from the leaf are branches and
// stay out of the conversation.
func (s *Session) activePath(records []Record) []Record {
	byID := indexByID(records)
	leaf := s.active
	if _, ok := byID[leaf]; !ok {
		if len(records) == 0 {
			return nil
		}
		leaf = records[len(records)-1].ID
	}
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := leaf; current != ""; {
		record, ok := byID[current]
		if !ok {
			break
		}
		if seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// readRecords parses a transcript. Blank and malformed lines are skipped so a
// torn append cannot make the whole session unreadable.
func readRecords(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	defer file.Close()

	records := make([]Record, 0, 16)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record.ID == "" {
			record.ID = fmt.Sprintf("n%06d", len(records)+1)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return records, fmt.Errorf("session: read %s: %w", path, err)
	}
	return records, nil
}

func indexByID(records []Record) map[string]Record {
	index := make(map[string]Record, len(records))
	for _, record := range records {
		index[record.ID] = record
	}
	return index
}

// nextID mints the lowest id that is not already used.
func nextID(records []Record) string {
	used := make(map[string]bool, len(records))
	for _, record := range records {
		used[record.ID] = true
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("n%06d", n)
		if !used[candidate] {
			return candidate
		}
	}
}

func encodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("session: encode payload: %w", err)
	}
	return data, nil
}

// messageEntry projects one record onto the conversation, or reports false
// when the record is metadata, an event, or a compaction marker.
func messageEntry(record Record) (Entry, bool) {
	entry := Entry{
		ID:     record.ID,
		Parent: record.Parent,
		TS:     record.TS,
		Data:   record.Data,
		Name:   fieldString(record.Data, "name", "tool"),
	}
	switch record.Kind {
	case KindUser, "prompt":
		entry.Role = "user"
		entry.Content = textOf(record.Data)
	case KindAssistant, "model", "reply":
		entry.Role = "assistant"
		entry.Content = textOf(record.Data)
		entry.ToolCallID = fieldString(record.Data, "toolCallId", "tool_call_id")
	case KindTool, "tool_result", "tool-result", "toolresult":
		entry.Role = "tool"
		entry.Content = firstNonEmpty(
			fieldString(record.Data, "output"),
			fieldString(record.Data, "content"),
			fieldString(record.Data, "result"),
			textOf(record.Data),
		)
		entry.ToolCallID = firstNonEmpty(
			fieldString(record.Data, "toolCallId", "tool_call_id"),
			fieldString(record.Data, "id"),
		)
	case KindMessage:
		entry.Role = fieldString(record.Data, "role")
		if entry.Role == "" {
			entry.Role = "user"
		}
		entry.Content = textOf(record.Data)
		entry.ToolCallID = fieldString(record.Data, "toolCallId", "tool_call_id")
	case KindSummary:
		entry.Role = "system"
		entry.Content = firstNonEmpty(fieldString(record.Data, "text"), textOf(record.Data))
	default:
		return Entry{}, false
	}
	if entry.Role == "" {
		return Entry{}, false
	}
	return entry, true
}

func messageEntries(records []Record) []Entry {
	out := make([]Entry, 0, len(records))
	for _, record := range records {
		if entry, ok := messageEntry(record); ok {
			out = append(out, entry)
		}
	}
	return out
}

// fieldString reads the first matching string key from a JSON object payload.
func fieldString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}

// textOf reads a message payload: a bare JSON string, or the usual text keys
// of an object (`content`, `text`, `output`, `result`, `message`).
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"content", "text", "output", "result", "message"} {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// truncateRunes clips text to limit runes, collapsing it to one line first.
func truncateRunes(text string, limit int) string {
	cleaned := strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	if limit <= 0 {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func firstLine(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateRunes(trimmed, limit)
		}
	}
	return ""
}
