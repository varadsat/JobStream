# JobStream

A job-application tracking pipeline: Chrome extension + manual dashboard → gRPC intake service → Kafka → consumer services (status tracker, reminder scheduler, analytics).

Built as a learning project; see `PLAN.md` for the phased roadmap.

## Quick start

```bash
make install-hooks   # one-time dev setup (installs git hooks)
make up              # start local infra (Postgres, Redis, Kafka)
make proto           # generate code from proto definitions
make test            # run all tests across every Go module
make lint            # run golangci-lint
make down            # stop local infra
```

## Layout

```
services/    Go microservices — one Go module per service
proto/       Protobuf definitions (source of truth for contracts)
sources/     Client programs that feed the pipeline
dashboard/   React + TypeScript frontend
deploy/      Docker Compose manifests (and later, Kubernetes)
scripts/     Developer utility scripts
gen/         Generated code — not committed; run `make proto`
```
