package transform_test

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/canonical"
	"github.com/varad/jobstream/services/intake/internal/transform"
)

// ── For ──────────────────────────────────────────────────────────────────────

func TestFor_AllKnownSources(t *testing.T) {
	cases := []intakev1.Source{
		intakev1.Source_SOURCE_DASHBOARD,
		intakev1.Source_SOURCE_CHROME_EXT,
		intakev1.Source_SOURCE_SCRAPER,
	}
	for _, src := range cases {
		t.Run(src.String(), func(t *testing.T) {
			fn, err := transform.For(src)
			if err != nil {
				t.Fatalf("For(%v): unexpected error: %v", src, err)
			}
			if fn == nil {
				t.Fatal("transformer must not be nil")
			}
			ev := fn(&intakev1.SubmitApplicationRequest{
				UserId: "u", JobTitle: "t", Company: "c", Url: "https://x.example", Source: src,
			})
			if ev.Source != int32(src) {
				t.Errorf("Source: want %d, got %d", src, ev.Source)
			}
			if ev.SchemaVersion != canonical.SchemaVersion {
				t.Errorf("SchemaVersion: want %s, got %s", canonical.SchemaVersion, ev.SchemaVersion)
			}
		})
	}
}

func TestFor_UnsupportedSource(t *testing.T) {
	if _, err := transform.For(intakev1.Source_SOURCE_UNSPECIFIED); err == nil {
		t.Fatal("expected error for SOURCE_UNSPECIFIED, got nil")
	}
	if _, err := transform.For(intakev1.Source(99)); err == nil {
		t.Fatal("expected error for unknown source enum, got nil")
	}
}

// ── Default applied_at ───────────────────────────────────────────────────────

func TestTransformers_DefaultAppliedAtToNow(t *testing.T) {
	transformers := map[string]transform.Transformer{
		"dashboard":  transform.FromDashboard,
		"chrome_ext": transform.FromChromeExt,
		"scraper":    transform.FromScraper,
	}
	for name, fn := range transformers {
		t.Run(name, func(t *testing.T) {
			before := time.Now().UTC()
			ev := fn(&intakev1.SubmitApplicationRequest{
				UserId: "u", JobTitle: "t", Company: "c", Url: "https://x.example",
			})
			after := time.Now().UTC()
			if ev.AppliedAt.Before(before) || ev.AppliedAt.After(after) {
				t.Errorf("AppliedAt %v not in [%v, %v]", ev.AppliedAt, before, after)
			}
		})
	}
}

func TestTransformers_PropagateAppliedAt(t *testing.T) {
	want := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, fn := range []transform.Transformer{
		transform.FromDashboard, transform.FromChromeExt, transform.FromScraper,
	} {
		ev := fn(&intakev1.SubmitApplicationRequest{
			UserId: "u", JobTitle: "t", Company: "c", Url: "https://x.example",
			AppliedAt: timestamppb.New(want),
		})
		if !ev.AppliedAt.Equal(want) {
			t.Errorf("AppliedAt: want %v, got %v", want, ev.AppliedAt)
		}
	}
}

// ── FromDashboard ────────────────────────────────────────────────────────────

func TestFromDashboard_TrimsWhitespace(t *testing.T) {
	ev := transform.FromDashboard(&intakev1.SubmitApplicationRequest{
		UserId: " user-1 ", JobTitle: "  Software Engineer ",
		Company: "Acme  ", Url: "  https://acme.example/jobs/1  ",
	})
	if ev.UserID != "user-1" {
		t.Errorf("UserID: want user-1, got %q", ev.UserID)
	}
	if ev.JobTitle != "Software Engineer" {
		t.Errorf("JobTitle: want %q, got %q", "Software Engineer", ev.JobTitle)
	}
	if ev.Company != "Acme" {
		t.Errorf("Company: want Acme, got %q", ev.Company)
	}
	if ev.URL != "https://acme.example/jobs/1" {
		t.Errorf("URL: got %q", ev.URL)
	}
}

// ── FromChromeExt ────────────────────────────────────────────────────────────

func TestFromChromeExt_NormalizesScrapedTitles(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"strips LinkedIn suffix", "Senior SWE | LinkedIn", "Senior SWE"},
		{"strips Indeed suffix (case-insensitive)", "Backend Engineer - INDEED.COM", "Backend Engineer"},
		{"collapses NBSP and ZWSP", "Senior SWE​", "Senior SWE"},
		{"collapses double spaces", "Senior   Backend  Engineer", "Senior Backend Engineer"},
		{"keeps inner 'at' untouched", "Engineer at Acme", "Engineer at Acme"},
		{"untouched if no suffix", "Software Engineer", "Software Engineer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := transform.FromChromeExt(&intakev1.SubmitApplicationRequest{
				UserId: "u", JobTitle: tc.in, Company: "Acme", Url: "https://x.example",
			})
			if ev.JobTitle != tc.want {
				t.Errorf("JobTitle: want %q, got %q", tc.want, ev.JobTitle)
			}
		})
	}
}

func TestFromChromeExt_StripsTrackingParams(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "linkedin trk param",
			in:   "https://linkedin.com/jobs/view/123?trk=public_jobs",
			want: "https://linkedin.com/jobs/view/123",
		},
		{
			name: "utm bundle",
			in:   "https://acme.example/jobs/1?utm_source=x&utm_medium=y&utm_campaign=z",
			want: "https://acme.example/jobs/1",
		},
		{
			name: "preserves unrelated params",
			in:   "https://acme.example/jobs/1?keep=this&utm_source=drop",
			want: "https://acme.example/jobs/1?keep=this",
		},
		{
			name: "no params untouched",
			in:   "https://acme.example/jobs/1",
			want: "https://acme.example/jobs/1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := transform.FromChromeExt(&intakev1.SubmitApplicationRequest{
				UserId: "u", JobTitle: "t", Company: "c", Url: tc.in,
			})
			if ev.URL != tc.want {
				t.Errorf("URL: want %q, got %q", tc.want, ev.URL)
			}
		})
	}
}

// ── FromScraper ──────────────────────────────────────────────────────────────

func TestFromScraper_CollapsesWhitespace(t *testing.T) {
	ev := transform.FromScraper(&intakev1.SubmitApplicationRequest{
		UserId:   "user-1",
		JobTitle: "  Senior\tSoftware\nEngineer  ",
		Company:  "Acme  Corp ",
		Url:      "https://acme.example/jobs/9",
	})
	if ev.JobTitle != "Senior Software Engineer" {
		t.Errorf("JobTitle: got %q", ev.JobTitle)
	}
	if ev.Company != "Acme Corp" {
		t.Errorf("Company: got %q", ev.Company)
	}
}

func TestFromScraper_DoesNotTouchURL(t *testing.T) {
	// Scraper output is trusted to have already canonicalised URLs; we don't
	// strip params here (different blast radius than the chrome ext).
	in := "https://acme.example/jobs/9?utm_source=x"
	ev := transform.FromScraper(&intakev1.SubmitApplicationRequest{
		UserId: "u", JobTitle: "t", Company: "c", Url: in,
	})
	if ev.URL != in {
		t.Errorf("URL must be passed through unchanged, got %q", ev.URL)
	}
}

// ── Validation interplay (smoke) ─────────────────────────────────────────────

// Each transformer's output should satisfy canonical.Validate when the input
// is well-formed. This guards against a transformer that forgets to stamp
// the source or schema version.
func TestTransformers_OutputValidates(t *testing.T) {
	in := &intakev1.SubmitApplicationRequest{
		UserId: "u", JobTitle: "t", Company: "c", Url: "https://x.example",
	}
	for _, fn := range []transform.Transformer{
		transform.FromDashboard, transform.FromChromeExt, transform.FromScraper,
	} {
		ev := fn(in)
		if err := ev.Validate(); err != nil {
			t.Errorf("transformer output failed validation: %v", err)
		}
	}
}

// Confirm that a transformer-cleaned title doesn't accidentally drop the
// whole string. Regression guard for an overly greedy suffix matcher.
func TestFromChromeExt_DoesNotEatLegitimateTitle(t *testing.T) {
	const title = "Lead Engineer, Trust & Safety"
	ev := transform.FromChromeExt(&intakev1.SubmitApplicationRequest{
		UserId: "u", JobTitle: title, Company: "c", Url: "https://x.example",
	})
	if !strings.Contains(ev.JobTitle, "Lead Engineer") {
		t.Errorf("unexpectedly mangled title: %q", ev.JobTitle)
	}
}
