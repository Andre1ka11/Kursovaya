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
)

const requestTimeout = 10 * time.Second

type CommandService interface {
	Start() string
	Help() string
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
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message"}),
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
	if update == nil || update.Message == nil {
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
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		c.reply(ctx, b, m.Chat.ID, "Отправь /help, чтобы увидеть доступные команды.")
		return
	}
	command, target, addressed := strings.Cut(fields[0], "@")
	if addressed && !strings.EqualFold(target, c.username) {
		return
	}
	var answer string
	switch command {
	case "/start":
		answer = c.commands.Start()
	case "/help":
		answer = c.commands.Help()
	default:
		answer = "Неизвестная команда. Доступны /start и /help."
	}
	c.reply(ctx, b, m.Chat.ID, answer)
}

func (c *Client) reply(ctx context.Context, b *tgbot.Bot, chatID int64, text string) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if _, err := b.SendMessage(requestCtx, &tgbot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		c.handleError(err)
	}
}

func (c *Client) handleError(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	// SDK errors can contain request URLs or incoming text. Log only a safe category.
	category := errorCategory(err)
	c.logger.Warn("telegram request failed", "category", category)
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
