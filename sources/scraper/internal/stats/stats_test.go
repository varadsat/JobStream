package stats_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/varad/jobstream/sources/scraper/internal/stats"
)

func TestRecordRPCError_countsBatchAsAllFailed(t *testing.T) {
	s := stats.New(time.Unix(0, 0))
	s.RecordRPCError(50)
	if s.RPCsAttempted != 1 || s.RPCsFailed != 1 || s.RPCsOK != 0 {
		t.Errorf("rpc counters wrong: %+v", s)
	}
	if s.AppsAttempted != 50 || s.AppsFailed != 50 || s.AppsOK != 0 {
		t.Errorf("app counters wrong: %+v", s)
	}
}

func TestRecordRPCSuccess_splitsOkAndFailed(t *testing.T) {
	s := stats.New(time.Unix(0, 0))
	s.RecordRPCSuccess(40, 10)
	if s.RPCsOK != 1 || s.RPCsFailed != 0 {
		t.Errorf("rpc ok counters wrong: %+v", s)
	}
	if s.AppsOK != 40 || s.AppsFailed != 10 || s.AppsAttempted != 50 {
		t.Errorf("app counters wrong: %+v", s)
	}
}

func TestAppsPerSec(t *testing.T) {
	start := time.Unix(1000, 0)
	s := stats.New(start)
	s.RecordRPCSuccess(100, 0)
	s.RecordRPCSuccess(100, 0)
	got := s.AppsPerSec(start.Add(2 * time.Second))
	if got != 100 {
		t.Errorf("apps/sec: want 100, got %f", got)
	}
}

func TestAppsPerSec_zeroElapsedReturnsZero(t *testing.T) {
	start := time.Unix(0, 0)
	s := stats.New(start)
	s.RecordRPCSuccess(10, 0)
	if got := s.AppsPerSec(start); got != 0 {
		t.Errorf("zero-elapsed should return 0, got %f", got)
	}
}

func TestPrint_includesAllCounters(t *testing.T) {
	start := time.Unix(0, 0)
	s := stats.New(start)
	s.RecordRPCSuccess(3, 1)
	s.RecordRPCError(5)

	var buf bytes.Buffer
	s.Print(&buf, start.Add(time.Second))
	out := buf.String()

	for _, want := range []string{"elapsed:", "rpcs:", "applications:", "throughput:", "9 attempted"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}
