package dedup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultTTL is how long a dedup entry is retained.
//
// Trade-off: too short (< 1 h) and accidental double-submits within a session
// slip through; too long (> 7 d) and a user who was rejected and legitimately
// wants to re-apply gets a stale "already submitted" response with no recourse
// short of waiting. 24 h covers the accidental case and expires well before the
// typical re-application window.
const DefaultTTL = 24 * time.Hour

// Deduper checks and records deduplication keys for submitted applications.
// Implementations must be safe for concurrent use.
type Deduper interface {
	// Check returns (applicationID, true, nil) when a dedup entry already exists.
	// Returns ("", false, nil) on a cache miss.
	Check(ctx context.Context, userID, company, jobTitle string) (string, bool, error)

	// Set stores applicationID under the dedup key with the configured TTL.
	Set(ctx context.Context, userID, company, jobTitle, applicationID string) error
}

type redisDeduper struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedis creates a Deduper backed by Redis.
// Pass DefaultTTL for the standard 24-hour window.
func NewRedis(client *redis.Client, ttl time.Duration) Deduper {
	return &redisDeduper{client: client, ttl: ttl}
}

func (d *redisDeduper) Check(ctx context.Context, userID, company, jobTitle string) (string, bool, error) {
	val, err := d.client.Get(ctx, Key(userID, company, jobTitle)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (d *redisDeduper) Set(ctx context.Context, userID, company, jobTitle, applicationID string) error {
	return d.client.Set(ctx, Key(userID, company, jobTitle), applicationID, d.ttl).Err()
}

// Key returns the Redis key for the given submission. Exported so Chunk 3.2 can
// use SET NX directly against the same key without re-deriving it.
//
// Format: dedup:v1:{userID}:{sha256(lower(company)|lower(jobTitle))}
//
// The hash normalises case and surrounding whitespace so "Google | SWE" and
// "google | swe" map to the same key, while staying fixed-width regardless of
// how long the company/title strings are.
func Key(userID, company, jobTitle string) string {
	payload := normalize(company) + "|" + normalize(jobTitle)
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("dedup:v1:%s:%x", userID, h)
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
