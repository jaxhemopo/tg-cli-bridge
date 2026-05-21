// Package tmux is a thin, well-behaved wrapper around the tmux command-line tool.
//
// Design choices worth knowing:
//
//   - We launch a plain `bash -l` first, then send-keys the agent command into
//     it. If we exec'd the agent over the shell, the pane would die when the
//     agent exits (auth prompt, crash, /quit), taking the whole session with
//     it. By keeping a shell behind the agent, /restart from Telegram is
//     recoverable and the user can read the agent's final output.
//
//   - remain-on-exit is enabled belt-and-braces, so even a dying pane stays
//     visible.
//
//   - PATH is force-set inside the shell via send-keys. Relying on the user's
//     .bashrc / .bash_profile / .zshrc is a common source of "works in my
//     terminal but not in the bridge" foot-guns.
package tmux

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jaxhemopo/tg-cli-bridge/internal/config"
	"github.com/jaxhemopo/tg-cli-bridge/internal/output"
)

// Session manages a single named tmux session that hosts an agent CLI.
type Session struct {
	cfg     *config.Config
	tmuxBin string
}

// New returns a Session bound to the given config.
func New(cfg *config.Config) *Session {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		bin = "/opt/homebrew/bin/tmux" // best-effort default on macOS
	}
	return &Session{cfg: cfg, tmuxBin: bin}
}

// envForTmux builds the environment block passed to tmux subprocess calls.
// tmux propagates these to the child shell, so anything set here is also
// visible inside the bash login that hosts the agent.
//
//   - PATH        — agent CLI lookup
//   - BASH_SILENCE_DEPRECATION_WARNING=1 — stop macOS bash from printing the
//     "default shell is now zsh" notice on every login, which used to show up
//     at the top of the captured pane.
func (s *Session) envForTmux() []string {
	return append(os.Environ(),
		"PATH="+s.cfg.PathEnv,
		"BASH_SILENCE_DEPRECATION_WARNING=1",
	)
}

// Exists reports whether the named session currently exists.
func (s *Session) Exists() bool {
	return s.run("has-session", "-t", s.cfg.TmuxSession) == nil
}

// Ensure creates the session if missing and launches the agent inside it.
//
// The session is created with bash -l as its entry process; the agent is then
// typed in via send-keys. If the agent ever exits, the bash prompt is left
// behind for recovery.
func (s *Session) Ensure() error {
	if s.Exists() {
		return nil
	}
	log.Printf("Creating tmux session %q and launching %q",
		s.cfg.TmuxSession, s.cfg.LaunchCommand)

	// Step 1: create the session running a plain login shell. The shell
	// anchors the session and survives anything the agent does.
	if err := s.run(
		"new-session", "-d",
		"-s", s.cfg.TmuxSession,
		"-x", strconv.Itoa(s.cfg.TmuxWidth), "-y", strconv.Itoa(s.cfg.TmuxHeight),
		"env", "BASH_SILENCE_DEPRECATION_WARNING=1", "bash", "-l",
	); err != nil {
		return fmt.Errorf("creating tmux session: %w", err)
	}

	// If any pane in this window ever exits, keep it visible.
	_ = s.run("set-window-option", "-t", s.cfg.TmuxSession, "remain-on-exit", "on")

	// Give the shell a beat to print its prompt before we start typing.
	time.Sleep(300 * time.Millisecond)

	// Step 2: force PATH inside the shell. Don't rely on shell rc files.
	if err := s.sendLine(fmt.Sprintf("export PATH=%s", shellQuote(s.cfg.PathEnv))); err != nil {
		return fmt.Errorf("setting PATH: %w", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Step 3: launch the agent inside the live shell.
	launchLine := fmt.Sprintf("cd %s && %s",
		shellQuote(s.cfg.WorkingDir), s.cfg.LaunchCommand)
	if err := s.sendLine(launchLine); err != nil {
		return fmt.Errorf("launching agent: %w", err)
	}
	return nil
}

// Kill destroys the session.
func (s *Session) Kill() {
	_ = s.run("kill-session", "-t", s.cfg.TmuxSession)
}

// Restart destroys and re-creates the session, relaunching the agent fresh.
func (s *Session) Restart() error {
	s.Kill()
	time.Sleep(300 * time.Millisecond) // tmux needs a tick to release the name
	return s.Ensure()
}

// SendText types text into the session as if it came from the keyboard.
func (s *Session) SendText(text string, pressEnter bool) error {
	if err := s.run("send-keys", "-t", s.cfg.TmuxSession, "-l", text); err != nil {
		return err
	}
	if pressEnter {
		return s.run("send-keys", "-t", s.cfg.TmuxSession, "Enter")
	}
	return nil
}

// SendKey sends a tmux key name (e.g. "Enter", "Escape", "Up", "C-c", "Tab").
func (s *Session) SendKey(key string) error {
	return s.run("send-keys", "-t", s.cfg.TmuxSession, key)
}

// Capture returns the current rendered pane content, ANSI-stripped.
//
// Retries briefly on failure and returns an empty string on persistent error,
// so callers can keep polling instead of crashing.
func (s *Session) Capture() string {
	var lastErr string
	for attempt := 0; attempt < 3; attempt++ {
		stdout, stderr, err := s.runOutput(
			"capture-pane",
			"-t", s.cfg.TmuxSession,
			"-p", "-J",
			"-S", fmt.Sprintf("-%d", s.cfg.CaptureLines),
		)
		if err == nil {
			return output.StripANSI(stdout)
		}
		lastErr = stderr
		if attempt < 2 {
			time.Sleep(150 * time.Millisecond)
		}
	}
	log.Printf("capture-pane failed after retries: %s", strings.TrimSpace(lastErr))
	return ""
}

// -- internals --------------------------------------------------------------

func (s *Session) sendLine(line string) error {
	if err := s.run("send-keys", "-t", s.cfg.TmuxSession, "-l", line); err != nil {
		return err
	}
	return s.run("send-keys", "-t", s.cfg.TmuxSession, "Enter")
}

func (s *Session) run(args ...string) error {
	cmd := exec.Command(s.tmuxBin, args...)
	cmd.Env = s.envForTmux()
	return cmd.Run()
}

func (s *Session) runOutput(args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(s.tmuxBin, args...)
	cmd.Env = s.envForTmux()
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), errBuf.String(), err
}

// shellQuote single-quotes a string safely for inclusion on a shell command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
