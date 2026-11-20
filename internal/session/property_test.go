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
)

// These are deterministic property/roundtrip tests over the durable session
// engine. They are deliberately bounded (small N, a few fixed seeds) so the
// whole file stays fast while still exploring arbitrary-looking sequences.
// No production code is touched here: everything below drives the public and
// package-internal APIs through temp dirs only.

var propRunePool = []rune("abcXYZ0123 界世界文🙂🚀e\u0301\u0300क")

// propText builds a bounded string from ASCII, CJK, emoji and combining marks.
// The pool contains no credential-shaped substrings, so security.Redact leaves
// progress/attempt text untouched and the roundtrips below compare exact bytes.
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
		// Include NUL and high bytes regularly so the manifest carries real
		// binary payloads (base64-encoded by encoding/json), not just text.
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
// Property 1: append N random records with valid parent links, reopen, verify.
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
	if len(expected) != n+1 { // header + n appended records
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
// Property 2: checkpoint capture/undo roundtrip and conflict refusal.
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
	// A repeated path exercises manifest deduplication.
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

	// Simulate the mutating turn: create absent files, change present ones.
	for _, spec := range specs {
		propWriteFile(t, root, spec.path, spec.after)
	}
	const extraPath = "outside-manifest.txt"
	propWriteFile(t, root, extraPath, []byte("untouched"))

	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatalf("CompleteCheckpoint: %v", err)
	}
	// byPath holds value copies from before completion; refresh them so the
	// post-turn assertions see the AfterSHA256/AfterAbsent fields.
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

	// A manual edit after the checkpoint must refuse the whole undo, with no
	// partial restore of any other manifest file.
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
// Property 3: compaction cut safety and evidence preservation.
// ---------------------------------------------------------------------------

type propMarkers struct {
	file string
	fail string
	step string
}

// propAssistantCalls builds an assistant Entry carrying a real tool_calls
// payload, optionally with a file path in the call arguments so Summarize must
// preserve it as a changed file.
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

// propEntrySequence builds a transcript whose first block carries unique
// file/failure/next-step markers, followed by random tool-call/result pairs.
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
			// A distinct assistant carries any extra call ids in the batch so
			// every result has a matching call in the transcript.
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

// propAssertNoSplitPairs checks that no cut strictly separates the last call
// declaring an id from the tool result carrying it.
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

// propAssertEvidenceSurvives checks the documented compaction contract: if an
// evidence-bearing entry falls in the dropped prefix, its marker must appear
// in the corresponding section of the summary payload.
func propAssertEvidenceSurvives(t *testing.T, entries []Entry, start int, markers propMarkers) {
	t.Helper()
	compaction := Summarize(entries[:start], len(entries)-start)
	for i, entry := range entries {
		if i >= start {
			continue // kept verbatim, nothing to preserve in the summary
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

			// A budget that fits exactly the final entry forces every evidence
			// entry into the dropped prefix.
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

			// End-to-end: append the transcript, compact with the small budget,
			// and verify the surviving messages have whole tool pairs and carry
			// the summary evidence.
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
// Property 4: rune clipping always yields valid UTF-8 and a rune-safe prefix.
// ---------------------------------------------------------------------------

func TestPropertyRuneClippingNeverSplitsRune(t *testing.T) {
	for _, seed := range []int64{5, 17, 29, 101} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 120; i++ {
				src := propText(rng, 80)
				limit := rng.Intn(90) - 4 // deliberately includes zero and negatives
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
					// already checked above
				case limit >= len(runes):
					if got := capProgressRunes(src, limit); got != src {
						t.Fatalf("capProgressRunes dropped bytes of %q at limit %d", src, limit)
					}
					cleaned := strings.Join(strings.Fields(strings.ReplaceAll(src, "\n", " ")), " ")
					if got := truncateRunes(src, limit); got != cleaned {
						t.Fatalf("truncateRunes(%q, %d) = %q, want %q", src, limit, got, cleaned)
					}
				default:
					if got, want := capProgressRunes(src, limit), string(runes[:limit]); got != want {
						t.Fatalf("capProgressRunes(%q, %d) = %q, want %q", src, limit, got, want)
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
// Property 5: SyncProgress durability and progress staleness guards.
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

	// The barrier form must be durable without adding a record.
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

	// Committing the turn makes its mid-turn progress stale by position.
	if err := reopened.Append(KindAssistant, propText(rng, 12)); err != nil {
		t.Fatal(err)
	}
	if recovered, ok := RecoverProgress(reopened); ok {
		t.Fatalf("stale progress accepted after assistant commit: %+v", recovered)
	}

	// A checkpoint whose turn number is not ahead of the committed count is
	// stale even though it is the newest record on the branch.
	if err := reopened.AppendProgress(Progress{
		Turn:          committed + 1,
		AssistantText: "old turn number",
	}); err != nil {
		t.Fatal(err)
	}
	if recovered, ok := RecoverProgress(reopened); ok {
		t.Fatalf("progress with an old turn number accepted: %+v", recovered)
	}

	// The next turn's progress is recoverable again and survives a reopen.
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
