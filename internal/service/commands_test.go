package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"study-agent/internal/domain"
)

type memoryTasks struct {
	mu         sync.Mutex
	tasks      map[int64]domain.Task
	next       int64
	failCreate bool
	lastFilter domain.TaskFilter
}

func newMemoryTasks() *memoryTasks { return &memoryTasks{tasks: map[int64]domain.Task{}} }
func (m *memoryTasks) CreateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Task{}, err
	}
	if m.failCreate {
		return domain.Task{}, errors.New("storage unavailable")
	}
	m.next++
	t.ID = m.next
	t.Status = domain.TaskTodo
	m.tasks[t.ID] = t
	return t, nil
}
func (m *memoryTasks) Task(_ context.Context, owner, id int64) (domain.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.OwnerID != owner {
		return domain.Task{}, domain.ErrNotFound
	}
	return t, nil
}
func (m *memoryTasks) CompleteTask(ctx context.Context, owner, id int64) (domain.Task, error) {
	t, err := m.Task(ctx, owner, id)
	if err != nil {
		return t, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t.Status = domain.TaskDone
	now := time.Now()
	t.CompletedAt = &now
	m.tasks[id] = t
	return t, nil
}
func (m *memoryTasks) DeleteTask(ctx context.Context, owner, id int64) error {
	if _, err := m.Task(ctx, owner, id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, id)
	return nil
}
func (m *memoryTasks) FindTasks(_ context.Context, owner int64, f domain.TaskFilter) ([]domain.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastFilter = f
	var tasks []domain.Task
	for _, t := range m.tasks {
		if t.OwnerID != owner || (f.Status != "" && t.Status != f.Status) {
			continue
		}
		if f.DueFrom != nil && (t.DueAt == nil || t.DueAt.Before(*f.DueFrom)) {
			continue
		}
		if f.DueBefore != nil && (t.DueAt == nil || !t.DueAt.Before(*f.DueBefore)) {
			continue
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	if f.Offset >= len(tasks) {
		return nil, nil
	}
	tasks = tasks[f.Offset:]
	if len(tasks) > f.Limit {
		tasks = tasks[:f.Limit]
	}
	return tasks, nil
}

func textRequest(t *testing.T, c *Commands, owner int64, text string) Response {
	t.Helper()
	out, err := c.Handle(context.Background(), Request{UserID: owner, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func buttonRequest(t *testing.T, c *Commands, owner int64, data string) Response {
	t.Helper()
	out, err := c.Handle(context.Background(), Request{UserID: owner, CallbackData: data})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAddDialogValidationAndRetry(t *testing.T) {
	repo := newMemoryTasks()
	zone, _ := time.LoadLocation("Europe/Moscow")
	c := NewCommands(repo, zone)
	textRequest(t, c, 42, "/add")
	if out := textRequest(t, c, 42, strings.Repeat("я", 201)); !strings.Contains(out.Text, "слишком") {
		t.Fatal("oversized title accepted")
	}
	textRequest(t, c, 42, "Лабораторная @университет")
	textRequest(t, c, 99, "/add Чужая задача")
	textRequest(t, c, 42, "Базы данных")
	if out := textRequest(t, c, 42, "31.02.2026"); !strings.Contains(out.Text, "дату") {
		t.Fatal("invalid date accepted")
	}
	textRequest(t, c, 42, "25.11.2026 18:30")
	if len(repo.tasks) != 0 {
		t.Fatal("task saved before final step")
	}
	repo.failCreate = true
	if _, err := c.Handle(context.Background(), Request{UserID: 42, Text: "Реализовать запросы"}); err == nil {
		t.Fatal("storage failure ignored")
	}
	repo.failCreate = false
	out := textRequest(t, c, 42, "Реализовать запросы")
	task := repo.tasks[1]
	if !strings.Contains(out.Text, "сохранена") || task.OwnerID != 42 || task.Subject != "Базы данных" || task.DueAt.Format(time.RFC3339) != "2026-11-25T18:30:00+03:00" || task.Description != "Реализовать запросы" {
		t.Fatalf("incorrect task: %+v", task)
	}
	if len(repo.tasks) != 1 || c.drafts[99].task.Title != "Чужая задача" {
		t.Fatal("dialogues were mixed")
	}
	textRequest(t, c, 42, "Реализовать запросы")
	if len(repo.tasks) != 1 {
		t.Fatal("repeated final reply created another task")
	}
}

func TestCancelExpiryAndOptionalFields(t *testing.T) {
	repo := newMemoryTasks()
	c := NewCommands(repo, time.UTC)
	textRequest(t, c, 42, "/add")
	textRequest(t, c, 42, "/cancel")
	textRequest(t, c, 42, "Задача")
	if len(c.drafts) != 0 || len(repo.tasks) != 0 {
		t.Fatal("cancel did not discard draft")
	}
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	textRequest(t, c, 42, "/add Test")
	now = now.Add(draftTTL)
	if out := textRequest(t, c, 42, "Предмет"); !strings.Contains(out.Text, "истекло") {
		t.Fatal("expired draft accepted")
	}
	for _, input := range []string{"/add Без срока", "-", "-", "-"} {
		textRequest(t, c, 42, input)
	}
	if task := repo.tasks[1]; task.DueAt != nil || task.Subject != "" || task.Description != "" {
		t.Fatal("optional fields are not empty")
	}
	textRequest(t, c, 42, "/add New")
	textRequest(t, c, 42, "/help")
	if c.drafts[42].step != 1 {
		t.Fatal("command consumed dialog input")
	}
}

func TestListNavigationCompletionAndDeletion(t *testing.T) {
	repo := newMemoryTasks()
	c := NewCommands(repo, time.UTC)
	for i := 1; i <= 6; i++ {
		_, _ = repo.CreateTask(context.Background(), domain.Task{OwnerID: 42, Title: fmt.Sprintf("Task %d", i)})
	}
	out := textRequest(t, c, 42, "/tasks")
	if len(out.Buttons) != 6 || out.Buttons[5][0].Data != "list:tasks:todo:1" {
		t.Fatalf("next-page button missing: %+v", out.Buttons)
	}
	out = buttonRequest(t, c, 42, out.Buttons[5][0].Data)
	if !strings.Contains(out.Text, "страница 2") || !strings.Contains(out.Text, "Task 6") || len(out.Buttons) != 2 {
		t.Fatal("page 2 incorrect")
	}
	out = buttonRequest(t, c, 42, out.Buttons[0][0].Data)
	if !strings.Contains(out.Text, "Задача #6") || out.Buttons[1][0].Data != "list:tasks:todo:1" {
		t.Fatal("card lost list context")
	}
	out = buttonRequest(t, c, 42, out.Buttons[0][0].Data)
	if repo.tasks[6].Status != domain.TaskDone || strings.Contains(out.Buttons[0][0].Data, "done:") {
		t.Fatal("task not completed")
	}
	if out := textRequest(t, c, 42, "/tasks done"); !strings.Contains(out.Text, "Task 6") {
		t.Fatal("completed task is not visible")
	}
	out = textRequest(t, c, 42, "/delete 6")
	if len(repo.tasks) != 6 {
		t.Fatal("task deleted without confirmation")
	}
	buttonRequest(t, c, 42, out.Buttons[0][1].Data)
	if len(repo.tasks) != 6 {
		t.Fatal("canceling deletion removed task")
	}
	buttonRequest(t, c, 42, out.Buttons[0][0].Data)
	if len(repo.tasks) != 5 {
		t.Fatal("confirmed deletion failed")
	}
	if out := buttonRequest(t, c, 42, "done:6:tasks:todo:0"); !strings.Contains(out.Text, "не найдена") {
		t.Fatal("stale task not handled")
	}
}

func TestOwnerChecksAndInvalidArguments(t *testing.T) {
	repo := newMemoryTasks()
	c := NewCommands(repo, time.UTC)
	_, _ = repo.CreateTask(context.Background(), domain.Task{OwnerID: 99, Title: "PRIVATE"})
	for _, text := range []string{"/done 1", "/delete 1", "/done -1", "/done abc", "/done", "/delete 1 extra", "/tasks 0", "/tasks -2", "/tasks done abc", "/today done", "/week 1000001"} {
		out := textRequest(t, c, 42, text)
		if out.Text == "" || strings.Contains(out.Text, "PRIVATE") {
			t.Fatalf("bad response to %s", text)
		}
	}
	for _, data := range []string{"task:1:tasks:todo:0", "done:1:tasks:todo:0", "confirm:1:tasks:todo:0", "done:-1:tasks:todo:0", "confirm:1:tasks:todo:-1", "list:week:all:0", "nonsense"} {
		out := buttonRequest(t, c, 42, data)
		if out.Text == "" || strings.Contains(out.Text, "PRIVATE") {
			t.Fatalf("bad response to %s", data)
		}
	}
	if repo.tasks[1].Status != domain.TaskTodo || len(repo.tasks) != 1 {
		t.Fatal("foreign task modified")
	}
}

func TestTodayAndWeekUseLocalMidnight(t *testing.T) {
	repo := newMemoryTasks()
	zone, _ := time.LoadLocation("Europe/Moscow")
	c := NewCommands(repo, zone)
	c.now = func() time.Time { return time.Date(2026, 11, 18, 22, 30, 0, 0, time.UTC) }
	for _, tc := range []struct {
		command string
		days    int
	}{{"/today", 1}, {"/week", 7}} {
		textRequest(t, c, 42, tc.command)
		f := repo.lastFilter
		want := time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC)
		if f.DueFrom == nil || !f.DueFrom.Equal(want) || !f.DueBefore.Equal(want.AddDate(0, 0, tc.days)) || f.Status != domain.TaskTodo {
			t.Fatalf("incorrect local date filter: %+v", f)
		}
	}
	date, err := parseDue("25.11.2026", zone)
	if err != nil || date.Hour() != 23 || date.Minute() != 59 {
		t.Fatal("date-only deadline must be 23:59")
	}
}

func TestCardFitsTelegramAndCallbacksFit64Bytes(t *testing.T) {
	c := NewCommands(nil, time.UTC)
	out := c.card(domain.Task{ID: 9223372036854775807, Title: strings.Repeat("🧑", 200), Subject: strings.Repeat("🧑", 100), Description: strings.Repeat("🧑", 2000)}, taskView{kind: "tasks", mode: "todo", page: 999999})
	if len(utf16.Encode([]rune(out.Text))) > 4096 {
		t.Fatal("card exceeds Telegram message limit")
	}
	for _, row := range out.Buttons {
		for _, button := range row {
			if len(button.Data) > 64 {
				t.Fatal("callback exceeds Telegram limit")
			}
		}
	}
}

func TestConcurrentFinalRepliesCreateOneTask(t *testing.T) {
	repo := newMemoryTasks()
	c := NewCommands(repo, time.UTC)
	for _, text := range []string{"/add One", "-", "-"} {
		textRequest(t, c, 42, text)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Handle(context.Background(), Request{UserID: 42, Text: "-"}) }()
	}
	wg.Wait()
	if len(repo.tasks) != 1 {
		t.Fatal("concurrent replies created duplicate tasks")
	}
}
