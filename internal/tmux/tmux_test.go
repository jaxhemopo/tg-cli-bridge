package tmux

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jaxhemopo/tg-cli-bridge/internal/config"
)

func skipIfNoTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
}

func newTestSession(t *testing.T, launch string) (*Session, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		BotToken:          "fake",
		AllowedUserIDs:    map[int64]struct{}{1: {}},
		TmuxSession:       "tg_cli_bridge_gotest",
		LaunchCommand:     launch,
		WorkingDir:        "/tmp",
		PathEnv:           "/usr/local/bin:/usr/bin:/bin",
		TmuxWidth:         80,
		TmuxHeight:        24,
		PollInterval:      0.2,
		QuiescenceSeconds: 0.4,
		MinCharsToFlush:   1,
		CaptureLines:      100,
		MaxMessageChars:   3800,
	}
	// Clean up any leftover from a previous failed run.
	_ = exec.Command("tmux", "kill-session", "-t", cfg.TmuxSession).Run()
	return New(cfg), cfg
}

func TestEnsureCreatesSession(t *testing.T) {
	skipIfNoTmux(t)
	s, _ := newTestSession(t, "echo hello")
	defer s.Kill()

	if s.Exists() {
		t.Fatal("session should not exist before Ensure")
	}
	if err := s.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !s.Exists() {
		t.Fatal("session should exist after Ensure")
	}
}

func TestSessionSurvivesAgentExit(t *testing.T) {
	skipIfNoTmux(t)
	// Simulate an agent that runs, prints, and exits like any other command.
	// The original Python bug was: agent exits -> pane dies -> session dies.
	s, _ := newTestSession(t, "echo agent-started; echo agent-done")
	defer s.Kill()

	if err := s.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	time.Sleep(800 * time.Millisecond)

	if !s.Exists() {
		t.Fatal("session was destroyed when the agent exited (regression!)")
	}
	pane := s.Capture()
	if !strings.Contains(pane, "agent-done") {
		t.Errorf("agent output missing from pane: %q", pane)
	}
}

func TestCanSendTextAndCaptureReply(t *testing.T) {
	skipIfNoTmux(t)
	s, _ := newTestSession(t, "echo started")
	defer s.Kill()

	if err := s.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	time.Sleep(800 * time.Millisecond)
	if err := s.SendText("echo recovered-marker", true); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	pane := s.Capture()
	if !strings.Contains(pane, "recovered-marker") {
		t.Errorf("expected 'recovered-marker' in pane, got: %q", pane)
	}
}

func TestKillAndRestart(t *testing.T) {
	skipIfNoTmux(t)
	s, _ := newTestSession(t, "echo started")
	defer s.Kill()

	if err := s.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !s.Exists() {
		t.Fatal("not alive")
	}
	s.Kill()
	if s.Exists() {
		t.Fatal("still alive after Kill")
	}
	if err := s.Ensure(); err != nil {
		t.Fatalf("Ensure (after kill): %v", err)
	}
	if !s.Exists() {
		t.Fatal("not alive after re-Ensure")
	}
}

func TestCaptureOnDeadSessionReturnsEmpty(t *testing.T) {
	skipIfNoTmux(t)
	s, _ := newTestSession(t, "echo started")
	if s.Exists() {
		t.Fatal("session already exists (cleanup leak)")
	}
	if got := s.Capture(); got != "" {
		t.Errorf("expected empty capture on dead session, got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"with'quote", `'with'\''quote'`},
		{"", "''"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.in)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
