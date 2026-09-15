package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestLoad_EnvironmentAndUnlimitedTimeout(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, `
[telegram]
bot_token = "test:token"
allowed_user_ids = [42]

[session]
launch_command = "agy"

[session.env]
API_BASE = "https://example.test"

[bridge]
turn_timeout_seconds = -1
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env["API_BASE"] != "https://example.test" {
		t.Fatalf("Env = %#v", cfg.Env)
	}
	if cfg.TurnTimeout() != 0 || cfg.TurnTimeoutLabel() != "unlimited" {
		t.Fatalf("timeout = %v (%s)", cfg.TurnTimeout(), cfg.TurnTimeoutLabel())
	}
}

func TestLoad_RejectsInvalidEnvironmentName(t *testing.T) {
	p := writeConfig(t, t.TempDir(), `
[telegram]
bot_token = "test:token"
allowed_user_ids = [42]
[session]
launch_command = "bash"
[session.env]
"BAD;NAME" = "value"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "invalid [session.env] name") {
		t.Fatalf("expected invalid env-name error, got %v", err)
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

func TestLoad_ExplicitEmptyResumeArgsDisablesResume(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, `
[telegram]
bot_token = "t"
allowed_user_ids = [1]

[session]
launch_command = "one-shot-agent"

[bridge]
resume_args = []
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ResumeArgs == nil || len(cfg.ResumeArgs) != 0 {
		t.Fatalf("ResumeArgs = %#v, want explicit empty slice", cfg.ResumeArgs)
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

func TestExampleConfig_MatchesCheckedInTemplate(t *testing.T) {
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "examples", "config.toml.example"))
	if err != nil {
		t.Fatalf("read checked-in example: %v", err)
	}
	if strings.TrimSpace(string(checkedIn)) != strings.TrimSpace(ExampleConfig) {
		t.Fatal("ExampleConfig and examples/config.toml.example have drifted")
	}
}

func TestDefaultPATH_HasHomebrew(t *testing.T) {
	p := DefaultPATH()
	if !strings.Contains(p, "/opt/homebrew/bin") {
		t.Errorf("DefaultPATH missing homebrew: %s", p)
	}
}

func TestKnownPresets(t *testing.T) {
	tests := map[string]CLIPreset{
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
	if len(KnownPresets) != len(tests) {
		t.Fatalf("KnownPresets has %d entries, want %d", len(KnownPresets), len(tests))
	}

	for name, want := range tests {
		got, ok := KnownPresets[name]
		if !ok {
			t.Errorf("missing %q preset", name)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("KnownPresets[%q] = %#v, want %#v", name, got, want)
		}
	}
}

func TestUpdateCLI_PreservesUnlimitedTimeout(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, Render(RenderParams{
		BotToken:   "test:token",
		UserID:     42,
		LaunchCmd:  KnownPresets["claude"].LaunchCmd,
		WorkingDir: dir,
		PromptFlag: KnownPresets["claude"].PromptFlag,
		ResumeArgs: KnownPresets["claude"].ResumeArgs,
	})+"\nturn_timeout_seconds = -1\n")

	if err := UpdateCLI(path, KnownPresets["agy"]); err != nil {
		t.Fatalf("UpdateCLI: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TurnTimeout() != 0 {
		t.Fatalf("TurnTimeout = %v, want unlimited", cfg.TurnTimeout())
	}
}

func TestRenderAndLoad_CodexPreset(t *testing.T) {
	preset := KnownPresets["codex"]
	body := Render(RenderParams{
		BotToken:   "test:token",
		UserID:     42,
		LaunchCmd:  preset.LaunchCmd,
		WorkingDir: t.TempDir(),
		PromptFlag: preset.PromptFlag,
		ResumeArgs: preset.ResumeArgs,
	})
	if !strings.Contains(body, `prompt_flag = "--"`) {
		t.Fatalf("rendered config missing positional prompt separator:\n%s", body)
	}
	if !strings.Contains(body, `resume_args = ["resume", "--last"]`) {
		t.Fatalf("rendered config missing Codex resume args:\n%s", body)
	}

	path := writeConfig(t, t.TempDir(), body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PromptFlag != "--" {
		t.Errorf("PromptFlag = %q, want %q", cfg.PromptFlag, "--")
	}
	if !reflect.DeepEqual(cfg.ResumeArgs, []string{"resume", "--last"}) {
		t.Errorf("ResumeArgs = %#v", cfg.ResumeArgs)
	}
}

func TestUpdateCLI_SwitchesToCodexAndPreservesConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, Render(RenderParams{
		BotToken:   "test:token",
		UserID:     42,
		LaunchCmd:  KnownPresets["agy"].LaunchCmd,
		WorkingDir: dir,
		PromptFlag: KnownPresets["agy"].PromptFlag,
		ResumeArgs: KnownPresets["agy"].ResumeArgs,
	}))

	if err := UpdateCLI(path, KnownPresets["codex"]); err != nil {
		t.Fatalf("UpdateCLI: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LaunchCommand != KnownPresets["codex"].LaunchCmd {
		t.Errorf("LaunchCommand = %q", cfg.LaunchCommand)
	}
	if cfg.PromptFlag != "--" {
		t.Errorf("PromptFlag = %q", cfg.PromptFlag)
	}
	if !reflect.DeepEqual(cfg.ResumeArgs, []string{"resume", "--last"}) {
		t.Errorf("ResumeArgs = %#v", cfg.ResumeArgs)
	}
	if cfg.BotToken != "test:token" || cfg.WorkingDir != dir {
		t.Error("UpdateCLI changed non-CLI settings")
	}
}
