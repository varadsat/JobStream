// Package canonical defines the one true in-memory shape every source must
// produce before persistence. Source-specific quirks live in internal/transform;
// downstream code (dedup, repo, future Kafka publisher) only ever sees this
// canonical form.
package canonical

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// SchemaVersion is the version stamped on every event we accept under the v1
// proto contract. Bumping this is the trigger for the schema-evolution drill
// described in PLAN.md Phase 10.
const SchemaVersion = "1.0"

// ApplicationEvent is the canonical shape for a submitted job application.
// Field order mirrors the proto Application message so reading the two
// side-by-side stays easy.
type ApplicationEvent struct {
	UserID        string
	JobTitle      string
	Company       string
	URL           string
	Source        int32 // jobstreamv1.Source enum value
	AppliedAt     time.Time
	SchemaVersion string
}

// Validation errors. Exported so callers (server, tests) can match on them
// rather than string-comparing messages.
var (
	ErrUserIDRequired   = errors.New("user_id is required")
	ErrJobTitleRequired = errors.New("job_title is required")
	ErrCompanyRequired  = errors.New("company is required")
	ErrURLRequired      = errors.New("url is required")
	ErrInvalidURL       = errors.New("url must be an absolute http(s) URL")
	ErrSourceRequired   = errors.New("source must be specified")
	ErrSchemaRequired   = errors.New("schema_version is required")
)

// Validate enforces every cross-source rule in one place. Transformers may
// trim/normalise fields, but the *rules* (URL must parse, title non-empty,
// applied_at not zero, etc.) live here so a future source cannot accidentally
// loosen them.
func (e ApplicationEvent) Validate() error {
	if e.UserID == "" {
		return ErrUserIDRequired
	}
	if e.JobTitle == "" {
		return ErrJobTitleRequired
	}
	if e.Company == "" {
		return ErrCompanyRequired
	}
	if e.URL == "" {
		return ErrURLRequired
	}
	if !isHTTPURL(e.URL) {
		return ErrInvalidURL
	}
	if e.Source == 0 {
		return ErrSourceRequired
	}
	if e.SchemaVersion == "" {
		return ErrSchemaRequired
	}
	return nil
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	return u.Host != ""
}
