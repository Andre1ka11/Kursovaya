// Package telegram adapts Telegram updates to application services.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"study-agent/internal/config"
	"study-agent/internal/service"
)

const requestTimeout = 10 * time.Second

type CommandService interface {
	Handle(context.Context, service.Request) (service.Response, error)
}

type Client struct {
	bot      *tgbot.Bot
	commands CommandService
	logger   *slog.Logger
	allowed  map[int64]bool
	username string
	fatal    chan error
}

func New(cfg config.Telegram, commands CommandService, logger *slog.Logger) (*Client, error) {
	return newClient(cfg, commands, logger)
}

// SDK options are internal: tests can replace the HTTP endpoint with httptest.Server.
func newClient(cfg config.Telegram, commands CommandService, logger *slog.Logger, extra ...tgbot.Option) (*Client, error) {
	if !cfg.Enabled {
		return nil, errors.New("Telegram client requires telegram.enabled=true")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Client{commands: commands, logger: logger, allowed: make(map[int64]bool), fatal: make(chan error, 1)}
	for _, id := range cfg.AllowedUserIDs {
		c.allowed[id] = true
	}
	options := []tgbot.Option{
		tgbot.WithSkipGetMe(), // Initialization belongs to Run and its cancellable context.
		tgbot.WithHTTPClient(30*time.Second, &http.Client{Timeout: 40 * time.Second}),
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithDefaultHandler(c.handle),
		tgbot.WithErrorsHandler(c.handleError),
		tgbot.WithNotAsyncHandlers(), // Start waits for the active handler during shutdown.
	}
	options = append(options, extra...)
	b, err := tgbot.New(cfg.Token, options...)
	if err != nil {
		return nil, errors.New("cannot initialize Telegram client; check AGENT_BOT_TOKEN")
	}
	c.bot = b
	return c, nil
}

func (c *Client) Run(ctx context.Context) error {
	initCtx, finishInit := context.WithTimeout(ctx, requestTimeout)
	defer finishInit()
	me, err := c.bot.GetMe(initCtx)
	if err != nil {
		return startupError(ctx, "getMe", err)
	}
	if !me.IsBot || me.ID <= 0 {
		return errors.New("Telegram getMe returned an invalid bot identity")
	}
	c.username = me.Username
	webhook, err := c.bot.GetWebhookInfo(initCtx)
	if err != nil {
		return startupError(ctx, "getWebhookInfo", err)
	}
	if webhook.URL != "" {
		return errors.New("Telegram bot has an active webhook; remove it before using long polling")
	}
	_, err = c.bot.SetMyCommands(initCtx, &tgbot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "start", Description: "Знакомство с помощником"},
		{Command: "help", Description: "Доступные команды"},
		{Command: "add", Description: "Добавить задачу"},
		{Command: "cancel", Description: "Отменить добавление"},
		{Command: "tasks", Description: "Список задач"},
		{Command: "today", Description: "Задачи на сегодня"},
		{Command: "week", Description: "Задачи на ближайшие 7 дней"},
		{Command: "done", Description: "Завершить задачу по ID"},
		{Command: "delete", Description: "Удалить задачу по ID"},
	}})
	if err != nil {
		return startupError(ctx, "setMyCommands", err)
	}
	finishInit()

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.bot.Start(runCtx)
	}()
	c.logger.Info("telegram bot started", "username", c.username, "mode", "long_polling")
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-c.fatal:
	case <-done:
		if ctx.Err() == nil {
			runErr = errors.New("Telegram polling stopped unexpectedly")
		}
	}
	stop()
	<-done
	c.logger.Info("telegram bot stopped")
	return runErr
}

func (c *Client) handle(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	if update == nil {
		return
	}
	if update.CallbackQuery != nil {
		c.handleCallback(ctx, b, update.CallbackQuery)
		return
	}
	if update.Message == nil {
		return
	}
	m := update.Message
	if m.From == nil || m.From.IsBot || m.Chat.Type != models.ChatTypePrivate {
		return
	}
	if !c.allowed[m.From.ID] {
		c.logger.Warn("telegram access denied", "user_id", m.From.ID)
		c.reply(ctx, b, m.Chat.ID, "Доступ к этому боту ограничен владельцем.")
		return
	}
	text := strings.TrimSpace(m.Text)
	fields := strings.Fields(text)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		command, target, addressed := strings.Cut(fields[0], "@")
		if addressed && !strings.EqualFold(target, c.username) {
			return
		}
		if addressed {
			text = command + strings.TrimPrefix(text, fields[0])
		}
	}
	c.dispatch(ctx, b, m.Chat.ID, 0, service.Request{UserID: m.From.ID, Text: text})
}

func (c *Client) handleCallback(ctx context.Context, b *tgbot.Bot, q *models.CallbackQuery) {
	if q.From.IsBot || !c.allowed[q.From.ID] {
		c.answerCallback(ctx, b, q.ID, "Доступ к этому боту ограничен владельцем.")
		return
	}
	m := q.Message.Message
	if m == nil || m.Chat.Type != models.ChatTypePrivate || m.Chat.ID != q.From.ID {
		c.answerCallback(ctx, b, q.ID, "Открой свежий список в личном чате: /tasks")
		return
	}
	if !c.answerCallback(ctx, b, q.ID, "") {
		return
	}
	c.dispatch(ctx, b, m.Chat.ID, m.ID, service.Request{UserID: q.From.ID, CallbackData: q.Data})
}

func (c *Client) answerCallback(ctx context.Context, b *tgbot.Bot, id, text string) bool {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_, err := b.AnswerCallbackQuery(requestCtx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, ShowAlert: text != ""})
	if err != nil {
		c.handleAPIError("answerCallbackQuery", err)
		return false
	}
	return true
}

func (c *Client) dispatch(ctx context.Context, b *tgbot.Bot, chatID int64, messageID int, r service.Request) {
	started := time.Now()
	logger := c.logger.With("user_id", r.UserID, "action", logAction(r))
	logger.Info("telegram message received")
	result := "ok"
	out, err := c.commands.Handle(ctx, r)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		logger.Warn("task command failed")
		result = "service_error"
		out = service.Response{Text: "Не удалось выполнить действие. Попробуй ещё раз. Незавершённое добавление можно отменить: /cancel"}
	}
	if c.respond(ctx, b, chatID, messageID, out) {
		logger.Info("telegram reply sent", "result", result, "duration_ms", time.Since(started).Milliseconds())
	} else if ctx.Err() == nil {
		logger.Warn("telegram reply failed", "duration_ms", time.Since(started).Milliseconds())
	}
}

// Action labels are fixed: command arguments, callback payloads and task text stay out of logs.
func logAction(r service.Request) string {
	if r.CallbackData != "" {
		action, _, _ := strings.Cut(r.CallbackData, ":")
		switch action {
		case "list", "task", "done", "delete", "confirm":
			return "button_" + action
		}
		return "button_unknown"
	}
	fields := strings.Fields(r.Text)
	if len(fields) == 0 {
		return "attachment_or_empty"
	}
	switch fields[0] {
	case "/start", "/help", "/add", "/cancel", "/tasks", "/today", "/week", "/done", "/delete":
		return fields[0]
	}
	if strings.HasPrefix(fields[0], "/") {
		return "unknown_command"
	}
	return "text"
}

func (c *Client) respond(ctx context.Context, b *tgbot.Bot, chatID int64, messageID int, out service.Response) bool {
	keyboard := models.InlineKeyboardMarkup{InlineKeyboard: make([][]models.InlineKeyboardButton, 0, len(out.Buttons))}
	for _, row := range out.Buttons {
		buttons := make([]models.InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			buttons = append(buttons, models.InlineKeyboardButton{Text: button.Text, CallbackData: button.Data})
		}
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, buttons)
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if messageID != 0 {
		_, err := b.EditMessageText(requestCtx, &tgbot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: out.Text, ReplyMarkup: keyboard})
		if err == nil || (errors.Is(err, tgbot.ErrorBadRequest) && strings.Contains(err.Error(), "message is not modified")) {
			return true
		}
		c.handleAPIError("editMessageText", err)
		if !errors.Is(err, tgbot.ErrorBadRequest) {
			return false
		}
	}
	if _, err := b.SendMessage(requestCtx, &tgbot.SendMessageParams{ChatID: chatID, Text: out.Text, ReplyMarkup: keyboard}); err != nil {
		c.handleAPIError("sendMessage", err)
		return false
	}
	return true
}

func (c *Client) reply(ctx context.Context, b *tgbot.Bot, chatID int64, text string) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if _, err := b.SendMessage(requestCtx, &tgbot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		c.handleAPIError("sendMessage", err)
	}
}

func (c *Client) handleError(err error) {
	c.handleAPIError("getUpdates", err)
}

func (c *Client) handleAPIError(operation string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	// SDK errors can contain request URLs or incoming text. Log only a safe category.
	category := errorCategory(err)
	c.logger.Warn("telegram request failed", "operation", operation, "category", category)
	if errors.Is(err, tgbot.ErrorUnauthorized) || errors.Is(err, tgbot.ErrorConflict) {
		select {
		case c.fatal <- fmt.Errorf("Telegram polling failed: %s", category):
		default:
		}
	}
}

func startupError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("Telegram %s failed: %s (check token, network and bot settings)", operation, errorCategory(err))
}

func errorCategory(err error) string {
	var rateLimit *tgbot.TooManyRequestsError
	switch {
	case errors.Is(err, tgbot.ErrorUnauthorized):
		return "unauthorized"
	case errors.Is(err, tgbot.ErrorConflict):
		return "conflict"
	case errors.Is(err, tgbot.ErrorForbidden):
		return "forbidden"
	case errors.Is(err, tgbot.ErrorBadRequest):
		return "bad_request"
	case errors.As(err, &rateLimit):
		return "rate_limit"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return "network"
	}
	return "request_error"
}
