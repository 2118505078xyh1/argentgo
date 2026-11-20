package tui

import (
	"errors"
	"reflect"
	"runtime"
	"testing"
)

// editorEnvLookup 构建一个由固定 map 支持的注入式查找。它替代
// os.Getenv，使这些测试从不读取真实环境。
func editorEnvLookup(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

// stubEditorLookPath 在测试期间替换 Windows PATH 解析器，
// 使对裸命令的断言不依赖机器的 PATH。
func stubEditorLookPath(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	orig := editorLookPath
	editorLookPath = fn
	t.Cleanup(func() { editorLookPath = orig })
}

func noEditorLookPath(t *testing.T) {
	t.Helper()
	stubEditorLookPath(t, func(string) (string, error) { return "", errors.New("not found") })
}

func TestResolveExternalEditorPrefersVisual(t *testing.T) {
	noEditorLookPath(t)
	lookup := func(key string) (string, bool) {
		switch key {
		case "VISUAL":
			return "vis", true
		case "EDITOR":
			t.Fatal("EDITOR must not be queried when VISUAL is set")
		}
		return "", false
	}
	cmd, args, ok := ResolveExternalEditor(lookup)
	if !ok || cmd != "vis" || len(args) != 0 {
		t.Fatalf("ResolveExternalEditor = (%q, %v, %v), want (\"vis\", [], true)", cmd, args, ok)
	}
}

func TestResolveExternalEditorParsesArguments(t *testing.T) {
	noEditorLookPath(t)
	cmd, args, ok := ResolveExternalEditor(editorEnvLookup(map[string]string{
		"VISUAL": "code --wait --reuse-window",
	}))
	if !ok || cmd != "code" {
		t.Fatalf("ResolveExternalEditor = (%q, %v, %v), want cmd \"code\"", cmd, args, ok)
	}
	want := []string{"--wait", "--reuse-window"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestResolveExternalEditorFallsBackToEditor(t *testing.T) {
	noEditorLookPath(t)
	cmd, args, ok := ResolveExternalEditor(editorEnvLookup(map[string]string{
		"EDITOR": "vim -f",
	}))
	if !ok || cmd != "vim" || !reflect.DeepEqual(args, []string{"-f"}) {
		t.Fatalf("ResolveExternalEditor = (%q, %v, %v), want (\"vim\", [\"-f\"], true)", cmd, args, ok)
	}
}

func TestResolveExternalEditorUnsetReturnsFalse(t *testing.T) {
	cmd, args, ok := ResolveExternalEditor(editorEnvLookup(nil))
	if ok || cmd != "" || args != nil {
		t.Fatalf("ResolveExternalEditor = (%q, %v, %v), want (\"\", nil, false)", cmd, args, ok)
	}
}

func TestResolveExternalEditorEmptyCommandReturnsFalse(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		cmd, args, ok := ResolveExternalEditor(editorEnvLookup(map[string]string{"VISUAL": raw}))
		if ok || cmd != "" || args != nil {
			t.Errorf("raw %q: got (%q, %v, %v), want (\"\", nil, false)", raw, cmd, args, ok)
		}
	}
}

func TestResolveExternalEditorNilLookupReturnsFalse(t *testing.T) {
	cmd, args, ok := ResolveExternalEditor(nil)
	if ok || cmd != "" || args != nil {
		t.Fatalf("ResolveExternalEditor(nil) = (%q, %v, %v), want (\"\", nil, false)", cmd, args, ok)
	}
}

func TestResolveExternalEditorKeepsQuotedWindowsPath(t *testing.T) {
	noEditorLookPath(t)
	cmd, args, ok := ResolveExternalEditor(editorEnvLookup(map[string]string{
		"VISUAL": `"C:\Program Files\Editor\code.cmd" --wait`,
	}))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	// 带引号的路径保持为单一字段且原样保留。
	if cmd != `C:\Program Files\Editor\code.cmd` {
		t.Errorf("cmd = %q, want the quoted path", cmd)
	}
	if !reflect.DeepEqual(args, []string{"--wait"}) {
		t.Errorf("args = %v, want [\"--wait\"]", args)
	}
}

func TestResolveExternalEditorResolvesWindowsShim(t *testing.T) {
	orig := editorLookPath
	defer func() { editorLookPath = orig }()
	editorLookPath = func(name string) (string, error) {
		if name == "code" {
			return `C:\Apps\code.cmd`, nil
		}
		return "", errors.New("not found")
	}

	cmd, args, ok := ResolveExternalEditor(editorEnvLookup(map[string]string{
		"VISUAL": "code --wait",
	}))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if runtime.GOOS == "windows" {
		if cmd != `C:\Apps\code.cmd` {
			t.Errorf("cmd = %q, want the resolved shim path", cmd)
		}
	} else if cmd != "code" {
		t.Errorf("cmd = %q, want the bare name on non-Windows", cmd)
	}
	if !reflect.DeepEqual(args, []string{"--wait"}) {
		t.Errorf("args = %v, want [\"--wait\"]", args)
	}
}

func TestResolveExternalEditorKeepsNameWhenLookPathFails(t *testing.T) {
	orig := editorLookPath
	defer func() { editorLookPath = orig }()
	editorLookPath = func(string) (string, error) { return "", errors.New("not found") }

	cmd, _, ok := ResolveExternalEditor(editorEnvLookup(map[string]string{"VISUAL": "code"}))
	if !ok || cmd != "code" {
		t.Fatalf("ResolveExternalEditor = (%q, _, %v), want (\"code\", _, true)", cmd, ok)
	}
}
