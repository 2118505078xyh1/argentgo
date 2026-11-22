package memory

import (
	"testing"
	"time"
)

func TestConsolidateMergesNearDuplicates(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	entries := []Memory{
		{ID: "a", Text: "database uses postgres primary replica", Source: "src1", TS: now},
		{ID: "b", Text: "database uses postgres primary replica config", Source: "src2", TS: now.Add(-time.Hour)},
		{ID: "c", Text: "unrelated fact about kubernetes", TS: now.Add(-2 * time.Hour)},
	}
	out, stats := Consolidate(entries)
	if stats.Before != 3 || stats.Merged != 1 || stats.After != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	// 合并项来源应含两者，且 TS 取较新的。
	var merged bool
	for _, m := range out {
		if m.ID == "a" {
			merged = true
			if m.Source != "src1,src2" {
				t.Fatalf("merged source = %q", m.Source)
			}
			if !m.TS.Equal(now) {
				t.Fatalf("merged TS = %v, want newer", m.TS)
			}
		}
	}
	if !merged {
		t.Fatalf("merged entry missing: %+v", out)
	}
	// 新近优先。
	if out[0].ID != "a" {
		t.Fatalf("not sorted by recency: %+v", out)
	}
}

func TestConsolidateKeepsInvalidatedAndPure(t *testing.T) {
	entries := []Memory{
		{ID: "a", Text: "dup fact one two three", TS: time.Now()},
		{ID: "b", Text: "dup fact one two three", Invalidated: "wrong", TS: time.Now()},
	}
	out, stats := Consolidate(entries)
	if stats.Merged != 0 || len(out) != 2 {
		t.Fatalf("invalidated entry should not be merged: %+v / %+v", out, stats)
	}
	// 入参未被修改。
	if entries[0].Source != "" || entries[1].ID != "b" {
		t.Fatalf("input was mutated: %+v", entries)
	}
}

func TestConsolidateEmpty(t *testing.T) {
	out, stats := Consolidate(nil)
	if len(out) != 0 || stats.Before != 0 || stats.After != 0 {
		t.Fatalf("empty = %+v / %+v", out, stats)
	}
}
