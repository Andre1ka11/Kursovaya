// Package domain contains data shared by services and storage adapters.
package domain

import "time"

type TaskStatus string

const (
	TaskTodo TaskStatus = "todo"
	TaskDone TaskStatus = "done"
)

type Task struct {
	ID, OwnerID                       int64
	Title, Description, Subject, Kind string
	DueAt                             *time.Time
	Status                            TaskStatus
	CompletedAt                       *time.Time
	CreatedAt, UpdatedAt              time.Time
}

type TaskFilter struct {
	Status             TaskStatus
	DueFrom, DueBefore *time.Time
	Limit, Offset      int
}

type Lesson struct {
	ID, OwnerID                       int64
	Source, SourceID, Title, Location string
	StartsAt, EndsAt                  time.Time
}

type Message struct {
	ID, OwnerID, ChatID, MessageID int64
	Text                           string
	SentAt                         time.Time
}

type Reminder struct {
	ID, OwnerID           int64
	TaskID, LessonID      *int64
	RemindAt              time.Time
	Status                string
	Attempts              int
	LastAttemptAt, SentAt *time.Time
}

type SyncState struct {
	OwnerID                  int64
	Source, SourceID, Cursor string
	SyncedAt                 time.Time
}
