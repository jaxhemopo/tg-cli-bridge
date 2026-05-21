package output

import (
	"regexp"
	"strings"
)

// Menu is a numbered selection menu detected at the bottom of a tmux pane —
// the kind of confirmation dialog agentic CLIs use ("Do you want to proceed?
// 1. Yes  2. ...  3. ...  4. No"). The bridge translates these into Telegram
// inline-keyboard buttons.
type Menu struct {
	// Question is the line that introduced the menu, e.g. "Do you want to
	// proceed?". May be empty when the prompt was on a line we couldn't
	// reliably identify.
	Question string

	// Options preserves the source ordering. Numbers are 1-based to match
	// what the user sees on screen.
	Options []MenuOption

	// Selected is the 1-based index marked by `>` in the source, or 0 when
	// no option is currently highlighted.
	Selected int
}

// MenuOption is one row of a numbered menu.
type MenuOption struct {
	Number int
	Label  string
}

// menuOptionRE matches a single numbered-option line:
//
//	[indent] [> ] N. Label
//
// The leading `>` indicates the currently-selected option in the source.
var menuOptionRE = regexp.MustCompile(`^[ \t]*(>?)[ \t]*([0-9]+)\.[ \t]+(.+?)[ \t]*$`)

// DetectMenu inspects the bottom of pane text for an active numbered-option
// menu and returns it. nil means no menu was detected.
//
// Heuristic: look at the last ~25 non-empty lines, scan for the longest run
// of consecutive option lines numbered sequentially starting at 1.
func DetectMenu(pane string) *Menu {
	if pane == "" {
		return nil
	}
	allLines := strings.Split(pane, "\n")
	// Walk from the bottom up; collect the last ~25 non-empty lines and
	// remember their original positions.
	type record struct {
		idx  int
		line string
	}
	var tail []record
	for i := len(allLines) - 1; i >= 0 && len(tail) < 25; i-- {
		if strings.TrimSpace(allLines[i]) == "" {
			continue
		}
		tail = append([]record{{idx: i, line: allLines[i]}}, tail...)
	}
	if len(tail) < 2 {
		return nil
	}

	// Find a run of consecutive numbered options at the bottom.
	type parsedOpt struct {
		number   int
		label    string
		selected bool
		srcIdx   int
	}
	var bestRun []parsedOpt
	var run []parsedOpt
	for _, r := range tail {
		m := menuOptionRE.FindStringSubmatch(r.line)
		if m == nil {
			// Some agents wrap long options onto a second line that doesn't
			// start with `N.`. Append to the previous option's label so we
			// don't lose it.
			if len(run) > 0 && strings.HasPrefix(r.line, "  ") {
				run[len(run)-1].label += " " + strings.TrimSpace(r.line)
				continue
			}
			if len(run) > len(bestRun) {
				bestRun = run
			}
			run = nil
			continue
		}
		num := atoi(m[2])
		// Expect consecutive numbering starting at 1.
		expected := len(run) + 1
		if num != expected {
			if len(run) > len(bestRun) {
				bestRun = run
			}
			run = []parsedOpt{{
				number:   num,
				label:    m[3],
				selected: m[1] == ">",
				srcIdx:   r.idx,
			}}
			continue
		}
		run = append(run, parsedOpt{
			number:   num,
			label:    m[3],
			selected: m[1] == ">",
			srcIdx:   r.idx,
		})
	}
	if len(run) > len(bestRun) {
		bestRun = run
	}

	if len(bestRun) < 2 || bestRun[0].number != 1 {
		return nil
	}

	menu := &Menu{Options: make([]MenuOption, 0, len(bestRun))}
	for i, opt := range bestRun {
		menu.Options = append(menu.Options, MenuOption{
			Number: opt.number,
			Label:  cleanLabel(opt.label),
		})
		if opt.selected {
			menu.Selected = i + 1
		}
	}
	// Question = the most recent non-empty line above the first option that
	// isn't itself an option line.
	firstSrcIdx := bestRun[0].srcIdx
	hasPromptMarker := false
	for j := firstSrcIdx - 1; j >= 0 && j >= firstSrcIdx-5; j-- {
		line := strings.TrimSpace(allLines[j])
		if line == "" {
			continue
		}
		if menuOptionRE.MatchString(line) {
			break
		}
		menu.Question = line
		hasPromptMarker = isHelpOrPrompt(line)
		break
	}

	// Heuristic refinement:
	// 1. If we have 3+ items, it's likely a menu even without a clear prompt.
	// 2. If we have only 2 items, we REQUIRE a clear prompt/help marker above
	//    or a prompt/help marker below. This avoids misidentifying small
	//    numbered lists in prose.
	if len(bestRun) == 2 && !hasPromptMarker {
		// Check if there's a prompt below the items.
		foundPromptBelow := false
		lastIdxInTail := -1
		for idx, opt := range tail {
			if opt.idx == bestRun[len(bestRun)-1].srcIdx {
				lastIdxInTail = idx
				break
			}
		}
		if lastIdxInTail != -1 {
			for i := lastIdxInTail + 1; i < len(tail); i++ {
				if isHelpOrPrompt(tail[i].line) {
					foundPromptBelow = true
					break
				}
			}
		}
		if !foundPromptBelow {
			return nil
		}
	}

	return menu
}

func isHelpOrPrompt(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "↑") ||
		strings.HasPrefix(trimmed, "↓") ||
		strings.HasPrefix(trimmed, ">") ||
		strings.HasPrefix(trimmed, "?") ||
		strings.Contains(lower, "navigate") ||
		strings.Contains(lower, "cancel") ||
		strings.Contains(lower, "amend") ||
		strings.Contains(lower, "choose") ||
		strings.Contains(lower, "select") {
		return true
	}
	return false
}

// cleanLabel tidies up a captured option label for use as a button caption:
// collapses whitespace and strips trailing punctuation noise.
func cleanLabel(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// atoi is a tiny strconv.Atoi without the error return — only called on a
// substring that has already matched [0-9]+ in the regex above.
func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
