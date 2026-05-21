# Architecture

## Current design: RPC mode

Each Telegram message spawns the agent CLI once in headless mode, waits for it
to exit, and ships the captured stdout as a Telegram reply. No tmux, no screen
diffing, no pane watching.

```
Phone (Telegram) ──HTTPS──► Telegram Bot API ──long-poll──► tg-cli-bridge
                                                              │
                                                   spawn once │  wait for exit
                                                              ▼
                                             gemini --yolo --resume latest --prompt "<text>"
                                                              │
                                                              ▼
                                                   stdout captured → sent to Telegram
```

### Why this replaced tmux

The original tmux-based design (v1) captured the pane every 0.6s and diffed
it. It broke in two ways:

1. **Rate-limit hammering.** Gemini CLI redraws its screen on every token, so
   the bridge fired dozens of Telegram sends per turn. Telegram's 429 flood
   limit (1 msg/s per chat) was hit immediately. The bridge had no backoff, so
   it just logged `Too Many Requests: retry after N` counting down from 60+ and
   dropped the output.

2. **Messy output.** Tool-call boxes, progress spinners, and banner art all
   landed in the chat as raw text, which was unreadable on a phone.

RPC mode sidesteps both: one Telegram send per turn, and we filter the output
through `output.FormatForTelegram` to keep only prose blocks.

### Session continuity

The bridge tracks per-chat session state in memory:

- First message → `gemini --yolo --prompt "<text>"` (new session, Gemini saves it)
- Subsequent messages → `gemini --yolo --resume latest --prompt "<text>"`

`--resume latest` picks up the most recently saved Gemini session for the
working directory. This preserves conversation context across turns.

**Gotcha:** if you start the bridge before writing a GEMINI.md, Gemini creates
a session without that context. `--resume latest` on the next turn inherits that
contextless session. Fix: send `/new` from Telegram to clear the session state
so the next turn starts fresh.

### Status messages

While the agent is running the bridge:

1. Posts a single `⏳ Working…` bubble
2. Streams stdout line-by-line via `rpc.OnProgress` callback
3. Edits that bubble in-place when it detects a status keyword (rate-limited to
   one edit per 2s): `📧 Checking email…`, `📁 Browsing Drive…`, etc.
4. **Deletes** the bubble before sending the real reply

The pane watcher goroutine also sends `tgbotapi.ChatTyping` every 4s so the
"typing…" indicator stays alive in Telegram throughout.

### Output filtering

Raw Gemini stdout includes tool-call boxes and progress lines. The bridge:

1. Strips ANSI with `output.StripANSI`
2. Classifies lines via `output.FormatForTelegram` (prose vs code blocks)
3. Keeps only prose blocks (`proseOnly` in bridge.go)
4. Falls back to the full output if everything was filtered (safety net)

`output.classifyLine` uses lead-character detection (`✦`, `●`, `✓`, box-drawing
chars, `CapitalCase(` tool calls) to identify non-prose lines.

### Menu handling

When the agent's reply ends with a numbered options list, `output.DetectMenu`
parses it and the bridge renders the options as Telegram inline-keyboard
buttons. Tapping a button sends the number as the next `--prompt`, resuming
the session so the agent advances.

### PATH determinism

The bridge sets an explicit `PATH` env var on the spawned process (from config
`[session].path`, defaulting to a sensible set of Homebrew/local bins). This
ensures the agent's binary and its tools (`gws`, etc.) are findable regardless
of what login shell rc files do.

## Package layout

```
cmd/tg-cli-bridge/main.go        — CLI entry point (subcommand dispatch)
internal/config/                 — TOML loading + defaults
internal/rpc/                    — spawn agent once, capture stdout with line callback
internal/output/                 — ANSI strip, Telegram chunking, prose/code classifier,
                                   markdown→HTML, menu detection
internal/bridge/                 — Telegram bot loop, runTurn, status bubble, menu buttons
internal/launchd/                — plist render + bootstrap/bootout helpers
internal/tmux/                   — kept for `tg-cli-bridge attach/status` subcommands only
```

## What runs where

| Component      | Lifetime            | Restarted by                          |
|----------------|---------------------|---------------------------------------|
| LaunchAgent    | Across reboots      | macOS at login                        |
| Bridge process | While agent loaded  | LaunchAgent (KeepAlive + 10s throttle)|
| Agent CLI      | Per Telegram turn   | Spawned fresh by bridge each message  |

There is no persistent tmux session in normal operation. `tg-cli-bridge attach`
and `tg-cli-bridge status` still reference `tmux_session` from config but that
is vestigial.
