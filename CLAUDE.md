# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Backend for TITIP DONG, an escrow-backed jastip platform. Go modular monolith: Fiber + Huma v2 (OpenAPI 3.1),
PostgreSQL via pgx v5 and sqlc, goose migrations. `docs/PLAN.md` holds architecture, scope decisions, and status;
`docs/DATABASE.md` is the schema design (marks tables not migrated yet); `docs/features/*.md` covers each module
(F01–F08) with rules, endpoints, and remaining work. Keep these docs in sync when a feature lands.

Scope decisions (don't re-litigate): fake payment gateway only (Midtrans is out of scope), background jobs run in a
loop inside the API process under advisory locks (no worker/queue), uploads go to Azure Blob via SAS (Azurite
locally), notifications are in-app only (no email), one role per user, trip capacity counted in items.

## Commands

All tools (sqlc, goose, golangci-lint) run at pinned versions via `go run` from the Makefile; nothing to install.

```sh
make db-up                              # Postgres 16 + API in Docker (compose.yaml), API on :8080
make run                                # run API on host instead (reads .env, applies migrations on startup)
make seed                               # demo accounts {jastiper,buyer,admin}@titipdong.test / demo12345
make test                               # unit tests, no DB needed
go test ./internal/auth -run TestLogin -v   # single test
make check                              # what CI runs: fmt-check, vet, lint, sqlc-check (drift), test
make migration name=create_foo          # new goose migration in db/migrations
make sqlc                               # regenerate internal/db after changing db/queries or migrations
```

DB-backed integration tests (`internal/payment/integration_test.go`, `internal/shipment/service_test.go`,
`internal/platform/database/tx_test.go`) skip unless `TEST_DATABASE_URL` is set, e.g.
`TEST_DATABASE_URL=$DATABASE_URL go test ./internal/payment -v`.

Config comes from env (`internal/platform/config`); `.env` is loaded but real env vars win. Required: `DATABASE_URL`,
`JWT_SECRET` (≥ 32 chars). `PAYMENT_GATEWAY` only supports `fake` for now.

## Architecture

- `cmd/api/main.go` wires everything: config → DB pool → embedded migrations (`db/migrations`, applied at startup)
  → `httpserver.New` → each module's `NewService` / `NewHandler(...).RegisterRoutes(api)`. `cmd/api seed-demo` is a
  subcommand.
- **SQL is shared, Go is per feature.** All queries live in `db/queries/*.sql` and generate one package,
  `internal/db` (never edit by hand; `make check` fails on drift). Any query may JOIN any table; reuse existing
  queries before adding new ones.
- **Feature layout** (`internal/<feature>/`, `internal/auth` is the reference): `handler.go` (Huma operations +
  request/response DTOs = the OpenAPI contract; thin), `service.go` (business rules, domain types and errors),
  `repository.go` (interface + impl over `internal/db`, maps driver errors to domain errors, returns domain types so
  services never touch generated types). Tests: service tests with an in-memory repository, handler tests through
  `httpserver.New`.
- **Cross-module calls go service → service**, one direction only to avoid import cycles:
  `order → trip`, `payment → order`, `shipment → order, payment`, `dispute → order, payment`, `admin → any`.
- **Money and order-state invariants:** order status changes only via the order service's `Mark*` methods (the state
  machine in `internal/order/service.go`); ledger entries are written only by the payment service (double-entry,
  append-only). Money is `BIGINT` rupiah; statuses are `TEXT` with `CHECK`.
- **Transactions:** `database.WithTx(ctx, pool, fn)` stores the tx in ctx and joins an existing one if present;
  repositories use `database.Conn(ctx, pool)` so nested service calls (e.g. shipment → order → payment release)
  share one transaction.
- **Auth:** routes declare `Security: auth.Security(roles...)` on the Huma operation; `auth.Middleware` enforces the
  bearer token and roles (403 on wrong role). Get the caller with `auth.PrincipalFrom(ctx)`. Roles: `buyer`,
  `jastiper`, `admin`. Refresh token is an HttpOnly cookie on `/auth`.
- **Errors:** RFC 9457 `application/problem+json` via Huma; validation failures are 422; 5xx details are logged,
  never returned.
- Endpoints: `/openapi.json` (the FE generates its client from it), `/docs`, `/health`, `/metrics`.

## Database changes

Migrations must have both `-- +goose Up` and `-- +goose Down` and stay compatible with the currently deployed code
(old and new pods run side by side during rolling updates). After changing queries or schema, run `make sqlc` and
commit the generated files.

## Working rules

- **Spec first.** `docs/features/<module>.md` defines endpoints (operationId, path, role, fields, status codes) and
  rules. Build what it says; if the spec is missing something or turns out wrong, update the doc in the same change
  and tell the user. Don't add endpoints, fields, statuses, events, or tables that aren't in the docs.
- **Verify before use.** Before calling a function, query, type, or column, confirm it exists (`grep` the package,
  `internal/db/*.go`, or the migration). `docs/DATABASE.md` describes target tables; only `db/migrations/` says what
  exists. Never invent APIs of libraries; check `go doc <pkg>.<Symbol>` when unsure.
- **Copy the neighbours.** Match the existing module's handler/service/repository/test shape before inventing a new
  pattern. Keep comments minimal (one short line, only when non-obvious).
- **Evidence over claims.** A task is done only after `make check` (and integration tests for money/state code) ran
  in this session; report failures and skipped steps verbatim.
- Project skills in `.claude/skills/`: `implement-feature` (any endpoint/service/job), `db-change` (migrations,
  queries, sqlc), `money-and-order-state` (order status, ledger, refunds, payouts, disputes), `finish-and-commit`
  (definition of done, docs sync, commit format). Use them for those tasks.

## Git

Commits and PRs are authored only by the repository owner's GitHub account (the local `git config` identity). Never
add `Co-Authored-By: Claude`, "Generated with Claude Code", or any other AI attribution to commit messages or PR
descriptions, and never override the author with `--author` or `-c user.name/user.email`. `.claude/settings.json`
disables Claude Code's built-in attribution.

## Deployment

Push to `main` runs `.github/workflows/deploy-aks.yml`: tests → `linux/amd64` image to ACR (tagged with commit SHA)
→ rolling update of the `titipdong-api` Deployment on AKS. Manifests in `deploy/k8s/`. Local Prometheus/Grafana/
Loki/Alloy stack in `monitoring/` (`cd monitoring && docker compose --env-file ../.env up -d`, Grafana on :3000).
