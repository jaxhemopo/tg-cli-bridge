# tg-cli-bridge

Control AGY, Gemini CLI, or Claude Code from your phone via Telegram. Send a
message, get a clean reply back. Drive long-running tasks, check your email,
manage files — all without opening your laptop.

A single static Go binary. No Python, no venv, no Docker.

```
Phone (Telegram) ──HTTPS──► Telegram Bot API ──long-poll──► tg-cli-bridge
                                                                   │
                                                        spawn once │
                                                                   ▼
                                   agy --dangerously-skip-permissions --print --continue "<text>"
                                                                   │
                                                       stdout      │  exit
                                                                   ▼
                                                 formatted reply → Telegram
```

## ⚠️ Security — read this first

This bridge runs your agent CLI with **all tool approvals disabled**
(`--dangerously-skip-permissions` for AGY, `--yolo` for Gemini). That means
the agent can run shell commands, read and write files, and make network
requests **without asking you first** — the same as running it locally in
auto-approve mode.

**What protects you:**

- `allowed_user_ids` in `config.toml` — only these Telegram IDs can send
  prompts to your bot. Keep this to just your own ID.
- Your bot token — if someone has it, they can impersonate any user to your
  bot. Treat it like a password. Never commit it, never share it.

**Be deliberate about what you ask remotely.** The agent acts with your local
user's full permissions. Don't leave the bridge running on a machine you
wouldn't otherwise leave unlocked.

---

## Quick start

```bash
# 1. Build
git clone https://github.com/jaxhemopo/tg-cli-bridge.git
cd tg-cli-bridge
go build -o tg-cli-bridge ./cmd/tg-cli-bridge

# 2. Configure (interactive wizard)
./tg-cli-bridge init

# 3. Smoke-test in the foreground
./tg-cli-bridge run
# Send /start to your bot from Telegram. Ctrl-C when happy.

# 4. Install as a background service (macOS)
tg-cli-bridge install
tg-cli-bridge status
```

## How it works

Each Telegram message spawns the agent CLI once in headless mode:

```
agy --dangerously-skip-permissions --print --continue "check my email"
```

The bridge waits for the process to exit, strips tool-call noise from the
output, and sends back only the agent's natural-language reply. Session
continuity is preserved via `--continue` (AGY) or `--resume latest` (Gemini)
so the agent remembers the conversation across messages.

While the agent works, the bridge posts a single status bubble that edits
itself in-place — `📧 Checking email…`, `📁 Browsing Drive…` — and deletes
it the moment the real reply is ready.

## Supported CLIs

The `init` wizard knows the right flags for each CLI out of the box.

| CLI | launch_command | Notes |
|-----|---------------|-------|
| **AGY / Antigravity** | `agy --dangerously-skip-permissions` | Auto-approves all tools |
| Gemini CLI | `gemini --yolo` | Auto-approves all tools |
| Claude Code | `claude` | Requires manual tool approval unless `--dangerously-skip-permissions` is set |
| Plain shell | `bash` | Useful for testing |

## Switching CLIs from Telegram

You can switch live without touching the terminal:

```
/switch agy
/switch gemini
/switch claude
```

The bridge updates `config.toml`, restarts itself, and comes back on the new
CLI within a few seconds. Send `/new` after switching to clear the previous
session state.

## Telegram commands

Send any plain text and it's forwarded to the agent as a prompt.

| Command | What it does |
|---------|-------------|
| `/new` | Start a fresh session (forget conversation history) |
| `/switch <name>` | Switch CLI live — `agy`, `gemini`, or `claude` |
| `/status` | Show current CLI and service state |
| `/yes` | Shorthand for sending "1" to a numbered menu |
| `/help` | List all commands |

## Configuration

Config lives at `~/.config/tg-cli-bridge/config.toml` (created by `init`).
See `examples/config.toml.example` for a fully annotated template.

```toml
[telegram]
bot_token        = "YOUR_BOT_TOKEN"   # from @BotFather
allowed_user_ids = [123456789]        # your Telegram user ID (@userinfobot)

[session]
launch_command = "agy --dangerously-skip-permissions"
working_dir    = "/Users/you/workspace"

[bridge]
max_message_chars   = 3800
prompt_flag         = "--print"
resume_args         = ["--continue"]
# turn_timeout_seconds = 600          # kill the CLI after this long (default 10m)
```

**The config contains your bot token — treat it like a password. Never commit it.**

## Context files

Put a `CLAUDE.md` or `GEMINI.md` in your `working_dir` to give the agent
context about what tools are available, how to use them, and any workspace
conventions. The bridge sets that directory as the working directory for every
invocation so the agent picks it up automatically.

See `examples/GEMINI.md.example` for a Google Workspace template (Gmail,
Drive, Calendar, Sheets via the `gws` CLI).

## CLI commands

| Command | Purpose |
|---------|---------|
| `tg-cli-bridge init` | Interactive setup wizard |
| `tg-cli-bridge run` | Run in foreground (debug) |
| `tg-cli-bridge install` | Install + start macOS LaunchAgent |
| `tg-cli-bridge uninstall` | Remove LaunchAgent |
| `tg-cli-bridge start / stop` | Control the service |
| `tg-cli-bridge status` | Show service state |
| `tg-cli-bridge logs` | Tail the log file |

## Why not tmux?

The original version ran the agent in a persistent tmux session and diffed the
pane every 0.6 seconds to detect new output. Two problems:

1. **Telegram rate limits.** Gemini CLI redraws its screen on every token. The
   bridge treated each redraw as new output and fired a Telegram message.
   Telegram throttles to ~1 message/second; the bridge had no backoff and
   dropped responses.

2. **Noisy output.** Tool-call boxes, progress spinners, and ASCII banners all
   landed in the chat. Readable in a terminal, unreadable on a phone.

RPC mode solves both: one Telegram send per turn, and tool-call noise is
filtered before the reply is sent.

## Requirements

- macOS (LaunchAgent install). Linux works with a manual systemd user unit.
- Go 1.22+
- The agent CLI you want to drive (AGY, Gemini CLI, Claude Code, etc.)

## License

MIT
