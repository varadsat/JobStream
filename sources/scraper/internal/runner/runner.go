// Package runner orchestrates a load test: it pulls fake applications from
// a generator, throttles RPCs with a token bucket, and accumulates Stats.
//
// The runner depends on a BatchSubmitter interface rather than the concrete
// gRPC client so it can be unit-tested with a fake.
package runner

import (
	"context"
	"fmt"
	"time"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/sources/scraper/internal/generator"
	"github.com/varad/jobstream/sources/scraper/internal/stats"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// BatchSubmitter is the seam over the gRPC client. The real implementation
// wraps intakev1.IntakeServiceClient.BatchSubmitApplication.
type BatchSubmitter interface {
	BatchSubmit(ctx context.Context, req *intakev1.BatchSubmitApplicationRequest) (*intakev1.BatchSubmitApplicationResponse, error)
}

// Config bundles the load-test knobs.
type Config struct {
	UserID    string
	Total     int           // total applications to submit
	BatchSize int           // applications per RPC
	RPS       float64       // RPC requests per second (token-bucket rate)
	Burst     int           // token-bucket burst (defaults to 1 if <=0)
	DupPct    float64       // 0..1 fraction of submissions that reuse a prior pair
	Seed      uint64        // deterministic generator seed
	Now       func() time.Time
}

// Run executes the load test and returns the populated Stats. It returns
// early if ctx is cancelled, but always returns a non-nil Stats reflecting
// progress so callers can still print a partial summary.
func Run(ctx context.Context, cfg Config, sub BatchSubmitter) (*stats.Stats, error) {
	if cfg.BatchSize <= 0 {
		return nil, fmt.Errorf("batch size must be > 0, got %d", cfg.BatchSize)
	}
	if cfg.Total < 0 {
		return nil, fmt.Errorf("total must be >= 0, got %d", cfg.Total)
	}
	if cfg.RPS <= 0 {
		return nil, fmt.Errorf("rps must be > 0, got %f", cfg.RPS)
	}
	if cfg.UserID == "" {
		return nil, fmt.Errorf("user id is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = 1
	}

	limiter := rate.NewLimiter(rate.Limit(cfg.RPS), burst)
	gen := generator.New(cfg.Seed, cfg.DupPct)
	st := stats.New(now())

	remaining := cfg.Total
	for remaining > 0 {
		batchSize := cfg.BatchSize
		if remaining < batchSize {
			batchSize = remaining
		}
		batch := buildBatch(gen, cfg.UserID, batchSize, now)

		if err := limiter.Wait(ctx); err != nil {
			return st, ctx.Err()
		}

		resp, err := sub.BatchSubmit(ctx, &intakev1.BatchSubmitApplicationRequest{Applications: batch})
		if err != nil {
			st.RecordRPCError(batchSize)
		} else {
			ok, failed := countResults(resp.GetResults())
			st.RecordRPCSuccess(ok, failed)
		}

		remaining -= batchSize
	}

	return st, nil
}

func buildBatch(gen *generator.Generator, userID string, n int, now func() time.Time) []*intakev1.SubmitApplicationRequest {
	out := make([]*intakev1.SubmitApplicationRequest, n)
	for i := 0; i < n; i++ {
		app := gen.Next()
		out[i] = &intakev1.SubmitApplicationRequest{
			UserId:    userID,
			JobTitle:  app.JobTitle,
			Company:   app.Company,
			Url:       app.URL,
			Source:    intakev1.Source_SOURCE_SCRAPER,
			AppliedAt: timestamppb.New(now()),
		}
	}
	return out
}

func countResults(results []*intakev1.BatchSubmitResult) (ok, failed int) {
	for _, r := range results {
		if r.GetApplicationId() != "" {
			ok++
		} else {
			failed++
		}
	}
	return ok, failed
}
