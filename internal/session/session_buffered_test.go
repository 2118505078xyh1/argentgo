package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// readFileLines 读取文件并按行切分，忽略空行。用于直接观察磁盘上 JSONL 的字节。
func readFileLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	raw := bytes.Split(data, []byte("\n"))
	lines := make([][]byte, 0, len(raw))
	for _, line := range raw {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// assertLinearChain 断言按文件顺序排列的记录通过 parent 首尾相连成一条链。
func assertLinearChain(t *testing.T, records []Record) {
	t.Helper()
	if len(records) == 0 {
		t.Fatal("empty records")
	}
	for i, record := range records {
		// Records() 会在最前面合成一条空 id 的 schema 元数据记录，
		// 它不是 id/parent 树的一部分，因此跳过对它的非空 id 检查。
		if i > 0 && record.ID == "" {
			t.Errorf("record %d has empty id", i)
		}
		if i == 0 {
			if record.Parent != "" {
				t.Errorf("first record parent = %q, want empty", record.Parent)
			}
			continue
		}
		if record.Parent != records[i-1].ID {
			t.Errorf("record %d parent = %q, want %q", i, record.Parent, records[i-1].ID)
		}
	}
}

// recordShape 提取记录中与时间戳无关的结构，便于跨会话比较内容。
func recordShape(record Record) string {
	return fmt.Sprintf("%s|%s|%s|%s", record.ID, record.Parent, record.Kind, string(record.Data))
}

func recordShapes(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, recordShape(record))
	}
	return out
}

// TestBufferedWriteDefaultPathUnchanged 验证默认（未开启缓冲）时每次追加都立即落盘，
// pending 始终为空，Flush 不改变文件字节，JSONL 结构与逐行写完全一致。
func TestBufferedWriteDefaultPathUnchanged(t *testing.T) {
	for _, count := range []int{1, 5, 20} {
		t.Run(fmt.Sprintf("appends=%d", count), func(t *testing.T) {
			session := newTestSession(t, t.TempDir(), "default")
			path := session.path()

			if session.BufferedWrite() {
				t.Fatal("buffered write must be off by default")
			}
			for i := 0; i < count; i++ {
				if err := session.Append(KindUser, fmt.Sprintf("msg-%d", i)); err != nil {
					t.Fatalf("Append(%d): %v", i, err)
				}
				// 默认路径必须逐行即时写入：磁盘上的行数随每次追加增长。
				if got := len(readFileLines(t, path)); got != i+2 { // 1 条 header + i+1 条
					t.Fatalf("after append %d lines = %d, want %d", i, got, i+2)
				}
				if got := session.PendingBytes(); got != 0 {
					t.Fatalf("pending = %d, want 0 on default path", got)
				}
			}

			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if err := session.Flush(); err != nil {
				t.Fatalf("Flush on default path: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("Flush changed bytes on the default path")
			}
			if !bytes.HasSuffix(after, []byte("\n")) {
				t.Fatal("default JSONL must end with a newline")
			}

			records, err := session.Records()
			if err != nil {
				t.Fatalf("Records: %v", err)
			}
			// Records() 会在开头合成一条 schema 元数据记录。
			if len(records) != count+2 {
				t.Fatalf("records = %d, want %d", len(records), count+2)
			}
			assertLinearChain(t, records)
			for _, line := range readFileLines(t, path) {
				var record Record
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatalf("line is not valid JSON: %s: %v", line, err)
				}
			}
		})
	}
}

// TestBufferedWriteContentMatchesDefault 验证 opt-in 批量写刷新后的内容与默认逐行写
// 在忽略时间戳后逐字段一致，且仍是一行一条、parent 相连的 JSONL。
func TestBufferedWriteContentMatchesDefault(t *testing.T) {
	payloads := []string{"alpha", "beta", "gamma", "delta"}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", enabled), func(t *testing.T) {
			control := newTestSession(t, t.TempDir(), "control")
			buffered := newTestSession(t, t.TempDir(), "buffered")
			if enabled {
				if err := buffered.SetBufferedWrite(true); err != nil {
					t.Fatalf("SetBufferedWrite: %v", err)
				}
			}

			for i, payload := range payloads {
				for _, s := range []*Session{control, buffered} {
					if err := s.Append(KindAssistant, payload); err != nil {
						t.Fatalf("Append(%d): %v", i, err)
					}
				}
			}

			if enabled {
				// 刷新前：磁盘上只有 header，暂存数据尚未落盘。
				if got := len(readFileLines(t, buffered.path())); got != 1 {
					t.Fatalf("lines before Flush = %d, want 1", got)
				}
				if buffered.PendingBytes() == 0 {
					t.Fatal("pending must be non-empty before Flush in buffered mode")
				}
				if err := buffered.Flush(); err != nil {
					t.Fatalf("Flush: %v", err)
				}
				if buffered.PendingBytes() != 0 {
					t.Fatal("pending must be empty after Flush")
				}
			}

			want, err := control.Records()
			if err != nil {
				t.Fatalf("control Records: %v", err)
			}
			got, err := buffered.Records()
			if err != nil {
				t.Fatalf("buffered Records: %v", err)
			}
			// 前两条是合成 schema 记录和会话各自的 header（标题/时间不同），
			// 真正要一致的是追加的记录：比较时跳过它们。
			if len(got) < 2 || len(want) < 2 {
				t.Fatalf("too few records: got %d, want %d", len(got), len(want))
			}
			if fmt.Sprint(recordShapes(got[2:])) != fmt.Sprint(recordShapes(want[2:])) {
				t.Fatalf("content differs:\n got = %v\nwant = %v", recordShapes(got[2:]), recordShapes(want[2:]))
			}
			assertLinearChain(t, got)

			lines := readFileLines(t, buffered.path())
			if len(lines) != len(got)-1 { // -1：schema 记录只存在于 Records() 视图中
				t.Fatalf("lines = %d, records = %d", len(lines), len(got))
			}
			data, err := os.ReadFile(buffered.path())
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !bytes.HasSuffix(data, []byte("\n")) {
				t.Fatal("buffered JSONL must end with a newline after Flush")
			}
		})
	}
}

// TestBufferedWriteConcurrentAppend 验证并发追加在默认和批量模式下都能生成一条
// 无断裂、无重复 id 的链，且所有载荷恰好出现一次。
func TestBufferedWriteConcurrentAppend(t *testing.T) {
	const goroutines, perGoroutine = 8, 25
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", enabled), func(t *testing.T) {
			session := newTestSession(t, t.TempDir(), "concurrent")
			if enabled {
				if err := session.SetBufferedWrite(true); err != nil {
					t.Fatalf("SetBufferedWrite: %v", err)
				}
			}

			var wg sync.WaitGroup
			errs := make(chan error, goroutines)
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					for i := 0; i < perGoroutine; i++ {
						if err := session.Append(KindUser, fmt.Sprintf("g%d-i%d", g, i)); err != nil {
							errs <- err
							return
						}
					}
				}(g)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatalf("concurrent Append: %v", err)
			}
			if enabled {
				if err := session.Flush(); err != nil {
					t.Fatalf("Flush: %v", err)
				}
			}

			records, err := session.Records()
			if err != nil {
				t.Fatalf("Records: %v", err)
			}
			want := 2 + goroutines*perGoroutine // 含合成 schema 记录和 header
			if len(records) != want {
				t.Fatalf("records = %d, want %d", len(records), want)
			}
			assertLinearChain(t, records)

			seen := make(map[string]int, goroutines*perGoroutine)
			for _, record := range records[2:] { // 跳过 schema 与 header
				seen[string(record.Data)]++
			}
			for g := 0; g < goroutines; g++ {
				for i := 0; i < perGoroutine; i++ {
					key := fmt.Sprintf("%q", fmt.Sprintf("g%d-i%d", g, i))
					if seen[key] != 1 {
						t.Fatalf("payload %s appeared %d times, want 1", key, seen[key])
					}
				}
			}
		})
	}
}

// TestBufferedWriteAfterFork 验证 fork 之后仍可继续追加：无论 fork 来源是默认模式
// 还是带暂存数据的批量模式，新会话都能保持 id/parent 链并正常落盘。
func TestBufferedWriteAfterFork(t *testing.T) {
	cases := []struct {
		name           string
		sourceBuffered bool // fork 来源使用批量写（fork 前应自动刷新暂存数据）
		forkBuffered   bool // 新会话使用批量写
	}{
		{"default-to-default", false, false},
		{"default-to-buffered", false, true},
		{"buffered-to-default", true, false},
		{"buffered-to-buffered", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := newTestSession(t, t.TempDir(), "source")
			if tc.sourceBuffered {
				if err := source.SetBufferedWrite(true); err != nil {
					t.Fatalf("SetBufferedWrite(source): %v", err)
				}
			}
			if err := source.Append(KindUser, "root"); err != nil {
				t.Fatalf("Append(root): %v", err)
			}
			branchAt, err := source.AppendRecord(KindAssistant, "branch point")
			if err != nil {
				t.Fatalf("AppendRecord(branch): %v", err)
			}
			if err := source.Append(KindUser, "not in fork"); err != nil {
				t.Fatalf("Append(not in fork): %v", err)
			}

			fork, err := source.Fork(branchAt.ID)
			if err != nil {
				t.Fatalf("Fork: %v", err)
			}
			// Fork 可能自动带上标题记录，因此追加的父节点是 fork 当前的叶子，
			// 而不是分叉点本身。
			beforeAppend, err := fork.Records()
			if err != nil {
				t.Fatalf("fork Records before append: %v", err)
			}
			expectedParent := beforeAppend[len(beforeAppend)-1].ID

			if tc.forkBuffered {
				if err := fork.SetBufferedWrite(true); err != nil {
					t.Fatalf("SetBufferedWrite(fork): %v", err)
				}
			}
			added, err := fork.AppendRecord(KindAssistant, "after fork")
			if err != nil {
				t.Fatalf("AppendRecord(fork): %v", err)
			}
			if added.Parent != expectedParent {
				t.Fatalf("fork append parent = %q, want %q", added.Parent, expectedParent)
			}
			if tc.forkBuffered {
				if err := fork.Flush(); err != nil {
					t.Fatalf("Flush(fork): %v", err)
				}
			}

			records, err := fork.Records()
			if err != nil {
				t.Fatalf("fork Records: %v", err)
			}
			last := records[len(records)-1]
			if last.ID != added.ID || last.Parent != expectedParent {
				t.Fatalf("fork last record = %+v, want id %q parent %q", last, added.ID, expectedParent)
			}
			assertLinearChain(t, records)
			for _, record := range records {
				if strings.Contains(string(record.Data), "not in fork") {
					t.Fatal("fork contains record beyond the branch point")
				}
			}
			// 源会话不受影响，且仍可追加。
			if err := source.Append(KindUser, "source still writable"); err != nil {
				t.Fatalf("Append(source after fork): %v", err)
			}
			if tc.sourceBuffered {
				if err := source.Flush(); err != nil {
					t.Fatalf("Flush(source): %v", err)
				}
			}
			sourceRecords, err := source.Records()
			if err != nil {
				t.Fatalf("source Records: %v", err)
			}
			assertLinearChain(t, sourceRecords)
		})
	}
}
