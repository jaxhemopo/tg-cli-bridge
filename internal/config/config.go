// Package config loads, validates, and writes the bridge's TOML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// TurnTimeout returns TurnTimeoutSeconds as a time.Duration. Convenience
// wrapper so callers don't repeat the conversion math.
func (c *Config) TurnTimeout() time.Duration {
	if c.TurnTimeoutSeconds <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(c.TurnTimeoutSeconds) * time.Second
}

// Config is the resolved, validated configuration the bridge needs to run.
type Config struct {
	// Telegram
	BotToken       string
	AllowedUserIDs map[int64]struct{}

	// Session
	TmuxSession   string
	LaunchCommand string
	WorkingDir    string
	PathEnv       string
	TmuxWidth     int
	TmuxHeight    int

	// Bridge tuning
	PollInterval      float64
	QuiescenceSeconds float64
	MinCharsToFlush   int
	CaptureLines      int
	MaxMessageChars   int
	CodeMaxLineChars  int  // truncate each line in code blocks to this many runes; 0 = unlimited
	HideToolCalls     bool // when true, drop code-classified blocks entirely (tool calls, banners, thoughts) — keep only prose + menus

	// RPC mode (default flow): each Telegram message spawns the agent CLI once
	// with the configured prompt shape and ships stdout as a clean reply.
	PromptFlag         string   // flag the CLI uses for the prompt, e.g. "--prompt"
	ResumeArgs         []string // args appended for session resume, e.g. ["--resume","latest"]
	TurnTimeoutSeconds int      // max seconds to wait for one agent invocation

	// Source of this config — useful for status and logs.
	SourcePath string
}

// rawConfig mirrors the on-disk TOML shape.
type rawConfig struct {
	Telegram struct {
		BotToken       string  `toml:"bot_token"`
		AllowedUserIDs []int64 `toml:"allowed_user_ids"`
	} `toml:"telegram"`

	Session struct {
		TmuxSession   string `toml:"tmux_session"`
		LaunchCommand string `toml:"launch_command"`
		WorkingDir    string `toml:"working_dir"`
		Path          string `toml:"path"`
		Width         int    `toml:"width"`
		Height        int    `toml:"height"`
	} `toml:"session"`

	Bridge struct {
		PollIntervalSeconds float64 `toml:"poll_interval_seconds"`
		QuiescenceSeconds   float64 `toml:"quiescence_seconds"`
		MinCharsToFlush     int     `toml:"min_chars_to_flush"`
		CaptureLines        int     `toml:"capture_lines"`
		MaxMessageChars     int     `toml:"max_message_chars"`
		CodeMaxLineChars    int     `toml:"code_max_line_chars"`
		// HideToolCalls is a *string* in TOML so a missing field defaults
		// to "true" while still letting users explicitly say "false". The
		// decoded value is normalised in Load().
		HideToolCalls *bool `toml:"hide_tool_calls"`

		// RPC mode plumbing.
		PromptFlag         string   `toml:"prompt_flag"`
		ResumeArgs         []string `toml:"resume_args"`
		TurnTimeoutSeconds int      `toml:"turn_timeout_seconds"`
	} `toml:"bridge"`
}

// CLIPreset holds the CLI-specific settings for a known agent.
type CLIPreset struct {
	LaunchCmd  string
	PromptFlag string   // empty = use default "--prompt"; "--" terminates options for positional prompts
	ResumeArgs []string // nil = use default ["--resume","latest"]
}

// KnownPresets maps the short names used by /switch to their CLI settings.
var KnownPresets = map[string]CLIPreset{
	"agy": {
		LaunchCmd:  "agy --dangerously-skip-permissions",
		PromptFlag: "--print",
		ResumeArgs: []string{"--continue"},
	},
	"claude": {
		LaunchCmd:  "claude --dangerously-skip-permissions",
		PromptFlag: "--print",
		ResumeArgs: []string{"--continue"},
	},
	"codex": {
		LaunchCmd:  "codex exec --sandbox workspace-write",
		PromptFlag: "--",
		ResumeArgs: []string{"resume", "--last"},
	},
}

// UpdateCLI rewrites the CLI-specific fields in an existing config file while
// preserving all other settings (Telegram credentials, working_dir, etc.).
func UpdateCLI(path string, p CLIPreset) error {
	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return err
	}
	raw.Session.LaunchCommand = p.LaunchCmd
	raw.Bridge.PromptFlag = p.PromptFlag
	raw.Bridge.ResumeArgs = p.ResumeArgs
	f, err := os.CreateTemp(filepath.Dir(path), ".tg-cli-bridge-config-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}
	if err := toml.NewEncoder(f).Encode(raw); err != nil {
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// DefaultPath returns the default config file location.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "tg-cli-bridge", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tg-cli-bridge", "config.toml")
}

// DefaultPATH returns a sensible PATH for the tmux shell. Includes Antigravity,
// Homebrew, common user bins.
func DefaultPATH() string {
	home, _ := os.UserHomeDir()
	parts := []string{
		filepath.Join(home, ".antigravity", "antigravity", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".cargo", "bin"),
	}
	return strings.Join(parts, ":")
}

// Load reads and validates a TOML config from path.
//
// Defaults are filled in for any [bridge] tuning fields left unset; missing
// required fields ([telegram].bot_token, etc.) produce a friendly error.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config not found at %s — run `tg-cli-bridge init` to create one", path)
	}

	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if raw.Telegram.BotToken == "" {
		return nil, fmt.Errorf("%s: missing [telegram].bot_token", path)
	}
	if len(raw.Telegram.AllowedUserIDs) == 0 {
		return nil, fmt.Errorf("%s: missing [telegram].allowed_user_ids", path)
	}
	if raw.Session.LaunchCommand == "" {
		return nil, fmt.Errorf("%s: missing [session].launch_command", path)
	}

	allowed := make(map[int64]struct{}, len(raw.Telegram.AllowedUserIDs))
	for _, id := range raw.Telegram.AllowedUserIDs {
		allowed[id] = struct{}{}
	}

	home, _ := os.UserHomeDir()

	cfg := &Config{
		BotToken:          raw.Telegram.BotToken,
		AllowedUserIDs:    allowed,
		TmuxSession:       defaultStr(raw.Session.TmuxSession, "ag"),
		LaunchCommand:     raw.Session.LaunchCommand,
		WorkingDir:        defaultStr(raw.Session.WorkingDir, home),
		PathEnv:           defaultStr(raw.Session.Path, DefaultPATH()),
		TmuxWidth:         defaultInt(raw.Session.Width, 80),
		TmuxHeight:        defaultInt(raw.Session.Height, 24),
		PollInterval:      defaultFloat(raw.Bridge.PollIntervalSeconds, 0.6),
		QuiescenceSeconds: defaultFloat(raw.Bridge.QuiescenceSeconds, 1.2),
		MinCharsToFlush:   defaultInt(raw.Bridge.MinCharsToFlush, 8),
		CaptureLines:      defaultInt(raw.Bridge.CaptureLines, 200),
		MaxMessageChars:   defaultInt(raw.Bridge.MaxMessageChars, 3800),
		CodeMaxLineChars:  defaultInt(raw.Bridge.CodeMaxLineChars, 80),
		HideToolCalls:     raw.Bridge.HideToolCalls == nil || *raw.Bridge.HideToolCalls,
		PromptFlag:        defaultStr(raw.Bridge.PromptFlag, "--prompt"),
		ResumeArgs: func() []string {
			if raw.Bridge.ResumeArgs != nil {
				return raw.Bridge.ResumeArgs
			}
			return []string{"--resume", "latest"}
		}(),
		TurnTimeoutSeconds: defaultInt(raw.Bridge.TurnTimeoutSeconds, 600),
		SourcePath:         path,
	}
	return cfg, nil
}

// WriteExample writes an annotated example config to path.
// Returns os.ErrExist if path already exists; refuses to overwrite.
func WriteExample(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists: %w", path, os.ErrExist)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.TrimSpace(ExampleConfig)+"\n"), 0o600)
}

// RenderParams holds the values collected by the init wizard.
type RenderParams struct {
	BotToken   string
	UserID     int64
	LaunchCmd  string
	WorkingDir string
	// PromptFlag and ResumeArgs are only written when non-default.
	PromptFlag string
	ResumeArgs []string
}

// Render returns a complete config.toml as a string. Used by `init`.
func Render(p RenderParams) string {
	extra := ""
	if p.PromptFlag != "" && p.PromptFlag != "--prompt" {
		extra += fmt.Sprintf("prompt_flag = %q\n", p.PromptFlag)
	}
	if p.ResumeArgs != nil {
		quoted := make([]string, len(p.ResumeArgs))
		for i, a := range p.ResumeArgs {
			quoted[i] = fmt.Sprintf("%q", a)
		}
		extra += fmt.Sprintf("resume_args = [%s]\n", strings.Join(quoted, ", "))
	}
	return fmt.Sprintf(`# tg-cli-bridge configuration. Treat this file as a secret.

[telegram]
bot_token = %q
allowed_user_ids = [%d]

[session]
launch_command = %q
working_dir = %q

[bridge]
max_message_chars = 3800
%s`, p.BotToken, p.UserID, p.LaunchCmd, p.WorkingDir, extra)
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func defaultFloat(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

func defaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// ExampleConfig is an annotated template printed by `tg-cli-bridge example`.
// Keep in sync with examples/config.toml.example.
const ExampleConfig = `
# tg-cli-bridge example configuration.
# Copy to ~/.config/tg-cli-bridge/config.toml and fill in your values.
# Treat the real file as a secret — it contains your bot token.

[telegram]
# Token from @BotFather. /newbot to create one.
bot_token = "PUT_YOUR_BOT_TOKEN_HERE"

# Your numeric Telegram user ID. Message @userinfobot on Telegram to find it.
# Only these IDs can send messages to your bot.
allowed_user_ids = [123456789]


[session]
# Command to launch your agent CLI. Examples:
#   "agy --dangerously-skip-permissions"     — AGY / Antigravity
#   "claude --dangerously-skip-permissions"  — Claude Code
#   "codex exec --sandbox workspace-write"   — Codex CLI
#   "bash"                                   — plain shell (useful for testing)
launch_command = "agy --dangerously-skip-permissions"

# Directory the agent launches in. Use a full path, ~ is not expanded.
# Put the instruction file recognized by your engine here (for example,
# AGENTS.md for Codex or CLAUDE.md for Claude Code).
working_dir = "/Users/YOUR_USERNAME/workspace"

# Optional: override the PATH the agent process inherits.
# Default covers Homebrew, ~/.local/bin, ~/.cargo/bin, ~/.bun/bin.
# path = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"


[bridge]
# Maximum characters per Telegram message. Telegram's hard limit is 4096.
max_message_chars = 3800

# Flag the CLI uses for a headless/non-interactive prompt.
# AGY and Claude use --print. Codex accepts a positional prompt, so use --
# to end option parsing before the Telegram message.
prompt_flag = "--print"
# prompt_flag = "--"  # Codex

# Args inserted after launch_command to resume the CLI's latest session.
resume_args = ["--continue"]
# resume_args = ["resume", "--last"]  # Codex
# resume_args = []                      # no session continuation

# The bridge does not store conversation IDs. Latest-session selection is not
# isolated per Telegram chat; use one engine and chat per working directory.

# How long to wait for one agent invocation before killing it (seconds).
# Default is 600 (10 minutes). Increase for very long-running tasks.
# turn_timeout_seconds = 600
`
