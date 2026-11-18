package postgres

import (
	"context"
	"study-agent/internal/domain"
)

const reminderColumns = `id,owner_id,task_id,lesson_id,remind_at,status,attempts,last_attempt_at,sent_at`

func scanReminder(row scanner) (domain.Reminder, error) {
	var r domain.Reminder
	err := row.Scan(&r.ID, &r.OwnerID, &r.TaskID, &r.LessonID, &r.RemindAt, &r.Status, &r.Attempts, &r.LastAttemptAt, &r.SentAt)
	return r, dbError(err)
}

func (s *Store) CreateReminder(ctx context.Context, r domain.Reminder) (domain.Reminder, error) {
	return scanReminder(s.db.QueryRow(ctx, `INSERT INTO study_agent.reminders (owner_id,task_id,lesson_id,remind_at)
VALUES ($1,$2,$3,$4) RETURNING `+reminderColumns, r.OwnerID, r.TaskID, r.LessonID, r.RemindAt))
}

func (s *Store) Reminder(ctx context.Context, ownerID, id int64) (domain.Reminder, error) {
	return scanReminder(s.db.QueryRow(ctx, "SELECT "+reminderColumns+" FROM study_agent.reminders WHERE owner_id=$1 AND id=$2", ownerID, id))
}

// RecordReminderAttempt stores delivery progress; the scheduler will use this in stage 5.
func (s *Store) RecordReminderAttempt(ctx context.Context, ownerID, id int64, sent bool) (domain.Reminder, error) {
	return scanReminder(s.db.QueryRow(ctx, `UPDATE study_agent.reminders
SET attempts=attempts+1, last_attempt_at=now(), status=CASE WHEN $3 THEN 'sent' ELSE 'pending' END,
sent_at=CASE WHEN $3 THEN now() ELSE NULL END
WHERE owner_id=$1 AND id=$2 AND status='pending' RETURNING `+reminderColumns, ownerID, id, sent))
}
