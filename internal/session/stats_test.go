package session

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStatsSession(t *testing.T) *Session {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s, err := store.NewSession("stats")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return s
}

func assertCostUSD(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("CostUSD = %v, want %v", got, want)
	}
}

func TestAppendStatsLatestRoundtrip(t *testing.T) {
	s := newStatsSession(t)
	want := SessionStats{
		PromptTokens:     120,
		CompletionTokens: 30,
		CachedTokens:     20,
		Calls:            2,
		CostUSD:          0.0123,
		CostSource:       "reported",
		Updated:          "2026-11-20T00:00:00Z",
	}
	if err := s.AppendStats(want); err != nil {
		t.Fatalf("AppendStats: %v", err)
	}
	got, ok := s.LatestStats()
	if !ok {
		t.Fatal("LatestStats: no stats record")
	}
	if got != want {
		t.Fatalf("LatestStats = %+v, want %+v", got, want)
	}
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	records = withoutSchemaHeader(records)
	if len(records) != 2 || records[1].Kind != kindStats {
		t.Fatalf("records = %+v, want header + stats", records)
	}
}

func TestMultipleStatsLatestAndAggregate(t *testing.T) {
	s := newStatsSession(t)
	first := SessionStats{
		PromptTokens: 100, CompletionTokens: 10, CachedTokens: 5,
		Calls: 1, CostUSD: 0.01, CostSource: "reported", Updated: "t1",
	}
	second := SessionStats{
		PromptTokens: 200, CompletionTokens: 20, CachedTokens: 15,
		Calls: 3, CostUSD: 0.02, CostSource: "estimated", Updated: "t2",
	}
	if err := s.AppendStats(first); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendStats(second); err != nil {
		t.Fatal(err)
	}

	latest, ok := s.LatestStats()
	if !ok || latest != second {
		t.Fatalf("LatestStats = %+v, %v; want %+v", latest, ok, second)
	}

	total := s.AggregateStats()
	if total.PromptTokens != 300 || total.CompletionTokens != 30 ||
		total.CachedTokens != 20 || total.Calls != 4 ||
		total.CostSource != "estimated" || total.Updated != "t2" {
		t.Fatalf("AggregateStats = %+v", total)
	}
	assertCostUSD(t, total.CostUSD, 0.03)
}

func TestLatestStatsPrefersActiveBranch(t *testing.T) {
	dir := t.TempDir()
	branchA := SessionStats{Calls: 1, CostSource: "reported", Updated: "A"}
	branchB := SessionStats{Calls: 9, CostSource: "estimated", Updated: "B"}
	// n05 位于不同分支上，但按文件顺序是最新的统计记录。
	// 活动叶节点（n06）往回经过 n03，因此 n03 必须胜出。
	records := []Record{
		{ID: "n01", Kind: KindHeader, Data: json.RawMessage(`{}`)},
		{ID: "n02", Parent: "n01", Kind: KindUser, Data: json.RawMessage(`"hi"`)},
		{ID: "n03", Parent: "n02", Kind: kindStats, Data: mustStatsJSON(t, branchA)},
		{ID: "n04", Parent: "n01", Kind: KindUser, Data: json.RawMessage(`"branch b"`)},
		{ID: "n05", Parent: "n04", Kind: kindStats, Data: mustStatsJSON(t, branchB)},
		{ID: "n06", Parent: "n03", Kind: KindAssistant, Data: json.RawMessage(`"reply"`)},
	}
	path := filepath.Join(dir, "branched"+fileExt)
	writeStatsRawSession(t, path, records)
	s, err := OpenSession(dir, "branched")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.LatestStats()
	if !ok || got != branchA {
		t.Fatalf("LatestStats = %+v, %v; want active-branch %+v", got, ok, branchA)
	}
	// 回退路径覆盖没有自己统计记录的分支。
	withoutActive := []Record{
		{ID: "n01", Kind: KindHeader, Data: json.RawMessage(`{}`)},
		{ID: "n02", Parent: "n01", Kind: KindUser, Data: json.RawMessage(`"hi"`)},
		{ID: "n03", Parent: "n01", Kind: KindUser, Data: json.RawMessage(`"branch b"`)},
		{ID: "n04", Parent: "n03", Kind: kindStats, Data: mustStatsJSON(t, branchB)},
		{ID: "n05", Parent: "n02", Kind: KindAssistant, Data: json.RawMessage(`"reply"`)},
	}
	path2 := filepath.Join(dir, "fallback"+fileExt)
	writeStatsRawSession(t, path2, withoutActive)
	s2, err := OpenSession(dir, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	got, ok = s2.LatestStats()
	if !ok || got != branchB {
		t.Fatalf("fallback LatestStats = %+v, %v; want newest %+v", got, ok, branchB)
	}
}

func TestAppendUsageOrderedAndBounded(t *testing.T) {
	s := newStatsSession(t)
	longModel := strings.Repeat("界", modelLimit+24)
	input := []UsageRecord{
		{Model: "gpt-4o", Input: 10, Output: 5, Cached: 2, TS: "t1"},
		{Model: longModel, Input: 20, Output: 6, Cached: 3, TS: "t2"},
		{Model: "claude", Input: 30, Output: 7, Cached: 4, TS: "t3"},
	}
	for _, u := range input {
		if err := s.AppendUsage(u); err != nil {
			t.Fatalf("AppendUsage: %v", err)
		}
	}
	got := s.UsageRecords()
	if len(got) != 3 {
		t.Fatalf("UsageRecords len = %d, want 3", len(got))
	}
	for i, want := range input {
		if got[i].Input != want.Input || got[i].Output != want.Output ||
			got[i].Cached != want.Cached || got[i].TS != want.TS {
			t.Fatalf("usage[%d] = %+v, want %+v", i, got[i], want)
		}
	}
	if got[0].Model != "gpt-4o" || got[2].Model != "claude" {
		t.Fatalf("usage models = %q, %q", got[0].Model, got[2].Model)
	}
	if runes := []rune(got[1].Model); len(runes) != modelLimit {
		t.Fatalf("bounded model runes = %d, want %d", len(runes), modelLimit)
	}
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range records {
		if record.Kind == kindUsage {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("usage records on disk = %d, want 3", count)
	}
}

func TestStatsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSession("durable")
	if err != nil {
		t.Fatal(err)
	}
	want := SessionStats{
		PromptTokens: 77, CompletionTokens: 11, Calls: 2,
		CostUSD: 0.5, CostSource: "estimated", Updated: "t9",
	}
	if err := s.AppendStats(want); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(UsageRecord{Model: "m", Input: 7, Output: 3, TS: "t9"}); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSession(dir, s.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	latest, ok := reopened.LatestStats()
	if !ok || latest != want {
		t.Fatalf("reopened LatestStats = %+v, %v; want %+v", latest, ok, want)
	}
	total := reopened.AggregateStats()
	if total != want {
		t.Fatalf("reopened AggregateStats = %+v, want %+v", total, want)
	}
	usage := reopened.UsageRecords()
	if len(usage) != 1 || usage[0].Input != 7 || usage[0].Output != 3 {
		t.Fatalf("reopened UsageRecords = %+v", usage)
	}
}

func TestEmptySessionHasNoStatsOrUsage(t *testing.T) {
	s := newStatsSession(t)
	if _, ok := s.LatestStats(); ok {
		t.Fatal("empty session reported stats")
	}
	if got := s.AggregateStats(); got != (SessionStats{}) {
		t.Fatalf("empty AggregateStats = %+v, want zero", got)
	}
	if got := s.UsageRecords(); len(got) != 0 {
		t.Fatalf("empty UsageRecords = %+v, want none", got)
	}
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Kind == kindStats || record.Kind == kindUsage {
			t.Fatalf("empty session has %q record", record.Kind)
		}
	}
}

func TestStatsRecordsDoNotLeakSecrets(t *testing.T) {
	s := newStatsSession(t)
	secret := "sk-ant-SECRETVALUE123"
	if err := s.AppendStats(SessionStats{
		Calls: 1, CostUSD: 0.5, CostSource: secret, Updated: secret,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(UsageRecord{Model: secret, Input: 1, Output: 1, TS: secret}); err != nil {
		t.Fatal(err)
	}
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	var persisted strings.Builder
	for _, record := range records {
		if record.Kind == kindStats || record.Kind == kindUsage {
			persisted.Write(record.Data)
			persisted.WriteByte('\n')
		}
	}
	if strings.Contains(persisted.String(), secret) {
		t.Fatalf("secret leaked into numeric records: %s", persisted.String())
	}
	latest, ok := s.LatestStats()
	if !ok || latest.CostSource != "" {
		t.Fatalf("CostSource = %q, want dropped %v", latest.CostSource, ok)
	}
	if u := s.UsageRecords(); len(u) != 1 || strings.Contains(u[0].Model, "SECRET") {
		t.Fatalf("usage record kept a secret: %+v", u)
	}
}

func mustStatsJSON(t *testing.T, st SessionStats) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeStatsRawSession(t *testing.T, path string, records []Record) {
	t.Helper()
	var builder strings.Builder
	for _, record := range records {
		record.TS = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(line)
		builder.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
