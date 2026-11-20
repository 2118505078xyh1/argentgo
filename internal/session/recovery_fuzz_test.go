package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// FuzzSessionRecordRoundtrip 把任意载荷送入 AppendRecord，
// 再送入恢复时使用的宽松读取/修复路径。检查的属性有：
// 任何输入都不 panic、修复后幸存的每一行都是
// 格式良好的 JSON 记录，且已知的哨兵记录绝不会因
// 残缺或格式错误的尾部而丢失。严重损坏是可接受的（不 panic）
// 结果，并由 ValidateAndRepair/OpenSession 如实报告。

type sessionFuzzSeed struct {
	seed    int64
	payload string
	tail    string
}

var sessionFuzzSeeds = []sessionFuzzSeed{
	// 格式良好的对象载荷，干净的尾部。
	{1, `{"role":"user","content":"你好🙂"}`, ""},
	// 截断的载荷和残缺的最后一行：载荷回退为 JSON
	// 字符串，尾部由残缺尾部修复丢弃。
	{2, `{"content":"trailed`, `{"id":"tor`},
	// 有效的非对象 JSON 载荷加上一个裸换行尾部。
	{3, `42`, "\n"},
	// 嵌套载荷以及作为原始 JSON 追加的一条有效额外记录。
	{4, `{"nested":{"x":[1,2,3]}}`, `{"kind":"entry"}`},
	// 空载荷（存为 null）和非 JSON 垃圾尾部。
	{5, "", "garbage\n"},
	// 组合字符和一条结构上有效的额外尾部记录。
	{6, `{"role":"assistant","content":"combining e\u0301"}`, `{"id":"x","kind":"message","data":null}`},
	// 非最后一条物理行上的语法错误属于严重损坏。
	{7, `{"role":"tool","output":"[exit 1] failed"}`, "{\"kind\":\"message\"}\n{oops}\n{\"kind\":\"user\"}"},
	// schema 错误（kind 不是字符串）属于严重损坏。
	{8, `{"id":"x","kind":"message"}`, `{"kind":123}`},
}

func FuzzSessionRecordRoundtrip(f *testing.F) {
	for _, item := range sessionFuzzSeeds {
		f.Add(item.seed, item.payload, item.tail)
	}
	f.Fuzz(sessionRoundtripTarget)
}

func sessionRoundtripTarget(t *testing.T, seed int64, payload, tail string) {
	t.Helper()
	// 让每个 fuzz 用例保持小且有界；种子语料本身已远
	// 低于这个限制。
	const maxFuzzBytes = 4096
	if len(payload) > maxFuzzBytes || len(tail) > maxFuzzBytes ||
		len(payload)+len(tail) > maxFuzzBytes {
		t.Skip("fuzz input too large")
	}

	dir := t.TempDir()
	sess := newTestSession(t, dir, "fuzz")

	// 确定性的短前缀同时覆盖多记录追加/重建路径
	// 和单个 fuzz 载荷。
	rng := rand.New(rand.NewSource(seed))
	prefixKinds := []string{KindUser, KindAssistant, KindTool, KindMessage}
	prefixCount := rng.Intn(4)
	for i := 0; i < prefixCount; i++ {
		kind := prefixKinds[rng.Intn(len(prefixKinds))]
		if _, err := sess.AppendRecord(kind, map[string]any{
			"content": fmt.Sprintf("prefix %d", i),
		}); err != nil {
			t.Fatalf("append prefix %d: %v", i, err)
		}
	}

	// 把任意字节送入 AppendRecord。无效 JSON 可能被拒绝
	// （且必须干净回滚）；唯一的硬性要求是不 panic。
	if json.Valid([]byte(payload)) {
		_, _ = sess.AppendRecord(KindMessage, json.RawMessage(payload))
	} else {
		_, _ = sess.AppendRecord(KindMessage, payload)
	}

	// 这条记录是我们的见证：宽松修复在处理损坏尾部时
	// 绝不会丢弃有效历史。
	if _, err := sess.AppendRecord(KindUser, map[string]any{"content": "sentinel"}); err != nil {
		t.Fatalf("append sentinel: %v", err)
	}

	records, err := sess.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	beforeTail := len(records)

	path := sess.path()
	if tail != "" {
		fd, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatalf("open tail: %v", err)
		}
		if _, err := fd.WriteString(tail); err != nil {
			_ = fd.Close()
			t.Fatalf("write tail: %v", err)
		}
		if err := fd.Close(); err != nil {
			t.Fatalf("close tail: %v", err)
		}
	}

	// 校验必须对每一行分类而不 panic。只有严重
	// 损坏才算错误；残缺的最后一行只报告而不拒绝。
	_, reportErr := ValidateAndRepair(path, false)
	if reportErr != nil && !errors.Is(reportErr, ErrHardCorruption) {
		t.Fatalf("ValidateAndRepair: %v", reportErr)
	}

	reopened, err := OpenSession(dir, sess.ID())
	if err != nil {
		if !errors.Is(err, ErrHardCorruption) {
			t.Fatalf("OpenSession: %v", err)
		}
		return // 严重损坏是可接受的、不 panic 的结果
	}

	// 修复后每一条幸存的物理行都是格式良好的记录。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repaired file: %v", err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Fatalf("malformed line %d survived repair: %q", i+1, line)
		}
		if kind, detail := decodeLine(line); kind != DecodeOK {
			t.Fatalf("line %d classified as %v after repair: %s", i+1, kind, detail)
		}
	}

	got, err := reopened.Records()
	if err != nil {
		t.Fatalf("Records after reopen: %v", err)
	}
	for _, record := range withoutSchemaHeader(got) {
		if record.ID == "" {
			t.Fatal("record with an empty id survived reopen")
		}
		// 记录合法地可以完全省略 "data"（decodeLine 只
		// 要求 kind 非空）；存在时必须是有效 JSON。
		if len(record.Data) != 0 && !json.Valid(record.Data) {
			t.Fatalf("record %s has malformed data: %s", record.ID, record.Data)
		}
	}

	found := false
	for _, record := range got {
		if record.Kind == KindUser && strings.Contains(string(record.Data), "sentinel") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sentinel record lost: %d records after reopen, %d before tail",
			len(got), beforeTail)
	}
}

// TestFuzzSessionRecordRoundtripSeeds 把 fuzz 种子语料作为普通的
// 确定性测试运行，使回归用例始终被 go test 覆盖。
func TestFuzzSessionRecordRoundtripSeeds(t *testing.T) {
	for _, item := range sessionFuzzSeeds {
		t.Run(fmt.Sprintf("seed=%d", item.seed), func(t *testing.T) {
			sessionRoundtripTarget(t, item.seed, item.payload, item.tail)
		})
	}
}
