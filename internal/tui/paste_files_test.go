package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeFileInfo struct{ name string }

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func fakeStat(existing ...string) PathStat {
	set := map[string]bool{}
	for _, p := range existing {
		set[p] = true
	}
	return func(p string) (os.FileInfo, error) {
		if set[p] {
			return fakeFileInfo{name: filepath.Base(p)}, nil
		}
		return nil, os.ErrNotExist
	}
}

func TestParsePastedFilesWT(t *testing.T) {
	a := `C:\proj\my file one.txt`
	b := `C:\proj\my file two.txt`
	stat := fakeStat(a, b)

	text := `"` + a + `" "` + b + `"`
	got, ok := ParsePastedFiles(text, stat)
	if !ok {
		t.Fatalf("WT quoted paste should parse: %q", text)
	}
	if want := []string{a, b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}

	// 混入一个不存在的路径时整段放弃，原样提交。
	text2 := `"` + a + `" "C:\nope\missing.txt"`
	if _, ok := ParsePastedFiles(text2, stat); ok {
		t.Fatalf("paste with a missing path must be rejected: %q", text2)
	}
}

func TestParsePastedFilesUnix(t *testing.T) {
	a := "/home/u/my file.txt"
	b := "/home/u/another file.md"
	stat := fakeStat(a, b)

	text := `/home/u/my\ file.txt /home/u/another\ file.md`
	got, ok := ParsePastedFiles(text, stat)
	if !ok {
		t.Fatalf("Unix escaped paste should parse: %q", text)
	}
	if want := []string{a, b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}

	// 单行路径（无转义无引号）也按逐行规则识别。
	if got, ok := ParsePastedFiles(a, stat); !ok || len(got) != 1 || got[0] != a {
		t.Fatalf("single path = %#v, ok=%v", got, ok)
	}
	// 普通单词即使恰好与文件名相同也不改写（无路径特征）。
	wordStat := fakeStat("hello")
	if _, ok := ParsePastedFiles("hello", wordStat); ok {
		t.Fatal("a bare word must not be treated as a pasted file")
	}
}

func TestParsePastedFilesRioNull(t *testing.T) {
	a := "/data/文件 一.txt"
	b := "/data/文件 二.txt"
	stat := fakeStat(a, b)

	// Rio 在 Windows 上插入 NUL 作为分隔；NUL + 引号组合必须仍能解析。
	text := `"` + a + `"\x00"` + b + `"\x00`
	text = strings.ReplaceAll(text, `\x00`, "\x00")
	got, ok := ParsePastedFiles(text, stat)
	if !ok {
		t.Fatalf("Rio NUL paste should parse: %q", text)
	}
	if want := []string{a, b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestPastedFilesToMentions(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "proj")
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	p := filepath.Join(root, "my file.txt")
	got := PastedFilesToMentions(root, []string{p})
	want := `@"my file.txt"`
	if got != want {
		t.Fatalf("mention = %q, want %q", got, want)
	}
}
