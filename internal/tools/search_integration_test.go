package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchToolsReportTruncationWithoutExtraLines(t *testing.T) {
	resetDirCache()
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < maxMatches+10; i++ {
		sb.WriteString("needle line\n")
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxMatches+10; i++ {
		name := filepath.Join(root, "many", "f.txt")
		_ = os.MkdirAll(filepath.Dir(name), 0o755)
		_ = os.WriteFile(filepath.Join(root, "many", "f"+itoa(i)+".go"), []byte("x"), 0o644)
	}

	for _, tc := range []struct {
		name string
		tool Tool
		args string
	}{
		{name: "grep", tool: grepTool{}, args: `{"pattern":"needle","path":"big.txt"}`},
		{name: "glob", tool: globTool{}, args: `{"pattern":"*.go","path":"many"}`},
		{name: "find", tool: findTool{}, args: `{"pattern":"*.go","path":"many"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.tool.Run(context.Background(), root, json.RawMessage(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(out, "\n") + 1; n != maxMatches {
				t.Fatalf("%s 输出行数 = %d，期望 %d：%q", tc.name, n, maxMatches, out[:min(len(out), 80)])
			}
			if !strings.Contains(out, "truncated") {
				t.Fatalf("%s 达到上限应标记 truncated", tc.name)
			}
		})
	}
}

func TestSearchToolsTolerateBadRegex(t *testing.T) {
	resetDirCache()
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "value is ${HOME}/bin here\n",
		"b.txt": "call fetchProvider( now\n",
		"c.txt": "look here (?=x) ok\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		pattern string
		want    string
	}{
		{`${HOME}`, "a.txt:1:"},
		{`fetchProvider(`, "b.txt:1:"},
		{`(?=x)`, "c.txt:1:"},
	} {
		out, err := grepTool{}.Run(context.Background(), root,
			json.RawMessage(`{"pattern":`+searchMustJSON(tc.pattern)+`}`))
		if err != nil {
			t.Fatalf("pattern %q 不应报错：%v", tc.pattern, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf("pattern %q 输出 = %q，期望含 %q", tc.pattern, out, tc.want)
		}
		if tc.pattern == "(?=x)" && !strings.Contains(out, "RE2") {
			t.Fatalf("lookaround 应带可读提示，实际 %q", out)
		}
	}

	// 合法正则不受影响：不报错、也不附带降级提示。
	out, err := grepTool{}.Run(context.Background(), root, json.RawMessage(`{"pattern":"a{2,4}"}`))
	if err != nil {
		t.Fatalf("合法正则不应报错：%v", err)
	}
	if strings.Contains(out, "RE2") || strings.Contains(out, "字面量") {
		t.Fatalf("合法正则不应附带降级提示：%q", out)
	}
}

func searchMustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestGrepExStreamsLargeFileAndCapsColumns(t *testing.T) {
	resetDirCache()
	dir := t.TempDir()
	longLine := strings.Repeat("x", searchScanMaxColumns+500)
	content := strings.Repeat("filler line\n", 300) + "NEEDLE " + longLine + "\n" + strings.Repeat("tail\n", 300)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, files, outcome, err := GrepExWithOutcome(GrepExOptions{
		Pattern: "NEEDLE", Path: dir, Literal: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || files["big.txt"] != 1 {
		t.Fatalf("匹配 = %+v / %v", matches, files)
	}
	if !strings.HasSuffix(matches[0].Text, lineTruncatedMarker) {
		t.Fatalf("超长命中行未按列截断：len=%d", len(matches[0].Text))
	}
	if outcome.Limit != grepExDefaultHeadLimit {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGrepExWithOutcomeTruncationSignal(t *testing.T) {
	resetDirCache()
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("hit\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, files, outcome, err := GrepExWithOutcome(GrepExOptions{
		Pattern: "hit", Path: dir, Literal: true, CaseSensitive: true, HeadLimit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 5 || files["f.txt"] != 5 {
		t.Fatalf("截断后命中数 = %d / %d", len(matches), files["f.txt"])
	}
	if !outcome.Truncated || outcome.Limit != 5 {
		t.Fatalf("截断信号 = %+v", outcome)
	}
}
