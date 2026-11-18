package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies embedded SQL atomically. A transaction lock serializes competing starters.
func (s *Store) Migrate(ctx context.Context) error { return s.migrate(ctx, migrations) }

func (s *Store) migrate(ctx context.Context, files fs.FS) error {
	if s.pool == nil {
		return errors.New("migrations require a connection pool")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	paths, err := fs.Glob(files, "migrations/*.sql")
	if err != nil || len(paths) == 0 {
		return errors.New("no database migrations found")
	}
	sort.Strings(paths)
	return s.WithinTx(ctx, func(tx *Store) error {
		if _, err := tx.db.Exec(ctx, "SELECT pg_advisory_xact_lock(726183491)"); err != nil {
			return dbError(err)
		}
		if _, err := tx.db.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS study_agent;
CREATE TABLE IF NOT EXISTS study_agent.schema_migrations (
    version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
			return dbError(err)
		}
		rows, err := tx.db.Query(ctx, "SELECT version, checksum FROM study_agent.schema_migrations")
		if err != nil {
			return dbError(err)
		}
		applied := make(map[string]string)
		for rows.Next() {
			var version, checksum string
			if err := rows.Scan(&version, &checksum); err != nil {
				rows.Close()
				return dbError(err)
			}
			applied[version] = checksum
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return dbError(err)
		}
		for _, path := range paths {
			sql, err := fs.ReadFile(files, path)
			if err != nil {
				return errors.New("cannot read embedded migration")
			}
			checksum := fmt.Sprintf("%x", sha256.Sum256(sql))
			if old, ok := applied[path]; ok {
				if old != checksum {
					return errors.New("applied database migration was modified")
				}
				delete(applied, path)
			}
		}
		if len(applied) > 0 {
			return errors.New("database contains migrations unknown to this application")
		}
		for _, path := range paths {
			var exists bool
			if err := tx.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM study_agent.schema_migrations WHERE version=$1)", path).Scan(&exists); err != nil {
				return dbError(err)
			}
			if exists {
				continue
			}
			sql, _ := fs.ReadFile(files, path)
			if _, err := tx.db.Exec(ctx, string(sql)); err != nil {
				return dbError(err)
			}
			checksum := fmt.Sprintf("%x", sha256.Sum256(sql))
			if _, err := tx.db.Exec(ctx, "INSERT INTO study_agent.schema_migrations (version, checksum) VALUES ($1,$2)", path, checksum); err != nil {
				return dbError(err)
			}
		}
		return nil
	})
}
