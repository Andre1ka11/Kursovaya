// Package service contains application behavior independent of Telegram.
package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"study-agent/internal/domain"
)

type TaskRepository interface {
	CreateTask(context.Context, domain.Task) (domain.Task, error)
	Task(context.Context, int64, int64) (domain.Task, error)
	FindTasks(context.Context, int64, domain.TaskFilter) ([]domain.Task, error)
	CompleteTask(context.Context, int64, int64) (domain.Task, error)
	DeleteTask(context.Context, int64, int64) error
}

type Request struct {
	UserID             int64
	Text, CallbackData string
}
type Button struct{ Text, Data string }
type Response struct {
	Text    string
	Buttons [][]Button
}

type Commands struct {
	repo   TaskRepository
	zone   *time.Location
	now    func() time.Time
	mu     sync.Mutex
	drafts map[int64]draft
}

func NewCommands(repo TaskRepository, zone *time.Location) *Commands {
	if zone == nil {
		zone = time.UTC
	}
	return &Commands{repo: repo, zone: zone, now: time.Now, drafts: make(map[int64]draft)}
}

func (*Commands) Start() string {
	return "Привет! Я помощник по учебным задачам и занятиям.\n\nДобавить задачу: /add\nСписок доступных действий: /help"
}

func (*Commands) Help() string {
	return "Доступные команды:\n/start — знакомство с помощником\n/help — эта справка\n" +
		"/add — добавить задачу пошагово\n/cancel — отменить добавление\n" +
		"/tasks — незавершённые задачи\n/tasks done — выполненные\n/tasks all — все задачи\n" +
		"/today — сроки на сегодня\n/week — сроки на ближайшие 7 дней\n" +
		"/done ID — отметить выполненной\n/delete ID — удалить с подтверждением\n\n" +
		"В списках есть кнопки карточек и перехода между страницами.\n" +
		"Срок: ДД.ММ.ГГГГ ЧЧ:ММ, ДД.ММ.ГГГГ (до 23:59) или - без срока."
}

func (c *Commands) Handle(ctx context.Context, r Request) (Response, error) {
	if r.UserID <= 0 {
		return Response{}, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if r.CallbackData != "" {
		if c.repo == nil {
			return say("Задачи временно недоступны."), nil
		}
		return c.callback(ctx, r)
	}
	text := strings.TrimSpace(r.Text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return say("Отправь текст или /help, чтобы увидеть доступные команды."), nil
	}
	command := fields[0]
	switch command {
	case "/start":
		c.cancelDraft(r.UserID)
		return say(c.Start()), nil
	case "/help":
		return say(c.Help()), nil
	case "/cancel":
		if c.cancelDraft(r.UserID) {
			return say("Добавление задачи отменено."), nil
		}
		return say("Сейчас нет незавершённого добавления. Новая задача: /add"), nil
	}
	if !strings.HasPrefix(command, "/") {
		return c.continueDraft(ctx, r.UserID, text)
	}
	switch command {
	case "/add", "/tasks", "/today", "/week", "/done", "/delete":
		if c.repo == nil {
			return say("Задачи временно недоступны."), nil
		}
	default:
		return say("Неизвестная команда. Список доступных действий: /help"), nil
	}
	switch command {
	case "/add":
		return c.beginDraft(r.UserID, strings.TrimSpace(strings.TrimPrefix(text, command))), nil
	case "/tasks", "/today", "/week":
		v, ok := parseListCommand(fields)
		if !ok {
			return say("Формат: /tasks [todo|done|all] [страница], /today [страница], /week [страница]. Номер страницы — от 1."), nil
		}
		return c.list(ctx, r.UserID, v)
	default:
		if len(fields) != 2 {
			return say("Укажи положительный ID задачи: " + command + " 123"), nil
		}
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || id <= 0 {
			return say("ID задачи должен быть положительным целым числом."), nil
		}
		return c.taskAction(ctx, r.UserID, strings.TrimPrefix(command, "/"), id, defaultView())
	}
}

func say(text string) Response { return Response{Text: text} }

func (c *Commands) taskAction(ctx context.Context, owner int64, action string, id int64, v taskView) (Response, error) {
	if action == "confirm" {
		if err := c.repo.DeleteTask(ctx, owner, id); err != nil {
			return taskError(err)
		}
		out, err := c.list(ctx, owner, v)
		out.Text = "Задача #" + strconv.FormatInt(id, 10) + " удалена.\n\n" + out.Text
		return out, err
	}
	var task domain.Task
	var err error
	if action == "done" {
		task, err = c.repo.CompleteTask(ctx, owner, id)
	} else {
		task, err = c.repo.Task(ctx, owner, id)
	}
	if err != nil {
		return taskError(err)
	}
	if action == "delete" {
		return Response{Text: "Удалить задачу #" + strconv.FormatInt(id, 10) + " «" + short(task.Title, 200) + "»?", Buttons: [][]Button{{
			{Text: "Да, удалить", Data: taskData("confirm", id, v)}, {Text: "Отмена", Data: taskData("task", id, v)},
		}}}, nil
	}
	out := c.card(task, v)
	if action == "done" {
		out.Text = "Готово! Задача выполнена.\n\n" + out.Text
	}
	return out, nil
}

func taskError(err error) (Response, error) {
	if errors.Is(err, domain.ErrNotFound) {
		return say("Задача не найдена или недоступна. Обнови список: /tasks"), nil
	}
	return Response{}, err
}
