package automation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSpec(id string) Spec {
	return Spec{
		ID:       id,
		Name:     id,
		Schedule: Schedule{Kind: KindInterval, Expr: "1h"},
		Prompt:   "do " + id,
		Enabled:  true,
		Missed:   MissedRunOnce,
	}
}

func TestSpecDefaultsAndRoundTrip(t *testing.T) {
	data := []byte(`{"name":"Nightly Review","schedule":{"kind":"cron","expr":"0 3 * * *"},"prompt":"review","timeout":"30m"}`)
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !spec.Enabled {
		t.Fatal("enabled should default to true")
	}
	if spec.MissedPolicy() != MissedRunOnce {
		t.Fatalf("missed default = %q", spec.MissedPolicy())
	}
	if spec.ID != "nightly-review" {
		t.Fatalf("id = %q, want a slug of the name", spec.ID)
	}
	if got := spec.Timeout.Std(); got != 30*time.Minute {
		t.Fatalf("timeout = %s", got)
	}

	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Spec
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if back.Timeout.Std() != 30*time.Minute || back.Schedule.Expr != "0 3 * * *" {
		t.Fatalf("round trip lost data: %+v", back)
	}

	numeric := []byte(`{"id":"a","name":"a","schedule":{"kind":"interval","expr":"5m"},"prompt":"p","timeout":90}`)
	var seconds Spec
	if err := json.Unmarshal(numeric, &seconds); err != nil {
		t.Fatalf("numeric timeout: %v", err)
	}
	if got := seconds.Timeout.Std(); got != 90*time.Second {
		t.Fatalf("numeric timeout = %s, want 1m30s", got)
	}
}

func TestSpecValidate(t *testing.T) {
	good := testSpec("ok")
	if err := good.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	bad := []Spec{
		{ID: "", Name: "x", Prompt: "p", Schedule: Schedule{Kind: KindCron, Expr: "* * * * *"}},
		{ID: "x", Name: "", Prompt: "p", Schedule: Schedule{Kind: KindCron, Expr: "* * * * *"}},
		{ID: "x", Name: "x", Prompt: "", Schedule: Schedule{Kind: KindCron, Expr: "* * * * *"}},
		{ID: "x", Name: "x", Prompt: "p", Schedule: Schedule{Kind: KindCron, Expr: "bad"}},
		{ID: "x", Name: "x", Prompt: "p", Schedule: Schedule{Kind: "sometimes", Expr: "1h"}},
	}
	for i, spec := range bad {
		if err := spec.Validate(); err == nil {
			t.Fatalf("case %d should fail validation", i)
		}
	}
}

func TestStoreRoundTripAndSeeding(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T00:07:00Z")
	store.SetClock(func() time.Time { return now })

	spec := testSpec("nightly")
	spec.Schedule = Schedule{Kind: KindCron, Expr: "*/15 * * * *"}
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	loaded, ok := reopened.Get("nightly")
	if !ok {
		t.Fatal("task missing after reopen")
	}
	if !loaded.NextDue.Equal(mustTime(t, "2026-01-01T00:15:00Z")) {
		t.Fatalf("nextDue = %s, want the seeded slot", loaded.NextDue)
	}
	if loaded.Name != "nightly" || loaded.Prompt != "do nightly" {
		t.Fatalf("task did not round trip: %+v", loaded)
	}
}

func TestStoreUpsertKeepsHistory(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T00:00:00Z")
	if err := store.Put(testSpec("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.RecordRun("a", StatusOK, "/tmp/a.log", now); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	updated := testSpec("a")
	updated.Prompt = "changed"
	if err := store.Put(updated); err != nil {
		t.Fatalf("Put update: %v", err)
	}
	got, _ := store.Get("a")
	if got.Prompt != "changed" {
		t.Fatalf("prompt = %q", got.Prompt)
	}
	if got.LastStatus != StatusOK || got.LastRun == nil || got.LastLog != "/tmp/a.log" {
		t.Fatalf("history lost on upsert: %+v", got)
	}
	if want := now.Add(time.Hour); !got.NextDue.Equal(want) {
		t.Fatalf("nextDue = %s, want %s", got.NextDue, want)
	}
}

func TestStoreAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Put(testSpec("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "automations.json" {
			t.Fatalf("unexpected file %q in the store directory", entry.Name())
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "automations.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var file storeFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("store file is not valid JSON: %v\n%s", err, data)
	}
	if file.Version != 1 || len(file.Specs) != 1 {
		t.Fatalf("unexpected store file contents: %+v", file)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("store file should end with a newline")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	const writers = 40
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spec := testSpec(fmt.Sprintf("task-%02d", i))
			if err := store.Put(spec); err != nil {
				t.Errorf("Put %d: %v", i, err)
				return
			}
			if _, ok := store.Get(spec.ID); !ok {
				t.Errorf("task %s vanished", spec.ID)
			}
		}(i)
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = store.List()
				_ = store.Due(time.Now())
			}
		}()
	}
	wg.Wait()

	if got := len(store.List()); got != writers {
		t.Fatalf("listed %d tasks, want %d", got, writers)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen after concurrent writes: %v", err)
	}
	if got := len(reopened.List()); got != writers {
		t.Fatalf("reopened %d tasks, want %d", got, writers)
	}
}

func TestStoreDeleteAndToggle(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Put(testSpec("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.SetEnabled("a", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if spec, _ := store.Get("a"); spec.Enabled {
		t.Fatal("task should be disabled")
	}
	if err := store.SetEnabled("missing", true); err == nil {
		t.Fatal("SetEnabled on a missing task should fail")
	}
	removed, err := store.Delete("a")
	if err != nil || !removed {
		t.Fatalf("Delete = %v, %v", removed, err)
	}
	if removed, err := store.Delete("a"); err != nil || removed {
		t.Fatalf("second Delete = %v, %v", removed, err)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Nightly Review": "nightly-review",
		"  ":             "task",
		"a/b c!":         "ab-c",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Fatalf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
