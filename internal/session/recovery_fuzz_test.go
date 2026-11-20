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

// FuzzSessionRecordRoundtrip drives arbitrary payloads through AppendRecord and
// then through the lenient read/repair path used on resume. The properties
// checked are: no panic on any input, every line that survives repair is a
// well-formed JSON record, and the known-good sentinel record is never lost to
// a torn or malformed tail. Hard corruption is an accepted (non-panicking)
// outcome and is reported as such by ValidateAndRepair/OpenSession.

type sessionFuzzSeed struct {
	seed    int64
	payload string
	tail    string
}

var sessionFuzzSeeds = []sessionFuzzSeed{
	// Well-formed object payload, clean tail.
	{1, `{"role":"user","content":"你好🙂"}`, ""},
	// Truncated payload and torn final line: payload falls back to a JSON
	// string, tail is dropped by torn-tail repair.
	{2, `{"content":"trailed`, `{"id":"tor`},
	// Valid non-object JSON payload plus a bare newline tail.
	{3, `42`, "\n"},
	// Nested payload and a valid extra record appended as raw JSON.
	{4, `{"nested":{"x":[1,2,3]}}`, `{"kind":"entry"}`},
	// Empty payload (stored as null) and non-JSON garbage tail.
	{5, "", "garbage\n"},
	// Combining marks and a structurally valid extra tail record.
	{6, `{"role":"assistant","content":"combining e\u0301"}`, `{"id":"x","kind":"message","data":null}`},
	// A syntax error on a non-final physical line is hard corruption.
	{7, `{"role":"tool","output":"[exit 1] failed"}`, "{\"kind\":\"message\"}\n{oops}\n{\"kind\":\"user\"}"},
	// A schema error (kind is not a string) is hard corruption.
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
	// Keep each fuzz case small and bounded; the seed corpus already stays well
	// under this limit.
	const maxFuzzBytes = 4096
	if len(payload) > maxFuzzBytes || len(tail) > maxFuzzBytes ||
		len(payload)+len(tail) > maxFuzzBytes {
		t.Skip("fuzz input too large")
	}

	dir := t.TempDir()
	sess := newTestSession(t, dir, "fuzz")

	// A deterministic short prefix exercises the multi-record append/rebuild
	// path as well as the single fuzzed payload.
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

	// Feed arbitrary bytes through AppendRecord. Invalid JSON may be rejected
	// (and must be rolled back cleanly); the only hard requirement is no panic.
	if json.Valid([]byte(payload)) {
		_, _ = sess.AppendRecord(KindMessage, json.RawMessage(payload))
	} else {
		_, _ = sess.AppendRecord(KindMessage, payload)
	}

	// This record is our witness that lenient repair never discards valid
	// history while coping with a damaged tail.
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

	// Validation must classify every line without panicking. Only hard
	// corruption is an error; a torn final line is reported, not rejected.
	_, reportErr := ValidateAndRepair(path, false)
	if reportErr != nil && !errors.Is(reportErr, ErrHardCorruption) {
		t.Fatalf("ValidateAndRepair: %v", reportErr)
	}

	reopened, err := OpenSession(dir, sess.ID())
	if err != nil {
		if !errors.Is(err, ErrHardCorruption) {
			t.Fatalf("OpenSession: %v", err)
		}
		return // hard corruption is an accepted, non-panicking outcome
	}

	// After repair every surviving physical line is a well-formed record.
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
		// A record may legitimately omit "data" entirely (decodeLine only
		// requires a non-empty kind); when present it must be valid JSON.
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

// TestFuzzSessionRecordRoundtripSeeds runs the fuzz seed corpus as an ordinary
// deterministic test so the regression cases are always covered by go test.
func TestFuzzSessionRecordRoundtripSeeds(t *testing.T) {
	for _, item := range sessionFuzzSeeds {
		t.Run(fmt.Sprintf("seed=%d", item.seed), func(t *testing.T) {
			sessionRoundtripTarget(t, item.seed, item.payload, item.tail)
		})
	}
}
