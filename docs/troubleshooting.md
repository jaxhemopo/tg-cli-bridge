# Troubleshooting

## `409 Conflict: terminated by other getUpdates request`

**Cause:** Two processes are polling the same bot token. Telegram allows exactly one.

**Fix:**
```bash
pgrep -afl "tg-cli-bridge"
pkill -f "tg-cli-bridge"   # kill all, then:
tg-cli-bridge start
```

## Bridge receives messages but the agent never replies

**Diagnose:**
```bash
tg-cli-bridge logs
# Look for lines after "Received from user_id=…"
# No follow-up lines = the agent process hung. See below.
```

**Cause A — agent process hangs:** The CLI ran but never exited. Check with
`ps aux | grep <your-cli>`. Kill the stuck process, send `/new` from Telegram
to reset session state, then retry.

**Cause B — CLI not on PATH:** Add the CLI's directory to `path` in `config.toml`:
```toml
[session]
path = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/Users/you/.local/bin"
```

**Cause C — wrong `prompt_flag`:** Test manually first:
```bash
your-cli --prompt "hello"   # or --print, -p, etc.
```
Then set `prompt_flag` in `[bridge]` to match.

## Agent replies but output is the whole conversation history

**Cause:** Your CLI's resume flag reprints full history on every invocation
(AGY does this with `--continue`). The bridge handles this automatically by
diffing each turn's output against the previous one.

If you're still seeing the full history, the bridge restarted and lost its
diff baseline. Send `/new` from Telegram to reset, then continue.

## `⚠️ agent failed` message on Telegram

The CLI exited non-zero with no output. Common causes:

- **Usage limit hit:** The CLI's daily quota is exhausted. Wait for it to reset.
- **Auth expired:** Re-run auth for your CLI in a regular terminal.
- **Stale session:** The resume session no longer exists. Send `/new` to start fresh.

## No reply, no error — just silence after a long wait

The agent exceeded `turn_timeout_seconds` (default 10 min) and was killed.
Look for `context deadline exceeded` in the logs. For tasks that genuinely
take longer, raise the timeout in `config.toml`:
```toml
[bridge]
turn_timeout_seconds = 1200   # 20 minutes
```

## Sending messages from phone but nothing in the logs

90% of the time: wrong bot, or wrong user ID.

```bash
tg-cli-bridge logs
# Send /start from your phone.
# "Received from user_id=12345" appears but you get "Unauthorized":
#   → add 12345 to allowed_user_ids in config.toml.
# Nothing appears at all:
#   → you're messaging the wrong bot. Double-check the token.
```

## `Bootstrap failed: 5: Input/output error` from launchctl

Previous LaunchAgent hasn't fully torn down.

```bash
launchctl bootout gui/$(id -u)/com.tg-cli-bridge.agent 2>/dev/null
sleep 2
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.tg-cli-bridge.agent.plist
```

## Mac sleeps and bridge goes offline

Expected. Options:
- System Settings → Energy → prevent sleep (recommended for Mac mini / desktop).
- `caffeinate -dimsu &` before stepping away.

## First-run auth or device confirmation in the CLI

Do interactive auth once in a regular terminal **before** starting the bridge.
The auth token persists to disk and will be reused by the bridge on every spawn.

## `go: cannot find main module` when building

```bash
cd /path/to/tg-cli-bridge
go build -o tg-cli-bridge ./cmd/tg-cli-bridge
```
