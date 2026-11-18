package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"study-agent/internal/domain"
)

const pageSize = 5

type taskView struct {
	kind, mode string
	page       int
}

func defaultView() taskView     { return taskView{kind: "tasks", mode: "todo"} }
func (v taskView) data() string { return fmt.Sprintf("%s:%s:%d", v.kind, v.mode, v.page) }
func taskData(action string, id int64, v taskView) string {
	return fmt.Sprintf("%s:%d:%s", action, id, v.data())
}

func parseListCommand(fields []string) (taskView, bool) {
	v := defaultView()
	v.kind = strings.TrimPrefix(fields[0], "/")
	args := fields[1:]
	if v.kind == "tasks" && len(args) > 0 && (args[0] == "todo" || args[0] == "done" || args[0] == "all") {
		v.mode = args[0]
		args = args[1:]
	}
	if len(args) > 1 {
		return v, false
	}
	if len(args) == 1 {
		page, err := strconv.Atoi(args[0])
		if err != nil || page < 1 || page > 1000000 {
			return v, false
		}
		v.page = page - 1
	}
	return v, true
}

func parseView(parts []string) (taskView, bool) {
	if len(parts) != 3 {
		return taskView{}, false
	}
	v := taskView{kind: parts[0], mode: parts[1]}
	if v.kind != "tasks" && v.kind != "today" && v.kind != "week" {
		return v, false
	}
	if v.mode != "todo" && v.mode != "done" && v.mode != "all" {
		return v, false
	}
	if v.kind != "tasks" && v.mode != "todo" {
		return v, false
	}
	page, err := strconv.Atoi(parts[2])
	if err != nil || page < 0 || page >= 1000000 {
		return v, false
	}
	v.page = page
	return v, true
}

func (c *Commands) callback(ctx context.Context, r Request) (Response, error) {
	parts := strings.Split(r.CallbackData, ":")
	if len(parts) == 4 && parts[0] == "list" {
		if v, ok := parseView(parts[1:]); ok {
			return c.list(ctx, r.UserID, v)
		}
	}
	if len(parts) == 5 && (parts[0] == "task" || parts[0] == "done" || parts[0] == "delete" || parts[0] == "confirm") {
		id, err := strconv.ParseInt(parts[1], 10, 64)
		v, ok := parseView(parts[2:])
		if err == nil && id > 0 && ok {
			return c.taskAction(ctx, r.UserID, parts[0], id, v)
		}
	}
	return say("Кнопка устарела или недоступна. Обнови список: /tasks"), nil
}

func (c *Commands) list(ctx context.Context, owner int64, v taskView) (Response, error) {
	f := domain.TaskFilter{Limit: pageSize + 1, Offset: v.page * pageSize}
	if v.mode != "all" {
		f.Status = domain.TaskStatus(v.mode)
	}
	heading := "Незавершённые задачи"
	if v.mode == "done" {
		heading = "Выполненные задачи"
	}
	if v.mode == "all" {
		heading = "Все задачи"
	}
	if v.kind == "today" || v.kind == "week" {
		now := c.now().In(c.zone)
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.zone)
		days := 1
		heading = "Задачи на сегодня"
		if v.kind == "week" {
			days = 7
			heading = "Задачи на ближайшие 7 дней"
		}
		before := from.AddDate(0, 0, days)
		f.DueFrom = &from
		f.DueBefore = &before
	}
	tasks, err := c.repo.FindTasks(ctx, owner, f)
	if err != nil {
		return Response{}, err
	}
	hasNext := len(tasks) > pageSize
	if hasNext {
		tasks = tasks[:pageSize]
	}
	out := say(fmt.Sprintf("%s · страница %d\nЧасовой пояс: %s", heading, v.page+1, c.zone))
	if len(tasks) == 0 {
		out.Text += "\n\nЗадач нет. Добавить: /add"
	}
	for _, task := range tasks {
		status := "○"
		if task.Status == domain.TaskDone {
			status = "✓"
		}
		out.Text += fmt.Sprintf("\n\n%s #%d %s\nСрок: %s", status, task.ID, short(task.Title, 160), c.dueLabel(task))
		out.Buttons = append(out.Buttons, []Button{{Text: fmt.Sprintf("Открыть #%d", task.ID), Data: taskData("task", task.ID, v)}})
	}
	var nav []Button
	if v.page > 0 {
		prev := v
		prev.page--
		nav = append(nav, Button{Text: "← Назад", Data: "list:" + prev.data()})
	}
	if hasNext {
		next := v
		next.page++
		nav = append(nav, Button{Text: "Далее →", Data: "list:" + next.data()})
	}
	if len(nav) > 0 {
		out.Buttons = append(out.Buttons, nav)
	}
	return out, nil
}

func (c *Commands) card(task domain.Task, v taskView) Response {
	status := "Не выполнена"
	if task.Status == domain.TaskDone {
		status = "Выполнена"
	}
	out := say(fmt.Sprintf("Задача #%d\n%s\n\nСтатус: %s\nСрок: %s", task.ID, short(task.Title, 200), status, c.dueLabel(task)))
	if task.Subject != "" {
		out.Text += "\nПредмет: " + short(task.Subject, 100)
	}
	if task.Description != "" {
		out.Text += "\n\n" + short(task.Description, 1500)
	}
	if task.DueAt != nil {
		out.Text += "\n\nЧасовой пояс: " + c.zone.String()
	}
	var actions []Button
	if task.Status != domain.TaskDone {
		actions = append(actions, Button{Text: "✅ Выполнено", Data: taskData("done", task.ID, v)})
	}
	actions = append(actions, Button{Text: "🗑 Удалить", Data: taskData("delete", task.ID, v)})
	out.Buttons = [][]Button{actions, {{Text: "← К списку", Data: "list:" + v.data()}}}
	return out
}

func (c *Commands) dueLabel(task domain.Task) string {
	if task.DueAt == nil {
		return "без срока"
	}
	text := task.DueAt.In(c.zone).Format("02.01.2006 15:04")
	if task.Status == domain.TaskTodo && task.DueAt.Before(c.now()) {
		text += " (просрочено)"
	}
	return text
}

func short(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}
