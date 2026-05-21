package output

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"basic colors", "\x1b[31mred\x1b[0m and \x1b[1;32mgreen\x1b[0m", "red and green"},
		{"osc title", "\x1b]0;window title\x07visible", "visible"},
		{"cursor positioning", "\x1b[2J\x1b[H\x1b[1;1Hhello", "hello"},
		{"clean text", "plain text\nwith newline", "plain text\nwith newline"},
		{"empty", "", ""},
		{"two-byte ESC =", "before\x1b=after", "beforeafter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.in)
			if got != tt.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitForTelegram_ShortPassesThrough(t *testing.T) {
	got := SplitForTelegram("hello", 100)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("expected single chunk 'hello', got %v", got)
	}
}

func TestSplitForTelegram_BreaksAtLineBoundaries(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("line\n")
	}
	text := b.String()
	chunks := SplitForTelegram(text, 30)
	for i, c := range chunks {
		if len(c) > 30 {
			t.Fatalf("chunk %d is %d chars, > limit 30", i, len(c))
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatal("reassembly is lossy")
	}
}

func TestSplitForTelegram_HandlesSingleOverlongLine(t *testing.T) {
	text := strings.Repeat("x", 10000)
	chunks := SplitForTelegram(text, 1000)
	for i, c := range chunks {
		if len(c) > 1000 {
			t.Fatalf("chunk %d is %d chars, > limit 1000", i, len(c))
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatal("reassembly is lossy")
	}
}

func TestSplitForTelegram_ExactBoundary(t *testing.T) {
	text := strings.Repeat("x", 100)
	got := SplitForTelegram(text, 100)
	if len(got) != 1 || got[0] != text {
		t.Fatalf("expected single 100-char chunk, got %d chunks", len(got))
	}
}

func TestFormatForTelegram_EmptyInput(t *testing.T) {
	if got := FormatForTelegram(""); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestFormatForTelegram_PureProse(t *testing.T) {
	in := "Hello! I'm Antigravity, your AI coding assistant.\nHow can I help you today?"
	got := FormatForTelegram(in)
	if len(got) != 1 {
		t.Fatalf("expected 1 block, got %d: %+v", len(got), got)
	}
	if got[0].IsCode {
		t.Error("prose was classified as code")
	}
	if got[0].Text != in {
		t.Errorf("text was modified: %q", got[0].Text)
	}
}

func TestFormatForTelegram_PureCode(t *testing.T) {
	in := "● Read(/Users/me/file.go)\n● ListDir(/Users/me)"
	got := FormatForTelegram(in)
	if len(got) != 1 {
		t.Fatalf("expected 1 block, got %d", len(got))
	}
	if !got[0].IsCode {
		t.Error("tool calls were classified as prose")
	}
}

func TestFormatForTelegram_AlternatingBlocks(t *testing.T) {
	in := "" +
		"● Read(/Users/me/foo.go)\n" +
		"▶ Thought for 2s, 352 tokens\n" +
		"\n" +
		"Hello! I'm Antigravity, your AI coding assistant.\n" +
		"How can I help you today?\n" +
		"\n" +
		"● ListDir(/Users/me)\n" +
		"\n" +
		"Here are some next steps you can take."
	blocks := FormatForTelegram(in)
	if len(blocks) != 4 {
		t.Fatalf("expected 4 blocks, got %d: %+v", len(blocks), blocks)
	}
	wantCode := []bool{true, false, true, false}
	for i, b := range blocks {
		if b.IsCode != wantCode[i] {
			t.Errorf("block %d IsCode = %v, want %v\ntext: %q",
				i, b.IsCode, wantCode[i], b.Text)
		}
	}
}

func TestFormatForTelegram_BannerDetected(t *testing.T) {
	// A simplified Antigravity banner line — mostly box chars + a few labels.
	banner := "▆▆▆▆▆▆     Antigravity CLI 1.0.0\n▆▆▆▆▆▆▆    user@example.com"
	got := FormatForTelegram(banner)
	if len(got) == 0 {
		t.Fatal("expected at least one block")
	}
	// First block should be classified as code (banner).
	if !got[0].IsCode {
		t.Errorf("banner was not classified as code: %+v", got)
	}
}

func TestFormatForTelegram_ThoughtPointerVariants(t *testing.T) {
	// Both U+25B6 (▶) and U+25B8 (▸) appear in agentic-CLI output for
	// "Thought for Xs, NNN tokens" lines. They look near-identical but are
	// different code points; the classifier must catch both.
	for _, prefix := range []string{"▶", "▸"} {
		in := prefix + " Thought for 2s, 642 tokens"
		got := FormatForTelegram(in)
		if len(got) != 1 || !got[0].IsCode {
			t.Errorf("thought line with %q prefix misclassified: %+v", prefix, got)
		}
	}
}

func TestFormatForTelegram_FunctionCallAtStart(t *testing.T) {
	got := FormatForTelegram("Read(/path)")
	if len(got) != 1 || !got[0].IsCode {
		t.Errorf("function-call line not classified as code: %+v", got)
	}
}

func TestFormatForTelegram_SentenceWithParensIsProse(t *testing.T) {
	got := FormatForTelegram("This sentence (with parens) is just prose.")
	if len(got) != 1 || got[0].IsCode {
		t.Errorf("sentence with parens misclassified as code: %+v", got)
	}
}

func TestFormatForTelegram_StripsInputFrameNoise(t *testing.T) {
	// The agent CLI re-draws its empty input box on every screen update.
	// Those underscore rows leak into the capture and should be dropped.
	in := "" +
		"____________________________________________\n" +
		"_______________________________\n" +
		"\n" +
		"Hello! I'm Antigravity, your AI coding assistant.\n"
	blocks := FormatForTelegram(in)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (just the prose), got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].IsCode {
		t.Error("the surviving block should be prose, not code")
	}
	if !strings.Contains(blocks[0].Text, "Antigravity") {
		t.Errorf("prose lost: %q", blocks[0].Text)
	}
	if strings.Contains(blocks[0].Text, "____") {
		t.Errorf("underscore noise leaked through: %q", blocks[0].Text)
	}
}

func TestFormatForTelegram_KeepsRealHorizontalRules(t *testing.T) {
	// A short rule with surrounding text should NOT be classified as input
	// frame noise — only the long empty-input-box bars are.
	in := "Section one\n────────────\nSection two"
	blocks := FormatForTelegram(in)
	// We don't enforce a specific count; just that nothing was dropped.
	joined := ""
	for _, b := range blocks {
		joined += b.Text + "\n"
	}
	if !strings.Contains(joined, "Section one") || !strings.Contains(joined, "Section two") {
		t.Errorf("real sections were stripped: %q", joined)
	}
}

func TestTruncateLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{
			"zero max returns unchanged",
			"hello world",
			0,
			"hello world",
		},
		{
			"negative max returns unchanged",
			"hello",
			-5,
			"hello",
		},
		{
			"empty input",
			"",
			10,
			"",
		},
		{
			"line shorter than limit untouched",
			"short",
			20,
			"short",
		},
		{
			"line at exactly the limit untouched",
			"12345",
			5,
			"12345",
		},
		{
			"line longer than limit gets ellipsis",
			"123456789012",
			10,
			"123456789…",
		},
		{
			"multiline truncates each line independently",
			"short\nthis line is much longer than the limit",
			15,
			"short\nthis line is m…",
		},
		{
			"preserves blank lines",
			"a\n\nb",
			10,
			"a\n\nb",
		},
		{
			"handles unicode runes correctly",
			"αβγδεζηθικλμν",
			5,
			"αβγδ…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateLines(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("TruncateLines(%q, %d) = %q, want %q",
					tt.in, tt.max, got, tt.want)
			}
		})
	}
}

func TestDiffSince(t *testing.T) {
	tests := []struct {
		name           string
		previous, curr string
		want           string
	}{
		{"empty previous returns all", "", "anything", "anything"},
		{"equal returns empty", "same", "same", ""},
		{"pure append", "hello", "hello world", " world"},
		{
			"scrolled content",
			"L1\nL2\nL3\n",
			"L2\nL3\nL4\n",
			"L4\n",
		},
		{"no overlap returns all current", "abc", "xyz", "xyz"},
		{
			"multiline append",
			"line a\nline b\n",
			"line a\nline b\nline c\nline d\n",
			"line c\nline d\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiffSince(tt.previous, tt.curr)
			if got != tt.want {
				t.Fatalf("DiffSince(%q, %q) = %q, want %q",
					tt.previous, tt.curr, got, tt.want)
			}
		})
	}
}

func TestFormatForTelegram_ThoughtPropagation(t *testing.T) {
	in := "▸ Thought for 2s, 642 tokens\n  Analyzing Output Function\n  Investigating Missing Files\n\nSome follow-up prose."
	got := FormatForTelegram(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %+v", len(got), got)
	}
	if !got[0].IsCode {
		t.Errorf("expected first block to be code (thought + indented sub-lines), got %+v", got[0])
	}
	if got[0].Text != "▸ Thought for 2s, 642 tokens\n  Analyzing Output Function\n  Investigating Missing Files" {
		t.Errorf("first block text wrong: %q", got[0].Text)
	}
	if got[1].IsCode {
		t.Errorf("expected second block to be prose, got %+v", got[1])
	}
	if got[1].Text != "Some follow-up prose." {
		t.Errorf("second block text wrong: %q", got[1].Text)
	}
}

func TestStripEchoedInput(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		lastInput string
		want      string
	}{
		{
			"empty input returns body",
			"hello world",
			"",
			"hello world",
		},
		{
			"exact match no prefix",
			"hello",
			"hello",
			"",
		},
		{
			"match with prompt prefix",
			"> hello\nworld reply",
			"hello",
			"world reply",
		},
		{
			"match multiline",
			"> line 1\nline 2\nreply starts here",
			"line 1\nline 2",
			"reply starts here",
		},
		{
			"no match returns body",
			"> line 1\nline 2\nreply",
			"different line",
			"> line 1\nline 2\nreply",
		},
		{
			"prefix mismatch returns body",
			"some other prefix hello\nreply",
			"hello",
			"some other prefix hello\nreply",
		},
		{
			"match with leading horizontal rule and trailing spaces/dividers",
			"────────────────────────────────────────────\n> hello\n\n────────────────────────────────────────────\nworld reply",
			"hello",
			"world reply",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripEchoedInput(tt.body, tt.lastInput)
			if got != tt.want {
				t.Errorf("StripEchoedInput(%q, %q) = %q, want %q",
					tt.body, tt.lastInput, got, tt.want)
			}
		})
	}
}
