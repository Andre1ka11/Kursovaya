// Package postgres implements persistent repositories using pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"study-agent/internal/config"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record already exists")
	ErrInvalid  = errors.New("invalid record or reference")
)

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct {
	db   querier
	pool *pgxpool.Pool
}

func Open(ctx context.Context, cfg config.Database) (*Store, error) {
	if !cfg.Enabled {
		return nil, errors.New("database is disabled")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Connection fields are assigned directly: passwords need no URL escaping.
	pc, err := pgxpool.ParseConfig("postgres://localhost/postgres?sslmode=disable")
	if err != nil {
		return nil, errors.New("cannot initialize database configuration")
	}
	pc.ConnConfig.Host, pc.ConnConfig.Port = cfg.Host, cfg.Port
	pc.ConnConfig.Database, pc.ConnConfig.User, pc.ConnConfig.Password = cfg.Name, cfg.User, cfg.Password
	pc.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	pc.ConnConfig.Fallbacks = nil
	pc.ConnConfig.RuntimeParams["application_name"] = "study-agent"
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pc.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	pc.MaxConns = cfg.MaxConns
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(connectCtx, pc)
	if err != nil {
		return nil, dbError(err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, dbError(err)
	}
	return &Store{db: pool, pool: pool}, nil
}

// Close is called after workers have stopped. The caller bounds how long it waits.
func (s *Store) Close(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("transaction store does not own a connection pool")
	}
	done := make(chan struct{})
	go func() { s.pool.Close(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WithinTx gives the callback repositories using one transaction. Any failure rolls it back.
func (s *Store) WithinTx(ctx context.Context, fn func(*Store) error) error {
	if s.pool == nil {
		return errors.New("nested transactions are not supported")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError(err)
	}
	defer rollback(tx)
	if err := fn(&Store{db: tx}); err != nil {
		return err
	}
	return dbError(tx.Commit(ctx))
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// Never return server messages or connection strings: they can contain secrets or user data.
func dbError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return ErrConflict
		case "23502", "23503", "23514", "22007", "22008":
			return ErrInvalid
		case "28P01", "28000":
			return errors.New("database authentication failed; check AGENT_DATABASE_PASSWORD and database.user")
		case "3D000":
			return errors.New("database does not exist; check database.name")
		case "42501":
			return errors.New("database permission denied; check database owner and user")
		case "57014":
			return errors.New("database query canceled or timed out")
		}
		return fmt.Errorf("database request failed (SQLSTATE %s)", pe.Code)
	}
	return errors.New("database connection or request failed; check host, port and PostgreSQL availability")
}
