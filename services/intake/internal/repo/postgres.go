package repo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/varad/jobstream/services/intake/internal/db"
	"github.com/varad/jobstream/services/intake/internal/migrations"
)

// PostgresRepo is the live Postgres implementation of ApplicationRepo.
type PostgresRepo struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// compile-time interface check
var _ ApplicationRepo = (*PostgresRepo)(nil)

// NewPostgres opens a connection pool to Postgres and verifies connectivity.
func NewPostgres(ctx context.Context, dsn string) (*PostgresRepo, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresRepo{
		pool:    pool,
		queries: db.New(pool),
	}, nil
}

// Close releases all pool connections. Call on service shutdown.
func (r *PostgresRepo) Close() {
	r.pool.Close()
}

// RunMigrations applies all pending up-migrations using the embedded SQL files.
// It is idempotent: calling it when migrations are already applied is a no-op.
func RunMigrations(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("migrate.New: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

func (r *PostgresRepo) Insert(ctx context.Context, p InsertParams) (ApplicationRecord, error) {
	row, err := r.queries.InsertApplication(ctx, db.InsertApplicationParams{
		UserID:        p.UserID,
		JobTitle:      p.JobTitle,
		Company:       p.Company,
		Url:           p.URL,
		Source:        int16(p.Source),
		Status:        int16(p.Status),
		AppliedAt:     pgtype.Timestamptz{Time: p.AppliedAt, Valid: true},
		SchemaVersion: p.SchemaVersion,
	})
	if err != nil {
		return ApplicationRecord{}, fmt.Errorf("insert application: %w", err)
	}
	return fromRow(row), nil
}

func (r *PostgresRepo) GetByIDAndUserID(ctx context.Context, id, userID string) (ApplicationRecord, error) {
	var uid pgtype.UUID
	if err := uid.Scan(id); err != nil {
		return ApplicationRecord{}, fmt.Errorf("invalid uuid %q: %w", id, err)
	}
	row, err := r.queries.GetApplicationByIDAndUserID(ctx, db.GetApplicationByIDAndUserIDParams{
		ID:     uid,
		UserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ApplicationRecord{}, ErrNotFound
	}
	if err != nil {
		return ApplicationRecord{}, fmt.Errorf("get application: %w", err)
	}
	return fromRow(row), nil
}

// fromRow converts a sqlc Application row to the domain ApplicationRecord.
func fromRow(r db.Application) ApplicationRecord {
	return ApplicationRecord{
		ID:            uuidToString(r.ID.Bytes),
		UserID:        r.UserID,
		JobTitle:      r.JobTitle,
		Company:       r.Company,
		URL:           r.Url,
		Source:        int32(r.Source),
		Status:        int32(r.Status),
		AppliedAt:     r.AppliedAt.Time,
		CreatedAt:     r.CreatedAt.Time,
		SchemaVersion: r.SchemaVersion,
	}
}

// uuidToString formats a raw [16]byte UUID as the canonical hyphenated string.
func uuidToString(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
