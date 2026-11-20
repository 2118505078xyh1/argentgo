package tui

import (
	"strings"
	"testing"
	"time"
)

func TestHeaderShowsContextPercentAndCostOnlyWhenConfigured(t *testing.T) {
	m := New(Config{Model: "test-model"})
	m.now = func() time.Time { return m.started }
	m.SetUsage(230, 0)

	before := m.Frame(80, 24).RowText(0)
	if strings.Contains(before, "ctx") || strings.Contains(before, "$") {
		t.Fatalf("the default header must not show context metadata: %q", before)
	}

	m.SetContextInfo(1000, "$0.01")
	after := m.Frame(80, 24).RowText(0)
	for _, want := range []string{"ctx " + contextBar(23, contextBarCells) + " 23%", "$0.01"} {
		if !strings.Contains(after, want) {
			t.Fatalf("header %q is missing %q", after, want)
		}
	}

	m.SetContextInfo(0, "")
	restored := m.Frame(80, 24).RowText(0)
	if restored != before {
		t.Fatalf("clearing context info must restore the original header:\n%q\n%q", before, restored)
	}
}

func TestHeaderContextPercentIsClamped(t *testing.T) {
	m := New(Config{})
	m.now = func() time.Time { return m.started }
	m.SetUsage(900, 200)
	m.SetContextInfo(1000, "")
	want := "ctx " + contextBar(100, contextBarCells) + " 100%"
	if got := m.Frame(80, 24).RowText(0); !strings.Contains(got, want) {
		t.Fatalf("overbudget usage should clamp to %q: %q", want, got)
	}
}
