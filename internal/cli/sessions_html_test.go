package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRichHTMLFromJSON(t *testing.T) {
	input := []byte(`[
		{"role":"user","text":"帮我看这段代码","kind":"user","ts":"2024-01-02T03:04:05Z"},
		{"role":"assistant","text":"好的","kind":"assistant","ts":"2024-01-02T03:04:06Z"},
		{"role":"tool","text":"<script>bad()</script> output","tool":"bash","kind":"tool"},
		{"kind":"header","text":"ignored header text"}
	]`)
	out, err := richHTMLFromJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, want := range []string{"<!doctype html>", "帮我看这段代码", "好的", "bash", "展开工具输出", "ignored header text"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(html, "<script>bad()") {
		t.Fatal("tool output XSS not escaped")
	}
	// 自包含：无外链。
	for _, forbidden := range []string{"http://", "https://", "src=/", "href=/"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("external reference %q in HTML", forbidden)
		}
	}
}

func TestRichHTMLFromJSONInvalid(t *testing.T) {
	if _, err := richHTMLFromJSON([]byte("not json")); err == nil {
		t.Fatal("invalid JSON should fail")
	}
}

func TestRichHTMLFromJSONEmpty(t *testing.T) {
	out, err := richHTMLFromJSON([]byte("[]"))
	if err != nil || !bytes.Contains(out, []byte("<!doctype html>")) {
		t.Fatalf("empty transcript: %v %s", err, out)
	}
}
