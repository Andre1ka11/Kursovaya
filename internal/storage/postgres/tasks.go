package postgres

import (
	"context"
	"errors"

	"study-agent/internal/domain"
)

const taskColumns = `id, owner_id, title, description, subject, kind, due_at, status, completed_at, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (domain.Task, error) {
	var t domain.Task
	err := row.Scan(&t.ID, &t.OwnerID, &t.Title, &t.Description, &t.Subject, &t.Kind,
		&t.DueAt, &t.Status, &t.CompletedAt, &t.CreatedAt, &t.UpdatedAt)
	return t, dbError(err)
}

func (s *Store) CreateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	return scanTask(s.db.QueryRow(ctx, `INSERT INTO study_agent.tasks
(owner_id,title,description,subject,kind,due_at) VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+taskColumns,
		t.OwnerID, t.Title, t.Description, t.Subject, t.Kind, t.DueAt))
}

func (s *Store) Task(ctx context.Context, ownerID, id int64) (domain.Task, error) {
	return scanTask(s.db.QueryRow(ctx, "SELECT "+taskColumns+" FROM study_agent.tasks WHERE owner_id=$1 AND id=$2", ownerID, id))
}

// Tasks is bounded and ordered by deadline, with undated tasks at the end.
func (s *Store) Tasks(ctx context.Context, ownerID int64, status domain.TaskStatus, limit, offset int) ([]domain.Task, error) {
	return s.FindTasks(ctx, ownerID, domain.TaskFilter{Status: status, Limit: limit, Offset: offset})
}

func (s *Store) FindTasks(ctx context.Context, ownerID int64, f domain.TaskFilter) ([]domain.Task, error) {
	if f.Limit < 1 || f.Limit > 100 || f.Offset < 0 {
		return nil, errors.New("task list requires limit 1..100 and nonnegative offset")
	}
	if f.Status != "" && f.Status != domain.TaskTodo && f.Status != domain.TaskDone {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, "SELECT "+taskColumns+` FROM study_agent.tasks
WHERE owner_id=$1 AND ($2='' OR status=$2)
AND ($3::timestamptz IS NULL OR due_at >= $3)
AND ($4::timestamptz IS NULL OR due_at < $4)
ORDER BY due_at NULLS LAST, id LIMIT $5 OFFSET $6`, ownerID, string(f.Status), f.DueFrom, f.DueBefore, f.Limit, f.Offset)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	tasks := make([]domain.Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, dbError(rows.Err())
}

func (s *Store) CompleteTask(ctx context.Context, ownerID, id int64) (domain.Task, error) {
	return scanTask(s.db.QueryRow(ctx, `WITH completed AS (UPDATE study_agent.tasks
SET status='done', completed_at=COALESCE(completed_at,now()), updated_at=now()
WHERE owner_id=$1 AND id=$2 RETURNING `+taskColumns+`), canceled AS (
UPDATE study_agent.reminders SET status='canceled'
WHERE owner_id=$1 AND task_id IN (SELECT id FROM completed) AND status='pending'
) SELECT `+taskColumns+` FROM completed`, ownerID, id))
}

func (s *Store) DeleteTask(ctx context.Context, ownerID, id int64) error {
	result, err := s.db.Exec(ctx, "DELETE FROM study_agent.tasks WHERE owner_id=$1 AND id=$2", ownerID, id)
	if err != nil {
		return dbError(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
