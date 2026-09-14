---
name: tg-cli-bridge
description: Install and configure tg-cli-bridge — a Telegram bot that lets you drive AGY/Antigravity, Claude Code, Codex, a Claude-compatible GLM wrapper, or another headless agent CLI from your phone. Each message spawns the CLI with your prompt, waits for it to finish, and replies with clean output. Supports latest-session continuity, inline menu buttons, and live status updates while the agent works. Single static Go binary, macOS LaunchAgent included.
---

# tg-cli-bridge — installation skill

This skill walks through setting up `tg-cli-bridge` so the user can control
an agentic CLI from Telegram.

## When to invoke

The user wants any of:

- "Control my AGY / Claude / Codex from my phone"
- "Send prompts to my agent via Telegram"
- "Check my email / Google Drive from Telegram"
- "Set up a Telegram bot that talks to my CLI agent"
- "Bridge my terminal agent to my phone"

## What you're delivering

A working setup where:

1. A **Telegram bot** receives messages from the user's phone.
2. Each message spawns the **agent CLI** once in headless mode with the user's
   text as the prompt.
3. The agent's output is cleaned up (ANSI stripped, tool-call boxes removed)
   and sent back as a Telegram reply.
4. A **macOS LaunchAgent** keeps the bridge running across reboots and crashes.
5. Session continuity uses the selected CLI's latest-session argument, with
   the isolation limitation described below.

## Prerequisites — confirm before installing

- **macOS** (the `install` subcommand writes a LaunchAgent plist; Linux needs
  manual systemd setup).
- **Go 1.22+** (`go version`). One-time: `brew install go`.
- **A Telegram bot token** from `@BotFather`:
  1. Open Telegram, search `@BotFather`, send `/newbot`.
  2. Pick a display name and a username ending in `bot`.
  3. Copy the token BotFather replies with.
- **The user's numeric Telegram user ID** — have them message `@userinfobot`.
- **The agent CLI they want to drive**, installed and working in a terminal
  first:
  - AGY: `curl -fsSL https://get.agy.app | bash` (or their installer)
  - Claude Code: per Anthropic install docs
  - Codex CLI: install and authenticate it according to the OpenAI docs

## Install steps

```bash
# 1. Build the binary (or go install for a released version)
git clone https://github.com/jaxhemopo/tg-cli-bridge.git
cd tg-cli-bridge
go build -o tg-cli-bridge ./cmd/tg-cli-bridge

# 2. Interactive config wizard — picks the right flags per CLI automatically
./tg-cli-bridge init

# 3. Smoke-test in the foreground
./tg-cli-bridge run
# Have the user send /start from their phone.
# You should see "Received from user_id=…" in the terminal.
# Ctrl-C when confirmed working.

# 4. Install as a background service
sudo cp tg-cli-bridge /usr/local/bin/
tg-cli-bridge install
tg-cli-bridge status   # should show LaunchAgent: 🟢 loaded
```

## CLI-specific flags

The `init` wizard handles this, but for reference:

| CLI | launch_command | prompt_flag | resume_args |
|-----|---------------|-------------|-------------|
| AGY | `agy --dangerously-skip-permissions` | `--print` | `["--continue"]` |
| Claude Code | `claude --dangerously-skip-permissions` | `--print` | `["--continue"]` |
| Codex CLI | `codex exec --sandbox workspace-write` | `--` | `["resume","--last"]` |
| Claude + GLM wrapper | `claude-glm --dangerously-skip-permissions` | `--print` | `["--continue"]` |

**AGY note:** AGY reprints the full conversation history in `--continue` mode.
The bridge handles this automatically by diffing each turn's output against
the previous one.

For a custom CLI, put fixed engine/model arguments in `launch_command`, use
the one-shot flag as `prompt_flag` (`--` for a positional prompt), and put the
latest-session arguments in `resume_args`. The command is split into arguments,
not evaluated by a shell, so do not use pipes, redirects, or shell assignments.
Use `[session.env]` for required environment variables. A negative
`turn_timeout_seconds` disables the deadline and should be used deliberately.

## Things to watch for

- **`bot_token` is a secret.** Never paste the rendered config into chat, logs,
  or shared screens.
- **Only one process can poll a Telegram bot at a time.** If you see
  `409 Conflict: terminated by other getUpdates request`, run
  `tg-cli-bridge status` and kill any duplicate bridge processes.
- **PATH inside the spawned process is set explicitly** from config
  `[session].path` (or the default which covers Homebrew/local bins). If the
  CLI isn't found, add its directory to `path` in `config.toml`.
- **Latest-session selection is not isolated per Telegram chat.** The bridge
  remembers only whether a chat has started; the CLI chooses its own newest
  saved conversation. Use one engine and one Telegram chat per `working_dir`,
  do not run the same CLI manually there, and do not run different engines
  against the same files simultaneously. After `/switch`, send `/new`.
- **The macOS installer manages one LaunchAgent.** Run one background bridge
  at a time. Concurrent bots require separate tokens, configs, working
  directories, and manually managed service identities.
- **File auto-send starts off.** Enable it per chat with `/files on` only when
  the agent is expected to create files that should be returned to Telegram.
- **`/switch` and `/model` have buttons.** Typed forms such as `/switch codex`
  still work; `/model` applies only to Claude-compatible launch commands.

## Troubleshooting flow

1. `tg-cli-bridge status` → is the LaunchAgent loaded?
2. `tg-cli-bridge logs` → tail errors. Common ones in `docs/troubleshooting.md`.
3. Try `./tg-cli-bridge run` in the foreground to see live output.

## What NOT to do

- Don't pipe the agent's stdout directly without ANSI stripping. The agent
  outputs escape sequences, progress bars, and tool-call boxes that render as
  garbage in Telegram.
- Don't grant bot access to multiple untrusted users — the bot has shell-level
  access via the agent CLI.

## Reference

- Architecture and design rationale: `docs/architecture.md`
- Troubleshooting catalogue: `docs/troubleshooting.md`
- Repo: <https://github.com/jaxhemopo/tg-cli-bridge>
