package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeScanFixture 写入一个测试文件并返回路径。
func writeScanFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanFileLinesSmallAndLargeAgree(t *testing.T) {
	// 同一份内容分别以"小文件单次读"和"大文件流式"两条路径扫描，
	// 行号、行文本、CRLF 归一必须逐字节一致。
	content := "alpha\nbeta\r\ngamma\n\nlast"
	for _, tc := range []struct {
		name       string
		forceLarge bool
	}{
		{name: "small"},
		{name: "large-via-reader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeScanFixture(t, "f.txt", content)
			var got []string
			var st scanStats
			visit := func(l scanLine) bool {
				got = append(got, fmt.Sprintf("%d:%s", l.no, l.text))
				return true
			}
			if tc.forceLarge {
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				scanLargeFile(f, visit, &st)
			} else {
				st = scanSearchFileLines(path, visit)
			}
			want := []string{"1:alpha", "2:beta", "3:gamma", "4:", "5:last"}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("行 = %q，期望 %q", got, want)
			}
			if st.binary || st.longLines != 0 || st.linesTotal != len(want) {
				t.Fatalf("stats = %+v", st)
			}
		})
	}
}

func TestScanFileLinesSkipsBinaryAndLongLines(t *testing.T) {
	binary := writeScanFixture(t, "blob.bin", "hello\x00world\nstill here\n")
	var seen []int
	st := scanSearchFileLines(binary, func(l scanLine) bool {
		seen = append(seen, l.no)
		return true
	})
	if !st.binary || len(seen) != 0 {
		t.Fatalf("二进制应被跳过：st=%+v seen=%v", st, seen)
	}

	// 超过单行上限的行整行跳过，但行号保持连续。
	long := strings.Repeat("x", searchScanLineLimitBytes+16) + "\n" + "tail\n"
	path := writeScanFixture(t, "long.txt", long)
	var lines []scanLine
	st = scanSearchFileLines(path, func(l scanLine) bool {
		lines = append(lines, l)
		return true
	})
	if st.longLines != 1 {
		t.Fatalf("longLines = %d，期望 1", st.longLines)
	}
	if len(lines) != 1 || lines[0].no != 2 || string(lines[0].text) != "tail" {
		t.Fatalf("剩余行 = %+v", lines)
	}
}

func TestScanFileLinesEarlyStop(t *testing.T) {
	path := writeScanFixture(t, "f.txt", "a\nb\nc\nd\n")
	count := 0
	scanSearchFileLines(path, func(scanLine) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("提前停止后回调次数 = %d，期望 1", count)
	}
}

func TestScanFileLinesKeepsHeapBounded(t *testing.T) {
	// 8 MiB 文件（超过 4 MiB 阈值，必走流式路径），每行约 1 KiB。
	// 扫描期间的总分配应远低于 16 MiB，证明没有整文件驻留或全量切片拷贝。
	line := strings.Repeat("a", 1000) + "\n"
	content := strings.Repeat(line, 8*1024)
	path := writeScanFixture(t, "big.txt", content)
	if info, err := os.Stat(path); err != nil || info.Size() <= searchScanMaxFileBytes {
		t.Fatalf("夹具大小未超过阈值：%v %+v", err, info)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	lines := 0
	st := scanSearchFileLines(path, func(scanLine) bool {
		lines++
		return true
	})
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	if alloc >= 16<<20 {
		t.Fatalf("扫描 8MiB 文件分配 %d 字节，期望 <16MiB", alloc)
	}
	if lines != 8*1024 || st.bytes < searchScanMaxFileBytes {
		t.Fatalf("扫描结果异常：lines=%d stats=%+v", lines, st)
	}
}

func TestLineTextCapsColumnsWithoutBreakingUTF8(t *testing.T) {
	line := []byte(strings.Repeat("中文", searchScanMaxColumns) + "tail")
	got := lineText(line)
	if !strings.HasSuffix(got, lineTruncatedMarker) {
		t.Fatalf("超长行应带截断标记：%q", got[len(got)-20:])
	}
	if n := len([]rune(strings.TrimSuffix(got, lineTruncatedMarker))); n != searchScanMaxColumns {
		t.Fatalf("保留 rune 数 = %d，期望 %d", n, searchScanMaxColumns)
	}
}
