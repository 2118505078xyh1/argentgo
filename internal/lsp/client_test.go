package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientHandshakeAnswersServerRequests(t *testing.T) {
	client, recorder := startPipeClient(t, fakeOptions{})
	if client.ServerName() != "fake" {
		t.Fatalf("server name = %q", client.ServerName())
	}
	waitFor(t, "initialized notification", recorder.isInitialized)
	waitFor(t, "workspace/configuration answer", func() bool {
		_, ok := recorder.answer("workspace/configuration")
		return ok
	})
	answer, _ := recorder.answer("workspace/configuration")
	if strings.TrimSpace(answer) != "[]" {
		t.Fatalf("workspace/configuration answer = %q, want []", answer)
	}
	if _, ok := recorder.answer("client/registerCapability"); !ok {
		t.Fatal("client/registerCapability was never answered")
	}
}

func TestDiagnosticsFollowOpenAndChange(t *testing.T) {
	client, recorder := startPipeClient(t, fakeOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, path, "package main\n")
	uri, err := client.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if uri != PathToURI(path) {
		t.Fatalf("uri = %q, want %q", uri, PathToURI(path))
	}
	if got := client.LanguageID(uri); got != "go" {
		t.Fatalf("language id = %q, want go", got)
	}

	diagnostics, err := client.Diagnostics(ctx, uri, 3*time.Second)
	if err != nil {
		t.Fatalf("Diagnostics after Open: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Severity != "error" || diagnostics[0].Message != "boom" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if diagnostics[0].URI != uri || diagnostics[0].Range.Start.Character != 2 || diagnostics[0].Source != "fake" {
		t.Fatalf("diagnostic fields = %+v", diagnostics[0])
	}

	if err := client.DidChange(ctx, uri, "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("DidChange: %v", err)
	}
	if got := client.Version(uri); got != 2 {
		t.Fatalf("version after change = %d, want 2", got)
	}
	diagnostics, err = client.Diagnostics(ctx, uri, 3*time.Second)
	if err != nil {
		t.Fatalf("Diagnostics after change: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Severity != "warning" || diagnostics[0].Message != "changed" {
		t.Fatalf("diagnostics after change = %+v", diagnostics)
	}

	if got := recorder.recordedVersions(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("versions seen by the server = %v, want [1 2]", got)
	}
}

func TestDidSavePublishesAnEmptyDiagnosticSet(t *testing.T) {
	client, _ := startPipeClient(t, fakeOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, path, "package main\n")
	uri, err := client.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := client.Diagnostics(ctx, uri, 3*time.Second); err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if err := client.DidSave(ctx, uri); err != nil {
		t.Fatalf("DidSave: %v", err)
	}
	diagnostics, err := client.Diagnostics(ctx, uri, 3*time.Second)
	if err != nil {
		t.Fatalf("Diagnostics after save: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics after save = %+v, want none", diagnostics)
	}
}

func TestDiagnosticsTimesOutWithoutAPublish(t *testing.T) {
	client, _ := startPipeClient(t, fakeOptions{neverPublish: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	uri := PathToURI(filepath.Join(t.TempDir(), "main.go"))
	if err := client.DidOpen(ctx, uri, "go", "package main\n"); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	start := time.Now()
	diagnostics, err := client.Diagnostics(ctx, uri, 150*time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Diagnostics error = %v, want ErrTimeout", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Diagnostics returned after %s, before the wait expired", elapsed)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diagnostics)
	}
}

func TestDiagnosticsRespectsContext(t *testing.T) {
	client, _ := startPipeClient(t, fakeOptions{neverPublish: true})
	uri := PathToURI(filepath.Join(t.TempDir(), "main.go"))
	openCtx, openCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer openCancel()
	if err := client.DidOpen(openCtx, uri, "go", "package main\n"); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.Diagnostics(ctx, uri, 5*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Diagnostics error = %v, want the context error", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Diagnostics ignored the context for %s", elapsed)
	}
}

func TestDiagnosticsWithoutWaitReturnsTheCache(t *testing.T) {
	client, _ := startPipeClient(t, fakeOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, path, "package main\n")
	uri, err := client.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := client.Diagnostics(ctx, uri, 3*time.Second); err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	diagnostics, err := client.Diagnostics(context.Background(), uri, 0)
	if err != nil {
		t.Fatalf("Diagnostics without wait: %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("cached diagnostics = %+v", diagnostics)
	}
}

func TestDefinitionAndHover(t *testing.T) {
	client, _ := startPipeClient(t, fakeOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, path, "package main\n")
	position := Human(4, 2)

	locations, err := client.DefinitionAt(ctx, path, position)
	if err != nil {
		t.Fatalf("DefinitionAt: %v", err)
	}
	if len(locations) != 1 || locations[0].URI != "file:///defs.go" {
		t.Fatalf("locations = %+v", locations)
	}
	if got := locations[0].Range.Start.String(); got != "4:2" {
		t.Fatalf("range start = %q, want 4:2", got)
	}

	hover, err := client.HoverAt(ctx, path, position)
	if err != nil {
		t.Fatalf("HoverAt: %v", err)
	}
	if hover == nil || hover.Contents != "**hover**" {
		t.Fatalf("hover = %+v", hover)
	}
	if hover.Range == nil || hover.Range.End.Character != 3 {
		t.Fatalf("hover range = %+v", hover.Range)
	}
}

func TestStartRejectsAServerWithoutCommand(t *testing.T) {
	if _, err := Start(context.Background(), Server{Name: "ghost"}, t.TempDir()); !errors.Is(err, ErrNoCommand) {
		t.Fatalf("Start error = %v, want ErrNoCommand", err)
	}
}

func TestStartSpawnsARealProcess(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	root := t.TempDir()
	server := Server{
		Name:        "fake-process",
		Command:     binary,
		Env:         []string{"ARGENT_GO_LSP_HELPER=1"},
		LanguageIDs: []string{"go"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := Start(ctx, server, root)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	path := filepath.Join(root, "main.go")
	writeTestFile(t, path, "package main\n")
	uri, err := client.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	diagnostics, err := client.Diagnostics(ctx, uri, 5*time.Second)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Message != "boom" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v\nserver stderr:\n%s", err, client.stderrText())
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
