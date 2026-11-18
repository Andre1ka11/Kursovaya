package telegram

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"study-agent/internal/service"
)

func TestCommandLogsTraceRepliesWithoutPrivateText(t *testing.T) {
	api := &fakeAPI{}
	var logs bytes.Buffer
	c := testClient(t, api, service.NewCommands(nil, time.UTC), &logs)
	for i, text := range []string{"/help", "/add PRIVATE_TITLE", "PRIVATE_DESCRIPTION", "/PRIVATE_SECRET"} {
		c.handle(context.Background(), c.bot, update(int64(i+1), 42, text))
	}
	out := logs.String()
	if strings.Count(out, "telegram message received") != 4 || strings.Count(out, "telegram reply sent") != 4 || !strings.Contains(out, "action=/help") || !strings.Contains(out, "action=/add") || !strings.Contains(out, "action=text") || !strings.Contains(out, "action=unknown_command") || !strings.Contains(out, "duration_ms=") {
		t.Fatalf("incomplete command trace: %s", out)
	}
	if strings.Contains(out, "PRIVATE_") || strings.Contains(out, fakeToken) {
		t.Fatal("private data leaked into command logs")
	}
}

func TestFailedSendIsLoggedWithOperationWithoutSuccess(t *testing.T) {
	api := &fakeAPI{failMethod: "sendMessage", failCode: 400}
	var logs bytes.Buffer
	c := testClient(t, api, &testCommands{}, &logs)
	c.handle(context.Background(), c.bot, update(1, 42, "/help"))
	out := logs.String()
	if !strings.Contains(out, "operation=sendMessage") || !strings.Contains(out, "category=bad_request") || !strings.Contains(out, "telegram reply failed") || strings.Contains(out, "telegram reply sent") {
		t.Fatalf("failed delivery was not diagnosed correctly: %s", out)
	}
	if strings.Contains(out, fakeToken) {
		t.Fatal("API description leaked into logs")
	}
}

type failingCommands struct{}

func (failingCommands) Handle(context.Context, service.Request) (service.Response, error) {
	return service.Response{}, errors.New("PRIVATE_STORAGE_ERROR")
}

func TestServiceFailureIsDistinctFromDeliveryFailure(t *testing.T) {
	api := &fakeAPI{}
	var logs bytes.Buffer
	c := testClient(t, api, failingCommands{}, &logs)
	c.handle(context.Background(), c.bot, update(1, 42, "/tasks"))
	out := logs.String()
	if !strings.Contains(out, "task command failed") || !strings.Contains(out, "result=service_error") || !strings.Contains(out, "telegram reply sent") || strings.Contains(out, "PRIVATE_STORAGE_ERROR") {
		t.Fatalf("service failure trace incorrect: %s", out)
	}
}

func TestPollingAndCallbackErrorsNameTheirOperations(t *testing.T) {
	api := &fakeAPI{failMethod: "answerCallbackQuery", failCode: 400}
	var logs bytes.Buffer
	c := testClient(t, api, &testCommands{}, &logs)
	c.handleError(context.DeadlineExceeded)
	c.handle(context.Background(), c.bot, callbackUpdate(1, 42, "task:1:tasks:todo:0"))
	out := logs.String()
	if !strings.Contains(out, "operation=getUpdates category=timeout") || !strings.Contains(out, "operation=answerCallbackQuery category=bad_request") {
		t.Fatalf("error operations missing: %s", out)
	}
	if strings.Contains(out, fakeToken) {
		t.Fatal("API description leaked into logs")
	}
}
