package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

func writeAuditFile(t *testing.T, dir, name string, entries ...security.AuditEntry) {
	t.Helper()
	var b strings.Builder
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	writeFile(t, filepath.Join(dir, name), b.String())
}

func TestParseAuditTimeAcceptsRFC3339DateAndCJK(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2026-01-02T03:04:05Z", "2026-01-02T03:04:05Z", true},
		{"2026-01-02 03:04:05", "2026-01-02T03:04:05Z", true},
		{"2026-01-02", "2026-01-02T00:00:00Z", true},
		{"2026年01月02日", "2026-01-02T00:00:00Z", true},
		{"2026年1月2日", "2026-01-02T00:00:00Z", true},
		{"2026年01月02日 15时04分05秒", "2026-01-02T15:04:05Z", true},
		{" 2026-01-02 ", "2026-01-02T00:00:00Z", true},
		{"", "", false},
		{"not-a-date", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseAuditTime(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if tc.ok && got.UTC().Format(time.RFC3339) != tc.want {
				t.Fatalf("time = %s, want %s", got.UTC().Format(time.RFC3339), tc.want)
			}
		})
	}
}

func TestFilterAuditEntriesByDecisionIsCaseInsensitive(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow"},
		{TS: "2026-01-01T00:00:01Z", Tool: "write", Decision: "deny"},
		{TS: "2026-01-01T00:00:02Z", Tool: "edit", Decision: "ALLOW"},
	}
	got := FilterAuditEntries(entries, AuditFilter{Decision: "allow"})
	if len(got) != 2 || got[0].Tool != "read" || got[1].Tool != "edit" {
		t.Fatalf("got %+v", got)
	}
}

func TestFilterAuditEntriesBySessionAndToolAreExact(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow", Session: "s1"},
		{TS: "2026-01-01T00:00:01Z", Tool: "reader", Decision: "allow", Session: "s1"},
		{TS: "2026-01-01T00:00:02Z", Tool: "read", Decision: "allow", Session: "s10"},
	}
	if got := FilterAuditEntries(entries, AuditFilter{Tool: "read"}); len(got) != 2 {
		t.Fatalf("tool filter got %+v", got)
	}
	got := FilterAuditEntries(entries, AuditFilter{Session: "s1"})
	if len(got) != 2 || got[0].Tool != "read" || got[1].Tool != "reader" {
		t.Fatalf("session filter got %+v", got)
	}
}

func TestFilterAuditEntriesByTextSearchesFields(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow", Reason: "Operator approved", Session: "s1"},
		{TS: "2026-01-01T00:00:01Z", Tool: "write", Decision: "deny", Reason: "blocked", Args: `{"p":"SECRET"}`},
	}
	if got := FilterAuditEntries(entries, AuditFilter{Text: "operator"}); len(got) != 1 || got[0].Tool != "read" {
		t.Fatalf("reason text got %+v", got)
	}
	if got := FilterAuditEntries(entries, AuditFilter{Text: "secret"}); len(got) != 1 || got[0].Tool != "write" {
		t.Fatalf("args text got %+v", got)
	}
}

func TestFilterAuditEntriesTimeRangeIsInclusiveAndSorted(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-03T00:00:00Z", Tool: "c"},
		{TS: "2026-01-01T00:00:00Z", Tool: "a"},
		{TS: "2026-01-02T00:00:00Z", Tool: "b"},
	}
	got := FilterAuditEntries(entries, AuditFilter{
		Since: "2026-01-01T00:00:00Z",
		Until: "2026-01-03T00:00:00Z",
	})
	if len(got) != 3 || got[0].Tool != "a" || got[1].Tool != "b" || got[2].Tool != "c" {
		t.Fatalf("got %+v", got)
	}
	// Both bounds fall exactly on the middle entry's timestamp.
	got = FilterAuditEntries(entries, AuditFilter{Since: "2026-01-02", Until: "2026-01-02"})
	if len(got) != 1 || got[0].Tool != "b" {
		t.Fatalf("boundary got %+v", got)
	}
}

func TestFilterAuditEntriesCJKTimeRange(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-01T00:00:00Z", Tool: "before"},
		{TS: "2026-01-02T12:00:00Z", Tool: "on"},
		{TS: "2026-01-03T00:00:00Z", Tool: "after"},
	}
	got := FilterAuditEntries(entries, AuditFilter{
		Since: "2026年01月02日",
		Until: "2026年01月02日 23时59分59秒",
	})
	if len(got) != 1 || got[0].Tool != "on" {
		t.Fatalf("got %+v", got)
	}
}

func TestFilterAuditEntriesCombinesDimensions(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-01T00:00:00Z", Tool: "write", Decision: "allow", Session: "s1", Reason: "operator yes", Args: `{"k":1}`},
		{TS: "2026-01-02T00:00:00Z", Tool: "write", Decision: "allow", Session: "s2", Reason: "operator yes"},
		{TS: "2026-01-10T00:00:00Z", Tool: "write", Decision: "allow", Session: "s1", Reason: "operator yes"},
		{TS: "2026-01-02T00:00:01Z", Tool: "read", Decision: "allow", Session: "s1", Reason: "operator yes"},
		{TS: "2026-01-02T00:00:02Z", Tool: "write", Decision: "deny", Session: "s1", Reason: "operator no"},
	}
	got := FilterAuditEntries(entries, AuditFilter{
		Decision: "allow",
		Session:  "s1",
		Tool:     "write",
		Since:    "2026-01-01",
		Until:    "2026-01-05",
		Text:     "operator",
	})
	if len(got) != 1 || got[0].Session != "s1" || got[0].TS != "2026-01-01T00:00:00Z" {
		t.Fatalf("got %+v", got)
	}
}

func TestFilterAuditEntriesEmptyResult(t *testing.T) {
	entries := []security.AuditEntry{{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow"}}
	if got := FilterAuditEntries(entries, AuditFilter{Decision: "refused"}); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestFilterAuditEntriesSortsAndDoesNotMutateInput(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-03T00:00:00Z", Tool: "c"},
		{TS: "bad", Tool: "bad1"},
		{TS: "2026-01-01T00:00:00Z", Tool: "a"},
		{TS: "bad", Tool: "bad2"},
		{TS: "2026-01-02T00:00:00Z", Tool: "b"},
	}
	got := FilterAuditEntries(entries, AuditFilter{})
	want := []string{"a", "b", "c", "bad1", "bad2"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, got %+v", len(got), got)
	}
	for i, tool := range want {
		if got[i].Tool != tool {
			t.Fatalf("got[%d].Tool = %q, want %q (%+v)", i, got[i].Tool, tool, got)
		}
	}
	if entries[0].Tool != "c" || entries[1].Tool != "bad1" {
		t.Fatalf("input was mutated: %+v", entries)
	}
}

func TestAuditWithoutFilterKeepsLatestFileBehavior(t *testing.T) {
	storage := t.TempDir()
	dir := filepath.Join(storage, "audit")
	writeAuditFile(t, dir, "2026-01-01.jsonl",
		security.AuditEntry{TS: "2026-01-01T00:00:00Z", Tool: "ancient", Decision: "allow", Reason: "old file"},
	)
	day := time.Now().UTC().Format("2006-01-02")
	writeAuditFile(t, dir, day+".jsonl",
		security.AuditEntry{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow", Reason: "first"},
		security.AuditEntry{TS: "2026-01-01T00:00:01Z", Tool: "write", Decision: "ask", Reason: "operator said yes"},
	)
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"audit", "--tail", "1"}); code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut.String())
	}
	want := fmt.Sprintf("%s\t%-8s\t%s\t%s\n", "2026-01-01T00:00:01Z", "ask", "write", "operator said yes")
	if out.String() != want {
		t.Fatalf("out = %q, want %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestAuditWithoutFilterJSONKeepsExactOutput(t *testing.T) {
	storage := t.TempDir()
	dir := filepath.Join(storage, "audit")
	entries := []security.AuditEntry{
		{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow"},
		{TS: "2026-01-01T00:00:01Z", Tool: "write", Decision: "deny"},
	}
	writeAuditFile(t, dir, "2026-01-01.jsonl", entries...)
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"audit", "--json"}); code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut.String())
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := string(data) + "\n"; out.String() != want {
		t.Fatalf("out = %q, want %q", out.String(), want)
	}
}

func TestAuditFilterSearchesAcrossDailyFiles(t *testing.T) {
	storage := t.TempDir()
	dir := filepath.Join(storage, "audit")
	writeAuditFile(t, dir, "2026-01-01.jsonl",
		security.AuditEntry{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow", Reason: "old read"},
		security.AuditEntry{TS: "2026-01-01T00:00:01Z", Tool: "write", Decision: "deny", Reason: "old deny"},
	)
	writeAuditFile(t, dir, "2026-01-02.jsonl",
		security.AuditEntry{TS: "2026-01-02T00:00:00Z", Tool: "read", Decision: "allow", Reason: "new read"},
	)
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, StorageDir: storage}, []string{"audit", "--tool", "read"}); code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"old read", "new read"} {
		if !strings.Contains(got, want) {
			t.Fatalf("out = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "old deny") {
		t.Fatalf("out = %q, unexpected old deny", got)
	}
}

func TestAuditFilterFlagSpellings(t *testing.T) {
	entries := []security.AuditEntry{
		{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow", Session: "s1", Reason: "alpha approved"},
		{TS: "2026-01-02T00:00:00Z", Tool: "write", Decision: "deny", Session: "s2", Reason: "beta blocked"},
		{TS: "2026-01-03T00:00:00Z", Tool: "edit", Decision: "ask", Session: "s1", Reason: "gamma pending"},
	}
	cases := []struct {
		name   string
		args   []string
		wanted string
		absent []string
	}{
		{"decision", []string{"audit", "--decision", "allow"}, "alpha", []string{"beta", "gamma"}},
		{"decision equals upper", []string{"audit", "--decision=DENY"}, "beta", []string{"alpha", "gamma"}},
		{"session", []string{"audit", "--session", "s1"}, "gamma", []string{"beta"}},
		{"tool equals", []string{"audit", "--tool=write"}, "beta", []string{"alpha", "gamma"}},
		{"since", []string{"audit", "--since", "2026-01-02"}, "beta", []string{"alpha"}},
		{"since CJK", []string{"audit", "--since=2026年01月03日"}, "gamma", []string{"alpha", "beta"}},
		{"combined", []string{"audit", "--session=s1", "--since", "2026-01-02"}, "gamma", []string{"alpha", "beta"}},
	}
	// Text-substring filtering is covered by
	// TestFilterAuditEntriesByTextSearchesFields on the pure function; the
	// command exposes decision/session/tool/since as filter flags.

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			writeAuditFile(t, filepath.Join(storage, "audit"), "2026-01-01.jsonl", entries...)
			ioState, out, errOut := newTestIO("", nil)
			if code := RunWith(App{IO: ioState, StorageDir: storage}, tc.args); code != 0 {
				t.Fatalf("exit = %d (stderr %q)", code, errOut.String())
			}
			got := out.String()
			if !strings.Contains(got, tc.wanted) {
				t.Fatalf("out = %q, want %q", got, tc.wanted)
			}
			for _, bad := range tc.absent {
				if strings.Contains(got, bad) {
					t.Fatalf("out = %q, unexpected %q", got, bad)
				}
			}
		})
	}
}

func TestAuditFilterJSONOutput(t *testing.T) {
	storage := t.TempDir()
	writeAuditFile(t, filepath.Join(storage, "audit"), "2026-01-01.jsonl",
		security.AuditEntry{TS: "2026-01-01T00:00:00Z", Tool: "read", Decision: "allow"},
		security.AuditEntry{TS: "2026-01-01T00:00:01Z", Tool: "write", Decision: "deny"},
	)
	ioState, out, errOut := newTestIO("", nil)
	args := []string{"audit", "--decision", "deny", "--json"}
	if code := RunWith(App{IO: ioState, StorageDir: storage}, args); code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut.String())
	}
	var got []security.AuditEntry
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	if len(got) != 1 || got[0].Tool != "write" {
		t.Fatalf("got %+v", got)
	}
}

func TestAuditFilterMissingValueAndNoLogs(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"audit", "--decision"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--decision needs a value") {
		t.Fatalf("stderr = %q", errOut.String())
	}

	storage := t.TempDir()
	ioState2, out2, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState2, StorageDir: storage}, []string{"audit", "--decision", "allow"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out2.String(), "no audit log under") {
		t.Fatalf("out = %q", out2.String())
	}
}
