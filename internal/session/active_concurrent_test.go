package session

import (
	"fmt"
	"sync"
	"testing"
)

// TestActiveConcurrentAppendAndRead 检验 s.active 的数据竞争：多个
// goroutine 执行追加（在 mu 下写 s.active），同时其他 goroutine 通过
// Records/Messages/Path 读取（它们必须通过 activeSnapshot 在 mu 下读 s.active）。
//
// 本机没有 cgo，因此无法运行竞态检测器。所以该测试改为
// 断言逻辑不变量：不 panic、最终记录数准确，
// 且每个 id/parent 链接完好。在带 cgo 的主机上用 -race 运行
// 还能额外捕获最初那个未同步的读取。
func TestActiveConcurrentAppendAndRead(t *testing.T) {
	const (
		writers   = 8
		perWriter = 20
		readers   = 6
	)

	s := newTestSession(t, t.TempDir(), "concurrent")

	// assertChain 检查并发读取者可能观察到的快照。由于
	// 追加由 mu 串行化且只会延长单条链，因此读取者看到的
	// 任何快照都是干净的前缀：id 唯一，且每个
	// 非根节点的 parent 都能在快照内解析。
	assertChain := func(records []Record) {
		ids := make(map[string]bool, len(records))
		for _, r := range records {
			if ids[r.ID] {
				t.Errorf("duplicate id %q in concurrent read", r.ID)
			}
			ids[r.ID] = true
		}
		for _, r := range records {
			if r.Parent != "" && !ids[r.Parent] {
				t.Errorf("record %q parent %q missing from concurrent read", r.ID, r.Parent)
			}
		}
	}

	stop := make(chan struct{})
	var readersWG sync.WaitGroup
	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				records, err := s.Records()
				if err != nil {
					t.Errorf("concurrent Records: %v", err)
					return
				}
				records = withoutSchemaHeader(records)
				assertChain(records)
				if len(records) > 0 {
					leaf := records[len(records)-1].ID
					path, err := s.Path(leaf)
					if err != nil {
						t.Errorf("concurrent Path(%q): %v", leaf, err)
						return
					}
					if len(path) != len(records) {
						t.Errorf("concurrent Path returned %d records, snapshot has %d", len(path), len(records))
					}
				}
				// Messages 通过 activePath -> activeSnapshot 遍历树。
				for _, entry := range s.Messages() {
					if entry.Role == "" {
						t.Error("concurrent Messages returned an entry with no role")
						break
					}
				}
			}
		}()
	}

	var writersWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writersWG.Add(1)
		go func(w int) {
			defer writersWG.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := s.AppendRecord(KindMessage, map[string]string{
					"role":    "user",
					"content": fmt.Sprintf("w%d-%d", w, i),
				}); err != nil {
					t.Errorf("concurrent AppendRecord: %v", err)
					return
				}
			}
		}(w)
	}
	writersWG.Wait()
	close(stop)
	readersWG.Wait()

	// 所有写入方完成后的权威检查：数量准确，且从 header 到活动叶节点
	// 是一条完整不间断的 id 链。
	records, err := s.Records()
	if err != nil {
		t.Fatalf("final Records: %v", err)
	}
	records = withoutSchemaHeader(records)
	want := 1 + writers*perWriter // header + 追加的记录
	if len(records) != want {
		t.Fatalf("final records = %d, want %d", len(records), want)
	}
	assertChain(records)
	if records[0].Parent != "" {
		t.Errorf("root parent = %q, want empty", records[0].Parent)
	}
	for i := 1; i < len(records); i++ {
		if records[i].Parent != records[i-1].ID {
			t.Fatalf("record %d parent = %q, want %q (id chain broken)", i, records[i].Parent, records[i-1].ID)
		}
	}

	path, err := s.Path(records[len(records)-1].ID)
	if err != nil {
		t.Fatalf("final Path: %v", err)
	}
	if len(path) != len(records) {
		t.Fatalf("final Path length = %d, want %d", len(path), len(records))
	}
	for i := range path {
		if path[i].ID != records[i].ID {
			t.Fatalf("final Path record %d id = %q, want %q", i, path[i].ID, records[i].ID)
		}
	}

	if msgs := s.Messages(); len(msgs) != writers*perWriter {
		t.Fatalf("final Messages = %d, want %d", len(msgs), writers*perWriter)
	}
}
