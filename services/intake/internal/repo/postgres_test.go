package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/varad/jobstream/services/intake/internal/repo"
)

// newTestDB spins up a real Postgres container, runs migrations, and returns
// a connected PostgresRepo. The container is terminated when the test ends.
func newTestDB(t *testing.T) *repo.PostgresRepo {
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

	return r
}

func TestPostgresRepo_Insert(t *testing.T) {
	r := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	got, err := r.Insert(ctx, repo.InsertParams{
		UserID:        "user-1",
		JobTitle:      "Software Engineer",
		Company:       "Acme Corp",
		URL:           "https://acme.example/jobs/1",
		Source:        1, // SOURCE_DASHBOARD
		Status:        1, // STATUS_APPLIED
		AppliedAt:     now,
		SchemaVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
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
	r := newTestDB(t)
	ctx := context.Background()

	inserted, err := r.Insert(ctx, repo.InsertParams{
		UserID:        "user-2",
		JobTitle:      "Backend Engineer",
		Company:       "Globex",
		URL:           "https://globex.example/jobs/42",
		Source:        3, // SOURCE_SCRAPER
		Status:        1, // STATUS_APPLIED
		AppliedAt:     time.Now().UTC(),
		SchemaVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
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
	r := newTestDB(t)
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
	r := newTestDB(t)
	ctx := context.Background()

	inserted, err := r.Insert(ctx, repo.InsertParams{
		UserID:        "user-3",
		JobTitle:      "DevOps Engineer",
		Company:       "Initech",
		URL:           "https://initech.example/jobs/7",
		Source:        2,
		Status:        1,
		AppliedAt:     time.Now().UTC(),
		SchemaVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Correct ID but wrong user — must be treated as not found (ownership check)
	_, err = r.GetByIDAndUserID(ctx, inserted.ID, "attacker-user")
	if err != repo.ErrNotFound {
		t.Errorf("want ErrNotFound for wrong user, got %v", err)
	}
}
