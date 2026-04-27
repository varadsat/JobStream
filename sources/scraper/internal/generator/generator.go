// Package generator produces synthetic job applications for load testing.
// All randomness flows through a seeded *gofakeit.Faker so tests can pin a
// seed and assert deterministic output.
package generator

import (
	"fmt"

	"github.com/brianvoe/gofakeit/v7"
)

// Application is the minimal payload the runner needs to build a
// SubmitApplicationRequest. Kept proto-free so this package has no gRPC deps.
type Application struct {
	JobTitle string
	Company  string
	URL      string
}

// Generator emits fake Applications with a configurable duplicate rate.
//
// Duplicates re-use an existing (Company, JobTitle) pair so the server's
// dedup key (sha256(company|job_title)) collides — that is what the load
// test is actually exercising. URLs are still freshly generated so the
// payloads are not byte-identical, only dedup-key-identical.
type Generator struct {
	faker  *gofakeit.Faker
	dupPct float64
	pool   []Application
}

// New returns a Generator seeded with the given seed and duplicate fraction.
// dupPct is clamped to [0, 1].
func New(seed uint64, dupPct float64) *Generator {
	if dupPct < 0 {
		dupPct = 0
	}
	if dupPct > 1 {
		dupPct = 1
	}
	return &Generator{
		faker:  gofakeit.New(seed),
		dupPct: dupPct,
	}
}

// Next returns the next application. The first call always produces a fresh
// pair (there's nothing to duplicate yet); subsequent calls roll against
// dupPct to decide whether to reuse an earlier pair.
func (g *Generator) Next() Application {
	if len(g.pool) > 0 && g.faker.Float64() < g.dupPct {
		base := g.pool[g.faker.IntRange(0, len(g.pool)-1)]
		// Fresh URL keeps the surface payload varied; the dedup key still
		// collides because dedup only hashes company + job_title.
		base.URL = g.faker.URL()
		return base
	}
	app := Application{
		JobTitle: g.faker.JobTitle(),
		Company:  g.faker.Company(),
		URL:      fmt.Sprintf("https://jobs.example.com/%s", g.faker.UUID()),
	}
	g.pool = append(g.pool, app)
	return app
}
