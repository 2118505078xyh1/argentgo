// Package session stores a conversation as JSONL files whose records form a tree.
//
// One file lives under a directory per session. Every line is a record
// `{"id","parent","ts","kind","data"}`; a record's parent is the id of the
// record it follows, so branching never rewrites history. The conversation the
// model sees is the path from the root to the active leaf, and a compaction
// record replaces the older part of that path with a summary.
package session

import (
	"encoding/json"
	"os"
	"runtime"
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
	ID      string
	Parent  string
	Role    string // system | user | assistant | tool
	Content string
	// Reasoning is the model's thought text on an assistant turn. It is
	// transcript-only metadata: empty on legacy records and on non-assistant
	// turns, and omitted from archives when unset so old bytes stay identical.
	Reasoning  string `json:"Reasoning,omitempty"`
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
