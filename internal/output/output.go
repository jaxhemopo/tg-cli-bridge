// Package output handles all the text-massaging the bridge needs: stripping
// ANSI escape sequences out of captured terminal panes, splitting long
// strings into Telegram-safe chunks, computing the new portion of a scrolled
// pane since the last view we sent, and classifying captured pane text into
// alternating prose and code blocks for nicer rendering in Telegram.
package output

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// ansiRE matches CSI, OSC, and the lone-ESC sequences emitted by terminal UIs.
//
// Built once at package init to avoid recompilation in the hot path. The
// alternation order matters: CSI/OSC are tried first because they're greedy
// and unambiguous; the final fallback catches any other ESC + printable-byte
// pair (the Fp/Fe/Fs families in ECMA-48, including ESC = for DECKPAM,
// ESC 7/8, ESC D/E/H/M, etc.).
var ansiRE = regexp.MustCompile(
	`\x1b(?:` +
		`\[[0-?]*[ -/]*[@-~]` + // CSI parameters... final byte
		`|\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC ... terminator (BEL or ST)
		`|[0-~]` + // Two-byte: ESC + any printable byte (0x30–0x7E)
		`)`,
)

// StripANSI removes ANSI/VT escape sequences from a string.
func StripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// SplitForTelegram breaks a long string into Telegram-safe chunks, preferring
// line boundaries. Telegram's hard limit is 4096 chars per message; maxChars
// should sit slightly below that to leave room for code-fence markup.
//
// The function is lossless: strings.Join(SplitForTelegram(s, n), "") == s.
func SplitForTelegram(s string, maxChars int) []string {
	if len(s) <= maxChars {
		return []string{s}
	}

	var chunks []string
	var buf strings.Builder

	flush := func() {
		if buf.Len() > 0 {
			chunks = append(chunks, buf.String())
			buf.Reset()
		}
	}

	// Walk line by line so we prefer to break at \n.
	lines := splitKeepNewlines(s)
	for _, line := range lines {
		// If the line alone is already larger than the limit, hard-split it
		// (after flushing whatever's buffered).
		if len(line) > maxChars {
			flush()
			for len(line) > maxChars {
				chunks = append(chunks, line[:maxChars])
				line = line[maxChars:]
			}
			buf.WriteString(line)
			continue
		}
		if buf.Len()+len(line) > maxChars {
			flush()
		}
		buf.WriteString(line)
	}
	flush()
	return chunks
}

// splitKeepNewlines is strings.Split-but-keepends: it returns lines with their
// trailing "\n" still attached, so reassembly is exact.
func splitKeepNewlines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Block is a classified chunk of captured pane text. Code blocks should be
// rendered in a monospace box; prose blocks should render as plain text.
type Block struct {
	IsCode bool
	Text   string
}

// toolCallRE matches lines that look like a tool / function call sitting at
// the start (after optional whitespace). The pattern is intentionally narrow
// — capitalised identifier directly followed by `(` — so it won't catch
// regular sentences that happen to contain parentheses.
var toolCallRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*\(`)

// FormatForTelegram parses captured pane text into alternating prose and code
// blocks. The bridge sends prose blocks as plain Telegram text and code blocks
// as <pre>…</pre>, giving a chat-like read for natural language while keeping
// tool calls and banners in a monospace box.
//
// Blank lines inherit the class of their neighbours so paragraph breaks don't
// fragment a block. Empty-input-box "noise" lines (rows of underscores from
// the agent CLI's text input frame) are dropped entirely.
func FormatForTelegram(s string) []Block {
	if s == "" {
		return nil
	}
	lines := stripNoise(strings.Split(s, "\n"))
	if len(lines) == 0 {
		return nil
	}
	classes := make([]int, len(lines))
	for i, line := range lines {
		classes[i] = classifyLine(line)
	}

	const (
		classBlank = 0
		classProse = 1
		classCode  = 2
	)

	// Propagate code/thought classification to subsequent indented lines.
	for i := 1; i < len(lines); i++ {
		if classes[i-1] == classCode && classes[i] == classProse {
			if strings.HasPrefix(lines[i], " ") || strings.HasPrefix(lines[i], "\t") {
				classes[i] = classCode
			}
		}
	}

	var blocks []Block
	var buf strings.Builder
	currentCode := false
	started := false

	flush := func() {
		text := strings.TrimRight(buf.String(), "\n")
		if text != "" {
			blocks = append(blocks, Block{IsCode: currentCode, Text: text})
		}
		buf.Reset()
	}

	for i, line := range lines {
		c := classes[i]
		var isCode bool
		switch c {
		case classBlank:
			// Blank lines stay with whatever class we're currently emitting.
			isCode = currentCode
		case classCode:
			isCode = true
		default:
			isCode = false
		}
		if started && isCode != currentCode {
			flush()
		}
		currentCode = isCode
		started = true
		buf.WriteString(line)
		// Don't trail the very last line with a newline.
		if i < len(lines)-1 {
			buf.WriteByte('\n')
		}
	}
	flush()
	return blocks
}

// stripNoise removes lines that are pure rendering artifacts from the agent
// CLI's input frame — long runs of underscores with little or no real
// content. These leak into pane captures every time the input area is
// re-drawn and add no information to the user.
func stripNoise(lines []string) []string {
	out := lines[:0]
	for _, line := range lines {
		if isInputFrameLine(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// isInputFrameLine reports whether a line is just the CLI's empty input box
// rendered as a sequence of underscores (occasionally with a stray `>` or
// `…`). It deliberately doesn't strip ordinary horizontal rules — they may
// be meaningful in the agent's output.
func isInputFrameLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	underscores := strings.Count(trimmed, "_")
	if underscores < 8 {
		return false
	}
	// Everything-except-underscores in the trimmed line. Allow a few stray
	// punctuation chars like `>` or `…` that the input frame draws around
	// its prompt, but reject anything that contains words.
	residue := strings.ReplaceAll(trimmed, "_", "")
	for _, r := range residue {
		if r == ' ' || r == '>' || r == '…' || r == '·' || r == '|' {
			continue
		}
		// Any other character means it's not just an input frame.
		return false
	}
	return true
}

// classifyLine returns 0 (blank), 1 (prose), or 2 (code).
func classifyLine(line string) int {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return 0
	}
	// Lead char-based tool-call markers used by Antigravity / Claude Code /
	// Gemini CLI / etc. (including black small triangles for thoughts).
	if r, _ := utf8.DecodeRuneInString(trimmed); r != utf8.RuneError {
		switch r {
		case '●', '▶', '►', '✓', '✗', '✔', '✘', '◆', '◇', '○', '◯', '⚡', '⏺', '▸', '◂', '▾', '▴', '✦', '✧', '✱', '✲', '✳', '❯', '»':
			return 2
		}
	}
	// Common thought prefixes.
	if strings.HasPrefix(trimmed, "Thinking...") || strings.HasPrefix(trimmed, "Thought for ") {
		return 2
	}
	// CapitalCase(...) call at start of line — Read(...), ListDir(...), etc.
	if toolCallRE.MatchString(trimmed) {
		return 2
	}
	// Lines that are mostly box-drawing or block characters (banners, dividers).
	if looksLikeBanner(trimmed) {
		return 2
	}
	return 1
}

// looksLikeBanner reports whether a line is part of ASCII art / banner /
// progress-bar output rather than natural prose.
//
// Heuristic in two parts:
//
//  1. Any run of 3+ block / shading characters in a row (▆▆▆, ███, ░░░…)
//     is a strong signal — those almost never appear in prose.
//  2. Lines composed almost entirely of box-drawing characters (horizontal
//     rules, table borders, divider lines).
func looksLikeBanner(s string) bool {
	const blockChars = "▀▁▂▃▄▅▆▇█▉▊▋▌▍▎▏▐░▒▓▔▕▖▗▘▙▚▛▜▝▞▟"
	run := 0
	for _, r := range s {
		if strings.ContainsRune(blockChars, r) {
			run++
			if run >= 3 {
				return true
			}
		} else {
			run = 0
		}
	}
	// Lines that are basically just box-drawing chars: dividers, table edges.
	boxChars := 0
	textChars := 0
	for _, r := range s {
		switch {
		case r >= 0x2500 && r <= 0x257F: // Unicode "Box Drawing" block
			boxChars++
		case r == ' ':
			// ignored
		default:
			textChars++
		}
	}
	return boxChars >= 4 && textChars <= 3
}

// TruncateLines shortens each line in s to at most maxLineChars runes,
// appending "…" to any line that was shortened. If maxLineChars <= 0 the
// input is returned unchanged.
//
// Used to keep tool-call lines (long file paths and the like) skimmable on
// a phone screen.
func TruncateLines(s string, maxLineChars int) string {
	if maxLineChars <= 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		runes := []rune(line)
		if len(runes) > maxLineChars {
			// Reserve one rune for the ellipsis.
			lines[i] = string(runes[:maxLineChars-1]) + "…"
		}
	}
	return strings.Join(lines, "\n")
}

// DiffSince returns the new portion of current that wasn't in previous.
//
// A naive longest-common-prefix approach fails for tmux capture-pane output
// because it returns a viewport that scrolls — old top lines disappear. We
// instead look for the longest suffix of `previous` that's a prefix of
// `current`, and return what comes after that anchor.
//
// When there's no overlap (huge update, scrollback eviction), all of `current`
// is returned.
func DiffSince(previous, current string) string {
	if previous == "" {
		return current
	}
	if previous == current {
		return ""
	}

	// Fast path: nothing scrolled, previous is a strict prefix.
	if strings.HasPrefix(current, previous) {
		return current[len(previous):]
	}

	// General case: longest suffix(previous) == prefix(current). Cap the
	// search window so we never burn CPU on huge buffers.
	window := len(previous)
	if len(current) < window {
		window = len(current)
	}
	if window > 8000 {
		window = 8000
	}
	for size := window; size > 0; size-- {
		if strings.HasSuffix(previous, current[:size]) {
			return current[size:]
		}
	}
	// No overlap at all — assume scrollback evicted everything.
	return current
}

// StripEchoedInput checks if the newly captured output starts with an echo of the
// user's last input (prefixed by terminal prompt symbols like ">" or "$"). If so,
// it strips the echoed prompt and returns the remaining output.
func StripEchoedInput(body string, lastInput string) string {
	if lastInput == "" {
		return body
	}
	// Split lastInput into lines. Normalize line endings.
	inputLines := strings.Split(strings.ReplaceAll(lastInput, "\r", ""), "\n")
	bodyLines := strings.Split(body, "\n")

	// Skip leading divider lines or empty lines in the body.
	startIdx := 0
	for startIdx < len(bodyLines) && isDividerLine(bodyLines[startIdx]) {
		startIdx++
	}

	if len(bodyLines)-startIdx < len(inputLines) {
		return body
	}

	// Verify first line of body matches the first line of input with a prompt prefix.
	firstLine := strings.TrimSpace(bodyLines[startIdx])
	firstInput := strings.TrimSpace(inputLines[0])

	isMatch := false
	if firstLine == firstInput {
		isMatch = true
	} else {
		// Match prefixes like "> ", "$ ", "# "
		for _, prefix := range []string{">", "$", "#"} {
			if strings.HasPrefix(firstLine, prefix) && strings.TrimSpace(strings.TrimPrefix(firstLine, prefix)) == firstInput {
				isMatch = true
				break
			}
		}
	}

	if !isMatch {
		return body
	}

	// Verify subsequent lines of the input match the body lines.
	for i := 1; i < len(inputLines); i++ {
		expected := strings.TrimSpace(inputLines[i])
		actual := strings.TrimSpace(bodyLines[startIdx+i])
		if actual != expected {
			return body
		}
	}

	// If everything matched, strip the input lines (including leading dividers).
	remainingLines := bodyLines[startIdx+len(inputLines):]
	// Skip any blank/divider lines directly following the stripped input.
	for len(remainingLines) > 0 && isDividerLine(remainingLines[0]) {
		remainingLines = remainingLines[1:]
	}
	return strings.Join(remainingLines, "\n")
}

func isDividerLine(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	// If it consists entirely of horizontal line symbols, it's a divider.
	// We check for: ─, _, =, *, -, ▆, █
	for _, r := range s {
		if r != '─' && r != '_' && r != '=' && r != '*' && r != '-' && r != '▆' && r != '█' {
			return false
		}
	}
	return true
}
