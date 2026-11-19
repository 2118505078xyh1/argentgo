//go:build windows

package launcher

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestWindowsCandidatePaths(t *testing.T) {
	got := windowsCandidates(`C:\Users\me\AppData\Local`)
	want := []candidate{
		{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, "edge"},
		{`C:\Program Files\Microsoft\Edge\Application\msedge.exe`, "edge"},
		{`C:\Program Files\Google\Chrome\Application\chrome.exe`, "chrome"},
		{`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`, "chrome"},
		{`C:\Users\me\AppData\Local\Google\Chrome\Application\chrome.exe`, "chrome"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windowsCandidates() = %#v, want %#v", got, want)
	}
}

func TestWindowsCandidatesSkipMissingLocalAppData(t *testing.T) {
	if got := len(windowsCandidates("")); got != 4 {
		t.Fatalf("windowsCandidates(\"\") has %d entries, want 4", got)
	}
}

func TestOpenUsesEdgeAppFlags(t *testing.T) {
	const edge = `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`
	var captured *exec.Cmd
	inject(t, func(path string) (string, error) {
		if path == edge {
			return path, nil
		}
		return "", errors.New("not found")
	}, func(cmd *exec.Cmd) error {
		captured = cmd
		return nil
	})

	if err := Open(context.Background(), "http://localhost:1234", `C:\profile`); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if captured == nil {
		t.Fatal("start was not called")
	}
	want := []string{
		edge,
		"--app=http://localhost:1234",
		`--user-data-dir=C:\profile`,
		"--no-first-run",
		"--window-size=1280,860",
	}
	if !reflect.DeepEqual(captured.Args, want) {
		t.Fatalf("edge args = %q, want %q", captured.Args, want)
	}
	if got := Name(); got != "edge" {
		t.Fatalf("Name() = %q, want edge", got)
	}
}

func TestOpenUsesChromeAppFlagsWhenEdgeIsAbsent(t *testing.T) {
	const chrome = `C:\Program Files\Google\Chrome\Application\chrome.exe`
	var captured *exec.Cmd
	inject(t, func(path string) (string, error) {
		if path == chrome {
			return path, nil
		}
		return "", errors.New("not found")
	}, func(cmd *exec.Cmd) error {
		captured = cmd
		return nil
	})

	if err := Open(context.Background(), "http://localhost:1234", `C:\profile`); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := []string{
		chrome,
		"--app=http://localhost:1234",
		`--user-data-dir=C:\profile`,
		"--no-first-run",
		"--window-size=1280,860",
	}
	if !reflect.DeepEqual(captured.Args, want) {
		t.Fatalf("chrome args = %q, want %q", captured.Args, want)
	}
	if got := Name(); got != "chrome" {
		t.Fatalf("Name() = %q, want chrome", got)
	}
}

func TestOpenPrefersFirstAvailableCandidate(t *testing.T) {
	var captured *exec.Cmd
	inject(t, func(path string) (string, error) { return path, nil }, func(cmd *exec.Cmd) error {
		captured = cmd
		return nil
	})
	if err := Open(context.Background(), "http://x", "p"); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := windowsCandidates("")[0].path
	if captured.Path != want {
		t.Fatalf("chosen binary = %q, want %q", captured.Path, want)
	}
	if got := Name(); got != "edge" {
		t.Fatalf("Name() = %q, want edge for first candidate", got)
	}
}

func TestPrepareUsesCreateNoWindow(t *testing.T) {
	cmd := &exec.Cmd{}
	prepare(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("prepare() did not set CREATE_NO_WINDOW: %#v", cmd.SysProcAttr)
	}
}
