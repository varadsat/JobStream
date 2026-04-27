// dashboard-cli submits a single, hand-crafted job application to the intake
// service. It is the "clean payload" path — every field comes from the
// operator on the command line, no transformation, no scraping. The real
// React form replaces it in Phase 9.
//
// Usage:
//
//	dashboard-cli \
//	    --user-id alice \
//	    --job-title "Staff Engineer" \
//	    --company "Acme Corp" \
//	    --url https://acme.example.com/jobs/123 \
//	    [--applied-at 2025-06-01T12:34:56Z]
//
// On success the application_id is printed to stdout; logs go to stderr so
// shell pipelines (`id=$(dashboard-cli ...)`) work cleanly.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/golang-jwt/jwt/v5"
	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/sources/dashboard-cli/internal/submit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "intake gRPC address")
	userID := flag.String("user-id", "", "user_id claim and request field (required)")
	jobTitle := flag.String("job-title", "", "job title (required)")
	company := flag.String("company", "", "company name (required)")
	jobURL := flag.String("url", "", "job posting URL (required)")
	appliedAt := flag.String("applied-at", "", "RFC3339 timestamp; defaults to now")
	jwtSecret := flag.String("jwt-secret", "dev-secret-change-in-production", "HS256 secret for minting the auth token")
	token := flag.String("token", "", "pre-baked JWT (overrides --jwt-secret + --user-id minting)")
	timeout := flag.Duration("timeout", 10*time.Second, "RPC deadline")
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

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	id, err := submit.Run(ctx, &grpcSubmitter{client: client, bearer: bearer}, submit.Args{
		UserID:    *userID,
		JobTitle:  *jobTitle,
		Company:   *company,
		URL:       *jobURL,
		AppliedAt: *appliedAt,
	})
	if err != nil {
		log.Fatalf("submit: %v", err)
	}
	fmt.Println(id)
}

type grpcSubmitter struct {
	client intakev1.IntakeServiceClient
	bearer string
}

func (g *grpcSubmitter) Submit(ctx context.Context, req *intakev1.SubmitApplicationRequest) (*intakev1.SubmitApplicationResponse, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+g.bearer)
	return g.client.SubmitApplication(ctx, req)
}

// mintJWT signs a short-lived HS256 token whose user_id claim matches the
// auth.Claims shape on the server side. Skipped when --token is supplied.
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
