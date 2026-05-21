package output

import "testing"

func TestDetectMenu_StandardYesNoMenu(t *testing.T) {
	pane := `Requesting permission for: tmux list-sessions

Do you want to proceed?
> 1. Yes
  2. Yes, and always allow in this conversation for commands that start with 'tmux'
  3. Yes, and always allow for commands that start with 'tmux' (Persist to settings.json)
  4. No

↑/↓ Navigate · tab Amend · e edit command
esc to cancel`
	m := DetectMenu(pane)
	if m == nil {
		t.Fatal("expected a menu")
	}
	if len(m.Options) != 4 {
		t.Fatalf("expected 4 options, got %d", len(m.Options))
	}
	if m.Options[0].Number != 1 || m.Options[0].Label != "Yes" {
		t.Errorf("option 1 wrong: %+v", m.Options[0])
	}
	if m.Options[3].Number != 4 || m.Options[3].Label != "No" {
		t.Errorf("option 4 wrong: %+v", m.Options[3])
	}
	if m.Selected != 1 {
		t.Errorf("expected Selected=1, got %d", m.Selected)
	}
	if m.Question != "Do you want to proceed?" {
		t.Errorf("question wrong: %q", m.Question)
	}
}

func TestDetectMenu_NoMenu(t *testing.T) {
	pane := "Hello! I'm Antigravity.\n\nJust ordinary prose here.\nNothing menu-like."
	if m := DetectMenu(pane); m != nil {
		t.Errorf("expected nil, got %+v", m)
	}
}

func TestDetectMenu_EmptyPane(t *testing.T) {
	if m := DetectMenu(""); m != nil {
		t.Error("empty pane should produce no menu")
	}
}

func TestDetectMenu_SingleOptionIsNotAMenu(t *testing.T) {
	// One numbered line isn't a menu — could just be a list-item in prose.
	pane := "Step 1: do the thing.\n  1. Read the README"
	if m := DetectMenu(pane); m != nil {
		t.Errorf("single option shouldn't be a menu: %+v", m)
	}
}

func TestDetectMenu_NonConsecutiveNumbersRestart(t *testing.T) {
	// Numbers must be consecutive starting at 1. "2. foo\n3. bar" is not
	// a complete menu we should bind buttons to.
	pane := "  2. foo\n  3. bar"
	if m := DetectMenu(pane); m != nil {
		t.Errorf("non-starts-at-1 menu should be rejected: %+v", m)
	}
}

func TestDetectMenu_WrappedSecondLineFolded(t *testing.T) {
	pane := `Pick one:
  1. The first option which has a very long description
     that wraps onto a second line of the terminal
  2. Second option`
	m := DetectMenu(pane)
	if m == nil {
		t.Fatal("expected menu")
	}
	if len(m.Options) != 2 {
		t.Fatalf("expected 2 options, got %d: %+v", len(m.Options), m.Options)
	}
	if !contains(m.Options[0].Label, "wraps onto a second line") {
		t.Errorf("wrapped continuation not folded into option 1: %q", m.Options[0].Label)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestDetectMenu_StaticListInProseIsNotAMenu(t *testing.T) {
	pane := `Here are some items you should check:
1. First item to check
2. Second item to check
3. Third item to check

For more details on the changes, please check the logs.`
	if m := DetectMenu(pane); m != nil {
		t.Errorf("static list followed by prose should not be a menu: %+v", m)
	}
}
