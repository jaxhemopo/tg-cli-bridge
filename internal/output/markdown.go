package output

import (
	"fmt"
	"regexp"
	"strings"
)

// MarkdownToHTML converts a subset of Markdown to Telegram-compatible HTML.
// It handles:
// - HTML escaping (&, <, >)
// - Fenced code blocks (```...```) -> <pre>...</pre>
// - Inline code (`...`) -> <code>...</code>
// - Bold (**...**) -> <b>...</b>
// - Italic (*...*) -> <i>...</i>
// - Headers (#...) -> <b>...</b>
func MarkdownToHTML(md string) string {
	if md == "" {
		return ""
	}

	// 1. Escape HTML specials first.
	// We only need to escape &, <, > for Telegram HTML mode.
	res := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(md)

	// 2. Protect fenced code blocks.
	// We replace them with placeholders so internal markdown isn't processed.
	var codeBlocks []string
	fencedRE := regexp.MustCompile("(?s)```(?:[a-zA-Z0-9]+)?\n?(.*?)```")
	res = fencedRE.ReplaceAllStringFunc(res, func(match string) string {
		content := fencedRE.FindStringSubmatch(match)[1]
		placeholder := fmt.Sprintf("\x00BLOCK%d\x00", len(codeBlocks))
		codeBlocks = append(codeBlocks, content)
		return placeholder
	})

	// 3. Protect inline code.
	var inlineCodes []string
	inlineRE := regexp.MustCompile("`([^`]+)`")
	res = inlineRE.ReplaceAllStringFunc(res, func(match string) string {
		content := inlineRE.FindStringSubmatch(match)[1]
		placeholder := fmt.Sprintf("\x00INLINE%d\x00", len(inlineCodes))
		inlineCodes = append(inlineCodes, content)
		return placeholder
	})

	// 4. Headers -> Bold
	headerRE := regexp.MustCompile(`(?m)^#+\s+(.*)$`)
	res = headerRE.ReplaceAllString(res, "<b>$1</b>")

	// 5. Bold (**...**)
	boldRE := regexp.MustCompile(`\*\*(.*?)\*\*`)
	res = boldRE.ReplaceAllString(res, "<b>$1</b>")

	// 6. Italic (*...*)
	italicRE := regexp.MustCompile(`\*(.*?)\*`)
	res = italicRE.ReplaceAllString(res, "<i>$1</i>")

	// 7. Restore protected blocks.
	for i, content := range inlineCodes {
		placeholder := fmt.Sprintf("\x00INLINE%d\x00", i)
		res = strings.Replace(res, placeholder, "<code>"+content+"</code>", 1)
	}
	for i, content := range codeBlocks {
		placeholder := fmt.Sprintf("\x00BLOCK%d\x00", i)
		res = strings.Replace(res, placeholder, "<pre>"+content+"</pre>", 1)
	}

	return res
}
