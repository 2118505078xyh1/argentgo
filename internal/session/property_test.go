package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// 这些是针对持久会话引擎的确定性属性/往返测试。
// 它们刻意有界（小 N、少量固定种子），使整个文件运行快速，
// 同时仍能探索看似任意的序列。
// 这里不触碰任何生产代码：下面所有内容仅通过临时目录驱动公开 API
// 和包内 API。

var propRunePool = []rune("abcXYZ0123 界世界文🙂🚀e\u0301\u0300क")

// propText 用 ASCII、中文、emoji 和组合字符构造有界字符串。
// 字符池中不含凭据形状的子串，因此 security.Redact 不会改动
// 进度/尝试文本，下面的往返比较精确字节。
func propText(rng *rand.Rand, maxRunes int) string {
	n := rng.Intn(maxRunes + 1)
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = propRunePool[rng.Intn(len(propRunePool))]
	}
	return string(runes)
}

func propBytes(rng *rand.Rand, maxLen int) []byte {
	data := make([]byte, rng.Intn(maxLen)+1)
	for i := range data {
		// 定期包含 NUL 和高位字节，使清单携带真实的
		// 二进制载荷（由 encoding/json 进行 base64 编码），而不只是文本。
		data[i] = byte(rng.Intn(256))
	}
	return data
}

func propWriteFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func propReadFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

// ---------------------------------------------------------------------------
// 属性 1：追加 N 条带有有效 parent 链接的随机记录，重新打开，验证。
// ---------------------------------------------------------------------------

func TestPropertyAppendRoundtripPreservesTree(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 7, 42} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			propAppendRoundtrip(t, seed, 40)
		})
	}
}

func propAppendRoundtrip(t *testing.T, seed int64, n int) {
	t.Helper()
	dir := t.TempDir()
	sess := newTestSession(t, dir, fmt.Sprintf("prop-%d", seed))
	rng := rand.New(rand.NewSource(seed))

	kinds := []string{
		KindMessage, KindUser, KindAssistant, KindTool,
		KindCheckpoint, "attempt", "progress",
	}
	for i := 0; i < n; i++ {
		kind := kinds[rng.Intn(len(kinds))]
		var err error
		switch kind {
		case "progress":
			err = sess.AppendProgress(Progress{Turn: i + 1, AssistantText: propText(rng, 24)})
		case "attempt":
			_, err = sess.AppendAttempt(Attempt{Reason: AttemptFailed, Message: propText(rng, 24)})
		case KindCheckpoint:
			cp := Checkpoint{
				Prompt: propText(rng, 16),
				Files:  []CheckpointFile{{Path: "f.txt", SHA256: hashBytes([]byte(propText(rng, 8)))}},
			}
			_, err = sess.AppendRecord(KindCheckpoint, cp)
		case KindTool:
			payload := map[string]any{
				"id":     fmt.Sprintf("c%d", i),
				"name":   "prop.tool",
				"output": propText(rng, 30),
			}
			_, err = sess.AppendRecord(KindTool, payload)
		default:
			payload := map[string]any{"content": propText(rng, 30)}
			_, err = sess.AppendRecord(kind, payload)
		}
		if err != nil {
			t.Fatalf("append %d (%s): %v", i, kind, err)
		}
	}

	expected, err := sess.Records()
	if err != nil {
		t.Fatalf("Records before reopen: %v", err)
	}
	expected = withoutSchemaHeader(expected)
	if len(expected) != n+1 { // header + n 条追加记录
		t.Fatalf("record count = %d, want %d", len(expected), n+1)
	}
	active := sess.activeSnapshot()
	id := sess.ID()

	reopened, err := OpenSession(dir, id)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	got, err := reopened.Records()
	if err != nil {
		t.Fatalf("Records after reopen: %v", err)
	}
	got = withoutSchemaHeader(got)
	if len(got) != len(expected) {
		t.Fatalf("record count after reopen = %d, want %d", len(got), len(expected))
	}

	seen := make(map[string]struct{}, len(got))
	for i, record := range got {
		if record.ID == "" {
			t.Fatalf("record %d has an empty id", i)
		}
		if _, dup := seen[record.ID]; dup {
			t.Fatalf("duplicate record id %q at index %d", record.ID, i)
		}
		seen[record.ID] = struct{}{}
		want := expected[i]
		if record.ID != want.ID || record.Kind != want.Kind ||
			record.Parent != want.Parent || !bytes.Equal(record.Data, want.Data) ||
			!record.TS.Equal(want.TS) {
			t.Fatalf("record %d changed after reopen:\n got %+v\nwant %+v", i, record, want)
		}
		if i > 0 && record.Parent != got[i-1].ID {
			t.Fatalf("broken parent link at %d: %q has parent %q, want %q",
				i, record.ID, record.Parent, got[i-1].ID)
		}
	}
	if reopened.activeSnapshot() != active || active != got[len(got)-1].ID {
		t.Fatalf("active leaf changed: before=%q after=%q last=%q",
			active, reopened.activeSnapshot(), got[len(got)-1].ID)
	}

	branches, err := Branches(filepath.Join(dir, id+fileExt))
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	branches = withoutSchemaBranches(branches)
	if len(branches) != len(got) {
		t.Fatalf("branches = %d, want %d", len(branches), len(got))
	}
	activeBranches := 0
	for i, branch := range branches {
		if branch.Active {
			activeBranches++
			if i != len(branches)-1 || branch.ID != active {
				t.Fatalf("wrong active branch: %+v", branch)
			}
		}
	}
	if activeBranches != 1 {
		t.Fatalf("active branches = %d, want 1", activeBranches)
	}
}

// ---------------------------------------------------------------------------
// 属性 2：检查点捕获/撤销往返以及冲突拒绝。
// ---------------------------------------------------------------------------

var propCheckpointPaths = []string{
	"a.txt",
	"b.bin",
	"src/文件.go",
	"docs/目录/数据.bin",
	"emoji/🙂.txt",
	"nested/space name.dat",
}

type propFileSpec struct {
	path   string
	before []byte
	after  []byte
	absent bool
}

func TestPropertyCheckpointCaptureUndoAndConflict(t *testing.T) {
	for _, seed := range []int64{11, 23, 37, 99} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			propCheckpointRoundtrip(t, seed)
		})
	}
}

func propCheckpointRoundtrip(t *testing.T, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	root := t.TempDir()

	count := 3 + rng.Intn(4)
	specs := make([]propFileSpec, 0, count)
	used := make(map[string]struct{}, count)
	for len(specs) < count {
		path := propCheckpointPaths[rng.Intn(len(propCheckpointPaths))]
		if _, exists := used[path]; exists {
			continue
		}
		used[path] = struct{}{}
		spec := propFileSpec{path: path, absent: rng.Intn(3) == 0}
		if !spec.absent {
			spec.before = propBytes(rng, 64)
		}
		spec.after = propBytes(rng, 64)
		if !spec.absent && bytes.Equal(spec.before, spec.after) {
			spec.after = append(append([]byte(nil), spec.after...), 0xAB)
		}
		specs = append(specs, spec)
	}

	for _, spec := range specs {
		if !spec.absent {
			propWriteFile(t, root, spec.path, spec.before)
		}
	}

	paths := make([]string, 0, count+1)
	for _, spec := range specs {
		paths = append(paths, spec.path)
	}
	// 重复的路径用于检验清单去重。
	paths = append(paths, specs[0].path)

	cp, err := CaptureCheckpoint(root, paths)
	if err != nil {
		t.Fatalf("CaptureCheckpoint: %v", err)
	}
	if len(cp.Files) != count {
		t.Fatalf("manifest size = %d, want %d (deduplicated)", len(cp.Files), count)
	}
	for i := 1; i < len(cp.Files); i++ {
		if cp.Files[i-1].Path > cp.Files[i].Path {
			t.Fatalf("manifest not sorted: %q before %q", cp.Files[i-1].Path, cp.Files[i].Path)
		}
	}
	byPath := make(map[string]CheckpointFile, len(cp.Files))
	for _, file := range cp.Files {
		byPath[file.Path] = file
	}
	for _, spec := range specs {
		file := byPath[spec.path]
		if spec.absent {
			if !file.Absent || len(file.Before) != 0 || file.SHA256 != "" {
				t.Fatalf("absent file recorded as present: %+v", file)
			}
			continue
		}
		if file.Absent || !bytes.Equal(file.Before, spec.before) ||
			file.SHA256 != hashBytes(spec.before) {
			t.Fatalf("pre-turn bytes/hash wrong for %s: %+v", spec.path, file)
		}
	}

	// 模拟变更轮次：创建不存在的文件，修改存在的文件。
	for _, spec := range specs {
		propWriteFile(t, root, spec.path, spec.after)
	}
	const extraPath = "outside-manifest.txt"
	propWriteFile(t, root, extraPath, []byte("untouched"))

	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatalf("CompleteCheckpoint: %v", err)
	}
	// byPath 持有完成之前的值副本；刷新它们，使
	// 轮次后断言能看到 AfterSHA256/AfterAbsent 字段。
	byPath = make(map[string]CheckpointFile, len(cp.Files))
	for _, file := range cp.Files {
		byPath[file.Path] = file
	}
	for _, spec := range specs {
		file := byPath[spec.path]
		if spec.absent {
			if file.AfterAbsent || file.AfterSHA256 == "" {
				t.Fatalf("created file not recorded in post-turn state: %+v", file)
			}
			continue
		}
		if file.AfterAbsent || file.AfterSHA256 != hashBytes(spec.after) {
			t.Fatalf("post-turn hash wrong for %s: %+v", spec.path, file)
		}
	}

	restored, err := UndoCheckpoint(root, cp)
	if err != nil {
		t.Fatalf("UndoCheckpoint: %v", err)
	}
	if len(restored) != count {
		t.Fatalf("restored = %v, want %d entries", restored, count)
	}
	for _, spec := range specs {
		if spec.absent {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(spec.path))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created file %s still exists after undo: %v", spec.path, err)
			}
			continue
		}
		if got := propReadFile(t, root, spec.path); !bytes.Equal(got, spec.before) {
			t.Fatalf("%s restored to %x, want %x", spec.path, got, spec.before)
		}
	}
	if got := propReadFile(t, root, extraPath); string(got) != "untouched" {
		t.Fatalf("out-of-manifest file was touched: %q", got)
	}

	// 检查点之后的手动编辑必须拒绝整个撤销，且不得
	// 部分还原任何其他清单文件。
	for _, spec := range specs {
		propWriteFile(t, root, spec.path, spec.after)
	}
	victimSpec := specs[rng.Intn(count)]
	victim := victimSpec.path
	edited := append(append([]byte(nil), victimSpec.after...), []byte("MANUAL-EDIT")...)
	propWriteFile(t, root, victim, edited)

	if _, err := UndoCheckpoint(root, cp); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("UndoCheckpoint conflict error = %v, want ErrCheckpointConflict", err)
	}
	for _, spec := range specs {
		if spec.path == victim {
			if got := propReadFile(t, root, victim); !bytes.Equal(got, edited) {
				t.Fatal("conflicting file was overwritten")
			}
			continue
		}
		if got := propReadFile(t, root, spec.path); !bytes.Equal(got, spec.after) {
			t.Fatalf("file %s was partially restored despite the conflict", spec.path)
		}
	}
}

// ---------------------------------------------------------------------------
// 属性 3：压缩切分安全性与证据保留。
// ---------------------------------------------------------------------------

type propMarkers struct {
	file string
	fail string
	step string
}

// propAssistantCalls 构建携带真实 tool_calls 载荷的 assistant Entry，
// 调用参数中可选地带有文件路径，使 Summarize 必须
// 将其保留为变更文件。
func propAssistantCalls(id, file, content string) Entry {
	args := "{}"
	if file != "" {
		raw, _ := json.Marshal(map[string]any{"file": file})
		args = string(raw)
	}
	data, _ := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": content,
		"tool_calls": []any{map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      "patch",
				"arguments": args,
			},
		}},
	})
	return Entry{Role: "assistant", Content: content, Data: data}
}

func propToolResult(id, name, content string) Entry {
	data, _ := json.Marshal(map[string]any{"id": id, "name": name, "output": content})
	return Entry{Role: "tool", Name: name, Content: content, ToolCallID: id, Data: data}
}

func propPlainEntry(role, content string) Entry {
	data, _ := json.Marshal(map[string]any{"role": role, "content": content})
	return Entry{Role: role, Content: content, Data: data}
}

// propEntrySequence 构建一份转录，其第一个块带有唯一的
// 文件/失败/下一步标记，随后是随机的工具调用/结果对。
func propEntrySequence(seed int64) ([]Entry, propMarkers, int) {
	rng := rand.New(rand.NewSource(seed))
	markers := propMarkers{
		file: fmt.Sprintf("src/PROPFILE-%d.go", seed),
		fail: fmt.Sprintf("PROPFAIL-%d", seed),
		step: fmt.Sprintf("PROPSTEP-%d", seed),
	}

	entries := []Entry{propPlainEntry("system", "sys")}
	entries = append(entries, propAssistantCalls(
		fmt.Sprintf("c-file-%d", seed), markers.file, "edit marker file"))
	entries = append(entries, propToolResult(fmt.Sprintf("c-file-%d", seed), "patch", "ok"))
	entries = append(entries, propPlainEntry("assistant", "Next step: "+markers.step))
	entries = append(entries, propAssistantCalls(
		fmt.Sprintf("c-fail-%d", seed), "", "run failing command"))
	entries = append(entries, propToolResult(
		fmt.Sprintf("c-fail-%d", seed), "shell",
		"[exit 1] error: "+markers.fail+" boom"))
	evidenceEnd := len(entries)

	blocks := 2 + rng.Intn(4)
	for i := 0; i < blocks; i++ {
		prefix := fmt.Sprintf("b%d-%d", seed, i)
		ids := []string{prefix + "-a"}
		if rng.Intn(2) == 0 {
			ids = append(ids, prefix+"-b")
		}
		entries = append(entries, propAssistantCalls(ids[0], "", "call batch "+prefix))
		for j, id := range ids {
			// 一个单独的 assistant 携带批次中任何额外的调用 id，使
			// 每个结果在转录中都有匹配的调用。
			if j > 0 {
				entries = append(entries, propAssistantCalls(id, "", "extra call "+id))
			}
			entries = append(entries, propToolResult(id, "prop.tool", fmt.Sprintf("result %d", j)))
		}
		if rng.Intn(2) == 0 {
			entries = append(entries, propPlainEntry("user", propText(rng, 12)))
		}
	}
	entries = append(entries, propPlainEntry("assistant", "done"))
	return entries, markers, evidenceEnd
}

func propContains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// propAssertNoSplitPairs 检查没有任何切分严格分隔声明某 id 的最后一次调用
// 与携带它的工具结果。
func propAssertNoSplitPairs(t *testing.T, entries []Entry, start int) {
	t.Helper()
	lastCall := make(map[string]int)
	for i, entry := range entries {
		if entry.Role != "assistant" {
			continue
		}
		for _, id := range toolCallIDs(entry) {
			lastCall[id] = i
		}
	}
	for i, entry := range entries {
		if entry.Role != "tool" || entry.ToolCallID == "" {
			continue
		}
		call, ok := lastCall[entry.ToolCallID]
		if !ok {
			continue
		}
		if call < start && start <= i {
			t.Fatalf("cut %d splits tool pair (%d,%d) id=%s", start, call, i, entry.ToolCallID)
		}
	}
}

// propAssertEvidenceSurvives 检查文档化的压缩契约：如果带有证据的条目
// 落在被丢弃的前缀中，其标记必须出现在
// 摘要载荷的相应章节中。
func propAssertEvidenceSurvives(t *testing.T, entries []Entry, start int, markers propMarkers) {
	t.Helper()
	compaction := Summarize(entries[:start], len(entries)-start)
	for i, entry := range entries {
		if i >= start {
			continue // 逐字保留，摘要中无需保留任何内容
		}
		if entry.Role == "tool" && strings.Contains(entry.Content, markers.fail) &&
			!strings.Contains(strings.Join(compaction.Failures, "\n"), markers.fail) {
			t.Fatalf("dropped failure %q missing from summary failures: %v",
				markers.fail, compaction.Failures)
		}
		if (entry.Role == "assistant" || entry.Role == "system") &&
			strings.Contains(entry.Content, markers.step) &&
			!strings.Contains(strings.Join(compaction.NextSteps, "\n"), markers.step) {
			t.Fatalf("dropped next step %q missing from summary next steps: %v",
				markers.step, compaction.NextSteps)
		}
		for _, path := range pathsFromData(entry.Data) {
			if path == markers.file && !propContains(compaction.Files, markers.file) {
				t.Fatalf("dropped file %q missing from summary files: %v",
					markers.file, compaction.Files)
			}
		}
	}
}

func TestPropertyCompactionPreservesPairsAndEvidence(t *testing.T) {
	for _, seed := range []int64{4, 8, 15, 16, 42} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			entries, markers, evidenceEnd := propEntrySequence(seed)
			if len(entries) <= evidenceEnd {
				t.Fatal("test setup produced no entries after the evidence block")
			}

			// 恰好容纳最后一条条目的预算会迫使每条带证据的
			// 条目进入被丢弃的前缀。
			smallBudget := estimateEntryTokens(entries[len(entries)-1])
			if start := CutForBudget(entries, smallBudget); start <= evidenceEnd {
				t.Fatalf("small budget cut at %d, want past evidence end %d", start, evidenceEnd)
			} else {
				propAssertNoSplitPairs(t, entries, start)
				propAssertEvidenceSurvives(t, entries, start, markers)
			}

			rng := rand.New(rand.NewSource(seed + 1000))
			total := 0
			for _, entry := range entries {
				total += estimateEntryTokens(entry)
			}
			for i := 0; i < 25; i++ {
				budget := 1 + rng.Intn(total+20)
				start := CutForBudget(entries, budget)
				if start <= 0 {
					continue
				}
				if start >= len(entries) {
					t.Fatalf("CutForBudget returned %d for %d entries", start, len(entries))
				}
				propAssertNoSplitPairs(t, entries, start)
				propAssertEvidenceSurvives(t, entries, start, markers)
			}

			// 端到端：追加转录，用小预算压缩，
			// 并验证幸存的消息具有完整的工具对，且带有
			// 摘要中的证据。
			dir := t.TempDir()
			sess := newTestSession(t, dir, fmt.Sprintf("compact-%d", seed))
			for _, entry := range entries {
				kind := KindMessage
				switch entry.Role {
				case "assistant":
					kind = KindAssistant
				case "tool":
					kind = KindTool
				case "user":
					kind = KindUser
				case "system":
					kind = KindSummary
				}
				if _, err := sess.AppendRecord(kind, entry.Data); err != nil {
					t.Fatalf("append %s: %v", kind, err)
				}
			}
			if err := sess.CompactBudget(smallBudget); err != nil {
				t.Fatalf("CompactBudget: %v", err)
			}
			messages := sess.Messages()
			if len(messages) == 0 || messages[0].Role != "system" {
				t.Fatalf("compacted messages = %+v", messages)
			}
			summary := messages[0].Content
			for _, marker := range []string{markers.file, markers.fail, markers.step} {
				if !strings.Contains(summary, marker) {
					t.Fatalf("summary missing evidence %q:\n%s", marker, summary)
				}
			}
			callIDs := make(map[string]bool)
			for _, message := range messages {
				if message.Role != "assistant" {
					continue
				}
				for _, id := range toolCallIDs(message) {
					callIDs[id] = true
				}
			}
			resultIDs := make(map[string]bool)
			for _, message := range messages {
				if message.Role != "tool" || message.ToolCallID == "" {
					continue
				}
				resultIDs[message.ToolCallID] = true
				if !callIDs[message.ToolCallID] {
					t.Fatalf("orphaned tool result %q after compaction", message.ToolCallID)
				}
			}
			for id := range callIDs {
				if !resultIDs[id] {
					t.Fatalf("tool call %q lost its result after compaction", id)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 属性 4：符文裁剪始终产生有效的 UTF-8 和符文安全的前缀。
// ---------------------------------------------------------------------------

func TestPropertyRuneClippingNeverSplitsRune(t *testing.T) {
	for _, seed := range []int64{5, 17, 29, 101} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 120; i++ {
				src := propText(rng, 80)
				limit := rng.Intn(90) - 4 // 刻意包含零和负数
				clippers := map[string]func(string, int) string{
					"truncateRunes":    truncateRunes,
					"capProgressRunes": capProgressRunes,
				}
				for name, clip := range clippers {
					got := clip(src, limit)
					if !utf8.ValidString(got) {
						t.Fatalf("%s(%q, %d) produced invalid UTF-8: %x", name, src, limit, got)
					}
					if limit <= 0 {
						if got != "" {
							t.Fatalf("%s(%q, %d) = %q, want empty", name, src, limit, got)
						}
						continue
					}
					if n := utf8.RuneCountInString(got); n > limit {
						t.Fatalf("%s(%q, %d) has %d runes", name, src, limit, n)
					}
				}

				runes := []rune(src)
				switch {
				case limit <= 0:
					// 上面已检查
				case limit >= len(runes):
					if got := capProgressRunes(src, limit); got != src {
						t.Fatalf("capProgressRunes dropped bytes of %q at limit %d", src, limit)
					}
					cleaned := strings.Join(strings.Fields(strings.ReplaceAll(src, "\n", " ")), " ")
					if got := truncateRunes(src, limit); got != cleaned {
						t.Fatalf("truncateRunes(%q, %d) = %q, want %q", src, limit, got, cleaned)
					}
				default:
					// 默认切点是第 limit 个 rune 之后的字节偏移。
					cut := len(string(runes[:limit]))
					if !textutil.GluedAt(src, cut) {
						// 切点本就是字簇边界：吸附不会改变它，
						// 新裁剪器必须与旧的 []rune 截断逐字节一致。
						if got, want := capProgressRunes(src, limit), string(runes[:limit]); got != want {
							t.Fatalf("capProgressRunes(%q, %d) = %q, want %q", src, limit, got, want)
						}
					} else {
						// 切点落在字簇内部（如 e\u0301 / क\u0301）：
						// 结果只会向前吸附到簇边界，因此仍是原串前缀且结尾不在簇内。
						got := capProgressRunes(src, limit)
						if !strings.HasPrefix(src, got) {
							t.Fatalf("capProgressRunes(%q, %d) = %q, not a prefix of src", src, limit, got)
						}
						if textutil.GluedAt(src, len(got)) {
							t.Fatalf("capProgressRunes(%q, %d) = %q ends mid-cluster", src, limit, got)
						}
					}
				}
			}
		})
	}
}

func TestPropertyProgressTextStaysRuneSafeAcrossReopen(t *testing.T) {
	for _, seed := range []int64{6, 12, 24} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			dir := t.TempDir()
			sess := newTestSession(t, dir, fmt.Sprintf("clip-%d", seed))
			src := propText(rng, maxProgressTextRunes+128)
			if err := sess.AppendProgress(Progress{Turn: 1, AssistantText: src}); err != nil {
				t.Fatalf("AppendProgress: %v", err)
			}
			reopened, err := OpenSession(dir, sess.ID())
			if err != nil {
				t.Fatalf("OpenSession: %v", err)
			}
			got, ok := reopened.LatestProgress()
			if !ok {
				t.Fatal("LatestProgress found no checkpoint")
			}
			if !utf8.ValidString(got.AssistantText) {
				t.Fatalf("restored progress text is not valid UTF-8: %x", got.AssistantText)
			}
			if n := utf8.RuneCountInString(got.AssistantText); n > maxProgressTextRunes {
				t.Fatalf("restored progress has %d runes, want <= %d", n, maxProgressTextRunes)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 属性 5：SyncProgress 持久性与进度过期防护。
// ---------------------------------------------------------------------------

func TestPropertySyncProgressReopenAndStaleness(t *testing.T) {
	for _, seed := range []int64{9, 19, 28, 61} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			propProgressRoundtrip(t, seed)
		})
	}
}

func propProgressRoundtrip(t *testing.T, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	dir := t.TempDir()
	sess := newTestSession(t, dir, fmt.Sprintf("sync-prop-%d", seed))

	committed := 0
	turns := 1 + rng.Intn(4)
	for i := 0; i < turns; i++ {
		if err := sess.Append(KindUser, propText(rng, 12)); err != nil {
			t.Fatal(err)
		}
		if err := sess.Append(KindAssistant, propText(rng, 12)); err != nil {
			t.Fatal(err)
		}
		committed++
	}

	inFlight := Progress{
		Turn:          committed + 1,
		AssistantText: propText(rng, 32),
		ToolCalls:     []string{"prop.read", "prop.patch"},
	}
	if err := sess.AppendProgress(inFlight); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}
	want, ok := sess.LatestProgress()
	if !ok {
		t.Fatal("LatestProgress found no in-flight checkpoint")
	}

	// 屏障形式必须持久，且不添加记录。
	before := sess.ProgressCount()
	if err := sess.SyncProgress(); err != nil {
		t.Fatalf("SyncProgress barrier: %v", err)
	}
	if after := sess.ProgressCount(); after != before {
		t.Fatalf("barrier added progress records: %d -> %d", before, after)
	}

	reopened, err := OpenSession(dir, sess.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	got, ok := reopened.LatestProgress()
	if !ok || got.Turn != want.Turn || got.AssistantText != want.AssistantText ||
		len(got.ToolCalls) != len(want.ToolCalls) {
		t.Fatalf("progress after reopen = %+v, %v; want %+v", got, ok, want)
	}
	for i := range want.ToolCalls {
		if got.ToolCalls[i] != want.ToolCalls[i] {
			t.Fatalf("tool call %d = %q, want %q", i, got.ToolCalls[i], want.ToolCalls[i])
		}
	}
	if recovered, ok := RecoverProgress(reopened); !ok || recovered.Turn != inFlight.Turn {
		t.Fatalf("in-flight progress not recoverable: %+v, %v", recovered, ok)
	}

	// 提交该轮次会使其轮中进度按位置过期。
	if err := reopened.Append(KindAssistant, propText(rng, 12)); err != nil {
		t.Fatal(err)
	}
	if recovered, ok := RecoverProgress(reopened); ok {
		t.Fatalf("stale progress accepted after assistant commit: %+v", recovered)
	}

	// 轮次号不领先于已提交数量的检查点，即使是分支上最新的记录，
	// 也属于过期。
	if err := reopened.AppendProgress(Progress{
		Turn:          committed + 1,
		AssistantText: "old turn number",
	}); err != nil {
		t.Fatal(err)
	}
	if recovered, ok := RecoverProgress(reopened); ok {
		t.Fatalf("progress with an old turn number accepted: %+v", recovered)
	}

	// 下一轮次的进度重新可恢复，并在重新打开后存活。
	if err := reopened.SyncProgress(Progress{
		Turn:          committed + 2,
		AssistantText: "next turn partial",
	}); err != nil {
		t.Fatalf("SyncProgress append: %v", err)
	}
	final, err := OpenSession(dir, reopened.ID())
	if err != nil {
		t.Fatalf("second OpenSession: %v", err)
	}
	if recovered, ok := RecoverProgress(final); !ok ||
		recovered.Turn != committed+2 || recovered.AssistantText != "next turn partial" {
		t.Fatalf("next-turn progress after reopen = %+v, %v", recovered, ok)
	}
}
