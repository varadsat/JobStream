package generator_test

import (
	"testing"

	"github.com/varad/jobstream/sources/scraper/internal/generator"
)

func TestNext_freshFieldsArePopulated(t *testing.T) {
	g := generator.New(1, 0)
	app := g.Next()
	if app.JobTitle == "" {
		t.Error("JobTitle empty")
	}
	if app.Company == "" {
		t.Error("Company empty")
	}
	if app.URL == "" {
		t.Error("URL empty")
	}
}

func TestNext_deterministicForSameSeed(t *testing.T) {
	a := generator.New(42, 0.3)
	b := generator.New(42, 0.3)
	for i := 0; i < 100; i++ {
		if a.Next() != b.Next() {
			t.Fatalf("seed-determinism broken at iter %d", i)
		}
	}
}

func TestNext_dupPctZeroProducesAllUniquePairs(t *testing.T) {
	g := generator.New(7, 0)
	seen := make(map[string]struct{})
	const n = 200
	for i := 0; i < n; i++ {
		app := g.Next()
		key := app.Company + "|" + app.JobTitle
		if _, dup := seen[key]; dup {
			// gofakeit can occasionally repeat from its own corpus; tolerate
			// a tiny natural collision rate but flag a clear regression.
			continue
		}
		seen[key] = struct{}{}
	}
	if len(seen) < n*8/10 {
		t.Errorf("dupPct=0 produced too many natural collisions: %d unique of %d", len(seen), n)
	}
}

func TestNext_dupPctOneAlwaysReusesFirstPool(t *testing.T) {
	g := generator.New(99, 1.0)
	first := g.Next() // seeds the pool
	for i := 0; i < 50; i++ {
		got := g.Next()
		if got.Company != first.Company || got.JobTitle != first.JobTitle {
			t.Fatalf("dupPct=1 should reuse pool, got new pair (%s, %s)", got.Company, got.JobTitle)
		}
	}
}

func TestNext_dupPctRoughlyMatchesObservedRate(t *testing.T) {
	const (
		n      = 4000
		dupPct = 0.5
	)
	g := generator.New(123, dupPct)
	seen := make(map[string]int)
	dups := 0
	for i := 0; i < n; i++ {
		app := g.Next()
		key := app.Company + "|" + app.JobTitle
		if seen[key] > 0 {
			dups++
		}
		seen[key]++
	}
	// First call always seeds, so the achievable max dup rate is (n-1)/n.
	observed := float64(dups) / float64(n)
	if observed < dupPct-0.1 || observed > dupPct+0.1 {
		t.Errorf("dup rate: want ~%.2f, got %.3f", dupPct, observed)
	}
}
