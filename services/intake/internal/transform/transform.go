// Package transform converts source-specific SubmitApplicationRequest payloads
// into the canonical ApplicationEvent. Each source has its own quirks
// (whitespace, tracking parameters, scraped boilerplate); centralising the
// cleanup here keeps the server handler small and source-agnostic.
package transform

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/varad/jobstream/services/intake/internal/canonical"
)

// ErrUnsupportedSource is returned by For when the proto Source enum is
// unspecified or unknown to this version of the service. It is exported so
// the server can map it to gRPC InvalidArgument with errors.Is.
var ErrUnsupportedSource = errors.New("unsupported source")

// Transformer converts a wire-format SubmitApplicationRequest into the
// canonical event for one specific source. It does not validate; that is
// canonical.ApplicationEvent.Validate's job, and is run by the server after
// transformation so all rules sit in one place.
type Transformer func(*intakev1.SubmitApplicationRequest) canonical.ApplicationEvent

// For dispatches on the proto Source enum. Unknown / unspecified sources
// surface as ErrUnsupportedSource so the caller can return InvalidArgument
// rather than silently writing a row tagged with an unknown origin.
func For(source intakev1.Source) (Transformer, error) {
	switch source {
	case intakev1.Source_SOURCE_DASHBOARD:
		return FromDashboard, nil
	case intakev1.Source_SOURCE_CHROME_EXT:
		return FromChromeExt, nil
	case intakev1.Source_SOURCE_SCRAPER:
		return FromScraper, nil
	default:
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedSource, source)
	}
}

// FromDashboard handles manual entries from the React form. Input is already
// clean: the form constrains length and trims, so we only do defensive
// trimming and stamp the source/schema fields.
func FromDashboard(req *intakev1.SubmitApplicationRequest) canonical.ApplicationEvent {
	return canonical.ApplicationEvent{
		UserID:        strings.TrimSpace(req.GetUserId()),
		JobTitle:      strings.TrimSpace(req.GetJobTitle()),
		Company:       strings.TrimSpace(req.GetCompany()),
		URL:           strings.TrimSpace(req.GetUrl()),
		Source:        int32(intakev1.Source_SOURCE_DASHBOARD),
		AppliedAt:     appliedAtOrNow(req.GetAppliedAt()),
		SchemaVersion: canonical.SchemaVersion,
	}
}

// FromChromeExt handles Chrome-extension scrapes from job-board pages.
// Quirks observed in the wild:
//   - DOM text often carries non-breaking spaces (U+00A0) and zero-width chars
//   - Job titles are wrapped with " | LinkedIn" / " - Indeed" suffixes
//   - URLs include tracking params (utm_*, gclid, refId) that bust dedup
func FromChromeExt(req *intakev1.SubmitApplicationRequest) canonical.ApplicationEvent {
	return canonical.ApplicationEvent{
		UserID:        strings.TrimSpace(req.GetUserId()),
		JobTitle:      stripBoardSuffix(collapseWhitespace(req.GetJobTitle())),
		Company:       collapseWhitespace(req.GetCompany()),
		URL:           stripTrackingParams(strings.TrimSpace(req.GetUrl())),
		Source:        int32(intakev1.Source_SOURCE_CHROME_EXT),
		AppliedAt:     appliedAtOrNow(req.GetAppliedAt()),
		SchemaVersion: canonical.SchemaVersion,
	}
}

// FromScraper handles bulk imports from headless scraping. Trust nothing:
// titles may contain raw HTML entities collapsed by the scraper, and
// applied_at can drift from the scrape time. We collapse whitespace
// aggressively but leave entity decoding to the scraper itself — fixing it
// here would mask scraper bugs.
func FromScraper(req *intakev1.SubmitApplicationRequest) canonical.ApplicationEvent {
	return canonical.ApplicationEvent{
		UserID:        strings.TrimSpace(req.GetUserId()),
		JobTitle:      collapseWhitespace(req.GetJobTitle()),
		Company:       collapseWhitespace(req.GetCompany()),
		URL:           strings.TrimSpace(req.GetUrl()),
		Source:        int32(intakev1.Source_SOURCE_SCRAPER),
		AppliedAt:     appliedAtOrNow(req.GetAppliedAt()),
		SchemaVersion: canonical.SchemaVersion,
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// appliedAtOrNow defaults a missing applied_at to the current UTC time. Done
// once here so every source picks up the same default.
func appliedAtOrNow(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Now().UTC()
	}
	return ts.AsTime()
}

// invisibleRunes are unicode code points scrapers commonly leak: NBSP, the
// three zero-width space variants, and the byte-order mark. Each is replaced
// before whitespace collapsing so a leading NBSP becomes an ordinary space
// rather than surviving into the output.
var invisibleReplacer = strings.NewReplacer(
	"\u00a0", " ", // NBSP -> ordinary space
	"\u200b", "", // zero-width space
	"\u200c", "", // zero-width non-joiner
	"\u200d", "", // zero-width joiner
	"\ufeff", "", // BOM / zero-width no-break space
)

// collapseWhitespace trims surrounding whitespace, drops zero-width and
// non-breaking-space code points, and collapses internal whitespace runs to a
// single ASCII space.
func collapseWhitespace(s string) string {
	s = invisibleReplacer.Replace(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}

// boardSuffixes are the trailing markers job boards append to <title> tags.
var boardSuffixes = []string{
	" | LinkedIn",
	" - LinkedIn",
	" | Indeed.com",
	" | Indeed",
	" - Indeed.com",
	" - Indeed",
	" | Glassdoor",
	" - Glassdoor",
}

// stripBoardSuffix removes a trailing site marker that a Chrome extension
// often picks up from <title> tags. Matching is case-insensitive but anchored
// to the end of the string, so a title that *contains* "| LinkedIn" mid-string
// is not mangled.
func stripBoardSuffix(s string) string {
	lower := strings.ToLower(s)
	for _, suf := range boardSuffixes {
		sufLower := strings.ToLower(suf)
		if strings.HasSuffix(lower, sufLower) {
			return strings.TrimSpace(s[:len(s)-len(suf)])
		}
	}
	return s
}

// trackingParams are the query keys we drop from URLs to keep dedup keys
// stable across re-shares of the same job posting.
var trackingParams = map[string]struct{}{
	"utm_source":   {},
	"utm_medium":   {},
	"utm_campaign": {},
	"utm_content":  {},
	"utm_term":     {},
	"gclid":        {},
	"fbclid":       {},
	"refId":        {},
	"ref":          {},
	"trk":          {}, // LinkedIn
	"trackingId":   {}, // LinkedIn
}

// stripTrackingParams parses the URL and removes well-known tracking query
// keys. On parse failure the input is returned unchanged so a malformed URL
// still hits the canonical validator (which will reject it cleanly).
func stripTrackingParams(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	dirty := false
	for k := range q {
		if _, drop := trackingParams[k]; drop {
			q.Del(k)
			dirty = true
		}
	}
	if !dirty {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}
