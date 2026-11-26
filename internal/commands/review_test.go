package commands

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
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
+apiKey := "FAKE_SECRET_VALUE"
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
	if strings.Contains(secret.Detail, "FAKE_SECRET_VALUE") {
		t.Fatalf("secret literal leaked: %q", secret.Detail)
	}
	if !strings.Contains(secret.Detail, "[redacted:") {
		t.Fatalf("secret detail not redacted: %q", secret.Detail)
	}
	if secret.Line != 12 {
		t.Fatalf("secret line = %d, want 12", secret.Line)
	}

	text := report.String()
	if strings.Contains(text, "FAKE_SECRET_VALUE") || !strings.Contains(text, "[redacted:") {
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

// 裁剪绝不能拆分多字节 rune：某条发现的 Detail 会进入
// 易读报告（以及提示词），留下不完整的 UTF-8
// 序列会产生无效文本。字符串先经过 Fields 规范化，
// 因此在应用字节预算前会折叠内嵌空白。
func TestClipFindingPreservesUTF8AndWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{
			name:  "short text unchanged",
			text:  "TODO: fix",
			limit: 120,
			want:  "TODO: fix",
		},
		{
			name:  "ascii truncation keeps ellipsis",
			text:  strings.Repeat("a", 130),
			limit: 120,
			want:  strings.Repeat("a", 119) + "…",
		},
		{
			name:  "rune straddling cut is dropped whole",
			text:  strings.Repeat("a", 117) + "中文",
			limit: 120,
			want:  strings.Repeat("a", 117) + "…",
		},
		{
			name:  "rune starting exactly at cut is excluded",
			text:  strings.Repeat("a", 116) + "中文",
			limit: 120,
			want:  strings.Repeat("a", 116) + "中" + "…",
		},
		{
			name:  "whitespace normalized before clipping",
			text:  "TODO:  fix\t中文",
			limit: 120,
			want:  "TODO: fix 中文",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clipFinding(tt.text, tt.limit)
			if got != tt.want {
				t.Errorf("clipFinding() = %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("clipFinding() produced invalid UTF-8: %q", got)
			}
		})
	}
}

// 对真实 diff 路径产生的发现也必须保持同样的保证，
// 即长的 Unicode TODO 行会流经 redactFinding + clipFinding。
func TestDiffFindingsDetailIsValidUTF8(t *testing.T) {
	line := strings.Repeat("中", 60) + " TODO"
	diff := "--- a/f.go\n+++ b/f.go\n@@ -1 +1 @@\n+" + line + "\n"
	findings := diffFindings(diff)
	if len(findings) == 0 {
		t.Fatal("expected a TODO finding")
	}
	for _, finding := range findings {
		if !utf8.ValidString(finding.Detail) {
			t.Errorf("finding detail is invalid UTF-8: %q", finding.Detail)
		}
	}
}
