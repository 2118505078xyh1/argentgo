package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// spillTestEnv builds a stdio-style env func from a sparse map.
func spillTestEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func writeSpillTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
}

// spillArtifactIn reports the single canonical artifact under dir (the app
// convention directory) and fails if there is not exactly one.
func spillArtifactIn(t *testing.T, dir string) (string, string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d artifacts in %q, want 1", len(entries), dir)
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "tool-output-") || !strings.HasSuffix(name, ".log") {
		t.Fatalf("unexpected artifact name %q", name)
	}
	return filepath.Join(dir, name), name
}

func TestOutputSpillEnabledTruthyValues(t *testing.T) {
	for raw, want := range map[string]bool{
		"1": true, "true": true, "YES": true, " on ": true,
		"": false, "0": false, "false": false, "no": false, "off": false, "junk": false,
	} {
		if got := outputSpillEnabled(raw); got != want {
			t.Errorf("outputSpillEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}

// No opt-in (nil env and empty env alike) must leave ctx untouched: large tool
// output follows the ordinary clipTo path and no artifact directory appears.
func TestConfigureOutputSpillDefaultLeavesOutputUnchanged(t *testing.T) {
	storage := t.TempDir()
	root := t.TempDir()
	big := strings.Repeat("abcdefghij", 30_000) // 300 KiB, over the default spill threshold
	writeSpillTestFile(t, root, "big.txt", big)
	reg := tools.New(root, true)

	for name, env := range map[string]func(string) string{
		"nil env":   nil,
		"empty env": spillTestEnv(nil),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := configureOutputSpill(context.Background(), env, storage)
			out, err := reg.Run(ctx, "read", `{"path":"big.txt"}`)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if strings.Contains(out, "bytes elided") {
				t.Fatal("default run produced a spill marker")
			}
			if !strings.Contains(out, "[truncated]") {
				t.Fatal("default run did not use the ordinary truncation path")
			}
		})
	}
	if _, err := os.Stat(outputSpillArtifactDir(storage)); !os.IsNotExist(err) {
		t.Fatalf("default run created an artifact directory: stat err = %v", err)
	}
}

// With the opt-in on, a large tool output spills into the convention directory
// under storage, returns a bounded view, and the full text is recoverable.
func TestConfigureOutputSpillOptInSpillsToConventionDir(t *testing.T) {
	storage := t.TempDir()
	root := t.TempDir()
	big := strings.Repeat("0123456789", 30_000) // 300 KiB
	writeSpillTestFile(t, root, "big.txt", big)

	ctx := configureOutputSpill(context.Background(),
		spillTestEnv(map[string]string{outputSpillEnableEnv: "1"}), storage)
	out, err := tools.New(root, true).Run(ctx, "read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "bytes elided") || !strings.Contains(out, "full output:") {
		t.Fatalf("output was not spilled: %.120q", out)
	}
	if len(out) >= len(big) {
		t.Fatalf("spilled output is %d bytes, want it bounded from %d", len(out), len(big))
	}

	dir := outputSpillArtifactDir(storage)
	path, _ := spillArtifactIn(t, dir)
	if !strings.Contains(out, path) {
		t.Fatalf("spilled output is missing the recovery path %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(artifact): %v", err)
	}
	if string(data) != big {
		t.Fatal("artifact does not contain the full, recoverable output")
	}
}

// The assembly step runs a retention sweep when opted in: stale artifacts in
// the convention directory disappear while fresh ones survive.
func TestConfigureOutputSpillCleansExpiredArtifacts(t *testing.T) {
	storage := t.TempDir()
	dir := outputSpillArtifactDir(storage)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "tool-output-0000000000000001.log")
	fresh := filepath.Join(dir, "tool-output-0000000000000002.log")
	for _, path := range []string{old, fresh} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	_ = configureOutputSpill(context.Background(),
		spillTestEnv(map[string]string{outputSpillEnableEnv: "1"}), storage)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired artifact survived assembly: stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh artifact was removed: %v", err)
	}
}

// A threshold override is honoured: a 5 KiB output spills when the assembly
// sets a 1 KiB threshold, instead of falling through to the ordinary cap.
func TestConfigureOutputSpillThresholdOverride(t *testing.T) {
	storage := t.TempDir()
	root := t.TempDir()
	big := strings.Repeat("qwerty", 900) // 5,400 bytes
	writeSpillTestFile(t, root, "big.txt", big)

	ctx := configureOutputSpill(context.Background(), spillTestEnv(map[string]string{
		outputSpillEnableEnv:    "1",
		outputSpillThresholdEnv: "1024",
	}), storage)
	out, err := tools.New(root, true).Run(ctx, "read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "bytes elided") {
		t.Fatalf("threshold override was ignored: %.120q", out)
	}
	spillArtifactIn(t, outputSpillArtifactDir(storage))
}
