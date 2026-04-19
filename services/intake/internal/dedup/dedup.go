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

// pendingTTL caps how long a "pending" sentinel lives if the owning process
// crashes between Claim and Set/Release. 30 s is generous for a Postgres insert
// while keeping the stuck-sentinel window short.
const pendingTTL = 30 * time.Second

// pendingSentinel is the value written by Claim. Check treats it as a cache miss
// so that concurrent callers proceed to Claim rather than returning a garbage ID.
const pendingSentinel = "__pending__"

// Deduper checks and records deduplication keys for submitted applications.
// Implementations must be safe for concurrent use.
type Deduper interface {
	// Check returns (applicationID, true, nil) when a committed dedup entry
	// exists. Returns ("", false, nil) on a cache miss or when the slot is
	// claimed but not yet committed (pending sentinel).
	Check(ctx context.Context, userID, company, jobTitle string) (string, bool, error)

	// Claim atomically writes a pending sentinel via SET NX. Returns true if
	// the claim was won (key did not exist). Returns false if another caller
	// already holds the slot.
	Claim(ctx context.Context, userID, company, jobTitle string) (bool, error)

	// Set overwrites the key with the real applicationID and the full TTL.
	// Called after a successful insert to commit the dedup entry.
	Set(ctx context.Context, userID, company, jobTitle, applicationID string) error

	// Release deletes the key. Called on insert failure to free the pending
	// claim so the next caller is not blocked by a stale sentinel.
	Release(ctx context.Context, userID, company, jobTitle string) error
}

// NoopDeduper never deduplicates. Use in tests or when Redis is intentionally
// disabled. Claim always returns true (every insert proceeds).
type NoopDeduper struct{}

func (NoopDeduper) Check(_ context.Context, _, _, _ string) (string, bool, error) {
	return "", false, nil
}
func (NoopDeduper) Claim(_ context.Context, _, _, _ string) (bool, error) { return true, nil }
func (NoopDeduper) Set(_ context.Context, _, _, _, _ string) error         { return nil }
func (NoopDeduper) Release(_ context.Context, _, _, _ string) error        { return nil }

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
	if val == pendingSentinel {
		// Another request is in-flight; treat as miss so callers try Claim.
		return "", false, nil
	}
	return val, true, nil
}

func (d *redisDeduper) Claim(ctx context.Context, userID, company, jobTitle string) (bool, error) {
	ok, err := d.client.SetNX(ctx, Key(userID, company, jobTitle), pendingSentinel, pendingTTL).Result()
	return ok, err
}

func (d *redisDeduper) Set(ctx context.Context, userID, company, jobTitle, applicationID string) error {
	return d.client.Set(ctx, Key(userID, company, jobTitle), applicationID, d.ttl).Err()
}

func (d *redisDeduper) Release(ctx context.Context, userID, company, jobTitle string) error {
	return d.client.Del(ctx, Key(userID, company, jobTitle)).Err()
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
