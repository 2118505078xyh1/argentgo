package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// sampleEntries is a small structured transcript used by the pure filter tests.
// Timestamp order matches Kind order so ranges are easy to assert.
func sampleEntries() []exportEntry {
	return []exportEntry{
		{Role: "system", TS: "2026-01-01T00:00:00Z", Kind: "header", Text: "标题"},
		{Role: "user", TS: "2026-01-02T00:00:00Z", Kind: "user", Text: "你好"},
		{Role: "assistant", TS: "2026-01-03T00:00:00Z", Kind: "assistant", Text: "收到"},
		{Role: "tool", TS: "2026-01-04T00:00:00Z", Kind: "tool", Text: "结果"},
	}
}

func kinds(entries []exportEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Kind)
	}
	return out
}

func TestFilterExportEntriesTimeRangeInclusive(t *testing.T) {
	got := filterExportEntries(sampleEntries(), "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z", nil)
	if want := []string{"user", "assistant"}; !reflect.DeepEqual(kinds(got), want) {
		t.Fatalf("range = %v, want %v", kinds(got), want)
	}
}

func TestFilterExportEntriesCJKTimeRange(t *testing.T) {
	got := filterExportEntries(sampleEntries(), "2026年01月02日", "2026年01月03日 23时59分59秒", nil)
	if want := []string{"user", "assistant"}; !reflect.DeepEqual(kinds(got), want) {
		t.Fatalf("CJK range = %v, want %v", kinds(got), want)
	}
}

func TestFilterExportEntriesByTypeMatchesKindAndRole(t *testing.T) {
	// Case-insensitive, and "system" only matches the header's Role (its Kind
	// is "header"), proving both fields are considered.
	for _, tc := range []struct {
		name  string
		types []string
		want  []string
	}{
		{"kind", []string{"USER", "tool"}, []string{"user", "tool"}},
		{"role", []string{"System"}, []string{"header"}},
		{"multiple", []string{"user", "assistant"}, []string{"user", "assistant"}},
	} {
		got := filterExportEntries(sampleEntries(), "", "", tc.types)
		if !reflect.DeepEqual(kinds(got), tc.want) {
			t.Errorf("%s: types = %v, want %v", tc.name, kinds(got), tc.want)
		}
	}
}

func TestFilterExportEntriesCombinedTimeAndType(t *testing.T) {
	got := filterExportEntries(sampleEntries(), "2026-01-02", "2026-01-04", []string{"assistant", "tool"})
	if want := []string{"assistant", "tool"}; !reflect.DeepEqual(kinds(got), want) {
		t.Fatalf("combined = %v, want %v", kinds(got), want)
	}
}

func TestFilterExportEntriesEmptyResult(t *testing.T) {
	got := filterExportEntries(sampleEntries(), "2030-01-01", "", nil)
	if len(got) != 0 {
		t.Fatalf("future range returned %d entries: %v", len(got), kinds(got))
	}
}

func TestFilterExportEntriesNoFilterKeepsEveryEntryInOrder(t *testing.T) {
	entries := sampleEntries()
	got := filterExportEntries(entries, "  ", "", []string{"", " "})
	if !reflect.DeepEqual(got, entries) {
		t.Fatalf("unfiltered = %v, want %v", kinds(got), kinds(entries))
	}
}

func TestFilterExportArtifactUnfilteredReserializeIsByteIdentical(t *testing.T) {
	storage := exportFilterFixture(t)
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "s", "--format", "json",
	}); code != 0 {
		t.Fatalf("export exit = %d, err %q", code, errOut.String())
	}
	original := out.Bytes()
	// A type list matching every entry forces a full parse + re-serialize. The
	// result must still be byte-identical to the unfiltered artifact.
	got, err := filterExportArtifact(original, "json", ExportFilter{
		Types: []string{"header", "user", "assistant", "tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("re-serialized export differs from original:\ngot:  %q\nwant: %q", got, original)
	}
}

func TestFilterExportArtifactInactiveReturnsOriginalBytes(t *testing.T) {
	data := []byte("not even JSON, but left untouched when no filter is active\n")
	got, err := filterExportArtifact(data, "json", ExportFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("inactive filter changed bytes: %q", got)
	}
}

// exportFilterFixture writes one four-record session with distinct timestamps
// and kinds, used by the end-to-end wiring tests.
func exportFilterFixture(t *testing.T) string {
	t.Helper()
	storage := t.TempDir()
	writeJSONLSession(t, storage, "s", []map[string]any{
		record("a", "", "2026-01-01T00:00:00Z", "header", map[string]any{"title": "标题"}),
		record("b", "a", "2026-01-02T00:00:00Z", "user", map[string]any{"text": "你好"}),
		record("c", "b", "2026-01-03T00:00:00Z", "assistant", map[string]any{"text": "收到"}),
		record("d", "c", "2026-01-04T00:00:00Z", "tool", map[string]any{"output": "结果"}),
	})
	return storage
}

func decodeExportEntries(t *testing.T, data []byte) []exportEntry {
	t.Helper()
	var entries []exportEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decode export: %v\n%s", err, data)
	}
	return entries
}

func TestSessionsExportSinceUntilWiredIntoJSON(t *testing.T) {
	storage := exportFilterFixture(t)
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "s", "--format", "json",
		"--since", "2026年01月02日", "--until=2026-01-03T23:59:59Z",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	got := kinds(decodeExportEntries(t, out.Bytes()))
	if want := []string{"user", "assistant"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exported kinds = %v, want %v", got, want)
	}
}

func TestSessionsExportTypeWiredIntoYAML(t *testing.T) {
	storage := exportFilterFixture(t)
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "s", "--format", "yaml", "--type", "assistant,tool",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !bytes.HasPrefix(out.Bytes(), []byte(yamlExportHeader)) {
		t.Fatalf("YAML header missing: %q", out.Bytes())
	}
	body := bytes.TrimPrefix(out.Bytes(), []byte(yamlExportHeader))
	got := kinds(decodeExportEntries(t, body))
	if want := []string{"assistant", "tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exported kinds = %v, want %v", got, want)
	}
}

func TestSessionsExportRepeatedTypeFlags(t *testing.T) {
	storage := exportFilterFixture(t)
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "s", "--format=json", "--type", "user", "--type", "tool",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	got := kinds(decodeExportEntries(t, out.Bytes()))
	if want := []string{"user", "tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exported kinds = %v, want %v", got, want)
	}
}

func TestSessionsExportTextFormatWarnsAndKeepsTranscript(t *testing.T) {
	for _, format := range []string{"html", "markdown"} {
		storage := exportFilterFixture(t)
		stdio, out, errOut := newTestIO("", nil)
		code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
			"sessions", "export", "s", "--format", format, "--since", "2026-01-02",
		})
		if code != 0 {
			t.Fatalf("%s exit = %d, err %q", format, code, errOut.String())
		}
		if !strings.Contains(errOut.String(), "only apply to json/yaml") {
			t.Errorf("%s: expected filter limitation warning, got %q", format, errOut.String())
		}
		if format == "html" && !strings.Contains(out.String(), "<!doctype html>") {
			t.Errorf("html transcript was modified: %q", out.String())
		}
		if format == "markdown" && !strings.Contains(out.String(), "你好") {
			t.Errorf("markdown transcript was modified: %q", out.String())
		}
	}
}

func TestSessionsExportFilterNeedsValue(t *testing.T) {
	storage := exportFilterFixture(t)
	stdio, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "s", "--since",
	}); code != 2 {
		t.Fatalf("exit = %d, want 2; err %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "--since needs a value") {
		t.Fatalf("err = %q", errOut.String())
	}
}
