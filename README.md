# tg-cli-bridge

Control AGY, Claude Code, Codex, a Claude-compatible GLM wrapper, or another
headless agent CLI from your phone via Telegram. Send a message and get a clean
reply back without opening your laptop.

A single static Go binary. No Python, no venv, no Docker.

```
Phone (Telegram) ──HTTPS──► Telegram Bot API ──long-poll──► tg-cli-bridge
                                                                   │
                                                        spawn once │
                                                                   ▼
                                   agy --dangerously-skip-permissions [--continue] --print "<text>"
                                                                   │
                                                       stdout      │  exit
                                                                   ▼
                                                 formatted reply → Telegram
```

## ⚠️ Security — read this first

The AGY, Claude, and GLM presets run with **all tool approvals disabled** via
`--dangerously-skip-permissions`. The Codex preset uses its `workspace-write`
sandbox. These agents can run commands and change files without a confirmation
round-trip through Telegram.

**What protects you:**

- `allowed_user_ids` in `config.toml` — only these Telegram IDs can send
  prompts to your bot. Keep this to just your own ID.
- Use the bot only in a private Telegram chat. The allowlist checks the sender,
  but replies sent from an allowed user in a group are visible to that group.
- Your bot token — if someone has it, they can control the bot, receive its
  updates, and send messages as the bot. Treat it like a password. Never
  commit it, never share it.

**Be deliberate about what you ask remotely.** AGY and Claude act with your
local user's full permissions; Codex acts within its configured sandbox. Don't
leave the bridge running on a machine you wouldn't otherwise leave unlocked.

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
./tg-cli-bridge install
./tg-cli-bridge status
```

## How it works

Each Telegram message spawns the agent CLI once in headless mode. For example,
an AGY turn after the first one runs:

```
agy --dangerously-skip-permissions --continue --print "check my email"
```

The bridge waits for the process to exit, strips tool-call noise from the
output, and sends back only the agent's natural-language reply. Session
continuity uses each CLI's own latest-session argument: `--continue` for AGY
and Claude, or `exec resume --last` for Codex.

While the agent works, the bridge posts a single status bubble that edits
itself in-place when the CLI reports recognizable progress on stdout —
`📧 Checking email…`, `📁 Browsing Drive…` — and deletes it when the reply is
ready. Codex sends progress to stderr, so Codex turns keep the generic
`⏳ Working…` bubble until the final answer arrives.

### Session isolation — important

The bridge remembers whether a Telegram chat has started, but it does not yet
store the CLI's conversation ID. AGY `--continue`, Claude `--continue`, and
Codex `exec resume --last` select that engine's most recent session — not a
session uniquely tied to a Telegram chat.

Until per-engine session-ID tracking is added:

- Use one CLI engine and one Telegram chat per running bridge.
- Dedicate each bot/config to one engine when you maintain more than one bot.
- Do not run that same CLI manually in the configured `working_dir` while the
  bridge is active.
- Do not run different engines against the same working directory at the same
  time, even though their conversation stores are separate.
- Wait for the current turn to finish before `/switch`, then send `/new`.

The supported macOS setup is one background bridge at a time because the
installer manages one LaunchAgent identity. If you maintain a Claude bot
(`tg1`) and an AGY bot (`tg2`), stop one before starting the other. Concurrent
bots require separate tokens, configs, working directories, and manually
managed service identities; the bundled installer does not set those up.

## Supported CLIs

The `init` wizard knows the right flags for each CLI out of the box.

| CLI | `launch_command` | Prompt | Resume |
|-----|------------------|--------|--------|
| **AGY / Antigravity** | `agy --dangerously-skip-permissions` | `--print` | `--continue` |
| Claude Code | `claude --dangerously-skip-permissions` | `--print` | `--continue` |
| Codex CLI | `codex exec --sandbox workspace-write` | positional (`--`) | `resume --last` |
| Claude + GLM wrapper | `claude-glm --dangerously-skip-permissions` | `--print` | `--continue` |
| Other / custom | Your headless command | CLI-specific | CLI-specific |

Codex normally requires `working_dir` to be a Git repository. Add
`--skip-git-repo-check` to its `launch_command` only when you deliberately want
to run elsewhere. Its `workspace-write` sandbox blocks outbound network access
by default; if Codex must call networked tools, deliberately add
`-c sandbox_workspace_write.network_access=true` to `launch_command`. See the
official [Codex non-interactive mode documentation](https://developers.openai.com/codex/noninteractive).

## Switching CLIs from Telegram

You can switch live without touching the terminal:

```
/switch agy
/switch claude
/switch codex
/switch glm
```

Sending `/switch` without a name opens the same choices as Telegram buttons.

The bridge updates `config.toml`. When managed by the bundled LaunchAgent it
restarts automatically and comes back on the new CLI within a few seconds; in
foreground mode you restart it manually. Switching is global, not per
Telegram chat. Finish the current turn first and send `/new` after switching.

## Telegram commands

Send any plain text and it's forwarded to the agent as a prompt.

| Command | What it does |
|---------|-------------|
| `/new` | Make the next message start without resume arguments |
| `/cancel` | Cancel the command currently running in this chat |
| `/kill` | Force-stop a stuck command and its child processes |
| `/retry` | Re-run the last message from this chat |
| `/files on\|off` | Toggle automatic sending of newly created files; off by default |
| `/switch [name]` | Open the switch menu or select `agy`, `claude`, `codex`, or `glm` |
| `/model` or `/m` | Choose the configured Claude/GLM model tier |
| `/status` | Show the current CLI and this chat's bridge state |
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

# Optional values passed directly to the child process.
[session.env]
# EXAMPLE_API_KEY = "replace-me"

[bridge]
max_message_chars   = 3800
prompt_flag         = "--print"
resume_args         = ["--continue"]
# turn_timeout_seconds = 600          # kill the CLI after this long (default 10m)
# turn_timeout_seconds = -1           # disable the deadline deliberately
```

**The config contains your bot token — treat it like a password. Never commit it.**

### Custom CLIs and model arguments

For each Telegram turn, the bridge constructs the command in this order:

```text
<launch_command> [resume_args after the first turn] <prompt_flag> <Telegram prompt>
```

Put fixed engine, model, profile, and permission arguments in
`launch_command`. For example:

```toml
# AGY
launch_command = "agy --dangerously-skip-permissions --model YOUR_MODEL --effort high"

# Claude Code
launch_command = "claude --dangerously-skip-permissions --model YOUR_MODEL"

# Codex
launch_command = "codex exec --sandbox workspace-write --model YOUR_MODEL"

# Codex with outbound access for networked tools
launch_command = "codex exec --sandbox workspace-write -c sandbox_workspace_write.network_access=true --model YOUR_MODEL"
```

Set `prompt_flag` to the CLI's one-shot/headless flag (`--print`, `--prompt`,
or similar). If the prompt is positional, set `prompt_flag = "--"` so option
parsing ends safely before the Telegram text. Set `resume_args` to exactly the
arguments that continue the engine's latest session. Check the installed
CLI's `--help`, test one new turn and one resumed turn directly in a terminal,
then send `/new` after changing any of these values.

For a CLI with no continuation support, set `resume_args = []`; every message
will start a separate CLI session.

`launch_command` is split into arguments; it is not run through a shell. Avoid
pipes, redirects, environment assignments, and quoted arguments containing
spaces. Put required environment variables under `[session.env]` instead.

## Context files

Put the context file recognized by your engine in `working_dir` — for example,
`AGENTS.md` for Codex or `CLAUDE.md` for Claude Code — to describe available
tools and workspace conventions. The bridge uses that directory for every
invocation.

See `examples/WORKSPACE_CONTEXT.md.example` for a context template covering
Gmail, Drive, Calendar, and Sheets through the `gws` CLI. Copy its contents to
the instruction file your engine recognizes.

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

1. **Telegram rate limits.** Interactive CLIs redraw their screens frequently.
   The bridge treated each redraw as new output and fired a Telegram message.
   Telegram throttles to ~1 message/second; the bridge had no backoff and
   dropped responses.

2. **Noisy output.** Tool-call boxes, progress spinners, and ASCII banners all
   landed in the chat. Readable in a terminal, unreadable on a phone.

RPC mode solves both: one Telegram send per turn, and tool-call noise is
filtered before the reply is sent.

## Requirements

- macOS (LaunchAgent install). Linux works with a manual systemd user unit.
- Go 1.22+
- The agent CLI you want to drive (AGY, Claude Code, Codex, or another
  headless CLI)

## License

MIT
