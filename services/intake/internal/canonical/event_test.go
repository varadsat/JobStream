package canonical_test

import (
	"errors"
	"testing"
	"time"

	"github.com/varad/jobstream/services/intake/internal/canonical"
)

func validEvent() canonical.ApplicationEvent {
	return canonical.ApplicationEvent{
		UserID:        "user-1",
		JobTitle:      "Software Engineer",
		Company:       "Acme",
		URL:           "https://acme.example/jobs/1",
		Source:        1,
		AppliedAt:     time.Now().UTC(),
		SchemaVersion: canonical.SchemaVersion,
	}
}

func TestApplicationEvent_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*canonical.ApplicationEvent)
		wantErr error
	}{
		{
			name:    "happy path",
			mutate:  func(e *canonical.ApplicationEvent) {},
			wantErr: nil,
		},
		{
			name:    "missing user_id",
			mutate:  func(e *canonical.ApplicationEvent) { e.UserID = "" },
			wantErr: canonical.ErrUserIDRequired,
		},
		{
			name:    "missing job_title",
			mutate:  func(e *canonical.ApplicationEvent) { e.JobTitle = "" },
			wantErr: canonical.ErrJobTitleRequired,
		},
		{
			name:    "missing company",
			mutate:  func(e *canonical.ApplicationEvent) { e.Company = "" },
			wantErr: canonical.ErrCompanyRequired,
		},
		{
			name:    "missing url",
			mutate:  func(e *canonical.ApplicationEvent) { e.URL = "" },
			wantErr: canonical.ErrURLRequired,
		},
		{
			name:    "url without scheme",
			mutate:  func(e *canonical.ApplicationEvent) { e.URL = "acme.example/jobs/1" },
			wantErr: canonical.ErrInvalidURL,
		},
		{
			name:    "url with non-http scheme",
			mutate:  func(e *canonical.ApplicationEvent) { e.URL = "ftp://acme.example/jobs/1" },
			wantErr: canonical.ErrInvalidURL,
		},
		{
			name:    "url javascript injection",
			mutate:  func(e *canonical.ApplicationEvent) { e.URL = "javascript:alert(1)" },
			wantErr: canonical.ErrInvalidURL,
		},
		{
			name:    "url missing host",
			mutate:  func(e *canonical.ApplicationEvent) { e.URL = "https:///path" },
			wantErr: canonical.ErrInvalidURL,
		},
		{
			name:    "source unspecified",
			mutate:  func(e *canonical.ApplicationEvent) { e.Source = 0 },
			wantErr: canonical.ErrSourceRequired,
		},
		{
			name:    "schema version empty",
			mutate:  func(e *canonical.ApplicationEvent) { e.SchemaVersion = "" },
			wantErr: canonical.ErrSchemaRequired,
		},
		{
			name:    "url with uppercase scheme accepted",
			mutate:  func(e *canonical.ApplicationEvent) { e.URL = "HTTPS://acme.example/jobs/1" },
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := validEvent()
			tc.mutate(&e)
			err := e.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(): want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestSchemaVersion_Constant(t *testing.T) {
	if canonical.SchemaVersion != "1.0" {
		t.Errorf("SchemaVersion: want 1.0, got %s", canonical.SchemaVersion)
	}
}
