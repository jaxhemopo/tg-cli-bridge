---
name: tg-cli-bridge
description: Install and configure tg-cli-bridge — a Telegram bot that lets you drive any agentic CLI (Gemini CLI, AGY/Antigravity, Claude Code) from your phone. Each message you send spawns the CLI with your prompt, waits for it to finish, and replies with the clean output. Supports session continuity, inline menu buttons, and live status updates while the agent works. Single static Go binary, macOS LaunchAgent included.
---

# tg-cli-bridge — installation skill

This skill walks through setting up `tg-cli-bridge` so the user can control
an agentic CLI from Telegram.

## When to invoke

The user wants any of:

- "Control my Gemini / AGY / Claude from my phone"
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
5. Session continuity is maintained via `--resume` or `--continue` flags so
   the agent remembers the conversation.

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
  - Gemini CLI: `npm install -g @google/gemini-cli`
  - AGY: `curl -fsSL https://get.agy.app | bash` (or their installer)
  - Claude Code: per Anthropic install docs

## Install steps

```bash
# 1. Build the binary (or go install for a released version)
git clone https://github.com/jnhemopo/tg-cli-bridge.git
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
| Gemini CLI | `gemini --yolo` | `--prompt` | `["--resume","latest"]` |
| AGY | `agy --dangerously-skip-permissions` | `--print` | `["--continue"]` |
| Claude Code | `claude` | `--print` | — |

**AGY note:** AGY reprints the full conversation history in `--continue` mode.
The bridge handles this automatically by diffing each turn's output against
the previous one.

## Things to watch for

- **`bot_token` is a secret.** Never paste the rendered config into chat, logs,
  or shared screens.
- **Only one process can poll a Telegram bot at a time.** If you see
  `409 Conflict: terminated by other getUpdates request`, run
  `tg-cli-bridge status` and kill any duplicate bridge processes.
- **PATH inside the spawned process is set explicitly** from config
  `[session].path` (or the default which covers Homebrew/local bins). If the
  CLI isn't found, add its directory to `path` in `config.toml`.
- **Session state is per-chat in memory.** If the bridge restarts, the next
  message starts a new session (no `--resume`). The agent usually recovers
  gracefully.

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
- Repo: <https://github.com/jnhemopo/tg-cli-bridge>
