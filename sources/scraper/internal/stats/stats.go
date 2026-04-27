// Package stats accumulates per-batch outcomes for the load-test summary.
package stats

import (
	"fmt"
	"io"
	"time"
)

// Stats is mutable but used from a single goroutine in the runner; we don't
// pay for sync primitives.
type Stats struct {
	RPCsAttempted int
	RPCsOK        int
	RPCsFailed    int
	AppsAttempted int
	AppsOK        int
	AppsFailed    int
	Started       time.Time
}

// New starts the clock.
func New(now time.Time) *Stats {
	return &Stats{Started: now}
}

// RecordRPCError logs a whole-batch failure (transport error, server-side
// rate-limit response, etc.). Every app in the batch is also counted as
// attempted+failed so per-app totals stay consistent.
func (s *Stats) RecordRPCError(batchSize int) {
	s.RPCsAttempted++
	s.RPCsFailed++
	s.AppsAttempted += batchSize
	s.AppsFailed += batchSize
}

// RecordRPCSuccess logs a batch where the RPC itself returned OK. okCount and
// failCount come from inspecting the per-item results in the response.
func (s *Stats) RecordRPCSuccess(okCount, failCount int) {
	s.RPCsAttempted++
	s.RPCsOK++
	s.AppsAttempted += okCount + failCount
	s.AppsOK += okCount
	s.AppsFailed += failCount
}

// AppsPerSec is the realised throughput including failures.
func (s *Stats) AppsPerSec(now time.Time) float64 {
	elapsed := now.Sub(s.Started).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(s.AppsAttempted) / elapsed
}

// Print writes a human-readable summary to w.
func (s *Stats) Print(w io.Writer, now time.Time) {
	elapsed := now.Sub(s.Started)
	fmt.Fprintf(w, "── scraper run summary ──\n")
	fmt.Fprintf(w, "  elapsed:        %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "  rpcs:           %d attempted, %d ok, %d failed\n", s.RPCsAttempted, s.RPCsOK, s.RPCsFailed)
	fmt.Fprintf(w, "  applications:   %d attempted, %d ok, %d failed\n", s.AppsAttempted, s.AppsOK, s.AppsFailed)
	fmt.Fprintf(w, "  throughput:     %.1f apps/sec\n", s.AppsPerSec(now))
}
