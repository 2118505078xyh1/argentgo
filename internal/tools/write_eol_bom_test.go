package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRawFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRaw(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestWriteNormalizeNewFileEOL 锁定 #34：EOL 按 before 检测——新文件无既有
// EOL 可检测，保持模型原始字节；既有文件统一到检测到的 EOL，
// 避免插入不同行尾的文本造成混合行尾。
func TestWriteNormalizeNewFileEOL(t *testing.T) {
	t.Run("new file keeps original bytes", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "new.txt")
		if _, err := runWriteTool(t, root, "new.txt", "alpha\nbeta\n"); err != nil {
			t.Fatal(err)
		}
		if got, want := string(readRaw(t, path)), "alpha\nbeta\n"; got != want {
			t.Fatalf("new file = %q, want %q", got, want)
		}
	})

	t.Run("existing CRLF file stays CRLF", func(t *testing.T) {
		root := t.TempDir()
		writeRawFixture(t, filepath.Join(root, "crlf.txt"), []byte("keep\r\nold\r\n"))
		if _, err := runWriteTool(t, root, "crlf.txt", "keep\nnew\n"); err != nil {
			t.Fatal(err)
		}
		if got, want := string(readRaw(t, filepath.Join(root, "crlf.txt"))), "keep\r\nnew\r\n"; got != want {
			t.Fatalf("crlf file = %q, want %q", got, want)
		}
	})

	t.Run("existing LF file stays LF", func(t *testing.T) {
		root := t.TempDir()
		writeRawFixture(t, filepath.Join(root, "lf.txt"), []byte("keep\nold\n"))
		if _, err := runWriteTool(t, root, "lf.txt", "keep\r\nnew\r\n"); err != nil {
			t.Fatal(err)
		}
		if got, want := string(readRaw(t, filepath.Join(root, "lf.txt"))), "keep\nnew\n"; got != want {
			t.Fatalf("lf file = %q, want %q", got, want)
		}
	})

	t.Run("escape hatch", func(t *testing.T) {
		t.Setenv(eolNormalizeEnv, "0")
		root := t.TempDir()
		path := filepath.Join(root, "raw.txt")
		if _, err := runWriteTool(t, root, "raw.txt", "alpha\nbeta\n"); err != nil {
			t.Fatal(err)
		}
		if got, want := string(readRaw(t, path)), "alpha\nbeta\n"; got != want {
			t.Fatalf("raw file = %q, want %q", got, want)
		}
	})
}

// TestEditPreservesDetectedEOL 锁定 #34 的 edit 落点：CRLF 文件编辑后仍是 CRLF。
func TestEditPreservesDetectedEOL(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "crlf.txt")
	writeRawFixture(t, path, []byte("head\r\nold\r\ntail\r\n"))
	raw, err := json.Marshal(map[string]string{"path": "crlf.txt", "old": "old", "new": "new"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, true).Run(context.Background(), "edit", string(raw)); err != nil {
		t.Fatal(err)
	}
	if got, want := string(readRaw(t, path)), "head\r\nnew\r\ntail\r\n"; got != want {
		t.Fatalf("edited file = %q, want %q", got, want)
	}
}

// TestWritePreservesExistingBOM 锁定 #35：整文件 write 不抹掉既有 UTF-8 BOM。
func TestWritePreservesExistingBOM(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bom.txt")
	writeRawFixture(t, path, append(append([]byte{}, utf8BOM...), []byte("old\n")...))
	if _, err := runWriteTool(t, root, "bom.txt", "new\n"); err != nil {
		t.Fatal(err)
	}
	got := readRaw(t, path)
	if !strings.HasPrefix(string(got), string(utf8BOM)) {
		t.Fatalf("BOM was dropped: %q", got)
	}
	body := strings.TrimPrefix(string(got), string(utf8BOM))
	if !strings.Contains(body, "new") {
		t.Fatalf("unexpected body: %q", body)
	}
}

// TestWriteBOMNoopUnchanged 锁定 #35 的判等顺序：BOM 在空操作判等之前补上，
// 因此真正同文（补 BOM 后相等）时走 no-op、modTime 不变。
func TestWriteBOMNoopUnchanged(t *testing.T) {
	root := t.TempDir()
	rel := "bom.txt"
	path := filepath.Join(root, rel)
	writeRawFixture(t, path, append(append([]byte{}, utf8BOM...), []byte("stable")...))
	setStaleModTime(t, path)
	before := modTime(t, path)

	out, err := runWriteTool(t, root, rel, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no write needed") {
		t.Fatalf("expected no-op, got %q", out)
	}
	if after := modTime(t, path); !after.Equal(before) {
		t.Fatalf("noop touched file: %v -> %v", before, after)
	}
	if got, want := string(readRaw(t, path)), string(utf8BOM)+"stable"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestWriteBOMEscapeHatch(t *testing.T) {
	t.Setenv(bomPreserveEnv, "0")
	root := t.TempDir()
	path := filepath.Join(root, "bom.txt")
	writeRawFixture(t, path, append(append([]byte{}, utf8BOM...), []byte("old")...))
	if _, err := runWriteTool(t, root, "bom.txt", "new"); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(readRaw(t, path)), string(utf8BOM)) {
		t.Fatal("BOM should not be preserved when escape hatch is set")
	}
}
