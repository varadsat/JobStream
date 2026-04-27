package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/sources/scraper/internal/runner"
)

type fakeSubmitter struct {
	calls         int
	apps          int
	failEveryNth  int // 0 = never fail
	perItemFailed int // how many items per batch to mark as errors
}

func (f *fakeSubmitter) BatchSubmit(_ context.Context, req *intakev1.BatchSubmitApplicationRequest) (*intakev1.BatchSubmitApplicationResponse, error) {
	f.calls++
	f.apps += len(req.GetApplications())
	if f.failEveryNth > 0 && f.calls%f.failEveryNth == 0 {
		return nil, errors.New("simulated transport error")
	}
	results := make([]*intakev1.BatchSubmitResult, len(req.GetApplications()))
	for i := range results {
		if i < f.perItemFailed {
			results[i] = &intakev1.BatchSubmitResult{
				Outcome: &intakev1.BatchSubmitResult_ErrorMessage{ErrorMessage: "validation"},
			}
			continue
		}
		results[i] = &intakev1.BatchSubmitResult{
			Outcome: &intakev1.BatchSubmitResult_ApplicationId{ApplicationId: "app-id"},
		}
	}
	return &intakev1.BatchSubmitApplicationResponse{Results: results}, nil
}

func baseCfg() runner.Config {
	return runner.Config{
		UserID:    "u-1",
		Total:     250,
		BatchSize: 100,
		RPS:       1000, // high so the limiter doesn't block tests
		Burst:     1000,
		DupPct:    0,
		Seed:      1,
		Now:       func() time.Time { return time.Unix(0, 0) },
	}
}

func TestRun_happyPath(t *testing.T) {
	sub := &fakeSubmitter{}
	st, err := runner.Run(context.Background(), baseCfg(), sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 250 / 100 = 3 batches (100, 100, 50)
	if sub.calls != 3 {
		t.Errorf("rpc calls: want 3, got %d", sub.calls)
	}
	if sub.apps != 250 {
		t.Errorf("apps sent: want 250, got %d", sub.apps)
	}
	if st.AppsOK != 250 || st.AppsFailed != 0 {
		t.Errorf("counters: ok=%d failed=%d", st.AppsOK, st.AppsFailed)
	}
}

func TestRun_perItemFailureSplitsCounters(t *testing.T) {
	sub := &fakeSubmitter{perItemFailed: 10}
	cfg := baseCfg()
	cfg.Total = 100
	cfg.BatchSize = 100
	st, err := runner.Run(context.Background(), cfg, sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if st.AppsOK != 90 || st.AppsFailed != 10 {
		t.Errorf("split wrong: ok=%d failed=%d", st.AppsOK, st.AppsFailed)
	}
	if st.RPCsOK != 1 || st.RPCsFailed != 0 {
		t.Errorf("rpc counters wrong: ok=%d failed=%d", st.RPCsOK, st.RPCsFailed)
	}
}

func TestRun_rpcErrorMarksWholeBatchFailed(t *testing.T) {
	sub := &fakeSubmitter{failEveryNth: 1} // every batch fails
	cfg := baseCfg()
	cfg.Total = 200
	cfg.BatchSize = 100
	st, err := runner.Run(context.Background(), cfg, sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if st.RPCsFailed != 2 || st.RPCsOK != 0 {
		t.Errorf("rpc failure counters wrong: %+v", st)
	}
	if st.AppsFailed != 200 || st.AppsOK != 0 {
		t.Errorf("apps failure counters wrong: %+v", st)
	}
}

func TestRun_partialFinalBatch(t *testing.T) {
	sub := &fakeSubmitter{}
	cfg := baseCfg()
	cfg.Total = 7
	cfg.BatchSize = 5
	if _, err := runner.Run(context.Background(), cfg, sub); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sub.calls != 2 {
		t.Errorf("calls: want 2, got %d", sub.calls)
	}
	if sub.apps != 7 {
		t.Errorf("apps: want 7, got %d", sub.apps)
	}
}

func TestRun_validatesConfig(t *testing.T) {
	cases := map[string]func(*runner.Config){
		"empty user":   func(c *runner.Config) { c.UserID = "" },
		"zero batch":   func(c *runner.Config) { c.BatchSize = 0 },
		"negative tot": func(c *runner.Config) { c.Total = -1 },
		"zero rps":     func(c *runner.Config) { c.RPS = 0 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseCfg()
			mut(&cfg)
			if _, err := runner.Run(context.Background(), cfg, &fakeSubmitter{}); err == nil {
				t.Errorf("expected validation error for %s", name)
			}
		})
	}
}

func TestRun_zeroTotalIsNoop(t *testing.T) {
	sub := &fakeSubmitter{}
	cfg := baseCfg()
	cfg.Total = 0
	st, err := runner.Run(context.Background(), cfg, sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sub.calls != 0 {
		t.Errorf("expected 0 rpc calls, got %d", sub.calls)
	}
	if st.AppsAttempted != 0 {
		t.Errorf("expected 0 apps attempted, got %d", st.AppsAttempted)
	}
}

func TestRun_cancelledContextReturnsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	sub := &fakeSubmitter{}
	cfg := baseCfg()
	cfg.Total = 1000
	cfg.RPS = 1 // limiter will block on Wait, then surface cancel
	st, err := runner.Run(ctx, cfg, sub)
	if err == nil {
		t.Fatalf("expected ctx error")
	}
	if st == nil {
		t.Fatalf("expected partial stats, got nil")
	}
}
