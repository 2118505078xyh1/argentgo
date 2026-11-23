package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func testInboxApp(t *testing.T) (App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	storage := t.TempDir()
	var out, errOut bytes.Buffer
	app := App{
		IO: IO{
			Out: &out, Err: &errOut, Env: func(string) string { return "" },
			HasTerminal: func() bool { return false },
		},
		StorageDir: storage,
	}
	_ = filepath.Join(storage, "inbox.jsonl")
	return app, &out, &errOut
}

func TestInboxCommandAddAndList(t *testing.T) {
	app, out, errOut := testInboxApp(t)
	if code := runInbox(app, context.Background(), []string{"add", "--steer", "hello", "world"}); code != 0 {
		t.Fatalf("add code = %d, err=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "enqueued") {
		t.Fatalf("add output = %q", out.String())
	}
	out.Reset()
	if code := runInbox(app, context.Background(), []string{"list"}); code != 0 {
		t.Fatalf("list code = %d", code)
	}
	if !strings.Contains(out.String(), "steer") || !strings.Contains(out.String(), "hello world") {
		t.Fatalf("list output = %q", out.String())
	}
}

func TestInboxCommandEmpty(t *testing.T) {
	app, out, _ := testInboxApp(t)
	if code := runInbox(app, context.Background(), []string{"list"}); code != 0 {
		t.Fatalf("list code = %d", code)
	}
	if !strings.Contains(out.String(), "empty") {
		t.Fatalf("empty output = %q", out.String())
	}
}

func TestInboxCommandCancel(t *testing.T) {
	app, out, errOut := testInboxApp(t)
	runInbox(app, context.Background(), []string{"add", "cancel me"})
	out.Reset()
	if code := runInbox(app, context.Background(), []string{"cancel", "missing-id"}); code == 0 {
		t.Fatal("cancel missing should fail")
	}
	if errOut.String() == "" {
		t.Fatal("cancel missing should print error")
	}
}

func TestInboxCommandHelp(t *testing.T) {
	app, out, _ := testInboxApp(t)
	if code := runInbox(app, context.Background(), []string{"--help"}); code != 0 {
		t.Fatalf("help code = %d", code)
	}
	if !strings.Contains(out.String(), "usage: argentgo inbox") {
		t.Fatalf("help output = %q", out.String())
	}
}

func TestInboxCommandPersists(t *testing.T) {
	app, _, _ := testInboxApp(t)
	runInbox(app, context.Background(), []string{"add", "persist me"})
	// 用相同 storage 重建 App，验证从磁盘恢复。
	app2 := App{
		IO:         app.IO,
		StorageDir: app.StorageDir,
	}
	var out bytes.Buffer
	app2.IO.Out = &out
	if code := runInbox(app2, context.Background(), []string{"list"}); code != 0 {
		t.Fatalf("list code = %d", code)
	}
	if !strings.Contains(out.String(), "persist me") {
		t.Fatalf("persisted item missing: %q", out.String())
	}
}
