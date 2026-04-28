package repo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/varad/jobstream/services/intake/internal/repo"
)

// newTestDB spins up a real Postgres container, runs migrations, and returns
// both a connected PostgresRepo and the DSN — tests that need to peek at
// raw rows (e.g. counting outbox entries) can open a side connection with
// the same DSN without exposing repo internals.
func newTestDB(t *testing.T) (*repo.PostgresRepo, string) {
	t.Helper()
	ctx := context.Background()

	ctr, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("testdb"),
		pgmodule.WithUsername("test"),
		pgmodule.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	if err := repo.RunMigrations(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	r, err := repo.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(r.Close)

	return r, dsn
}

// constPayload returns a payload builder that always emits the same bytes;
// handy for tests that don't care about the actual payload content.
func constPayload(b []byte) repo.PayloadBuilder {
	return func(_ repo.ApplicationRecord) ([]byte, error) { return b, nil }
}

// countRows runs a single COUNT(*) against the given table using a fresh pool.
func countRows(t *testing.T, dsn, table string) int {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestPostgresRepo_Insert(t *testing.T) {
	r, _ := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	got, err := r.InsertWithOutbox(ctx, repo.InsertParams{
		UserID:        "user-1",
		JobTitle:      "Software Engineer",
		Company:       "Acme Corp",
		URL:           "https://acme.example/jobs/1",
		Source:        1, // SOURCE_DASHBOARD
		Status:        1, // STATUS_APPLIED
		AppliedAt:     now,
		SchemaVersion: "1.0",
	}, "jobs.submitted", constPayload([]byte("payload-bytes")))
	if err != nil {
		t.Fatalf("InsertWithOutbox: %v", err)
	}

	if got.ID == "" {
		t.Error("ID should be non-empty")
	}
	if got.UserID != "user-1" {
		t.Errorf("UserID: want user-1, got %s", got.UserID)
	}
	if got.JobTitle != "Software Engineer" {
		t.Errorf("JobTitle: want Software Engineer, got %s", got.JobTitle)
	}
	if got.Company != "Acme Corp" {
		t.Errorf("Company: want Acme Corp, got %s", got.Company)
	}
	if got.SchemaVersion != "1.0" {
		t.Errorf("SchemaVersion: want 1.0, got %s", got.SchemaVersion)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set by the DB")
	}
}

func TestPostgresRepo_GetByIDAndUserID_Found(t *testing.T) {
	r, _ := newTestDB(t)
	ctx := context.Background()

	inserted, err := r.InsertWithOutbox(ctx, repo.InsertParams{
		UserID:        "user-2",
		JobTitle:      "Backend Engineer",
		Company:       "Globex",
		URL:           "https://globex.example/jobs/42",
		Source:        3, // SOURCE_SCRAPER
		Status:        1, // STATUS_APPLIED
		AppliedAt:     time.Now().UTC(),
		SchemaVersion: "1.0",
	}, "jobs.submitted", constPayload([]byte("p")))
	if err != nil {
		t.Fatalf("InsertWithOutbox: %v", err)
	}

	got, err := r.GetByIDAndUserID(ctx, inserted.ID, "user-2")
	if err != nil {
		t.Fatalf("GetByIDAndUserID: %v", err)
	}
	if got.ID != inserted.ID {
		t.Errorf("ID: want %s, got %s", inserted.ID, got.ID)
	}
	if got.Company != "Globex" {
		t.Errorf("Company: want Globex, got %s", got.Company)
	}
}

func TestPostgresRepo_GetByIDAndUserID_NotFound(t *testing.T) {
	r, _ := newTestDB(t)
	ctx := context.Background()

	// Valid UUID that doesn't exist in the DB
	_, err := r.GetByIDAndUserID(ctx, "00000000-0000-0000-0000-000000000000", "user-x")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if err != repo.ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestPostgresRepo_GetByIDAndUserID_WrongUser(t *testing.T) {
	r, _ := newTestDB(t)
	ctx := context.Background()

	inserted, err := r.InsertWithOutbox(ctx, repo.InsertParams{
		UserID:        "user-3",
		JobTitle:      "DevOps Engineer",
		Company:       "Initech",
		URL:           "https://initech.example/jobs/7",
		Source:        2,
		Status:        1,
		AppliedAt:     time.Now().UTC(),
		SchemaVersion: "1.0",
	}, "jobs.submitted", constPayload([]byte("p")))
	if err != nil {
		t.Fatalf("InsertWithOutbox: %v", err)
	}

	// Correct ID but wrong user — must be treated as not found (ownership check)
	_, err = r.GetByIDAndUserID(ctx, inserted.ID, "attacker-user")
	if err != repo.ErrNotFound {
		t.Errorf("want ErrNotFound for wrong user, got %v", err)
	}
}

// ── Outbox dual-write ─────────────────────────────────────────────────────────

// TestPostgresRepo_InsertWithOutbox_DualWrite is the headline guarantee:
// a successful call commits exactly one application row AND one outbox row,
// with the outbox row pointing at the application by aggregate_id and
// carrying the bytes the payload builder produced.
func TestPostgresRepo_InsertWithOutbox_DualWrite(t *testing.T) {
	r, dsn := newTestDB(t)
	ctx := context.Background()

	wantPayload := []byte{0x01, 0x02, 0x03, 0xff}
	rec, err := r.InsertWithOutbox(ctx, repo.InsertParams{
		UserID:        "user-outbox",
		JobTitle:      "SWE",
		Company:       "Outbox Co",
		URL:           "https://outbox.example/jobs/1",
		Source:        1,
		Status:        1,
		AppliedAt:     time.Now().UTC(),
		SchemaVersion: "1.0",
	}, "jobs.submitted", constPayload(wantPayload))
	if err != nil {
		t.Fatalf("InsertWithOutbox: %v", err)
	}

	if got := countRows(t, dsn, "applications"); got != 1 {
		t.Errorf("applications rows: want 1, got %d", got)
	}
	if got := countRows(t, dsn, "outbox"); got != 1 {
		t.Errorf("outbox rows: want 1, got %d", got)
	}

	// Outbox row contents — aggregate_id must match the application id, and
	// payload bytes must round-trip exactly (BYTEA is opaque).
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	var aggregateID, topic string
	var payload []byte
	var publishedAt *time.Time
	row := pool.QueryRow(ctx, `SELECT aggregate_id::text, topic, payload, published_at FROM outbox LIMIT 1`)
	if err := row.Scan(&aggregateID, &topic, &payload, &publishedAt); err != nil {
		t.Fatalf("scan outbox: %v", err)
	}
	if aggregateID != rec.ID {
		t.Errorf("aggregate_id: want %s, got %s", rec.ID, aggregateID)
	}
	if topic != "jobs.submitted" {
		t.Errorf("topic: want jobs.submitted, got %s", topic)
	}
	if string(payload) != string(wantPayload) {
		t.Errorf("payload: want %v, got %v", wantPayload, payload)
	}
	if publishedAt != nil {
		t.Errorf("published_at: must be NULL on insert, got %v", *publishedAt)
	}
}

// TestPostgresRepo_InsertWithOutbox_RollsBackOnPayloadError simulates the
// "kill the transaction mid-flight" scenario from the plan: the application
// row was already inserted inside the tx when the payload builder fails.
// Both tables must be empty after the call returns.
func TestPostgresRepo_InsertWithOutbox_RollsBackOnPayloadError(t *testing.T) {
	r, dsn := newTestDB(t)
	ctx := context.Background()

	boom := errors.New("payload boom")
	failBuilder := func(_ repo.ApplicationRecord) ([]byte, error) { return nil, boom }

	_, err := r.InsertWithOutbox(ctx, repo.InsertParams{
		UserID:        "user-rollback",
		JobTitle:      "SWE",
		Company:       "Rollback Co",
		URL:           "https://rollback.example/jobs/1",
		Source:        1,
		Status:        1,
		AppliedAt:     time.Now().UTC(),
		SchemaVersion: "1.0",
	}, "jobs.submitted", failBuilder)
	if err == nil {
		t.Fatal("expected error from payload builder, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error chain should wrap boom, got %v", err)
	}

	// The transaction was rolled back: neither row may exist.
	if got := countRows(t, dsn, "applications"); got != 0 {
		t.Errorf("applications rows after rollback: want 0, got %d", got)
	}
	if got := countRows(t, dsn, "outbox"); got != 0 {
		t.Errorf("outbox rows after rollback: want 0, got %d", got)
	}
}
