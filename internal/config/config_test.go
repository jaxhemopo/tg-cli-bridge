package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestLoad_MinimalConfig(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, `
[telegram]
bot_token = "test:token"
allowed_user_ids = [42]

[session]
launch_command = "bash"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BotToken != "test:token" {
		t.Errorf("BotToken = %q", cfg.BotToken)
	}
	if _, ok := cfg.AllowedUserIDs[42]; !ok {
		t.Error("AllowedUserIDs missing 42")
	}
	if cfg.LaunchCommand != "bash" {
		t.Errorf("LaunchCommand = %q", cfg.LaunchCommand)
	}
	// Defaults populated:
	if cfg.TmuxSession != "ag" {
		t.Errorf("TmuxSession default = %q", cfg.TmuxSession)
	}
	if cfg.PollInterval != 0.6 {
		t.Errorf("PollInterval default = %v", cfg.PollInterval)
	}
	if cfg.CodeMaxLineChars != 80 {
		t.Errorf("CodeMaxLineChars default = %d", cfg.CodeMaxLineChars)
	}
	if cfg.TmuxWidth != 80 {
		t.Errorf("TmuxWidth default = %d", cfg.TmuxWidth)
	}
	if cfg.TmuxHeight != 24 {
		t.Errorf("TmuxHeight default = %d", cfg.TmuxHeight)
	}
	if !cfg.HideToolCalls {
		t.Error("HideToolCalls should default to true")
	}
}

func TestLoad_CustomDimensions(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, `
[telegram]
bot_token = "test:token"
allowed_user_ids = [42]

[session]
launch_command = "bash"
width = 40
height = 20
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TmuxWidth != 40 {
		t.Errorf("TmuxWidth = %d", cfg.TmuxWidth)
	}
	if cfg.TmuxHeight != 20 {
		t.Errorf("TmuxHeight = %d", cfg.TmuxHeight)
	}
}

func TestLoad_HideToolCallsExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, `
[telegram]
bot_token = "t"
allowed_user_ids = [1]

[session]
launch_command = "bash"

[bridge]
hide_tool_calls = false
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HideToolCalls {
		t.Error("explicit hide_tool_calls=false was overridden")
	}
	if cfg.SourcePath != p {
		t.Errorf("SourcePath = %q", cfg.SourcePath)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("error should mention `init`: %v", err)
	}
}

func TestLoad_MissingToken(t *testing.T) {
	p := writeConfig(t, t.TempDir(), `
[telegram]
allowed_user_ids = [42]
[session]
launch_command = "bash"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "bot_token") {
		t.Fatalf("expected bot_token error, got %v", err)
	}
}

func TestLoad_MissingUserIDs(t *testing.T) {
	p := writeConfig(t, t.TempDir(), `
[telegram]
bot_token = "t"
[session]
launch_command = "bash"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "allowed_user_ids") {
		t.Fatalf("expected allowed_user_ids error, got %v", err)
	}
}

func TestLoad_MissingLaunchCommand(t *testing.T) {
	p := writeConfig(t, t.TempDir(), `
[telegram]
bot_token = "t"
allowed_user_ids = [1]
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "launch_command") {
		t.Fatalf("expected launch_command error, got %v", err)
	}
}

func TestWriteExample_CreatesFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := WriteExample(target); err != nil {
		t.Fatalf("WriteExample: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "[telegram]") {
		t.Error("example file missing [telegram] section")
	}
}

func TestWriteExample_RefusesOverwrite(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(target, []byte("existing"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := WriteExample(target)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected ErrExist, got %v", err)
	}
}

func TestDefaultPATH_HasHomebrew(t *testing.T) {
	p := DefaultPATH()
	if !strings.Contains(p, "/opt/homebrew/bin") {
		t.Errorf("DefaultPATH missing homebrew: %s", p)
	}
}
