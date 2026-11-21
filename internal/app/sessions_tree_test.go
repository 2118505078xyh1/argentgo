package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// writeTreeSession 把一组受控记录写成一个会话 JSONL 文件，便于构造分叉树。
func writeTreeSession(t *testing.T, dir, id string, records []session.Record) {
	t.Helper()
	var buf bytes.Buffer
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode record: %v", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
}

func treeRecord(id, parent, kind, data string) session.Record {
	return session.Record{
		ID:     id,
		Parent: parent,
		Kind:   kind,
		TS:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Data:   json.RawMessage(data),
	}
}

// TestSessionStoreBranchesMapsTree 验证 app 适配器把 session 分支正确映射为
// RPC wire 类型：过滤 schema 头、保持顺序/字段，并按同父子节点数派生 Fork。
func TestSessionStoreBranchesMapsTree(t *testing.T) {
	dir := t.TempDir()
	writeTreeSession(t, dir, "s1", []session.Record{
		treeRecord("h", "", session.KindHeader, `{"title":"root"}`),
		treeRecord("sch", "h", session.KindSchema, `{"version":1}`), // 必须被过滤
		treeRecord("n2", "h", session.KindUser, `"hello"`),
		treeRecord("n3", "n2", session.KindAssistant, `"first"`), // 分叉子 1
		treeRecord("n4", "n2", session.KindUser, `"second"`),     // 分叉子 2，活动叶
	})

	store := &sessionStore{dir: dir}
	branches, err := store.Branches("s1")
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(branches) != 4 {
		t.Fatalf("branches = %+v, want 4 (schema 被过滤)", branches)
	}

	wantIDs := []string{"h", "n2", "n3", "n4"}
	for i, want := range wantIDs {
		if branches[i].ID != want {
			t.Fatalf("branch[%d].ID = %q, want %q", i, branches[i].ID, want)
		}
	}

	// h: 根，深度 0；其非 schema 子节点只有 n2，因此不是分叉点。
	if branches[0].Parent != "" || branches[0].Kind != session.KindHeader || branches[0].Depth != 0 || branches[0].Fork {
		t.Fatalf("branch h = %+v", branches[0])
	}
	// n2: 父为 h，深度 1，单子节点，不是分叉点。
	if branches[1].Parent != "h" || branches[1].Depth != 1 || branches[1].Fork {
		t.Fatalf("branch n2 = %+v", branches[1])
	}
	// n3/n4 同为 n2 的子节点，必须都标记为 Fork；n4 是最后一条记录，故 Active。
	if !branches[2].Fork || branches[2].Parent != "n2" || branches[2].Active {
		t.Fatalf("branch n3 = %+v", branches[2])
	}
	if !branches[3].Fork || branches[3].Parent != "n2" || !branches[3].Active {
		t.Fatalf("branch n4 = %+v", branches[3])
	}
	// 消息分支应带有从内容派生的标签。
	if branches[1].Label == "" || branches[3].Label == "" {
		t.Fatalf("分支标签为空: %+v", branches)
	}
}

// TestSessionStoreBranchesMissingSession 验证未知 id 返回错误，而不是空树。
func TestSessionStoreBranchesMissingSession(t *testing.T) {
	store := &sessionStore{dir: t.TempDir()}
	if _, err := store.Branches("missing"); err == nil {
		t.Fatal("Branches on missing session should fail")
	}
}
