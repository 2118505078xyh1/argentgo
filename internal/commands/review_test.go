package commands

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type recordingRunner struct {
	outputs map[string]string
	calls   [][]string
}

func (r *recordingRunner) Run(name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	key := strings.Join(call, " ")
	out, ok := r.outputs[key]
	if !ok {
		return nil, fmt.Errorf("unexpected command %q", key)
	}
	return []byte(out), nil
}

func TestReviewReportsRedactedSecretAndIssuesOnlyGitReads(t *testing.T) {
	runner := &recordingRunner{outputs: map[string]string{
		"git status --porcelain": " M src/main.go\n",
		"git diff --unified=3": `diff --git a/src/main.go b/src/main.go
--- a/src/main.go
+++ b/src/main.go
@@ -10,3 +10,5 @@ func main() {
 context
+// TODO: remove this
+apiKey := "2118505078xyh1"
+fmt.Println("debugging")
`,
	}}
	report, err := Review(runner)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if report.Clean || !reflect.DeepEqual(report.Files, []string{"src/main.go"}) {
		t.Fatalf("report files = %v clean = %v", report.Files, report.Clean)
	}

	kinds := make(map[string]Finding, len(report.Findings))
	for _, finding := range report.Findings {
		kinds[finding.Kind] = finding
	}
	for _, want := range []string{"todo", "debug", "secret"} {
		if _, ok := kinds[want]; !ok {
			t.Fatalf("missing %q finding in %+v", want, report.Findings)
		}
	}
	secret := kinds["secret"]
	if strings.Contains(secret.Detail, "2118505078xyh1") {
		t.Fatalf("secret literal leaked: %q", secret.Detail)
	}
	if !strings.Contains(secret.Detail, "[redacted:") {
		t.Fatalf("secret detail not redacted: %q", secret.Detail)
	}
	if secret.Line != 12 {
		t.Fatalf("secret line = %d, want 12", secret.Line)
	}

	text := report.String()
	if strings.Contains(text, "2118505078xyh1") || !strings.Contains(text, "[redacted:") {
		t.Fatalf("report text = %q", text)
	}

	wantCalls := [][]string{
		{"git", "status", "--porcelain"},
		{"git", "diff", "--unified=3"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("runner calls = %v, want only read-only git commands %v", runner.calls, wantCalls)
	}
}

func TestReviewCleanTreeReportsNoChanges(t *testing.T) {
	runner := &recordingRunner{outputs: map[string]string{
		"git status --porcelain": "\n",
	}}
	report, err := Review(runner)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !report.Clean || report.String() != "no changes" {
		t.Fatalf("clean report = %+v (%q)", report, report.String())
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v, want only git status", runner.calls)
	}
}

func TestReviewRejectsNilRunner(t *testing.T) {
	if _, err := Review(nil); err == nil {
		t.Fatal("Review(nil) succeeded")
	}
}

func TestReviewHookAndBuiltinRegistration(t *testing.T) {
	runner := &recordingRunner{outputs: map[string]string{
		"git status --porcelain": "?? new.go\n",
		"git diff --unified=3":   "",
	}}
	text, err := ReviewHook{Runner: runner}.Run("")
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if !strings.Contains(text, "new.go") || !strings.Contains(text, "findings: none") {
		t.Fatalf("hook output = %q", text)
	}

	set := Discover(nil)
	set.Add(ReviewCommand())
	if cmd, ok := set.Get("review"); !ok || !strings.Contains(strings.ToLower(cmd.Description), "read-only") ||
		!strings.Contains(cmd.Description, "without modifying") {
		t.Fatalf("registered review command = %+v, ok=%v", cmd, ok)
	}
}
