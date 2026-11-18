package postgres

import (
	"context"
	"study-agent/internal/domain"
)

func (s *Store) SaveMessage(ctx context.Context, m domain.Message) (domain.Message, error) {
	err := s.db.QueryRow(ctx, `INSERT INTO study_agent.messages (owner_id,chat_id,message_id,text,sent_at)
VALUES ($1,$2,$3,$4,$5) ON CONFLICT (owner_id,chat_id,message_id)
DO UPDATE SET text=EXCLUDED.text, sent_at=EXCLUDED.sent_at
RETURNING id,owner_id,chat_id,message_id,text,sent_at`, m.OwnerID, m.ChatID, m.MessageID, m.Text, m.SentAt).
		Scan(&m.ID, &m.OwnerID, &m.ChatID, &m.MessageID, &m.Text, &m.SentAt)
	return m, dbError(err)
}

func (s *Store) LinkTaskMessage(ctx context.Context, ownerID, taskID, messageID int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO study_agent.task_sources (owner_id,task_id,message_id)
VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, ownerID, taskID, messageID)
	return dbError(err)
}

func (s *Store) SaveLesson(ctx context.Context, l domain.Lesson) (domain.Lesson, error) {
	err := s.db.QueryRow(ctx, `INSERT INTO study_agent.lessons
(owner_id,source,source_id,title,location,starts_at,ends_at) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (owner_id,source,source_id) DO UPDATE SET
title=EXCLUDED.title, location=EXCLUDED.location, starts_at=EXCLUDED.starts_at, ends_at=EXCLUDED.ends_at
RETURNING id,owner_id,source,source_id,title,location,starts_at,ends_at`,
		l.OwnerID, l.Source, l.SourceID, l.Title, l.Location, l.StartsAt, l.EndsAt).
		Scan(&l.ID, &l.OwnerID, &l.Source, &l.SourceID, &l.Title, &l.Location, &l.StartsAt, &l.EndsAt)
	return l, dbError(err)
}

func (s *Store) SaveSyncState(ctx context.Context, state domain.SyncState) error {
	_, err := s.db.Exec(ctx, `INSERT INTO study_agent.sync_state (owner_id,source,source_id,cursor)
VALUES ($1,$2,$3,$4) ON CONFLICT (owner_id,source,source_id)
DO UPDATE SET cursor=EXCLUDED.cursor, synced_at=now()`, state.OwnerID, state.Source, state.SourceID, state.Cursor)
	return dbError(err)
}

func (s *Store) SyncState(ctx context.Context, ownerID int64, source, sourceID string) (domain.SyncState, error) {
	var state domain.SyncState
	err := s.db.QueryRow(ctx, `SELECT owner_id,source,source_id,cursor,synced_at FROM study_agent.sync_state
WHERE owner_id=$1 AND source=$2 AND source_id=$3`, ownerID, source, sourceID).
		Scan(&state.OwnerID, &state.Source, &state.SourceID, &state.Cursor, &state.SyncedAt)
	return state, dbError(err)
}
