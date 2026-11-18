package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"study-agent/internal/config"
	"study-agent/internal/domain"
)

func TestErrorsHideSecrets(t *testing.T) {
	for _, code := range []string{"28P01", "3D000", "42501", "23505", "23503", "23514", "57014", "XX000"} {
		err := dbError(fmt.Errorf("wrapper: %w", &pgconn.PgError{Code: code, Message: "secret-password", Detail: "private content"}))
		if err == nil || strings.Contains(err.Error(), "secret-password") || strings.Contains(err.Error(), "private content") {
			t.Fatal("database details leaked")
		}
	}
	if !errors.Is(dbError(fmt.Errorf("wrapped: %w", context.Canceled)), context.Canceled) {
		t.Fatal("context cancellation was lost")
	}
}

func TestPostgresIntegration(t *testing.T) {
	if os.Getenv("AGENT_TEST_DATABASE") != "true" {
		t.Skip("set AGENT_TEST_DATABASE=true to test the configured PostgreSQL database")
	}
	cfg, err := config.Load("../../../configs/agent.yaml", "../../../.env")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s := openTestStore(t, ctx, cfg.Database)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	newOwner := func(t *testing.T) int64 {
		t.Helper()
		owner := time.Now().UnixNano()
		t.Cleanup(func() {
			cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, table := range []string{"tasks", "messages", "lessons", "sync_state"} {
				if _, err := s.db.Exec(cleanCtx, "DELETE FROM study_agent."+table+" WHERE owner_id=$1", owner); err != nil {
					t.Error(dbError(err))
				}
			}
		})
		return owner
	}

	t.Run("MigrationsAreRepeatableAndSerialized", func(t *testing.T) {
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); results <- s.Migrate(ctx) }()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		var count int
		if err := s.db.QueryRow(ctx, "SELECT count(*) FROM study_agent.schema_migrations").Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected one migration, count=%d err=%v", count, dbError(err))
		}
	})

	t.Run("TaskSurvivesNewConnectionAndChecksOwner", func(t *testing.T) {
		owner := newOwner(t)
		due := time.Date(2026, 12, 1, 18, 0, 0, 0, time.FixedZone("MSK", 3*3600))
		task, err := s.CreateTask(ctx, domain.Task{OwnerID: owner, Title: "Проверка сохранения", DueAt: &due})
		if err != nil {
			t.Fatal(err)
		}
		other := openTestStore(t, ctx, cfg.Database)
		loaded, err := other.Task(ctx, owner, task.ID)
		if err != nil || loaded.Title != task.Title || loaded.DueAt == nil || !loaded.DueAt.Equal(due) || loaded.Status != domain.TaskTodo {
			t.Fatalf("task did not persist correctly: %v", err)
		}
		if _, err := other.Task(ctx, owner+1, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another owner can read task: %v", err)
		}
		if _, err := s.CompleteTask(ctx, owner+1, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another owner can complete task: %v", err)
		}
		if err := s.DeleteTask(ctx, owner+1, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another owner can delete task: %v", err)
		}
		if _, err := s.CreateTask(ctx, domain.Task{OwnerID: owner, Title: "  "}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("blank task title accepted: %v", err)
		}
		undated, err := s.CreateTask(ctx, domain.Task{OwnerID: owner, Title: "Без срока"})
		if err != nil {
			t.Fatal(err)
		}
		tasks, err := s.Tasks(ctx, owner, domain.TaskTodo, 10, 0)
		if err != nil || len(tasks) != 2 || tasks[0].ID != task.ID || tasks[1].ID != undated.ID {
			t.Fatalf("deadline ordering failed: %v", err)
		}
		done, err := s.CompleteTask(ctx, owner, task.ID)
		if err != nil || done.CompletedAt == nil || done.Status != domain.TaskDone {
			t.Fatalf("completion failed: %v", err)
		}
		again, err := s.CompleteTask(ctx, owner, task.ID)
		if err != nil || !again.CompletedAt.Equal(*done.CompletedAt) {
			t.Fatalf("completion is not idempotent: %v", err)
		}
		tasks, err = s.Tasks(ctx, owner, domain.TaskTodo, 10, 0)
		if err != nil || len(tasks) != 1 || tasks[0].ID != undated.ID {
			t.Fatalf("status filter failed: %v", err)
		}
		if _, err := s.Tasks(ctx, owner, "", 0, 0); err == nil {
			t.Fatal("unbounded list accepted")
		}
	})

	t.Run("TransactionRollsBackTaskReminderAndCursor", func(t *testing.T) {
		owner := newOwner(t)
		abort := errors.New("abort transaction")
		var taskID int64
		err := s.WithinTx(ctx, func(tx *Store) error {
			task, err := tx.CreateTask(ctx, domain.Task{OwnerID: owner, Title: "Rollback"})
			if err != nil {
				return err
			}
			taskID = task.ID
			if _, err := tx.CreateReminder(ctx, domain.Reminder{OwnerID: owner, TaskID: &task.ID, RemindAt: time.Now()}); err != nil {
				return err
			}
			if err := tx.SaveSyncState(ctx, domain.SyncState{OwnerID: owner, Source: "telegram", SourceID: "chat", Cursor: "100"}); err != nil {
				return err
			}
			return abort
		})
		if !errors.Is(err, abort) {
			t.Fatal(err)
		}
		if _, err := s.Task(ctx, owner, taskID); !errors.Is(err, ErrNotFound) {
			t.Fatal("task escaped rollback")
		}
		if _, err := s.SyncState(ctx, owner, "telegram", "chat"); !errors.Is(err, ErrNotFound) {
			t.Fatal("cursor escaped rollback")
		}
		var count int
		if err := s.db.QueryRow(ctx, "SELECT count(*) FROM study_agent.reminders WHERE owner_id=$1", owner).Scan(&count); err != nil || count != 0 {
			t.Fatal("reminder escaped rollback")
		}
	})

	t.Run("SourceUpsertsAndAtomicCommit", func(t *testing.T) {
		owner := newOwner(t)
		now := time.Now().UTC().Truncate(time.Microsecond)
		message := domain.Message{OwnerID: owner, ChatID: -100, MessageID: 1, Text: "Задание", SentAt: now}
		lesson := domain.Lesson{OwnerID: owner, Source: "ical", SourceID: "event/occurrence", Title: "Занятие", StartsAt: now, EndsAt: now.Add(time.Hour)}
		var messageID, lessonID int64
		err := s.WithinTx(ctx, func(tx *Store) error {
			m, err := tx.SaveMessage(ctx, message)
			if err != nil {
				return err
			}
			messageID = m.ID
			l, err := tx.SaveLesson(ctx, lesson)
			if err != nil {
				return err
			}
			lessonID = l.ID
			task, err := tx.CreateTask(ctx, domain.Task{OwnerID: owner, Title: "Из сообщения"})
			if err != nil {
				return err
			}
			if err := tx.LinkTaskMessage(ctx, owner, task.ID, messageID); err != nil {
				return err
			}
			return tx.SaveSyncState(ctx, domain.SyncState{OwnerID: owner, Source: "telegram", SourceID: "chat", Cursor: "1"})
		})
		if err != nil {
			t.Fatal(err)
		}
		message.Text = "Исправленное задание"
		updated, err := s.SaveMessage(ctx, message)
		if err != nil || updated.ID != messageID || updated.Text != message.Text {
			t.Fatalf("message was duplicated: %v", err)
		}
		lesson.Title = "Перенесённое занятие"
		updatedLesson, err := s.SaveLesson(ctx, lesson)
		if err != nil || updatedLesson.ID != lessonID || updatedLesson.Title != lesson.Title {
			t.Fatalf("lesson was duplicated: %v", err)
		}
		state, err := s.SyncState(ctx, owner, "telegram", "chat")
		if err != nil || state.Cursor != "1" {
			t.Fatalf("cursor missing after commit: %v", err)
		}
		if _, err := s.SyncState(ctx, owner+1, "telegram", "chat"); !errors.Is(err, ErrNotFound) {
			t.Fatal("cursor is not isolated by owner")
		}
		foreign, err := s.CreateTask(ctx, domain.Task{OwnerID: newOwner(t), Title: "Чужая задача"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.LinkTaskMessage(ctx, owner, foreign.ID, messageID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cross-owner source link accepted: %v", err)
		}
		lesson.EndsAt = lesson.StartsAt
		if _, err := s.SaveLesson(ctx, lesson); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid lesson interval accepted: %v", err)
		}
	})

	t.Run("ReminderConstraintsAttemptsAndCascade", func(t *testing.T) {
		owner := newOwner(t)
		task, err := s.CreateTask(ctx, domain.Task{OwnerID: owner, Title: "Напомнить"})
		if err != nil {
			t.Fatal(err)
		}
		input := domain.Reminder{OwnerID: owner, TaskID: &task.ID, RemindAt: time.Now().UTC().Truncate(time.Microsecond)}
		r, err := s.CreateReminder(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateReminder(ctx, input); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate reminder accepted: %v", err)
		}
		if _, err := s.Reminder(ctx, owner+1, r.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("another owner can read reminder")
		}
		input.OwnerID = owner + 1
		input.RemindAt = input.RemindAt.Add(time.Minute)
		if _, err := s.CreateReminder(ctx, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cross-owner reminder accepted: %v", err)
		}
		if _, err := s.CreateReminder(ctx, domain.Reminder{OwnerID: owner, RemindAt: time.Now()}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("reminder without target accepted: %v", err)
		}
		lesson, err := s.SaveLesson(ctx, domain.Lesson{OwnerID: owner, Source: "yaml", SourceID: "lesson", Title: "Занятие", StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateReminder(ctx, domain.Reminder{OwnerID: owner, TaskID: &task.ID, LessonID: &lesson.ID, RemindAt: time.Now()}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("reminder with two targets accepted: %v", err)
		}
		if _, err := s.CreateReminder(ctx, domain.Reminder{OwnerID: owner, LessonID: &lesson.ID, RemindAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		r, err = s.RecordReminderAttempt(ctx, owner, r.ID, false)
		if err != nil || r.Attempts != 1 || r.Status != "pending" || r.LastAttemptAt == nil {
			t.Fatalf("failed attempt was not recorded: %v", err)
		}
		r, err = s.RecordReminderAttempt(ctx, owner, r.ID, true)
		if err != nil || r.Attempts != 2 || r.Status != "sent" || r.SentAt == nil {
			t.Fatalf("delivery was not recorded: %v", err)
		}
		if _, err := s.RecordReminderAttempt(ctx, owner, r.ID, true); !errors.Is(err, ErrNotFound) {
			t.Fatal("sent reminder was processed twice")
		}
		if err := s.DeleteTask(ctx, owner, task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Reminder(ctx, owner, r.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("task deletion left an orphan reminder")
		}
	})

	t.Run("CanceledRequestDoesNotBreakPool", func(t *testing.T) {
		owner := newOwner(t)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.CreateTask(canceled, domain.Task{OwnerID: owner, Title: "Canceled"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was lost: %v", err)
		}
		if _, err := s.CreateTask(ctx, domain.Task{OwnerID: owner, Title: "After cancellation"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("FailedMigrationRollsBackAndChecksumsAreChecked", func(t *testing.T) {
		original, err := migrations.ReadFile("migrations/001_initial.sql")
		if err != nil {
			t.Fatal(err)
		}
		altered := fstest.MapFS{"migrations/001_initial.sql": &fstest.MapFile{Data: append(append([]byte{}, original...), []byte("\n-- changed")...)}}
		if err := s.migrate(ctx, altered); err == nil || !strings.Contains(err.Error(), "modified") {
			t.Fatalf("modified migration accepted: %v", err)
		}
		probe := fmt.Sprintf("migration_probe_%d", time.Now().UnixNano())
		broken := fstest.MapFS{
			"migrations/001_initial.sql": &fstest.MapFile{Data: original},
			"migrations/002_broken.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE study_agent." + probe + " (id bigint); SELECT 1/0;")},
		}
		if err := s.migrate(ctx, broken); err == nil {
			t.Fatal("broken migration succeeded")
		}
		var exists bool
		if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname='study_agent' AND tablename=$1)", probe).Scan(&exists); err != nil || exists {
			t.Fatal("broken migration was not rolled back")
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("failed migration damaged migration history: %v", err)
		}
	})
}

func openTestStore(t *testing.T, ctx context.Context, cfg config.Database) *Store {
	t.Helper()
	s, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
