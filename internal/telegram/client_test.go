package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"study-agent/internal/config"
	"study-agent/internal/service"
)

const fakeToken = "123:fake-token"

type testCommands struct{ start, help atomic.Int32 }

func (c *testCommands) Start() string { c.start.Add(1); return "start-response" }
func (c *testCommands) Help() string  { c.help.Add(1); return "help-response" }

func (c *testCommands) Handle(_ context.Context, r service.Request) (service.Response, error) {
	switch r.Text {
	case "/start":
		return service.Response{Text: c.Start()}, nil
	case "/help":
		return service.Response{Text: c.Help()}, nil
	case "":
		if r.CallbackData != "" {
			c.help.Add(1)
			return service.Response{Text: "callback-response", Buttons: [][]service.Button{{{Text: "К списку", Data: "list:tasks:todo:0"}}}}, nil
		}
		return service.Response{Text: "Отправь /help"}, nil
	default:
		return service.Response{Text: "Неизвестная команда"}, nil
	}
}

type sentMessage struct {
	chatID   int64
	text     string
	method   string
	keyboard models.InlineKeyboardMarkup
}

type fakeAPI struct {
	updates          []*models.Update
	failMethod       string
	failCode         int
	webhook          string
	blockSend        bool
	sent             chan sentMessage
	sending          chan struct{}
	sendOnce         sync.Once
	polls            atomic.Int32
	menus            atomic.Int32
	answers          atomic.Int32
	callbacksAllowed atomic.Bool
}

func (a *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Consume the request before waiting: HTTP/1 must finish reading its body
	// before it can detect a disconnected client and cancel the server context.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
	} else {
		_, _ = io.Copy(io.Discard, r.Body)
	}
	method := path.Base(r.URL.Path)
	if method == a.failMethod {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": a.failCode, "description": fakeToken + " private error"})
		return
	}
	var result any
	switch method {
	case "getMe":
		result = models.User{ID: 123, IsBot: true, Username: "study_agent_bot"}
	case "getWebhookInfo":
		result = models.WebhookInfo{URL: a.webhook}
	case "setMyCommands":
		a.menus.Add(1)
		result = true
	case "getUpdates":
		if strings.Contains(r.FormValue("allowed_updates"), "callback_query") {
			a.callbacksAllowed.Store(true)
		}
		if a.polls.Add(1) == 1 {
			result = a.updates
			if result == nil || len(a.updates) == 0 {
				result = []*models.Update{}
			}
		} else {
			<-r.Context().Done()
			return
		}
	case "answerCallbackQuery":
		a.answers.Add(1)
		result = true
	case "sendMessage", "editMessageText":
		if a.blockSend {
			a.sendOnce.Do(func() { close(a.sending) })
			<-r.Context().Done()
			return
		}
		id, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
		var keyboard models.InlineKeyboardMarkup
		_ = json.Unmarshal([]byte(r.FormValue("reply_markup")), &keyboard)
		a.sent <- sentMessage{chatID: id, text: r.FormValue("text"), method: method, keyboard: keyboard}
		result = models.Message{ID: 1}
	default:
		http.Error(w, "unexpected method", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func update(id int64, userID int64, text string) *models.Update {
	return &models.Update{ID: id, Message: &models.Message{
		ID: int(id), From: &models.User{ID: userID},
		Chat: models.Chat{ID: userID, Type: models.ChatTypePrivate}, Text: text,
	}}
}

func testClient(t *testing.T, api *fakeAPI, commands CommandService, out io.Writer) *Client {
	t.Helper()
	api.sent = make(chan sentMessage, 20)
	api.sending = make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(server.Close)
	c, err := newClient(config.Telegram{Enabled: true, Token: fakeToken, AllowedUserIDs: []int64{42}},
		commands, slog.New(slog.NewTextHandler(out, nil)), tgbot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startClient(t *testing.T, c *Client) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return cancel, done
}

func awaitStop(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Telegram client did not stop")
		return nil
	}
}

func TestLongPollingCommandsAndAccessControl(t *testing.T) {
	group := update(7, 42, "/help")
	group.Message.Chat = models.Chat{ID: -100, Type: models.ChatTypeGroup}
	otherBot := update(8, 42, "/help@other_bot")
	missingSender := update(9, 42, "/help")
	missingSender.Message.From = nil
	api := &fakeAPI{updates: []*models.Update{
		update(1, 42, "/start"), update(2, 42, "/help"), update(3, 42, "/help@study_agent_bot"),
		update(4, 42, "/unknown"), update(5, 42, ""), update(6, 99, "/help"), group, otherBot, missingSender,
	}}
	commands := &testCommands{}
	var logs bytes.Buffer
	c := testClient(t, api, commands, &logs)
	cancel, done := startClient(t, c)
	var sent []sentMessage
	for range 6 {
		select {
		case m := <-api.sent:
			sent = append(sent, m)
		case <-time.After(5 * time.Second):
			t.Fatal("expected bot replies were not received")
		}
	}
	cancel()
	if err := awaitStop(t, done); err != nil {
		t.Fatal(err)
	}
	if commands.start.Load() != 1 || commands.help.Load() != 2 {
		t.Fatal("commands bypassed access checks or were dispatched incorrectly")
	}
	if api.menus.Load() != 1 || api.polls.Load() < 1 {
		t.Fatal("command menu or polling was not started")
	}
	if sent[0].text != "start-response" || sent[1].text != "help-response" ||
		!strings.Contains(sent[3].text, "Неизвестная") || sent[5].chatID != 99 || !strings.Contains(sent[5].text, "ограничен") {
		t.Fatalf("unexpected command responses: %+v", sent)
	}
	if len(api.sent) != 0 {
		t.Fatal("a group or unrelated update received a response")
	}
	if strings.Contains(logs.String(), fakeToken) {
		t.Fatal("token leaked into the log")
	}
}

func TestAPIErrorsDoNotExposeToken(t *testing.T) {
	for _, tc := range []struct {
		method   string
		code     int
		category string
	}{
		{"getMe", http.StatusUnauthorized, "unauthorized"},
		{"getUpdates", http.StatusUnauthorized, "unauthorized"},
		{"getUpdates", http.StatusConflict, "conflict"},
	} {
		t.Run(tc.method+"/"+tc.category, func(t *testing.T) {
			api := &fakeAPI{failMethod: tc.method, failCode: tc.code}
			var logs bytes.Buffer
			c := testClient(t, api, &testCommands{}, &logs)
			_, done := startClient(t, c)
			err := awaitStop(t, done)
			if err == nil || !strings.Contains(err.Error(), tc.category) {
				t.Fatalf("expected %s failure, got %v", tc.category, err)
			}
			if strings.Contains(err.Error()+logs.String(), fakeToken) {
				t.Fatal("token leaked into diagnostics")
			}
		})
	}
}

func TestExistingWebhookPreventsPolling(t *testing.T) {
	api := &fakeAPI{webhook: "https://example.invalid/webhook"}
	c := testClient(t, api, &testCommands{}, io.Discard)
	_, done := startClient(t, c)
	if err := awaitStop(t, done); err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("expected webhook conflict, got %v", err)
	}
	if api.polls.Load() != 0 || api.menus.Load() != 0 {
		t.Fatal("bot started polling with an active webhook")
	}
}

func TestShutdownCancelsInFlightReply(t *testing.T) {
	api := &fakeAPI{blockSend: true, updates: []*models.Update{update(1, 42, "/help")}}
	c := testClient(t, api, &testCommands{}, io.Discard)
	cancel, done := startClient(t, c)
	select {
	case <-api.sending:
	case <-time.After(5 * time.Second):
		t.Fatal("reply did not start")
	}
	cancel()
	if err := awaitStop(t, done); err != nil {
		t.Fatal(err)
	}
}

func callbackUpdate(id int64, owner int64, data string) *models.Update {
	return &models.Update{ID: id, CallbackQuery: &models.CallbackQuery{
		ID: strconv.FormatInt(id, 10), From: models.User{ID: owner}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 10, Date: 1, Chat: models.Chat{ID: owner, Type: models.ChatTypePrivate}}},
	}}
}

func TestCallbacksAreAuthorizedAcknowledgedAndEditCards(t *testing.T) {
	denied := callbackUpdate(1, 99, "done:1:tasks:todo:0")
	group := callbackUpdate(2, 42, "done:1:tasks:todo:0")
	group.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeGroup
	old := callbackUpdate(3, 42, "done:1:tasks:todo:0")
	old.CallbackQuery.Message = models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeInaccessibleMessage, InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, MessageID: 10}}
	valid := callbackUpdate(4, 42, "task:1:tasks:todo:0")
	api := &fakeAPI{updates: []*models.Update{denied, group, old, valid}}
	commands := &testCommands{}
	c := testClient(t, api, commands, io.Discard)
	cancel, done := startClient(t, c)
	select {
	case m := <-api.sent:
		if m.method != "editMessageText" || m.text != "callback-response" || len(m.keyboard.InlineKeyboard) != 1 || m.keyboard.InlineKeyboard[0][0].CallbackData != "list:tasks:todo:0" {
			t.Fatalf("callback response was not rendered: %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback reply missing")
	}
	cancel()
	if err := awaitStop(t, done); err != nil {
		t.Fatal(err)
	}
	if commands.help.Load() != 1 || api.answers.Load() != 4 || !api.callbacksAllowed.Load() {
		t.Fatal("callback authorization, acknowledgment or subscription failed")
	}
}

func TestHelpAndDisabledStorageThroughTelegramAPI(t *testing.T) {
	// A nil repository still allows /help and demonstrates the real service transport.
	api := &fakeAPI{updates: []*models.Update{update(1, 42, "/help"), update(2, 42, "/help@study_agent_bot"), update(3, 42, "/add")}}
	c := testClient(t, api, service.NewCommands(nil, time.UTC), io.Discard)
	cancel, done := startClient(t, c)
	for i := 0; i < 3; i++ {
		select {
		case m := <-api.sent:
			if i < 2 && (!strings.Contains(m.text, "/tasks") || !strings.Contains(m.text, "/cancel") || !strings.Contains(m.text, "/add") || !strings.Contains(m.text, "/delete")) {
				t.Fatal("task help was not sent")
			}
			if i == 2 && !strings.Contains(m.text, "недоступны") {
				t.Fatal("disabled storage was not handled")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("service reply missing")
		}
	}
	cancel()
	if err := awaitStop(t, done); err != nil {
		t.Fatal(err)
	}
}
