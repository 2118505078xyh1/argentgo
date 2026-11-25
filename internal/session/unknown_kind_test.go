package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnknownKindFailsClosedUnlessIgnorable(t *testing.T) {
	// 未知 kind 未标记 ignorable 必须报错；标记后允许跳过（#186）。
	unknown := Record{ID: "a", Kind: "future_event", Data: []byte(`{}`)}
	err := CheckUnknownKinds([]Record{unknown})
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("未知 kind 应报 ErrUnknownKind，得到 %v", err)
	}

	ignorable := Record{ID: "b", Kind: "future_event", Data: []byte(`{}`), Ignorable: true}
	if err := CheckUnknownKinds([]Record{ignorable}); err != nil {
		t.Fatalf("ignorable 事件应被允许跳过，得到 %v", err)
	}

	known := Record{ID: "c", Kind: KindUser, Data: []byte(`{}`)}
	if err := CheckUnknownKinds([]Record{known}); err != nil {
		t.Fatalf("已知 kind 不应报错，得到 %v", err)
	}
}

func TestOpenSessionRejectsUnknownKindFromFile(t *testing.T) {
	dir := t.TempDir()
	id := "s1"
	line := `{"id":"n1","kind":"future_event","ts":"2026-01-01T00:00:00Z","data":{}}` + "\n"
	if err := writeSessionFile(filepath.Join(dir, id+fileExt), []byte(line)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(dir, id); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("读取含未知 kind 的会话应失败，得到 %v", err)
	}
}

// writeSessionFile 是本测试文件内的最小写入助手（避免依赖生产写入路径的副作用）。
func writeSessionFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
