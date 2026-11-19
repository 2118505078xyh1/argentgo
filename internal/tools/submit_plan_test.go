package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

type planDoc struct {
	Steps     []string `json:"steps"`
	Status    string   `json:"status"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

func readPlanDoc(t *testing.T, path string) planDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc planDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("plan file is not JSON: %v", err)
	}
	return doc
}

func TestSubmitPlanWritesAndReadsSessionState(t *testing.T) {
	stateDir := t.TempDir()
	registry := tools.New(t.TempDir(), false)
	registry.SetSessionDir(stateDir)

	out := mustRun(t, registry, "submit_plan", `{"steps":["first","second"],"status":"review"}`)
	for _, want := range []string{"1. first", "2. second", "status: review", "no steps were executed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("submit_plan = %q, want %q", out, want)
		}
	}

	doc := readPlanDoc(t, filepath.Join(stateDir, "plan.json"))
	if len(doc.Steps) != 2 || doc.Steps[0] != "first" || doc.Status != "review" {
		t.Fatalf("plan file = %+v", doc)
	}
	for _, stamp := range []string{doc.CreatedAt, doc.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339, stamp); err != nil {
			t.Fatalf("timestamp %q is not RFC3339: %v", stamp, err)
		}
	}

	for _, args := range []string{`{}`, `{"steps":[]}`} {
		read := mustRun(t, registry, "submit_plan", args)
		if !strings.Contains(read, "1. first") || !strings.Contains(read, "2. second") {
			t.Fatalf("submit_plan read (%s) = %q", args, read)
		}
	}
}

func TestSubmitPlanKeepsCreatedAtAndDefaultsStatus(t *testing.T) {
	stateDir := t.TempDir()
	registry := tools.New(t.TempDir(), true)
	registry.SetSessionDir(stateDir)

	mustRun(t, registry, "submit_plan", `{"steps":["one"]}`)
	first := readPlanDoc(t, filepath.Join(stateDir, "plan.json"))
	if first.Status != "draft" {
		t.Fatalf("status = %q, want draft", first.Status)
	}
	mustRun(t, registry, "submit_plan", `{"steps":["two"]}`)
	second := readPlanDoc(t, filepath.Join(stateDir, "plan.json"))
	if second.CreatedAt != first.CreatedAt {
		t.Fatal("rewriting the plan changed created_at")
	}
	if len(second.Steps) != 1 || second.Steps[0] != "two" {
		t.Fatalf("plan was not replaced: %+v", second)
	}
}

func TestSubmitPlanRejectsNonStringAndEmptySteps(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	registry.SetSessionDir(t.TempDir())

	for _, args := range []string{`{"steps":[""]}`, `{"steps":["  "]}`, `{"steps":[123]}`, `{`} {
		if _, err := registry.Run(context.Background(), "submit_plan", args); err == nil {
			t.Fatalf("submit_plan accepted invalid args %s", args)
		}
	}
	if _, err := registry.Run(context.Background(), "submit_plan", `{"steps":["ok",""]}`); err == nil || !strings.Contains(err.Error(), "non-empty string") {
		t.Fatalf("err = %v, want a non-empty-string error", err)
	}
	if out := mustRun(t, registry, "submit_plan", `{}`); out != "plan is empty" {
		t.Fatalf("plan after rejected writes = %q", out)
	}
}
