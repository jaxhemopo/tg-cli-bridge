# tg-cli-bridge

Go binary that bridges a Telegram bot to an agentic CLI (AGY/Antigravity,
Claude Code, Codex, a Claude-compatible GLM wrapper, or a custom command).

## Build

```bash
go build -o tg-cli-bridge ./cmd/tg-cli-bridge
go test ./...
go vet ./...
```

## Install & run (macOS)

```bash
tg-cli-bridge init          # interactive config wizard
tg-cli-bridge run           # foreground test
tg-cli-bridge install       # register LaunchAgent + start
```

## Key files

| Path | What it does |
|------|-------------|
| `cmd/tg-cli-bridge/main.go` | CLI entrypoint, all subcommands |
| `internal/bridge/bridge.go` | Core loop: receives Telegram updates, runs agent, sends reply |
| `internal/rpc/rpc.go` | Spawns the CLI process, captures stdout, streams progress |
| `internal/output/output.go` | ANSI stripping, prose/code classification, menu detection, `DiffSince` |
| `internal/config/config.go` | TOML load/validate, `KnownPresets`, `UpdateCLI` |
| `internal/launchd/launchd.go` | macOS LaunchAgent install/uninstall/status |

## Architecture — RPC mode

Each Telegram message spawns the agent CLI once as a subprocess:

```
Telegram message
  → bridge receives it
  → posts ⏳ status bubble
  → rpc.Run: exec CLI with engine-specific prompt and resume arguments
  → streams stdout; edits bubble in-place as keywords are detected
  → CLI exits
  → delete status bubble
  → send prose-only reply to Telegram
```

**No persistent process.** The CLI starts fresh each turn and exits. Session
continuity comes from the CLI's own latest-session arguments.

**Session IDs are not tracked.** The per-chat state records only whether to
append resume arguments. AGY/Claude `--continue` and Codex `resume --last`
select the engine's latest session, not a Telegram-chat-specific session. Do
not run multiple chats or CLI processes against the same working directory.

## Design decisions

- **One spawn per message** — avoids tmux pane-watching and Telegram rate-limit hammering from constant redraws.
- **Prose-only output** — `output.FormatForTelegram` classifies lines as prose vs code/tool-call banners. Only prose reaches Telegram so tool-call boxes don't pollute the chat.
- **Status bubble** — single ⏳ message edits in-place as agent stdout reveals what it's doing (email, drive, shell, etc.). Deleted before the real reply lands so the chat stays clean.
- **`DiffSince`** — some CLIs (AGY `--continue`) reprint the entire conversation history on every invocation. `DiffSince(prev, curr)` extracts only the new content.
- **`KnownPresets`** in `config.go` — maps short names (`agy`, `claude`,
  `codex`, `glm`) to the right flags. Powers both the `init` wizard and
  `/switch` buttons.
- **Positional prompts** — set `prompt_flag` to `--`; the separator ends
  option parsing before the prompt. Codex uses this command shape.

## Telegram bot commands

| Command | Effect |
|---------|--------|
| `/new` | Make the next message omit resume arguments |
| `/cancel` | Cancel the command currently running in this chat |
| `/kill` | Force-stop the command and its child processes |
| `/retry` | Re-run this chat's last message |
| `/files on\|off` | Toggle sending newly created files; off by default |
| `/switch [name]` | Open buttons or switch CLI globally. A LaunchAgent run restarts automatically; foreground mode needs a manual restart. |
| `/model`, `/m` | Select the configured Claude/GLM model tier |
| `/status` | Show current launch command and this chat's bridge state |
| `/yes` | Send "1" to a numbered menu |
| `/help` | List commands |

## Adding a new CLI preset

1. Add an entry to `KnownPresets` in `internal/config/config.go`.
2. Add the same entry to `cliPresets` in `cmd/tg-cli-bridge/main.go` (for the init wizard).
3. Verify the CLI supports a headless prompt, set `prompt_flag` (`--` for a
   positional prompt) and `resume_args`, then test new and resumed turns with
   `tg-cli-bridge run`.
4. Put required process environment variables under `[session.env]`; set
   `turn_timeout_seconds = -1` only when the engine must run without a deadline.

## Context files

Put the instruction file recognized by the selected engine in `working_dir`
(`AGENTS.md` for Codex, `CLAUDE.md` for Claude Code, or the AGY equivalent).
See `examples/WORKSPACE_CONTEXT.md.example` for a Google Workspace template.
