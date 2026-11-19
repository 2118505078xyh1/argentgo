package session

// Writer is the legacy single-file recorder. It now writes through the tree
// store, so records carry ids and parents like every other session.
type Writer struct {
	session *Session
}

// New creates dir if needed and opens a fresh timestamped session file.
func New(dir string) (*Writer, error) {
	store, err := Open(dir)
	if err != nil {
		return nil, err
	}
	session, err := store.NewSession("")
	if err != nil {
		return nil, err
	}
	return &Writer{session: session}, nil
}

// Write appends one record, chained to the previously written record.
func (w *Writer) Write(kind string, payload any) error {
	return w.session.Append(kind, payload)
}

// Close flushes the session. Records are written per append, so there is no
// buffered state to lose; Close exists for callers that defer it.
func (w *Writer) Close() error { return nil }

// Session exposes the underlying session handle.
func (w *Writer) Session() *Session { return w.session }

// ID is the id of the session being written.
func (w *Writer) ID() string { return w.session.ID() }
