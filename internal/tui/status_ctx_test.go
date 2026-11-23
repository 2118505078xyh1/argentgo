package tui

import (
	"strings"
	"testing"
	"time"
)

func TestStatusShowsContextPercentAndCostOnlyWhenConfigured(t *testing.T) {
	m := New(Config{Model: "test-model"})
	m.now = func() time.Time { return m.started }
	m.SetUsageVisible(true)
	m.SetUsage(230, 0)

	before := m.Frame(80, 24).RowText(22)
	if strings.Contains(before, "ctx") || strings.Contains(before, "$") {
		t.Fatalf("the default status must not show context metadata: %q", before)
	}

	m.SetContextInfo(1000, "$0.01")
	after := m.Frame(80, 24).RowText(22)
	for _, want := range []string{"ctx " + contextBar(23, contextBarCells) + " 23%", "$0.01"} {
		if !strings.Contains(after, want) {
			t.Fatalf("status %q is missing %q", after, want)
		}
	}

	m.SetContextInfo(0, "")
	restored := m.Frame(80, 24).RowText(22)
	if restored != before {
		t.Fatalf("clearing context info must restore the original status:\n%q\n%q", before, restored)
	}
}

func TestStatusContextPercentIsClamped(t *testing.T) {
	m := New(Config{})
	m.now = func() time.Time { return m.started }
	m.SetUsageVisible(true)
	m.SetUsage(900, 200)
	m.SetContextInfo(1000, "")
	want := "ctx " + contextBar(100, contextBarCells) + " 100%"
	if got := m.Frame(80, 24).RowText(22); !strings.Contains(got, want) {
		t.Fatalf("overbudget usage should clamp to %q: %q", want, got)
	}
}
