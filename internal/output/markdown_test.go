package output

import (
	"strings"
	"testing"
)

func TestMarkdownToHTML_Plain(t *testing.T) {
	got := MarkdownToHTML("hello world")
	if got != "hello world" {
		t.Errorf("plain text was modified: %q", got)
	}
}

func TestMarkdownToHTML_Bold(t *testing.T) {
	got := MarkdownToHTML("This is **important** stuff")
	if !strings.Contains(got, "<b>important</b>") {
		t.Errorf("bold not converted: %q", got)
	}
}

func TestMarkdownToHTML_Italic(t *testing.T) {
	got := MarkdownToHTML("emphasis on *one* word")
	if !strings.Contains(got, "<i>one</i>") {
		t.Errorf("italic not converted: %q", got)
	}
}

func TestMarkdownToHTML_InlineCode(t *testing.T) {
	got := MarkdownToHTML("run `tg-cli-bridge` from your terminal")
	if !strings.Contains(got, "<code>tg-cli-bridge</code>") {
		t.Errorf("inline code not converted: %q", got)
	}
}

func TestMarkdownToHTML_FencedCodeBlock(t *testing.T) {
	in := "Try this:\n```go\nfmt.Println(\"hi\")\n```\nDone."
	got := MarkdownToHTML(in)
	if !strings.Contains(got, "<pre>") || !strings.Contains(got, "fmt.Println") {
		t.Errorf("fenced block not converted: %q", got)
	}
}

func TestMarkdownToHTML_EscapesHTMLSpecials(t *testing.T) {
	got := MarkdownToHTML("a<b>c & d>e")
	if !strings.Contains(got, "&lt;b&gt;") || !strings.Contains(got, "&amp;") {
		t.Errorf("specials not escaped: %q", got)
	}
}

func TestMarkdownToHTML_HeadersBecomeBold(t *testing.T) {
	got := MarkdownToHTML("# Title\nbody")
	if !strings.Contains(got, "<b>Title</b>") {
		t.Errorf("header not converted: %q", got)
	}
}

func TestMarkdownToHTML_CodeContentsNotInterpretedAsMarkdown(t *testing.T) {
	// Bold markers inside a code block should NOT become <b>.
	in := "```\nuse **kw args** here\n```"
	got := MarkdownToHTML(in)
	if strings.Contains(got, "<b>") {
		t.Errorf("bold inside code block was incorrectly converted: %q", got)
	}
}
