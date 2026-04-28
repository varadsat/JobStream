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

// PayloadBuilder constructs the outbox payload bytes for a freshly-inserted
// application. It runs inside the same transaction as the application insert,
// so it can include DB-assigned fields (id, created_at) and any error it
// returns rolls back the entire dual-write.
type PayloadBuilder func(rec ApplicationRecord) ([]byte, error)

// ApplicationRepo is the persistence contract for the intake service.
// Implementations must be safe for concurrent use.
type ApplicationRepo interface {
	// InsertWithOutbox persists a new application and an outbox row pointing
	// at it, atomically. The outbox row's aggregate_id is set from the
	// inserted application's ID; the payload comes from build(rec). Either
	// both rows are committed or neither is — there is no in-between state
	// where the application exists but the event was never queued.
	InsertWithOutbox(ctx context.Context, p InsertParams, topic string, build PayloadBuilder) (ApplicationRecord, error)

	// GetByIDAndUserID fetches one application, enforcing ownership.
	// Returns ErrNotFound when no row matches both id and user_id.
	GetByIDAndUserID(ctx context.Context, id, userID string) (ApplicationRecord, error)
}
