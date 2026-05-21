# tg-cli-bridge

Bridge any agentic CLI — Gemini CLI, AGY, Claude Code, plain shell — to a
Telegram chat. Send a message from your phone, get a clean reply back. Drive
long-running tasks, check your email, manage files — all from Telegram.

A single static Go binary. No Python, no venv, no Docker.

```
Phone (Telegram) ──HTTPS──► Telegram Bot API ──long-poll──► tg-cli-bridge
                                                                   │
                                                        spawn once │
                                                                   ▼
                                          gemini --yolo --resume latest --prompt "<text>"
                                                                   │
                                                       stdout      │  exit
                                                                   ▼
                                                 formatted reply → Telegram
```

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
gemini --yolo --resume latest --prompt "check my email"
```

The bridge waits for the process to exit, strips tool-call noise from the
output, and sends back only the agent's natural-language reply. Session
continuity is preserved via `--resume` (Gemini) or `--continue` (AGY) so
the agent remembers the conversation.

While the agent works, the bridge posts a single status bubble that edits
itself in-place — `📧 Checking email…`, `📁 Browsing Drive…` — and deletes
it the moment the real reply is ready.

## Telegram commands

Send plain text → forwarded to the agent as a prompt.

| Command   | What it does |
|-----------|-------------|
| `/new`    | Start a fresh session (forget conversation history) |
| `/status` | Show bridge and service state |
| `/yes`    | Shorthand for sending "1" to a numbered menu |
| `/help`   | List all commands |

## Supported CLIs

The `init` wizard knows the right flags for common CLIs. You can also set
them manually in `config.toml`.

| CLI | `launch_command` | `prompt_flag` | `resume_args` |
|-----|-----------------|---------------|---------------|
| Gemini CLI | `gemini --yolo` | `--prompt` *(default)* | `--resume latest` *(default)* |
| AGY / Antigravity | `agy --dangerously-skip-permissions` | `--print` | `--continue` |
| Claude Code | `claude` | `--print` | *(none)* |
| Plain shell | `bash` | *(set manually)* | *(none)* |

### Switching CLIs

If you've set up multiple presets, see `docs/switching.md`. The quick version:

```bash
# copy the right preset and restart
cp ~/.config/tg-cli-bridge/presets/gemini.toml ~/.config/tg-cli-bridge/config.toml
tg-cli-bridge start
```

## Configuration

Config lives at `~/.config/tg-cli-bridge/config.toml` (created by `init`).
See `examples/config.toml.example` for a fully annotated template.

```toml
[telegram]
bot_token      = "YOUR_BOT_TOKEN"   # from @BotFather
allowed_user_ids = [123456789]      # your Telegram user ID (@userinfobot)

[session]
launch_command = "gemini --yolo"
working_dir    = "/Users/you/workspace"

[bridge]
max_message_chars = 3800            # Telegram's limit is 4096
# prompt_flag = "--prompt"          # flag the CLI uses for headless prompts
# resume_args = ["--resume", "latest"]  # args to continue a previous session
# turn_timeout_seconds = 600        # kill the CLI after this long (default 10m)
```

**The config contains your bot token — treat it like a password. Never commit it.**

## Mac commands

| Command | Purpose |
|---------|---------|
| `tg-cli-bridge init` | Interactive setup wizard |
| `tg-cli-bridge run` | Run in foreground (debug) |
| `tg-cli-bridge install` | Install + start macOS LaunchAgent |
| `tg-cli-bridge uninstall` | Remove LaunchAgent |
| `tg-cli-bridge start / stop` | Control the service |
| `tg-cli-bridge status` | Show service state |
| `tg-cli-bridge logs` | Tail the log file |

## Google Workspace

If your CLI has access to Google Workspace tools (Gmail, Drive, Calendar),
add a `GEMINI.md` (or equivalent context file) to your `working_dir` that
describes how to use them. The bridge sets that directory as the working
directory for every invocation so the agent picks it up automatically.

See `examples/GEMINI.md.example` for a template using the `gws` CLI.

## Why not tmux?

The original version of this bridge ran the agent in a persistent tmux session
and diffed the pane every 0.6 seconds to detect new output. It worked, but had
two problems in practice:

1. **Telegram rate limits.** Gemini CLI redraws its screen on every token. The
   bridge treated each redraw as new output and fired a Telegram message.
   Telegram throttles to ~1 message/second per chat; the bridge had no backoff,
   so it hammered the API and dropped responses.

2. **Noisy output.** Tool-call boxes, progress spinners, and ASCII banners all
   landed in the chat. Readable in a terminal, unreadable on a phone.

RPC mode solves both: one Telegram send per turn, and tool-call noise is
filtered before the reply is sent.

## Requirements

- macOS (LaunchAgent install). Linux works fine with manual systemd setup.
- Go 1.22+
- The agent CLI you want to drive

## License

MIT
