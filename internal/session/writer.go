package session

// Writer 是旧的单文件记录器。它现在通过树形 store 写入，因此记录与其他
// 会话一样带有 id 和父节点。
type Writer struct {
	session *Session
}

// New 在需要时创建 dir，并打开一个带时间戳的新会话文件。
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

// Write 追加一条记录，并将其链接到之前写入的记录。
func (w *Writer) Write(kind string, payload any) error {
	return w.session.Append(kind, payload)
}

// Close 刷新会话。记录是每次追加时写入的，因此没有缓冲状态会丢失；
// Close 的存在只是为了方便使用 defer 的调用方。
func (w *Writer) Close() error { return nil }

// Session 暴露底层的会话句柄。
func (w *Writer) Session() *Session { return w.session }

// ID 是正在写入的会话的 id。
func (w *Writer) ID() string { return w.session.ID() }
