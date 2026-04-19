package dedup_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/varad/jobstream/services/intake/internal/dedup"
)

// ---------------------------------------------------------------------------
// Unit tests — key generation (no external deps)
// ---------------------------------------------------------------------------

func TestKey_Format(t *testing.T) {
	k := dedup.Key("u1", "Google", "Software Engineer")
	const prefix = "dedup:v1:u1:"
	if len(k) < len(prefix)+64 {
		t.Fatalf("key too short: %q", k)
	}
	if k[:len(prefix)] != prefix {
		t.Fatalf("unexpected prefix: %q", k)
	}
}

func TestKey_CaseInsensitive(t *testing.T) {
	a := dedup.Key("u1", "Google", "Software Engineer")
	b := dedup.Key("u1", "GOOGLE", "software engineer")
	c := dedup.Key("u1", "google", "  Software Engineer  ")
	if a != b || b != c {
		t.Fatalf("keys differ: %q %q %q", a, b, c)
	}
}

func TestKey_Deterministic(t *testing.T) {
	k1 := dedup.Key("u1", "Acme", "Backend Dev")
	k2 := dedup.Key("u1", "Acme", "Backend Dev")
	if k1 != k2 {
		t.Fatal("key is not deterministic")
	}
}

func TestKey_DistinctForDifferentInputs(t *testing.T) {
	cases := []struct{ company, title string }{
		{"Google", "SWE"},
		{"Google", "SRE"},
		{"Amazon", "SWE"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		k := dedup.Key("u1", c.company, c.title)
		if seen[k] {
			t.Fatalf("collision for %+v", c)
		}
		seen[k] = true
	}
}

func TestKey_UserIsolation(t *testing.T) {
	k1 := dedup.Key("alice", "Google", "SWE")
	k2 := dedup.Key("bob", "Google", "SWE")
	if k1 == k2 {
		t.Fatal("keys for different users must differ")
	}
}

// ---------------------------------------------------------------------------
// Integration tests — Redis round-trip via testcontainers
// ---------------------------------------------------------------------------

func newRedisContainer(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()

	c, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	addr, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	// ConnectionString returns "redis://host:port" — strip the scheme.
	const scheme = "redis://"
	if len(addr) > len(scheme) && addr[:len(scheme)] == scheme {
		addr = addr[len(scheme):]
	}

	return redis.NewClient(&redis.Options{Addr: addr})
}

func TestRedisDeduper_MissAndHit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newRedisContainer(t)
	d := dedup.NewRedis(client, dedup.DefaultTTL)
	ctx := context.Background()

	// Cache miss
	id, ok, err := d.Check(ctx, "u1", "Acme", "Backend Dev")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Fatalf("expected miss, got hit with id=%q", id)
	}

	// Store
	if err := d.Set(ctx, "u1", "Acme", "Backend Dev", "app-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Cache hit
	id, ok, err = d.Check(ctx, "u1", "Acme", "Backend Dev")
	if err != nil {
		t.Fatalf("Check after Set: %v", err)
	}
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if id != "app-123" {
		t.Fatalf("expected app-123, got %q", id)
	}
}

func TestRedisDeduper_CaseInsensitiveHit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newRedisContainer(t)
	d := dedup.NewRedis(client, dedup.DefaultTTL)
	ctx := context.Background()

	if err := d.Set(ctx, "u1", "Google", "SWE", "app-999"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	id, ok, err := d.Check(ctx, "u1", "GOOGLE", "swe")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok || id != "app-999" {
		t.Fatalf("expected hit with app-999, got ok=%v id=%q", ok, id)
	}
}

func TestRedisDeduper_UserIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newRedisContainer(t)
	d := dedup.NewRedis(client, dedup.DefaultTTL)
	ctx := context.Background()

	if err := d.Set(ctx, "alice", "Acme", "SRE", "app-alice"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, ok, err := d.Check(ctx, "bob", "Acme", "SRE")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Fatal("bob should not see alice's entry")
	}
}

func TestRedisDeduper_TTLExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newRedisContainer(t)
	// Use a very short TTL to test expiry without sleeping long.
	d := dedup.NewRedis(client, 100*time.Millisecond)
	ctx := context.Background()

	if err := d.Set(ctx, "u1", "Expiry Corp", "Dev", "app-exp"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	time.Sleep(250 * time.Millisecond)

	_, ok, err := d.Check(ctx, "u1", "Expiry Corp", "Dev")
	if err != nil {
		t.Fatalf("Check after TTL: %v", err)
	}
	if ok {
		t.Fatal("entry should have expired")
	}
}

func TestRedisDeduper_SetOverwrite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newRedisContainer(t)
	d := dedup.NewRedis(client, dedup.DefaultTTL)
	ctx := context.Background()

	if err := d.Set(ctx, "u1", "Corp", "Dev", "app-old"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := d.Set(ctx, "u1", "Corp", "Dev", "app-new"); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	id, ok, err := d.Check(ctx, "u1", "Corp", "Dev")
	if err != nil || !ok || id != "app-new" {
		t.Fatalf("expected app-new, got ok=%v id=%q err=%v", ok, id, err)
	}
}

func TestRedisDeduper_ClaimAndRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newRedisContainer(t)
	d := dedup.NewRedis(client, dedup.DefaultTTL)
	ctx := context.Background()

	// First claim wins.
	won, err := d.Claim(ctx, "u1", "Acme", "Dev")
	if err != nil || !won {
		t.Fatalf("first Claim: won=%v err=%v", won, err)
	}

	// Check sees a pending sentinel as a miss.
	_, ok, err := d.Check(ctx, "u1", "Acme", "Dev")
	if err != nil || ok {
		t.Fatalf("Check during pending: ok=%v err=%v (expected miss)", ok, err)
	}

	// Second claim loses (key exists).
	won2, err := d.Claim(ctx, "u1", "Acme", "Dev")
	if err != nil || won2 {
		t.Fatalf("second Claim should lose: won=%v err=%v", won2, err)
	}

	// Release the claim.
	if err := d.Release(ctx, "u1", "Acme", "Dev"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Now the third claim wins again.
	won3, err := d.Claim(ctx, "u1", "Acme", "Dev")
	if err != nil || !won3 {
		t.Fatalf("post-release Claim: won=%v err=%v", won3, err)
	}
	_ = d.Release(ctx, "u1", "Acme", "Dev")
}

func TestRedisDeduper_ClaimThenSetThenCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newRedisContainer(t)
	d := dedup.NewRedis(client, dedup.DefaultTTL)
	ctx := context.Background()

	if _, err := d.Claim(ctx, "u1", "Corp", "SWE"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := d.Set(ctx, "u1", "Corp", "SWE", "app-committed"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	id, ok, err := d.Check(ctx, "u1", "Corp", "SWE")
	if err != nil || !ok || id != "app-committed" {
		t.Fatalf("Check after commit: ok=%v id=%q err=%v", ok, id, err)
	}
}

// NoopDeduper satisfies the interface — compile-time check.
var _ dedup.Deduper = dedup.NoopDeduper{}

// fakeDeduper is a minimal in-memory implementation for unit tests in other packages.
var _ dedup.Deduper = (*fakeDeduper)(nil)

type fakeDeduper struct{ m map[string]string }

func (f *fakeDeduper) Check(_ context.Context, userID, company, jobTitle string) (string, bool, error) {
	v, ok := f.m[fmt.Sprintf("%s|%s|%s", userID, company, jobTitle)]
	return v, ok, nil
}

func (f *fakeDeduper) Claim(_ context.Context, _, _, _ string) (bool, error) { return true, nil }

func (f *fakeDeduper) Set(_ context.Context, userID, company, jobTitle, id string) error {
	f.m[fmt.Sprintf("%s|%s|%s", userID, company, jobTitle)] = id
	return nil
}

func (f *fakeDeduper) Release(_ context.Context, _, _, _ string) error { return nil }
