// scraper is a load-test simulator for the JobStream intake service.
//
// It generates synthetic applications with gofakeit, throttles RPC traffic
// with a token bucket, and exercises the BatchSubmitApplication endpoint.
// A configurable duplicate fraction lets you exercise the Phase-3 dedup path:
// matching (company, job_title) pairs collide on the server's dedup key and
// should resolve to the same application_id without writing a second row.
//
// Usage:
//
//	scraper --total 10000 --rps 5 --batch-size 200 --dup-pct 0.1
//
// The simulator mints its own HS256 JWT from --jwt-secret + --user-id so the
// operator does not have to juggle tokens during a run. Override with --token
// when targeting a service that uses a different signer.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/sources/scraper/internal/runner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "intake gRPC address")
	userID := flag.String("user-id", "scraper-bot", "user_id claim and request field")
	jwtSecret := flag.String("jwt-secret", "dev-secret-change-in-production", "HS256 secret for minting the auth token")
	token := flag.String("token", "", "pre-baked JWT (overrides --jwt-secret + --user-id minting)")
	total := flag.Int("total", 1000, "total applications to submit")
	rps := flag.Float64("rps", 5, "RPC requests per second (token-bucket rate)")
	burst := flag.Int("burst", 0, "token-bucket burst (0 = same as rps)")
	batchSize := flag.Int("batch-size", 100, "applications per RPC")
	dupPct := flag.Float64("dup-pct", 0.1, "fraction (0..1) of submissions that reuse an earlier (company, job_title) pair")
	seed := flag.Uint64("seed", uint64(time.Now().UnixNano()), "deterministic generator seed")
	timeoutFlag := flag.Duration("timeout", 0, "overall deadline; 0 = no deadline")
	flag.Parse()

	bearer := *token
	if bearer == "" {
		var err error
		bearer, err = mintJWT(*jwtSecret, *userID)
		if err != nil {
			log.Fatalf("mint jwt: %v", err)
		}
	}

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial %s: %v", *addr, err)
	}
	defer conn.Close()

	client := intakev1.NewIntakeServiceClient(conn)
	sub := &grpcSubmitter{client: client, bearer: bearer}

	ctx, cancel := withSignal(context.Background())
	defer cancel()
	if *timeoutFlag > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, *timeoutFlag)
		defer stop()
	}

	cfg := runner.Config{
		UserID:    *userID,
		Total:     *total,
		BatchSize: *batchSize,
		RPS:       *rps,
		Burst:     *burst,
		DupPct:    *dupPct,
		Seed:      *seed,
	}

	fmt.Fprintf(os.Stderr, "starting scraper: total=%d rps=%.1f batch=%d dup_pct=%.2f addr=%s\n",
		cfg.Total, cfg.RPS, cfg.BatchSize, cfg.DupPct, *addr)

	st, runErr := runner.Run(ctx, cfg, sub)
	if st != nil {
		st.Print(os.Stdout, time.Now())
	}
	if runErr != nil {
		log.Fatalf("run: %v", runErr)
	}
}

type grpcSubmitter struct {
	client intakev1.IntakeServiceClient
	bearer string
}

func (g *grpcSubmitter) BatchSubmit(ctx context.Context, req *intakev1.BatchSubmitApplicationRequest) (*intakev1.BatchSubmitApplicationResponse, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+g.bearer)
	return g.client.BatchSubmitApplication(ctx, req)
}

// mintJWT signs a short-lived HS256 token whose user_id claim matches the
// auth.Claims shape on the server side.
func mintJWT(secret, userID string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"user_id": userID,
		"iat":     now.Unix(),
		"exp":     now.Add(time.Hour).Unix(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(secret))
}

func withSignal(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(ch)
	}()
	return ctx, cancel
}
