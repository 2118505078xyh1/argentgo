package session

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

// TestForkDoesNotAppendLineageRecord 锁定"默认字节不变"不变量：Fork 逐字复制
// 到分叉点的记录，不追加 forkLineagePayload 那样的血缘记录。若将来要引入
// 血缘记录，必须显式 opt-in，并在本测试之外另立开关，否则这里会先失败。
func TestForkDoesNotAppendLineageRecord(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "lineage wire")
	for _, text := range []string{"one", "two"} {
		if err := sess.Append(KindMessage, text); err != nil {
			t.Fatalf("Append(%q): %v", text, err)
		}
	}
	all, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	forkPoint := all[len(all)-1].ID

	before, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatal(err)
	}
	fork, err := sess.Fork(forkPoint)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	after, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("源会话在 Fork 后字节发生了变化")
	}
	if got := len(fork.Messages()); got != 2 {
		t.Fatalf("fork messages = %d, want 2", got)
	}

	forkRecords, err := fork.Records()
	if err != nil {
		t.Fatal(err)
	}
	// 只检查载荷 Data：记录信封本身带 "ts" 时间戳字段，不能在整行字节上搜 "ts"。
	for _, record := range forkRecords {
		for _, key := range []string{forkLineageParentKey, forkLineageAtRecordKey, forkLineageTsKey} {
			if bytes.Contains(record.Data, []byte(`"`+key+`"`)) {
				t.Fatalf("Fork 写入了血缘键 %q，改变了默认字节：%s", key, record.Data)
			}
		}
	}
}

// TestSelectForChildrenIsNonMutatingSelector 锁定 SelectForChildren 的可选语义：
// 它是纯选择器，只挑出带保留标记的记录，不修改入参、不追加字节；无标记的默认
// 记录不会被选中，因此默认子级上下文保持原样。
func TestSelectForChildrenIsNonMutatingSelector(t *testing.T) {
	a := markedRecord("a", RetainAlways)
	b := plainRecord("b")
	c := markedRecord("c", RetainInChildren)
	records := []Record{a, b, c}
	snapshot := append([]Record(nil), records...)

	got := SelectForChildren(records)
	if want := []string{"a", "c"}; !reflect.DeepEqual(recordIDs(got), want) {
		t.Fatalf("SelectForChildren = %v, want %v", recordIDs(got), want)
	}
	if !reflect.DeepEqual(records, snapshot) {
		t.Fatal("SelectForChildren 修改了入参切片")
	}
	if !reflect.DeepEqual(got, []Record{a, c}) {
		t.Fatal("SelectForChildren 返回的记录字节被改写")
	}
}
