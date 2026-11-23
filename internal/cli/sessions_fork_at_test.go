package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// newForkAtFixture 建一个含两轮对话的会话，返回 store、会话句柄与第二轮用户消息记录。
func newForkAtFixture(t *testing.T, storage string) (*session.Store, *session.Session, session.Record) {
	t.Helper()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("fork-at")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(session.KindUser, "第一轮提问"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(session.KindAssistant, "第一轮回答"); err != nil {
		t.Fatal(err)
	}
	second, err := sess.AppendRecord(session.KindUser, "第二轮提问")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(session.KindAssistant, "第二轮回答"); err != nil {
		t.Fatal(err)
	}
	return store, sess, second
}

// TestSessionsForkAtCommand 验证 sessions fork-at 在指定用户消息处创建新会话，
// 打印新 id 与还原 prompt；新会话停在分叉点并追加只读边界，原会话保持不变。
func TestSessionsForkAtCommand(t *testing.T) {
	storage := t.TempDir()
	store, sess, second := newForkAtFixture(t, storage)
	before, err := store.List()
	if err != nil {
		t.Fatal(err)
	}

	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "fork-at", sess.ID(), second.ID,
	}); code != 0 {
		t.Fatalf("fork-at exit = %d, out %q err %q", code, out.String(), errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "forked session") {
		t.Fatalf("fork-at output = %q", text)
	}
	if !strings.Contains(text, "restored prompt: 第二轮提问") {
		t.Fatalf("fork-at output missing restored prompt: %q", text)
	}

	// 从输出解析新会话 id，确认确实是新文件且对话停在分叉点。
	var newID string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && fields[0] == "forked" && fields[1] == "session" {
			newID = fields[2]
		}
	}
	if newID == "" || newID == sess.ID() {
		t.Fatalf("newID = %q, want a fresh session id", newID)
	}
	fork, err := session.OpenSession(filepath.Join(storage, "sessions"), newID)
	if err != nil {
		t.Fatalf("OpenSession(%q): %v", newID, err)
	}
	messages := fork.Messages()
	// 分叉点之后追加了 developer 边界指令与带稳定标记的边界用户消息，
	// 因此末条不再是原 prompt（原 prompt 只作为继承历史保留在前）。
	if len(messages) != 5 {
		t.Fatalf("fork messages = %+v, want 5 (user, assistant, user, developer, boundary)", messages)
	}
	last := messages[len(messages)-1]
	if !strings.Contains(last.Content, session.ForkBoundaryMarker) {
		t.Fatalf("fork last message is not the boundary marker: %+v", last)
	}
	if messages[2].ID != second.ID || messages[2].Content != "第二轮提问" {
		t.Fatalf("fork point message = %+v, want %s/第二轮提问", messages[2], second.ID)
	}
	after, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("session count = %d, want %d", len(after), len(before)+1)
	}
	if got := len(sess.Messages()); got != 4 {
		t.Fatalf("original messages = %d, want 4 (unchanged)", got)
	}
}

// TestSessionsForkAtInvalid 验证缺参、未知消息、非用户消息与非法会话 id 都返回
// 合适的退出码，且失败时不会创建新会话文件。
func TestSessionsForkAtInvalid(t *testing.T) {
	storage := t.TempDir()
	store, sess, _ := newForkAtFixture(t, storage)
	before, err := store.List()
	if err != nil {
		t.Fatal(err)
	}

	// 找出一条 assistant 记录，作为“非用户消息”的分叉点。
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	assistantID := ""
	for _, record := range records {
		if record.Kind == session.KindAssistant {
			assistantID = record.ID
			break
		}
	}
	if assistantID == "" {
		t.Fatal("fixture has no assistant record")
	}

	cases := []struct {
		name string
		args []string
		code int
	}{
		{"missing message-id", []string{"sessions", "fork-at", sess.ID()}, 2},
		{"unknown message", []string{"sessions", "fork-at", sess.ID(), "missing"}, 1},
		{"assistant message", []string{"sessions", "fork-at", sess.ID(), assistantID}, 1},
		{"unknown session", []string{"sessions", "fork-at", "no-such-session", "n000001"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdio, out, errOut := newTestIO("", nil)
			if code := RunWith(App{IO: stdio, StorageDir: storage}, tc.args); code != tc.code {
				t.Fatalf("exit = %d, want %d; out %q err %q", code, tc.code, out.String(), errOut.String())
			}
			if strings.Contains(errOut.String(), "unknown sessions command") {
				t.Fatalf("fork-at not recognized as a subcommand: %q", errOut.String())
			}
		})
	}

	after, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed forks created %d session(s): before %d after %d", len(after)-len(before), len(before), len(after))
	}
}

// TestSessionsListMessagesCommand 验证 list-messages 只列出用户消息的 id 与文本，
// 不列出 assistant 消息，并对缺参与非法会话返回合适的退出码。
func TestSessionsListMessagesCommand(t *testing.T) {
	storage := t.TempDir()
	_, sess, second := newForkAtFixture(t, storage)

	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "list-messages", sess.ID(),
	}); code != 0 {
		t.Fatalf("list-messages exit = %d, err %q", code, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "第一轮提问") || !strings.Contains(text, "第二轮提问") {
		t.Fatalf("list-messages output = %q", text)
	}
	if strings.Contains(text, "第一轮回答") || strings.Contains(text, "第二轮回答") {
		t.Fatalf("list-messages listed assistant messages: %q", text)
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 {
		t.Fatalf("list-messages lines = %d, want 2: %q", len(lines), text)
	}
	if !strings.Contains(lines[1], second.ID) {
		t.Fatalf("list-messages line 2 = %q, want id %s", lines[1], second.ID)
	}

	// 缺少 <id> → 用法退出码；非法会话 → 错误退出码。
	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "list-messages",
	}); code != 2 {
		t.Fatalf("missing id exit = %d, out %q err %q", code, out.String(), errOut.String())
	}
	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "list-messages", "no-such-session",
	}); code != 1 {
		t.Fatalf("invalid id exit = %d, out %q err %q", code, out.String(), errOut.String())
	}
}

// TestSessionsForkAtDocumentedInUsage 验证两个新子命令出现在 sessions 与顶层 usage 中。
func TestSessionsForkAtDocumentedInUsage(t *testing.T) {
	var sessions bytes.Buffer
	sessionsUsage(&sessions)
	var top bytes.Buffer
	writeUsage(&top)
	for _, want := range []string{"fork-at", "list-messages"} {
		if !strings.Contains(sessions.String(), want) {
			t.Errorf("sessions usage does not mention %q", want)
		}
		if !strings.Contains(top.String(), want) {
			t.Errorf("top-level usage does not mention %q", want)
		}
	}
}
