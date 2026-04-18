package repo

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested application does not exist for the given user.
var ErrNotFound = errors.New("application not found")

// ApplicationRecord is the domain model returned by the repo layer.
// It deliberately avoids proto types so the repo stays independently testable.
type ApplicationRecord struct {
	ID            string
	UserID        string
	JobTitle      string
	Company       string
	URL           string
	Source        int32 // maps to jobstreamv1.Source enum values
	Status        int32 // maps to jobstreamv1.Status enum values
	AppliedAt     time.Time
	CreatedAt     time.Time
	SchemaVersion string
}

// InsertParams carries everything the repo needs to persist a new application.
type InsertParams struct {
	UserID        string
	JobTitle      string
	Company       string
	URL           string
	Source        int32
	Status        int32
	AppliedAt     time.Time
	SchemaVersion string
}

// ApplicationRepo is the persistence contract for the intake service.
// Implementations must be safe for concurrent use.
type ApplicationRepo interface {
	// Insert persists a new application and returns the saved record (with DB-generated ID).
	Insert(ctx context.Context, p InsertParams) (ApplicationRecord, error)

	// GetByIDAndUserID fetches one application, enforcing ownership.
	// Returns ErrNotFound when no row matches both id and user_id.
	GetByIDAndUserID(ctx context.Context, id, userID string) (ApplicationRecord, error)
}
