package bridge

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/jnhemopo/tg-cli-bridge/internal/config"
	"github.com/jnhemopo/tg-cli-bridge/internal/output"
	"github.com/jnhemopo/tg-cli-bridge/internal/rpc"
)

// Bridge owns the bot and per-chat session state.
type Bridge struct {
	cfg *config.Config
	bot *tgbotapi.BotAPI

	mu sync.Mutex
	// Per-chat: has the user already started a session? If yes, the next
	// prompt is run with --resume so the agent retains context.
	sessions map[int64]bool
	// Per-chat: accumulated ANSI-stripped stdout from all previous turns.
	// Some CLIs (e.g. AGY with --continue) reprint the entire conversation
	// history on every invocation; we diff against this to extract only the
	// new reply.
	lastOutput map[int64]string

	// Active inline-keyboard menu, if any. Set when the agent's reply
	// ended in a numbered options list.
	activeMenu *activeMenu
}

// activeMenu tracks a numbered menu currently shown as an inline keyboard.
type activeMenu struct {
	chatID      int64
	messageID   int
	fingerprint string
	options     []output.MenuOption
}

// Run boots the bot and processes updates until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return fmt.Errorf("connecting to Telegram: %w", err)
	}
	log.Printf("Authorized as bot @%s", bot.Self.UserName)

	// Register slash-command hints so the / popup works in Telegram.
	names := make([]string, 0, len(config.KnownPresets))
	for k := range config.KnownPresets {
		names = append(names, k)
	}
	sort.Strings(names)
	cmds := []tgbotapi.BotCommand{
		{Command: "new", Description: "Start a fresh session"},
		{Command: "switch", Description: "Switch CLI: /switch " + strings.Join(names, " or /switch ")},
		{Command: "status", Description: "Show bridge state"},
		{Command: "yes", Description: "Pick option 1 from a numbered menu"},
		{Command: "help", Description: "List all commands"},
	}
	if _, err := bot.Request(tgbotapi.NewSetMyCommands(cmds...)); err != nil {
		log.Printf("setMyCommands: %v", err)
	}

	b := &Bridge{
		cfg:        cfg,
		bot:        bot,
		sessions:   make(map[int64]bool),
		lastOutput: make(map[int64]string),
	}
	log.Printf("Bridge online (RPC mode). launch_command=%q", cfg.LaunchCommand)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			bot.StopReceivingUpdates()
			log.Println("Bridge shutting down.")
			return nil
		case update := <-updates:
			// Each update goes in its own goroutine so a long agent call
			// doesn't block other commands or callbacks.
			go b.handleUpdate(ctx, update)
		}
	}
}

// -- update routing --------------------------------------------------------

func (b *Bridge) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in handleUpdate: %v", r)
		}
	}()

	if update.CallbackQuery != nil {
		b.handleCallback(ctx, update.CallbackQuery)
		return
	}
	msg := update.Message
	if msg == nil {
		return
	}
	user := msg.From
	if user == nil {
		return
	}
	log.Printf("Received from user_id=%d username=%s text=%q",
		user.ID, user.UserName, msg.Text)

	if _, ok := b.cfg.AllowedUserIDs[user.ID]; !ok {
		log.Printf("Unauthorized message from user_id=%d", user.ID)
		b.reply(msg.Chat.ID, "⚠️ Unauthorized.")
		return
	}

	if msg.IsCommand() {
		b.dispatchCommand(ctx, msg)
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	b.runTurn(ctx, msg.Chat.ID, text)
}

// -- one user turn = one CLI invocation -----------------------------------

func (b *Bridge) runTurn(ctx context.Context, chat int64, prompt string) {
	_, _ = b.bot.Request(tgbotapi.NewChatAction(chat, tgbotapi.ChatTyping))

	// Post a single status bubble we'll edit in-place while the agent works,
	// then delete entirely before sending the real reply.
	statusSent, err := b.bot.Send(tgbotapi.NewMessage(chat, "⏳ Working…"))
	statusMsgID := 0
	if err == nil {
		statusMsgID = statusSent.MessageID
	}

	// Keep the Telegram "typing" indicator alive every 4 s.
	typingDone := make(chan struct{})
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-typingDone:
				return
			case <-t.C:
				_, _ = b.bot.Request(tgbotapi.NewChatAction(chat, tgbotapi.ChatTyping))
			}
		}
	}()

	// updateStatus edits the status bubble, rate-limited to one edit per 2 s
	// and only when the label actually changes.
	var (
		lastStatus string
		lastEditAt time.Time
		statusMu   sync.Mutex
	)
	updateStatus := func(s string) {
		if statusMsgID == 0 || s == "" {
			return
		}
		statusMu.Lock()
		defer statusMu.Unlock()
		if s == lastStatus || time.Since(lastEditAt) < 2*time.Second {
			return
		}
		lastStatus = s
		lastEditAt = time.Now()
		edit := tgbotapi.NewEditMessageText(chat, statusMsgID, s)
		_, _ = b.bot.Request(edit)
	}

	b.mu.Lock()
	resume := b.sessions[chat]
	b.mu.Unlock()

	res := rpc.Run(ctx, rpc.Options{
		LaunchCommand: b.cfg.LaunchCommand,
		PromptFlag:    b.cfg.PromptFlag,
		ResumeArgs:    b.cfg.ResumeArgs,
		Resume:        resume,
		WorkingDir:    b.cfg.WorkingDir,
		PathEnv:       b.cfg.PathEnv,
		Timeout:       b.cfg.TurnTimeout(),
		Prompt:        prompt,
		OnProgress: func(rawLine string) {
			if s := statusFor(output.StripANSI(rawLine)); s != "" {
				updateStatus(s)
			}
		},
	})

	close(typingDone)

	// Remove the status bubble before the real reply lands.
	if statusMsgID != 0 {
		_, _ = b.bot.Request(tgbotapi.NewDeleteMessage(chat, statusMsgID))
	}

	if res.Err != nil && strings.TrimSpace(res.Stdout) == "" {
		errMsg := fmt.Sprintf("⚠️ agent failed: %v", res.Err)
		if res.Stderr != "" {
			errMsg += "\n" + truncate(res.Stderr, 400)
		}
		b.reply(chat, errMsg)
		return
	}

	b.mu.Lock()
	b.sessions[chat] = true
	b.mu.Unlock()

	// Strip ANSI, then diff against the accumulated history. Some CLIs (AGY
	// with --continue) reprint every previous reply on each invocation; we
	// only want the new part at the end.
	raw := output.StripANSI(res.Stdout)
	b.mu.Lock()
	prev := b.lastOutput[chat]
	b.lastOutput[chat] = raw
	b.mu.Unlock()
	newRaw := strings.TrimLeft(output.DiffSince(prev, raw), "\n")
	if newRaw == "" {
		newRaw = raw // diff failed — fall back to full output
	}

	// Keep only prose blocks (drops tool-call boxes, banners, progress lines).
	body := proseOnly(newRaw)
	if body == "" {
		body = strings.TrimSpace(newRaw)
	}
	if body == "" {
		b.reply(chat, "(no output)")
		return
	}

	if menu := output.DetectMenu(body); menu != nil {
		b.sendMenuReply(chat, body, menu)
		return
	}

	b.sendBody(chat, body)
}

// sendBody chunks body into Telegram-safe pieces and ships each as
// Telegram-flavour HTML so markdown formatting renders.
func (b *Bridge) sendBody(chat int64, body string) {
	html := output.MarkdownToHTML(body)
	for _, chunk := range output.SplitForTelegram(html, b.cfg.MaxMessageChars) {
		m := tgbotapi.NewMessage(chat, chunk)
		m.ParseMode = tgbotapi.ModeHTML
		b.sendWithRetry(m)
	}
}

func (b *Bridge) sendWithRetry(c tgbotapi.Chattable) {
	for i := 0; i < 3; i++ {
		_, err := b.bot.Send(c)
		if err == nil {
			return
		}

		if err, ok := err.(tgbotapi.Error); ok && err.Code == 429 {
			seconds := err.RetryAfter
			if seconds == 0 {
				seconds = 5
			}
			log.Printf("Rate limited (429). Retrying after %d seconds...", seconds)
			time.Sleep(time.Duration(seconds) * time.Second)
			continue
		}

		log.Printf("send failed (%v); retrying plain", err)
		if msg, ok := c.(tgbotapi.MessageConfig); ok {
			msg.ParseMode = ""
			_, err2 := b.bot.Send(msg)
			if err2 == nil {
				return
			}
			log.Printf("plain send also failed: %v", err2)
		}
		break
	}
}

// sendMenuReply ships the prose preceding a detected menu, then posts a
// follow-up message carrying the numbered options as inline-keyboard
// buttons. Tapping a button sends that number to the agent as the user's
// next message.
func (b *Bridge) sendMenuReply(chat int64, body string, menu *output.Menu) {
	prose := stripMenuLines(body, menu)
	if strings.TrimSpace(prose) != "" {
		b.sendBody(chat, prose)
	}

	text := buildMenuText(menu)
	kb := buildMenuKeyboard(menu)
	m := tgbotapi.NewMessage(chat, text)
	m.ReplyMarkup = kb
	sent, err := b.bot.Send(m)
	if err != nil {
		log.Printf("send menu failed: %v", err)
		return
	}
	b.mu.Lock()
	b.activeMenu = &activeMenu{
		chatID:      chat,
		messageID:   sent.MessageID,
		fingerprint: menuFingerprint(menu),
		options:     append([]output.MenuOption(nil), menu.Options...),
	}
	b.mu.Unlock()
}

// stripMenuLines removes the menu lines (and the immediately preceding
// blank line) from body so the menu doesn't appear twice (once as prose,
// once as buttons).
func stripMenuLines(body string, menu *output.Menu) string {
	if menu == nil {
		return body
	}
	lines := strings.Split(body, "\n")
	keep := lines[:0]
	for _, line := range lines {
		if isMenuLine(line, menu) {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimRight(strings.Join(keep, "\n"), "\n")
}

func isMenuLine(line string, menu *output.Menu) bool {
	trimmed := strings.TrimSpace(line)
	for _, opt := range menu.Options {
		// Both "1. Yes" and "> 1. Yes" forms.
		if strings.HasPrefix(trimmed, strconv.Itoa(opt.Number)+".") ||
			strings.HasPrefix(trimmed, "> "+strconv.Itoa(opt.Number)+".") {
			return true
		}
	}
	return false
}

// -- commands --------------------------------------------------------------

func (b *Bridge) dispatchCommand(ctx context.Context, msg *tgbotapi.Message) {
	chat := msg.Chat.ID
	switch msg.Command() {
	case "start", "help":
		names := make([]string, 0, len(config.KnownPresets))
		for k := range config.KnownPresets {
			names = append(names, "/switch "+k)
		}
		sort.Strings(names)
		b.reply(chat,
			"✅ Bridge is live.\n"+
				fmt.Sprintf("Running: %s\n\n", b.cfg.LaunchCommand)+
				"Just send any text — I'll forward it to the agent and reply with what it says.\n\n"+
				"Commands:\n"+
				"/new — start a fresh session\n"+
				"/yes — pick option 1 from a numbered menu\n"+
				"/status — show bridge state\n"+
				strings.Join(names, ", ")+" — switch CLI")
	case "new":
		b.mu.Lock()
		delete(b.sessions, chat)
		delete(b.lastOutput, chat)
		b.activeMenu = nil
		b.mu.Unlock()
		b.reply(chat, "🆕 Session reset. Your next message starts fresh (no --resume).")
	case "status":
		b.mu.Lock()
		active := b.sessions[chat]
		b.mu.Unlock()
		state := "🟢 ready"
		if active {
			state += " (resumed)"
		}
		b.reply(chat, fmt.Sprintf("Launch: %s\nSession: %s",
			b.cfg.LaunchCommand, state))
	case "yes", "y":
		// Shorthand for picking option 1 in a numbered menu.
		b.runTurn(ctx, chat, "1")
	case "switch":
		arg := strings.ToLower(strings.TrimSpace(msg.CommandArguments()))
		if arg == "" {
			names := make([]string, 0, len(config.KnownPresets))
			for k := range config.KnownPresets {
				names = append(names, k)
			}
			sort.Strings(names)
			b.reply(chat, "Usage: /switch "+strings.Join(names, "  or  /switch "))
			return
		}
		preset, ok := config.KnownPresets[arg]
		if !ok {
			names := make([]string, 0, len(config.KnownPresets))
			for k := range config.KnownPresets {
				names = append(names, k)
			}
			sort.Strings(names)
			b.reply(chat, "Unknown CLI \""+arg+"\". Available: "+strings.Join(names, ", "))
			return
		}
		if err := config.UpdateCLI(b.cfg.SourcePath, preset); err != nil {
			b.reply(chat, "❌ Failed to update config: "+err.Error())
			return
		}
		b.reply(chat, fmt.Sprintf("✅ Switched to %s. Restarting…\nSend /new after it comes back.", arg))
		time.Sleep(600 * time.Millisecond) // let the reply flush before exit
		os.Exit(0)                          // LaunchAgent KeepAlive restarts with new config
	default:
		b.reply(chat, "Unknown command. Try /help.")
	}
}

// -- inline-keyboard callbacks --------------------------------------------

func (b *Bridge) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	if cb.From == nil {
		return
	}
	if _, ok := b.cfg.AllowedUserIDs[cb.From.ID]; !ok {
		log.Printf("Unauthorized callback from user_id=%d", cb.From.ID)
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, "Unauthorized"))
		return
	}
	const prefix = "menu:"
	if !strings.HasPrefix(cb.Data, prefix) {
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return
	}
	num, err := strconv.Atoi(strings.TrimPrefix(cb.Data, prefix))
	if err != nil {
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return
	}

	b.mu.Lock()
	active := b.activeMenu
	b.mu.Unlock()
	if active == nil || cb.Message == nil || active.messageID != cb.Message.MessageID {
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, "This menu is no longer active."))
		return
	}

	var label string
	for _, opt := range active.options {
		if opt.Number == num {
			label = opt.Label
			break
		}
	}

	_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Picked %d", num)))
	summary := fmt.Sprintf("✓ Picked %d. %s", num, label)
	edit := tgbotapi.NewEditMessageText(active.chatID, active.messageID, summary)
	_, _ = b.bot.Request(edit)

	b.mu.Lock()
	b.activeMenu = nil
	b.mu.Unlock()

	// Now treat the choice as a normal turn so the agent advances.
	b.runTurn(ctx, active.chatID, strconv.Itoa(num))
}

// -- helpers ---------------------------------------------------------------

// statusFor maps a single (ANSI-stripped) output line to a human-readable
// status label, or "" if the line doesn't indicate anything interesting.
func statusFor(line string) string {
	low := strings.ToLower(line)
	switch {
	case containsAny(low, "gmail", "email", "mail", "inbox", "thread", "message"):
		return "📧 Checking email…"
	case containsAny(low, "gdrive", "google drive", "drive", "gdoc", "gsheet", "spreadsheet", "document"):
		return "📁 Browsing Drive…"
	case containsAny(low, "calendar", "event", "schedule"):
		return "📅 Checking calendar…"
	case containsAny(low, "readfile", "read_file", "reading"):
		return "📖 Reading files…"
	case containsAny(low, "writefile", "write_file", "editfile", "edit_file", "creating file", "writing"):
		return "✏️ Writing files…"
	case containsAny(low, "shell(", "bash(", "runcmd", "execute", "running"):
		return "⚙️ Running commands…"
	case containsAny(low, "web_search", "googlesearch", "searching", "research"):
		return "🔍 Searching…"
	case containsAny(low, "fetch", "http", "download", "request"):
		return "🌐 Fetching…"
	default:
		return ""
	}
}

// proseOnly keeps only the natural-language prose blocks from captured CLI
// output, stripping tool-call boxes, banners, and progress indicators.
func proseOnly(s string) string {
	var parts []string
	for _, blk := range output.FormatForTelegram(s) {
		if !blk.IsCode {
			if t := strings.TrimSpace(blk.Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func (b *Bridge) reply(chat int64, text string) {
	b.sendWithRetry(tgbotapi.NewMessage(chat, text))
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func menuFingerprint(m *output.Menu) string {
	var sb strings.Builder
	for _, opt := range m.Options {
		fmt.Fprintf(&sb, "%d:%s\n", opt.Number, opt.Label)
	}
	return sb.String()
}

func buildMenuText(m *output.Menu) string {
	var sb strings.Builder
	if m.Question != "" {
		sb.WriteString(m.Question)
		sb.WriteString("\n\n")
	}
	for _, opt := range m.Options {
		fmt.Fprintf(&sb, "%d. %s\n", opt.Number, opt.Label)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func buildMenuKeyboard(m *output.Menu) tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(m.Options))
	for _, opt := range m.Options {
		label := opt.Label
		const maxBtnLabel = 40
		if len([]rune(label)) > maxBtnLabel {
			runes := []rune(label)
			label = string(runes[:maxBtnLabel-1]) + "…"
		}
		btnText := fmt.Sprintf("%d. %s", opt.Number, label)
		data := fmt.Sprintf("menu:%d", opt.Number)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(btnText, data),
		))
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}
