package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPTYInteractiveEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"unset", "", false},
		{"zero", "0", false},
		{"false", "false", false},
		{"one", "1", true},
		{"true", "true", true},
		{"trimmed uppercase", " TRUE ", true},
		{"garbage", "yes", false},
	}
	for _, tc := range tests {
		got := ptyInteractiveEnabled(func(string) string { return tc.value })
		if got != tc.want {
			t.Errorf("%s: enabled=%v, want %v", tc.name, got, tc.want)
		}
	}
	if ptyInteractiveEnabled(nil) {
		t.Error("nil lookup must be disabled")
	}
}

func TestParsePTYArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantCols  uint16
		wantRows  uint16
		wantLimit int
		wantCmd   []string
		wantErr   bool
	}{
		{"defaults", []string{"python", "-i"}, 120, 40, defaultPTYMaxBytes, []string{"python", "-i"}, false},
		{"explicit separator", []string{"--", "cmd", "/C", "echo hi"}, 120, 40, defaultPTYMaxBytes, []string{"cmd", "/C", "echo hi"}, false},
		{"sizes and limit", []string{"--cols", "80", "--rows", "24", "--max-bytes", "999", "sh"}, 80, 24, 999, []string{"sh"}, false},
		{"clamped size", []string{"--cols", "999999", "sh"}, ^uint16(0), 40, defaultPTYMaxBytes, []string{"sh"}, false},
		{"missing value", []string{"--cols"}, 0, 0, 0, nil, true},
		{"bad value", []string{"--rows", "x", "sh"}, 0, 0, 0, nil, true},
		{"unknown option", []string{"--bogus", "sh"}, 0, 0, 0, nil, true},
	}
	for _, tc := range tests {
		size, limit, cmd, err := parsePTYArgs(tc.args)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if size.Cols != tc.wantCols || size.Rows != tc.wantRows || limit != tc.wantLimit {
			t.Errorf("%s: size=%+v limit=%d", tc.name, size, limit)
		}
		if strings.Join(cmd, "|") != strings.Join(tc.wantCmd, "|") {
			t.Errorf("%s: cmd=%q, want %q", tc.name, cmd, tc.wantCmd)
		}
	}
}

func TestPTYCommandDisabledByDefault(t *testing.T) {
	var out bytes.Buffer
	app := App{IO: IO{
		In:  strings.NewReader(""),
		Out: &out,
		Err: &out,
		Env: func(string) string { return "" },
	}}
	if code := runPTY(app, nil, []string{"echo", "hi"}); code != 2 {
		t.Fatalf("disabled pty returned %d, want 2", code)
	}
	if !strings.Contains(out.String(), "ARGENT_PTY_INTERACTIVE") {
		t.Fatalf("disabled hint missing: %q", out.String())
	}
}

func TestPTYUsageDocumentsOptIn(t *testing.T) {
	var out bytes.Buffer
	ptyUsage(&out)
	if !strings.Contains(out.String(), "ARGENT_PTY_INTERACTIVE=1") {
		t.Fatalf("usage must mention the opt-in switch: %q", out.String())
	}
}
