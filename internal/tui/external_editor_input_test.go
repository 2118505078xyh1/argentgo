package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// stubEditorRun 把编辑器进程启动器换成假对象。这些测试中
// 生产代码从不启动真实编辑器。
func stubEditorRun(t *testing.T, fn func(cmd string, args []string, path string) error) {
	t.Helper()
	orig := externalEditorRun
	externalEditorRun = fn
	t.Cleanup(func() { externalEditorRun = orig })
}

// editorConfigEnv 是模型测试使用的注入式环境查找。
func editorConfigEnv(value string) func(string) string {
	return func(key string) string {
		if key == "EDITOR" {
			return value
		}
		return ""
	}
}

func TestDecoderReadsCtrlGAsExternalEditorKey(t *testing.T) {
	keys := (&Decoder{}).Feed([]byte{0x07})
	if len(keys) != 1 || keys[0].Kind != KindCtrlG {
		t.Fatalf("ctrl+g decoded to %v, want KindCtrlG", keys)
	}

	// 现有控制键必须保持其解码后的含义。
	if keys := (&Decoder{}).Feed([]byte{0x05}); len(keys) != 1 || keys[0].Kind != KindCtrlE {
		t.Fatalf("ctrl+e decoded to %v, want KindCtrlE", keys)
	}
}

func TestCtrlGWritesPromptAndLoadsEditedTextBack(t *testing.T) {
	noEditorLookPath(t)
	var gotCmd string
	var gotArgs []string
	var gotPath string
	var gotInitial string
	stubEditorRun(t, func(cmd string, args []string, path string) error {
		gotCmd, gotArgs, gotPath = cmd, args, path
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		gotInitial = string(data)
		return os.WriteFile(path, []byte("edited long prompt\nsecond line"), 0o600)
	})

	m := New(Config{Locale: "en", Env: editorConfigEnv("fake-editor --wait")})
	m.editor.SetText("original short")
	if action := m.HandleKey(Key{Kind: KindCtrlG}); action != ActionNone {
		t.Fatalf("action = %v, want ActionNone", action)
	}

	if gotCmd != "fake-editor" {
		t.Errorf("cmd = %q, want fake-editor", gotCmd)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--wait" {
		t.Errorf("args = %v, want [--wait]", gotArgs)
	}
	if gotInitial != "original short" {
		t.Errorf("temp file initial content = %q, want the original prompt", gotInitial)
	}
	if got := m.Input(); got != "edited long prompt\nsecond line" {
		t.Errorf("input = %q, want the edited text", got)
	}
	if gotPath == "" {
		t.Fatal("the editor was never launched")
	}
	if _, err := os.Stat(gotPath); !os.IsNotExist(err) {
		t.Errorf("temporary file %q was not removed: %v", gotPath, err)
	}
}

func TestCtrlGWithoutConfiguredEditorKeepsPrompt(t *testing.T) {
	called := false
	stubEditorRun(t, func(string, []string, string) error {
		called = true
		return nil
	})

	m := New(Config{Locale: "en", Env: editorConfigEnv("")})
	m.editor.SetText("keep me")
	if action := m.HandleKey(Key{Kind: KindCtrlG}); action != ActionNone {
		t.Fatalf("action = %v, want ActionNone", action)
	}
	if called {
		t.Error("an editor was launched with no $EDITOR configured")
	}
	if got := m.Input(); got != "keep me" {
		t.Errorf("input = %q, want the original prompt", got)
	}
}

func TestCtrlGEditorFailureKeepsPrompt(t *testing.T) {
	noEditorLookPath(t)
	var gotPath string
	stubEditorRun(t, func(_ string, _ []string, path string) error {
		gotPath = path
		return errors.New("editor exited with an error")
	})

	m := New(Config{Locale: "en", Env: editorConfigEnv("fake-editor")})
	m.editor.SetText("keep me")
	if action := m.HandleKey(Key{Kind: KindCtrlG}); action != ActionNone {
		t.Fatalf("action = %v, want ActionNone", action)
	}
	if got := m.Input(); got != "keep me" {
		t.Errorf("input = %q, want the original prompt after a failure", got)
	}
	if gotPath == "" {
		t.Fatal("the editor was never launched")
	}
	if _, err := os.Stat(gotPath); !os.IsNotExist(err) {
		t.Errorf("temporary file %q was not removed after a failure: %v", gotPath, err)
	}
}

func TestCtrlGWhileWorkingDoesNotOpenEditor(t *testing.T) {
	called := false
	stubEditorRun(t, func(string, []string, string) error {
		called = true
		return nil
	})

	m := New(Config{Locale: "en", Env: editorConfigEnv("fake-editor")})
	m.editor.SetText("keep me")
	m.working = true
	if action := m.HandleKey(Key{Kind: KindCtrlG}); action != ActionNone {
		t.Fatalf("action = %v, want ActionNone", action)
	}
	if called {
		t.Error("the external editor opened while a turn was running")
	}
	if got := m.Input(); got != "keep me" {
		t.Errorf("input = %q, want the original prompt", got)
	}
}

func TestRunCtrlGSuspendsAndRestoresTheAlternateScreen(t *testing.T) {
	noEditorLookPath(t)
	stubEditorRun(t, func(_ string, _ []string, path string) error {
		return os.WriteFile(path, []byte("from editor"), 0o600)
	})

	reader, writer := io.Pipe()
	var out bytes.Buffer
	ready := make(chan *Model, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := Options{
		IO:      &IO{In: reader, Out: &out},
		Env:     editorConfigEnv("fake-editor"),
		OnReady: func(m *Model) { ready <- m },
	}
	go func() { done <- Run(ctx, opts) }()

	var m *Model
	select {
	case m = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never became ready")
	}

	if _, err := writer.Write([]byte("seed\x07")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "the edited prompt", func() bool { return m.Input() == "from editor" })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	written := out.String()
	if n := strings.Count(written, leaveAltScreen); n != 2 {
		t.Errorf("leave alternate screen count = %d, want 2 (editor + cleanup): %q", n, written)
	}
	if n := strings.Count(written, enterAltScreen); n != 2 {
		t.Errorf("enter alternate screen count = %d, want 2 (startup + editor): %q", n, written)
	}
}
