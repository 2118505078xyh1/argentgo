package tools

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdownLinks(t *testing.T) {
	input := `<p>See <a href="https://example.com/docs">the docs</a> for details.</p>`
	got := htmlToMarkdown(input)
	if !strings.Contains(got, "[the docs](https://example.com/docs)") {
		t.Fatalf("link not converted: %q", got)
	}
	if !strings.Contains(got, "See") || !strings.Contains(got, "for details") {
		t.Fatalf("surrounding text lost: %q", got)
	}
}

func TestHTMLToMarkdownHeadings(t *testing.T) {
	input := `<h1>Title</h1><h2>Sub</h2><h6>Six</h6>`
	got := htmlToMarkdown(input)
	for _, want := range []string{"# Title", "## Sub", "###### Six"} {
		if !strings.Contains(got, want) {
			t.Errorf("heading %q missing in %q", want, got)
		}
	}
}

func TestHTMLToMarkdownLists(t *testing.T) {
	input := `<ul><li>one</li><li>two</li></ul>`
	got := htmlToMarkdown(input)
	if !strings.Contains(got, "- one") || !strings.Contains(got, "- two") {
		t.Fatalf("list not converted: %q", got)
	}
}

func TestHTMLToMarkdownTable(t *testing.T) {
	input := `<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>`
	got := htmlToMarkdown(input)
	if !strings.Contains(got, "|") || !strings.Contains(got, "A") || !strings.Contains(got, "2") {
		t.Fatalf("table not converted: %q", got)
	}
}

func TestHTMLToMarkdownDropsNoise(t *testing.T) {
	input := `<div>keep<script>alert(1)</script><style>.x{}</style><nav>menu</nav>end</div>`
	got := htmlToMarkdown(input)
	if strings.Contains(got, "alert(1)") || strings.Contains(got, ".x{}") || strings.Contains(got, "menu") {
		t.Fatalf("noise not dropped: %q", got)
	}
	if !strings.Contains(got, "keep") || !strings.Contains(got, "end") {
		t.Fatalf("content lost: %q", got)
	}
}

func TestHTMLToMarkdownEntityDecode(t *testing.T) {
	input := `<p>a &amp; b &lt;tag&gt; &#39;quote&#39;</p>`
	got := htmlToMarkdown(input)
	if !strings.Contains(got, "a & b <tag> 'quote'") {
		t.Fatalf("entities not decoded: %q", got)
	}
}

func TestHTMLToMarkdownRejectsJSLink(t *testing.T) {
	input := `<a href="javascript:alert(1)">click</a>`
	got := htmlToMarkdown(input)
	if strings.Contains(got, "javascript:") {
		t.Fatalf("javascript link not rejected: %q", got)
	}
	if !strings.Contains(got, "click") {
		t.Fatalf("link text lost: %q", got)
	}
}

func TestHTMLToMarkdownEmptyAndPlain(t *testing.T) {
	if htmlToMarkdown("") != "" {
		t.Fatal("empty input should produce empty output")
	}
	if got := htmlToMarkdown("plain text"); got != "plain text" {
		t.Fatalf("plain text = %q", got)
	}
}

func TestAttrHrefForms(t *testing.T) {
	cases := map[string]string{
		`href="https://x.com"`:    "https://x.com",
		`href='/relative/path'`:   "/relative/path",
		`href=bare`:               "bare",
		`href="javascript:x"`:     "",
		`href="data:text/html,x"`: "",
	}
	for attrs, want := range cases {
		if got := attrHref(attrs); got != want {
			t.Errorf("attrHref(%q) = %q, want %q", attrs, got, want)
		}
	}
}

func TestHTMLToMarkdownBounded(t *testing.T) {
	// 大量空行不应产生无界输出。
	input := strings.Repeat("<p>x</p>", mdMaxOutputLines+100)
	got := htmlToMarkdown(input)
	if len(got) > mdMaxOutputBytes {
		t.Fatalf("output %d bytes exceeds bound", len(got))
	}
}
