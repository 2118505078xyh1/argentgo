package app

// 来源 URL 标注的离线测试。覆盖 web_fetch / web_search、其它工具不变、
// userinfo 脱敏、多 URL、CJK 与符文安全截断，以及默认关闭的接线接缝。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/hooks"
)

func TestAnnotateSourceURLWebFetchAndWebSearch(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		args   string
		output string
		want   string
	}{
		{
			name:   "web_fetch 单行标注",
			tool:   "web_fetch",
			args:   `{"url":"https://example.com/page"}`,
			output: "body text",
			want:   "[source: https://example.com/page]\nbody text",
		},
		{
			name:   "web_search 识别带 url 的参数",
			tool:   "web_search",
			args:   `{"query":"go","url":"https://search.example/api"}`,
			output: `web search results for "go":`,
			want:   "[source: https://search.example/api]\n" + `web search results for "go":`,
		},
		{
			name:   "web_search 常规参数无 URL 时不变",
			tool:   "web_search",
			args:   `{"query":"go","limit":2}`,
			output: "results",
			want:   "results",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := annotateSourceURL(tc.tool, tc.args, tc.output); got != tc.want {
				t.Fatalf("annotateSourceURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnnotateSourceURLOtherToolsUnchanged(t *testing.T) {
	output := "the untouched output"
	for _, tool := range []string{"bash", "read", "grep", "WebFetch", "web_fetch_extra", ""} {
		if got := annotateSourceURL(tool, `{"url":"https://example.com/x"}`, output); got != output {
			t.Errorf("tool %q changed output: %q", tool, got)
		}
	}
}

func TestAnnotateSourceURLStripsUserInfo(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"用户名与密码", "https://user:secret@example.com/p?q=1", "https://example.com/p?q=1"},
		{"仅用户名", "https://alice@example.com/", "https://example.com/"},
		{"无认证信息保持不变", "https://example.com/p?q=a@b.com", "https://example.com/p?q=a@b.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := jsonArgs(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			want := "[source: " + tc.want + "]\nbody"
			if got := annotateSourceURL("web_fetch", args, "body"); got != want {
				t.Fatalf("annotateSourceURL = %q, want %q", got, want)
			}
		})
	}
}

func TestAnnotateSourceURLCJKAndRuneSafe(t *testing.T) {
	// URL 中的 CJK 字面量应原样保留，不被百分号编码或截断成半个 rune。
	args, err := jsonArgs("https://example.com/中文/页面?q=测试")
	if err != nil {
		t.Fatal(err)
	}
	want := "[source: https://example.com/中文/页面?q=测试]\nbody"
	if got := annotateSourceURL("web_fetch", args, "body"); got != want {
		t.Fatalf("CJK URL = %q, want %q", got, want)
	}

	// 超长 CJK URL 在符文边界截断，结果仍是合法 UTF-8 且带截断标记。
	longURL := "https://example.com/" + strings.Repeat("中", 300)
	args, err = jsonArgs(longURL)
	if err != nil {
		t.Fatal(err)
	}
	got := annotateSourceURL("web_fetch", args, "body")
	if !utf8.ValidString(got) {
		t.Fatalf("annotated output is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, sourceURLTruncatedMark) {
		t.Fatalf("long URL was not marked as truncated: %q", got)
	}
	annotation := strings.SplitN(got, "\n", 2)[0]
	urlPart := strings.TrimSuffix(strings.TrimPrefix(annotation, sourceAnnotationPrefix), sourceAnnotationSuffix)
	if n := utf8.RuneCountInString(urlPart); n != sourceURLMaxRunes+len([]rune(sourceURLTruncatedMark)) {
		t.Fatalf("truncated URL rune count = %d, want %d", n, sourceURLMaxRunes+1)
	}
}

func TestAnnotateSourceURLMultipleURLs(t *testing.T) {
	args := `{"urls":["https://a.example/1","https://b.example/2","https://a.example/1"]}`
	want := "[source: https://a.example/1, https://b.example/2]\nbody"
	if got := annotateSourceURL("web_fetch", args, "body"); got != want {
		t.Fatalf("multiple URLs = %q, want %q", got, want)
	}

	// 单个 url 字段同样可以是数组。
	args = `{"url":["https://a.example/1","https://b.example/2"]}`
	if got := annotateSourceURL("web_fetch", args, "body"); got != want {
		t.Fatalf("url array = %q, want %q", got, want)
	}
}

func TestAnnotateSourceURLInvalidArgsOrSchemeUnchanged(t *testing.T) {
	for _, args := range []string{
		``,
		`not json`,
		`{"url":`,
		`{"url":""}`,
		`{"url":"file:///etc/passwd"}`,
		`{"url":"ftp://example.com/x"}`,
		`{"url":"//example.com/x"}`,
		`{"url":"not a url"}`,
		`{"query":"go"}`,
	} {
		if got := annotateSourceURL("web_fetch", args, "body"); got != "body" {
			t.Errorf("args %q changed output: %q", args, got)
		}
	}
}

func TestHookedToolsSourceAnnotatorIsOptIn(t *testing.T) {
	bridge := &hookBridge{dispatcher: &recordingDispatcher{}, config: &hooks.Config{}}
	inner := &stubToolRunner{out: "fetched body"}
	h := &hookedTools{
		inner:           inner,
		bridge:          bridge,
		sourceAnnotator: annotateSourceURL,
	}

	out, err := h.Run(context.Background(), "web_fetch", `{"url":"https://example.com/x"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := "[source: https://example.com/x]\nfetched body"; out != want {
		t.Fatalf("wired Run = %q, want %q", out, want)
	}

	// 未接线时不得改写输出，且与接线前的工具正文逐字节一致。
	h.sourceAnnotator = nil
	unwired, err := h.Run(context.Background(), "web_fetch", `{"url":"https://example.com/x"}`)
	if err != nil {
		t.Fatalf("unwired Run: %v", err)
	}
	if unwired != "fetched body" {
		t.Fatalf("unwired Run = %q, want %q", unwired, "fetched body")
	}
}

// jsonArgs 把单个 URL 包装成 web_fetch 的参数 JSON，避免测试里重复拼接。
func jsonArgs(url string) (string, error) {
	raw, err := json.Marshal(map[string]string{"url": url})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
