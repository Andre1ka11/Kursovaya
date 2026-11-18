package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"study-agent/internal/domain"
)

const draftTTL = 20 * time.Minute

type draft struct {
	task    domain.Task
	step    int
	updated time.Time
}

var prompts = []string{
	"Как называется задача? До 200 символов. Отмена: /cancel",
	"Какой предмет? До 100 символов; отправь - без предмета. Отмена: /cancel",
	"Когда срок? ДД.ММ.ГГГГ ЧЧ:ММ или ДД.ММ.ГГГГ (до 23:59). Отправь - без срока. Отмена: /cancel",
	"Добавь описание до 2000 символов или отправь -. Отмена: /cancel",
}

func (c *Commands) cancelDraft(owner int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.drafts[owner]
	delete(c.drafts, owner)
	return ok
}

func (c *Commands) beginDraft(owner int64, title string) Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.drafts[owner]; ok && c.now().Sub(d.updated) < draftTTL {
		return say("Ты уже добавляешь задачу. " + prompts[d.step])
	}
	d := draft{task: domain.Task{OwnerID: owner}, updated: c.now()}
	if title != "" {
		if utf8.RuneCountInString(title) > 200 {
			return say("Название слишком длинное: максимум 200 символов. Повтори /add.")
		}
		d.task.Title = title
		d.step = 1
	}
	c.drafts[owner] = d
	return say(prompts[d.step])
}

func (c *Commands) continueDraft(ctx context.Context, owner int64, text string) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.drafts[owner]
	if !ok {
		return say("Для добавления задачи отправь /add. Все команды: /help"), nil
	}
	if c.now().Sub(d.updated) >= draftTTL {
		delete(c.drafts, owner)
		return say("Время ввода истекло. Начни заново: /add"), nil
	}
	switch d.step {
	case 0:
		if utf8.RuneCountInString(text) > 200 {
			return say("Название слишком длинное: максимум 200 символов."), nil
		}
		d.task.Title = text
	case 1:
		if utf8.RuneCountInString(text) > 100 {
			return say("Предмет слишком длинный: максимум 100 символов."), nil
		}
		if text != "-" {
			d.task.Subject = text
		}
	case 2:
		due, err := parseDue(text, c.zone)
		if err != nil {
			return say("Не удалось распознать дату. " + prompts[2]), nil
		}
		d.task.DueAt = due
	case 3:
		if utf8.RuneCountInString(text) > 2000 {
			return say("Описание слишком длинное: максимум 2000 символов."), nil
		}
		if text != "-" {
			d.task.Description = text
		}
		// Keep the draft until the insert succeeds; a failed request can be retried.
		task, err := c.repo.CreateTask(ctx, d.task)
		if err != nil {
			return Response{}, err
		}
		delete(c.drafts, owner)
		out := c.card(task, defaultView())
		out.Text = "Задача сохранена.\n\n" + out.Text
		return out, nil
	}
	d.step++
	d.updated = c.now()
	c.drafts[owner] = d
	out := prompts[d.step]
	if d.step == 2 {
		out += "\nЧасовой пояс: " + c.zone.String()
	}
	return say(out), nil
}

func parseDue(text string, zone *time.Location) (*time.Time, error) {
	if text == "-" {
		return nil, nil
	}
	layout := "02.01.2006 15:04"
	if !strings.Contains(text, " ") {
		layout = "02.01.2006"
	}
	due, err := time.ParseInLocation(layout, text, zone)
	if err != nil {
		return nil, err
	}
	if due.Format(layout) != text {
		return nil, domain.ErrInvalid
	}
	if layout == "02.01.2006" {
		due = time.Date(due.Year(), due.Month(), due.Day(), 23, 59, 0, 0, zone)
	}
	return &due, nil
}
