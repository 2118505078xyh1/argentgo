package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestModelsProfile(t *testing.T) {
	var out, errOut bytes.Buffer
	app := App{IO: IO{Out: &out, Err: &errOut, Env: func(string) string { return "" }}}
	if code := runModels(app, nil, []string{"--profile", "openai", "gpt-4o"}); code != 0 {
		t.Fatalf("code = %d, err = %s", code, errOut.String())
	}
	got := out.String()
	for _, key := range []string{"SupportsThinking", "SupportsImage", "SupportsTools", "ContextWindow"} {
		if !strings.Contains(got, key) {
			t.Fatalf("profile output missing %q: %s", key, got)
		}
	}
}

func TestModelsProfileNeedsProvider(t *testing.T) {
	var out, errOut bytes.Buffer
	app := App{IO: IO{Out: &out, Err: &errOut, Env: func(string) string { return "" }}}
	if code := runModels(app, nil, []string{"--profile"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
