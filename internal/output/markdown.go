package output

import (
	"regexp"
	"strings"
)

// MarkdownToHTML converts the markdown an agent CLI typically writes
// (``code blocks``, **bold**, *italic*, headers, inline `code`) into the
// Telegram-flavour HTML subset that the Bot API understands.
//
// Order matters here: we extract fenced code blocks first (so their interior
// isn't mangled), then handle inline code, then escape the remaining text,
// then convert bold/italic/headers.
func MarkdownToHTML(s string) string {
	// Stash fenced ```code``` blocks behind sentinels so we don't escape
	// or interpret their contents.
	var codeBlocks []string
	s = fencedRE.ReplaceAllStringFunc(s, func(m string) string {
		// Strip the leading ``` (with optional language) and trailing ```.
		body := fencedRE.ReplaceAllString(m, "$1")
		codeBlocks = append(codeBlocks, "<pre>"+htmlSafe(strings.TrimRight(body, "\n"))+"</pre>")
		return placeholder("CB", len(codeBlocks)-1)
	})

	// Stash `inline code` next, same trick.
	var inlines []string
	s = inlineRE.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimPrefix(strings.TrimSuffix(m, "`"), "`")
		inlines = append(inlines, "<code>"+htmlSafe(inner)+"</code>")
		return placeholder("IC", len(inlines)-1)
	})

	// Now it's safe to HTML-escape the body proper.
	s = htmlSafe(s)

	// Headers: # / ## / ### at start of line → bold.
	s = headerRE.ReplaceAllString(s, "<b>$1</b>")

	// Bold: **text**. We require non-whitespace next to the asterisks so
	// "**" alone or stray pairs don't match.
	s = boldRE.ReplaceAllString(s, "<b>$1</b>")

	// Italic: single *text*. Avoid eating already-converted bold by
	// rejecting adjacent asterisks.
	s = italicRE.ReplaceAllString(s, "<i>$1</i>")

	// Re-inject the inline-code stash, then fenced blocks.
	s = restorePlaceholders(s, "IC", inlines)
	s = restorePlaceholders(s, "CB", codeBlocks)

	return s
}

// htmlSafe escapes the three characters that Telegram's HTML parser cares
// about. We keep things minimal because Telegram's parser is fairly
// permissive — over-escaping would break things like ``->'' arrows.
func htmlSafe(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// placeholder is a sentinel pattern unlikely to appear in real prose.
func placeholder(kind string, n int) string {
	return "\x00" + kind + "_" + itoa(n) + "\x00"
}

func restorePlaceholders(s, kind string, vals []string) string {
	for i, v := range vals {
		s = strings.ReplaceAll(s, placeholder(kind, i), v)
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Regexes used by MarkdownToHTML. Compiled once at package init.
var (
	// ```optional-lang\n…body…``` (multi-line capture).
	fencedRE = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n(.*?)```")

	// `inline code` — backtick-delimited, no embedded backtick.
	inlineRE = regexp.MustCompile("`[^`\\n]+`")

	// ### Header / ## Header / # Header (start of line).
	headerRE = regexp.MustCompile(`(?m)^#{1,3}\s+(.+)$`)

	// **bold** — non-greedy, non-whitespace edges.
	boldRE = regexp.MustCompile(`\*\*(\S(?:.*?\S)?)\*\*`)

	// *italic* — non-greedy, requires non-whitespace inside, doesn't match
	// pairs that touch other asterisks (so we don't fight with **bold**).
	italicRE = regexp.MustCompile(`(^|[^*])\*(\S(?:[^*]*?\S)?)\*([^*]|$)`)
)
