package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

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
	// Auto-stamp durable kinds (checkpoint/summary/compaction/title) so the
	// compaction and child-context selectors can find them. Default kinds stay
	// byte-identical, so ordinary message/tool writes are unchanged.
	data = StampIfImportant(kind, data)

	s.mu.Lock()
	defer s.mu.Unlock()

	// prepareAppend mints an id and bumps nextSeq before anything reaches the
	// disk. Keep the pre-append value so a failed write can undo that drift and
	// leave the index describing exactly what is in the file.
	prevNextSeq := s.nextSeq

	parent, id, err := s.prepareAppend()
	if err != nil {
		return Record{}, err
	}
	// Records() is always a fresh read from disk, so `active`, `lastID` and the
	// `ids` entry for this record are only advanced after a successful write.
	// On failure the only in-memory change left behind is the bumped nextSeq;
	// drop the minted id from the index as well in case a writer added it
	// before reporting an error. writeAppend already drops the fd/cache when
	// the write itself failed, so the next append safely rebuilds from disk.
	rollback := func() {
		s.nextSeq = prevNextSeq
		if s.ids != nil {
			delete(s.ids, id)
		}
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
		rollback()
		return Record{}, fmt.Errorf("session: encode record: %w", err)
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if err := s.writeAppend(record.ID, buf); err != nil {
		rollback()
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
