# TITIP DONG backend plan

This is the implementation plan for the production backend. It turns the product and architecture plan in
`titipdong-docs/PLAN.md` into concrete modules, tables, endpoints, and an order of work. When the two documents
disagree on _what_ the product does, `titipdong-docs/PLAN.md` wins. This document decides _how_ the backend builds it.

Last updated: 2026-09-27.

## 1. Current state

| Area                   | State                                                                                           |
| ---------------------- | ----------------------------------------------------------------------------------------------- |
| Platform: HTTP         | Fiber v2 + Huma v2. OpenAPI 3.1 generated from code at `/openapi.json`, docs UI at `/docs`       |
| Platform: errors       | RFC 9457 `application/problem+json` (Huma `ErrorModel`); 5xx details are logged, never returned |
| Platform: database     | pgx v5 pool, sqlc per module, goose migrations embedded and applied at startup under a lock      |
| Platform: config       | Hand-rolled env loader (`DATABASE_URL`, `JWT_SECRET`, `PORT`), reads `.env.local` in dev         |
| F01 Auth & User        | Register, login, `GET /me`, bearer middleware with role checks. Refresh tokens not done          |
| F02–F08                | Not started (empty packages)                                                                     |
| Worker (`cmd/worker`)  | Not started                                                                                      |
| CI/CD                  | `main` → `go test` → image to ACR → rollout to AKS. No PR checks, no lint                        |
| Kubernetes             | 2 replicas with anti-affinity, `ClusterIP` only. **Manifests do not pass secrets yet** (section 6)      |

`internal/auth` is the reference implementation of the feature layout in section 2.2. New features copy its shape.

### Contract changes from the auth refactor

The refactor replaced the hand-written spec with Huma. The FE has not integrated these endpoints yet, but note:

- Request validation failures now return **422** with an `errors` array (was 400).
- Problem responses omit `type` (RFC 9457 default `about:blank`) and `instance`; use `status` and `detail`.
- Added limits: password ≤ 128 chars, email ≤ 254 chars.
- Schema names in the spec: `RegisterRequest`, `LoginRequest`, `AuthResponse`, `UserResponse`, `ErrorModel`.

## 2. Architecture

### 2.1 Processes

One codebase, one image, two processes:

- `cmd/api`: the HTTP API (Huma on Fiber). Stateless; any replica serves any request.
- `cmd/worker`: River job workers (notifications, auto-cancel, auto-release). Separate Deployment so job load never
  affects API latency.

Both connect to the same PostgreSQL. River stores its queue in PostgreSQL, so jobs are inserted in the **same
transaction** as the business change that caused them.

### 2.2 Feature layout

Go code is organized by feature. SQL is kept in one place and generates one shared package:

```
db/
├── migrations/       goose migrations (+ embed.go, which embeds them into the binary)
└── queries/          all sqlc queries, one file per table or topic (users.sql, orders.sql, …)
internal/
├── db/               sqlc output for every query: one package, one set of model types (generated, never edit)
└── <feature>/
    ├── handler.go    Huma operations and request/response DTOs (the OpenAPI contract)
    ├── service.go    business rules, domain types, errors
    ├── repository.go repository interface + implementation on internal/db; maps driver errors to domain errors
    └── *_test.go     service tests with an in-memory repository, handler tests through httpserver.New
```

Supporting files are fine when they keep a file focused (`auth` has `token.go`, `password.go`, `middleware.go`).

Rules:

1. **Queries are shared.** Any query in `db/queries/` may read and JOIN any table, and any feature's repository may
   call any generated query. Reuse an existing query before writing a new one; for example, the order list can JOIN
   `users` for the buyer's name, and every feature uses the same `GetUserByID`. Repositories stay per feature and
   return that feature's domain types, so services never depend on generated types directly.
2. **Services may call other features' services directly.** Go forbids import cycles, so package dependencies must
   stay one-directional. The planned direction is `order → trip`, `payment → order`, `shipment → order`,
   `dispute → order, payment`, `admin → any`, and everything that notifies → `notification`. When two features need
   each other, the lower one exposes a method and the higher one calls it (see `trip.Reserve` in section 4).
3. **Writes that protect money and order state go through one place.** Order status changes only through the order
   service (the state machine), and ledger entries are written only by the payment service. Other features call
   those services instead of updating `orders.status` or inserting ledger rows themselves. This is a convention
   checked in review, not by tooling.
4. Handlers stay thin: decode, call the service, map domain errors to Huma errors. Business rules live in services.
5. Domain errors are sentinel errors (`ErrEmailTaken`) or typed errors carrying user-safe detail (`*InputError`).
   Anything else becomes a 500 with the detail logged.
6. `internal/platform/` holds shared infrastructure only and imports no feature package.

### 2.3 Transactions across features

Money and order status must change together (for example: record payment in the ledger, set order `Paid`, enqueue
a notification job). Those steps run in different features' repositories, so a transaction is carried in
`context.Context`:

- `platform/database` exposes `WithTx(ctx, fn func(ctx) error)`, which begins a transaction, stores it in the
  context, and commits or rolls back.
- Each repository runs its sqlc queries on the transaction from the context when present, otherwise on the pool.
- River jobs are inserted with `InsertTx` on the same transaction.
- State changes lock the aggregate first (`SELECT … FOR UPDATE` on the order row) so two replicas cannot process
  the same order at once.

The service that starts a use case owns the transaction (for example `payment.Service.HandleCallback`).

### 2.4 HTTP conventions

- Tags equal module names (`auth`, `trip`, `order`, …). The FE's orval config splits generated code by tag.
- `operationId` is camelCase verb-first (`createTrip`, `listOrders`, `approveOrder`); it becomes the FE hook name.
- Protected operations declare `Security: auth.Security(roles...)`. The middleware returns 401 without a valid token
  and 403 for the wrong role. Frontend guards are UX only.
- Ownership checks (a buyer can only see their own order) happen in the service, returning `ErrNotFound` rather than
  403 so IDs of other users' resources are not confirmed.
- Lists use cursor pagination: `?limit=` (default 20, max 100) and `?cursor=`; responses carry `items` and
  `nextCursor`.
- Money is `int64` whole rupiah in the database, JSON, and Go. No floats anywhere. Totals and fees are computed only
  in the backend.
- Timestamps are `TIMESTAMPTZ` and serialized as RFC 3339.
- The spec must be generated without a database so CI and the FE can pull it offline (section 5, `openapi` command).

## 3. Platform work (before or alongside F02)

| Item                  | Plan                                                                                                                                                        |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Config                | Move to `caarlos0/env` with typed fields and validation; keep `.env.local` loading for dev only                                                              |
| Logging               | `log/slog` JSON handler; request ID middleware (accept/propagate `X-Request-ID`); access log with status and latency; silence the Fiber startup banner        |
| Graceful shutdown     | Handle SIGTERM: stop accepting, drain in-flight requests (`ShutdownWithContext`), close pool; `preStop` sleep in k8s so the Service stops routing first      |
| Health                | Keep `/health` as liveness (no dependencies). Add `/health/ready` that pings PostgreSQL, used as the readiness probe                                          |
| CORS                  | Allow-list from config (production FE origin, preview origins, `http://localhost:5173`), `Allow-Credentials: true` for the refresh cookie                     |
| Transactions          | `database.WithTx` and a tx-aware `DBTX` accessor for repositories (section 2.3)                                                                                     |
| OpenAPI export        | `go run ./cmd/api openapi > openapi.json` builds the API without connecting to the database, so the FE can regenerate and CI can diff                       |
| Worker                | `cmd/worker` with River client, queues, and job registration per module; River migrations run with ours                                                      |
| Blob storage          | `platform/blob` on `azblob`: short-lived SAS upload URLs (client uploads directly), object keys stored in the DB, private container                         |
| Metrics               | `/metrics` via `prometheus/client_golang`: request count/latency by route and status, pool stats, job counts. Not exposed through the Ingress                |
| Audit log             | `platform/audit` writer used inside money/admin transactions; the `admin` module only reads it (avoids an `admin ↔ payment` cycle)                           |
| Clock                 | Inject `now func() time.Time` in services that use deadlines so tests can control time                                                                     |

## 4. Modules

Endpoint lists are the intended contract; adjust in the module's PR if the spec in `titipdong-docs/features/<ID>/`
says otherwise.

Table lists below are summaries. Full definitions, constraints, and indexes are in [`DATABASE.md`](DATABASE.md),
which takes precedence.

### F01 Auth & User (Carlo)

Done: `POST /auth/register`, `POST /auth/login`, `GET /me`, `auth.Security`, `auth.Middleware`, `auth.PrincipalFrom`.

To do:

- **Refresh tokens.** Table `refresh_tokens` (id, user_id, token_hash, family_id, expires_at, revoked_at,
  created_at). Opaque random token in an `HttpOnly; Secure; SameSite=Lax` cookie scoped to `/auth`. Rotation on every
  use; reuse of a rotated token revokes the whole family. Endpoints: `POST /auth/refresh`, `POST /auth/logout`.
  Login and register also set the cookie. The FE already calls `/auth/refresh` on app load.
- **Profile.** `PATCH /me` (name, phone, password change with current password).
- **Account status.** `users.status` (`active`, `suspended`); login and refresh reject suspended users. F08 sets it.
- **Admin provisioning.** `go run ./cmd/api create-admin` CLI command; admins are never created over HTTP.
- Other features that need user names or emails JOIN `users` in their own queries.

### F02 Trip & Catalog (Carlo)

Tables: `trips` (id, jastiper_id, slug, destination_city, destination_country, departs_on, returns_on,
request_deadline, capacity, reserved_capacity, status, timestamps), `trip_items` (popular catalog items per trip with
photo key and reference price).

Endpoints: `GET /trips` (filter by city, date range; cursor), `GET /trips/{slug}`, `POST /trips` (jastiper),
`PATCH /trips/{id}`, `POST /trips/{id}/close`, `GET /me/trips`, catalog item CRUD under `/trips/{id}/items`.

Capacity: `trip.Service.Reserve(ctx, tripID, amount)` and `Release(...)` lock the trip row and update
`reserved_capacity`; Order calls them inside its transaction. That keeps the dependency one-way (`order → trip`) while
the trip page can still show remaining capacity.

### F03 Order (Alfian)

Tables: `orders` (id, trip_id, buyer_id, jastiper_id, product_name, product_url, photo_key, quantity, notes,
item_price, jastip_fee, total, status, payment_deadline, timestamps), `order_events` (order_id, from_status,
to_status, actor_id, reason, at).

State machine lives only in `order`. A transition table defines allowed moves and which actor may make them; every
change locks the order row, writes `order_events`, and enqueues a notification in the same transaction. Other modules
call explicit methods (`MarkPaid`, `MarkShipped`, `MarkDelivered`, `MarkDisputed`, `ResolveDispute`, `MarkReleased`,
`MarkRefunded`), never a generic `SetStatus`.

Endpoints: `POST /trips/{slug}/orders` (buyer), `GET /orders` (buyer's or jastiper's view by role),
`GET /orders/{id}`, `GET /me/trips/{id}/orders` (jastiper inbox), `POST /orders/{id}/approve` (price + fee),
`POST /orders/{id}/reject`, `POST /orders/{id}/cancel`.

Jobs: `order.expire_unpaid` scheduled at approval for `payment_deadline`; cancels if still `Approved` and releases
trip capacity.

### F04 Payment & Escrow (Barru)

Tables: `payments` (id, order_id, amount, status, provider, provider_ref UNIQUE, raw_callback, timestamps),
`ledger_transactions` (id, kind, order_id, created_at), `ledger_entries` (transaction_id, account, debit, credit),
`payouts` (simulated disbursement to the jastiper).

Ledger rules (from `titipdong-docs/PLAN.md`): append-only (no UPDATE/DELETE grants on ledger tables), double-entry
enforced by a deferred constraint trigger that checks debits = credits per transaction, balances computed from entries.
Accounts and postings are defined in [`DATABASE.md`](DATABASE.md#ledger-ledger_accounts-ledger_transactions-ledger_entries).

Flows:

- `POST /orders/{id}/payments`: creates a payment for the order total, returns the checkout info.
- Gateway callback (`POST /payments/callback`): verify signature, dedupe by `provider_ref`, then in one transaction:
  mark payment, ledger `buyer → escrow`, `order.MarkPaid`. Repeated callbacks are no-ops.
- Release (on `Delivered`, or by `payment.auto_release` job N days after `Shipped`): `escrow → jastiper (95%) +
platform:revenue (5%)`, `order.MarkReleased`, audit log.
- Refund (dispute decision or jastiper cancel after payment): `escrow → buyer_refund`, `order.MarkRefunded`.
- `GET /wallet` and `GET /wallet/transactions` for the jastiper.

The gateway is Midtrans (Snap for checkout, HTTP notifications as the callback, verified by signature key). Build it
behind a `gateway` interface: the Midtrans adapter for staging (sandbox) and production, and an in-memory fake for
tests. Keep the Midtrans server key only in backend secrets.

### F05 Shipment (Alfian)

Table: `shipments` (order_id PK, method `cod`/`courier`, courier, tracking_number, proof_key, shipped_at,
delivered_at).

Endpoints: `PUT /orders/{id}/shipment` (jastiper; sets method, tracking, proof → `order.MarkShipped`),
`GET /orders/{id}/shipment` (includes tracking status from the tracking provider, behind an interface), `POST /orders/{id}/confirm-receipt` (buyer →
`order.MarkDelivered` → payment release). Proof photos use blob SAS uploads.

### F06 Dispute (Barru)

Tables: `disputes` (id, order_id, opened_by, reason, status, decision, decided_by, decided_at),
`dispute_evidence` (dispute_id, uploader_id, blob_key, note), optional `dispute_messages`.

Endpoints: `POST /orders/{id}/dispute` (buyer, only while `Shipped`), `GET /orders/{id}/dispute`,
`GET /admin/disputes`, `GET /admin/disputes/{id}`, `POST /admin/disputes/{id}/decision` (`refund` or `release`).
The decision runs in one transaction: dispute update, `order.ResolveDispute`, payment refund/release, audit log.

### F07 Notification (Azfa)

Table: `notifications` (id, user_id, type, data JSONB, read_at, created_at); `outbound_emails` for the delivery
status of emails sent through the email provider (provider still to choose; development uses a logging implementation).

`notification.Service.Notify(ctx, userID, event)` inserts a River job on the caller's transaction; the worker writes
the in-app row and sends/records the email.

Endpoints: `GET /notifications` (cursor), `GET /notifications/unread-count`, `POST /notifications/{id}/read`,
`POST /notifications/read-all`.

### F08 Admin (Azfa)

Reads with its own queries, JOINing any tables it needs; changes user status only through `auth.Service`.

Endpoints: `GET /admin/stats`, `GET /admin/transactions`, `GET /admin/users`, `PATCH /admin/users/{id}` (suspend or
activate), `GET /admin/audit-logs`. All require `auth.Security(auth.RoleAdmin)`.

## 5. Tooling and testing

- **Go 1.26** (`go.mod`, Dockerfile, CI). Go 1.24 is no longer supported.
- **Makefile** is the single entry point for local and CI commands. sqlc, goose, and golangci-lint versions are pinned
  there and run via `go run pkg@version`, so they always build with the project's Go version. CI should call
  `make check` rather than repeat the commands.
- **Local dependencies** run from `compose.yaml` (PostgreSQL now; Mailpit and Azurite when F07 and uploads start).
- **sqlc**: one entry in `sqlc.yaml` (`db/queries/` → `internal/db`). Generated code is committed; `make check` runs
  `sqlc diff` to catch drift.
- **goose**: sequential SQL files in `db/migrations/` (`00002_create_trips.sql`, …), always with a `-- +goose Down`.
  Migrations must be backward compatible with the previous release (expand, deploy, then contract), because old
  and new pods run side by side during a rollout.
- **Tests**:
  - Service tests with an in-memory repository (fast, no DB), as in `internal/auth/service_test.go`.
  - Handler tests through `httpserver.New()` for status codes, auth, and problem bodies.
  - Repository and cross-module transaction tests against real PostgreSQL via testcontainers-go, behind a build tag
    (`//go:build integration`), run in CI. Required for ledger, state machine, and idempotency code.
  - k6 load tests and failover per `titipdong-docs/MONITORING_PLAN.md`.

## 6. Delivery pipeline and infrastructure

CI (to add):

- New PR workflow running `make check` (gofmt, vet, golangci-lint, sqlc diff, unit tests; integration tests to be
  added),
  and OpenAPI export uploaded as an artifact for the FE.
- The existing `main` workflow keeps test → build/push → rollout, and gains a smoke check of `/health/ready` after
  rollout.
- Branch protection on `main`; work lands through `dev` → PR → `main`.

Kubernetes (to add, in this order):

1. **Secrets** for `DATABASE_URL` and `JWT_SECRET` (k8s Secret, or Azure Key Vault via the CSI driver), wired with
   `envFrom`. Until this exists, merging the current code to `main` would crash-loop the new pods.
2. Azure Database for PostgreSQL reachable from the cluster (private access), TLS (`sslmode=require`).
3. Readiness probe on `/health/ready`, resource requests/limits, `preStop` hook, PodDisruptionBudget
   (`minAvailable: 1`).
4. Ingress with HTTPS (cert-manager) at the API domain; CORS configured to match.
5. Worker Deployment (same image, `cmd/worker` entrypoint), also 2 replicas with anti-affinity.
6. Blob Storage account and container; credentials via workload identity rather than keys.

Migrations currently run at API startup under a PostgreSQL advisory lock, which is safe with several replicas. Revisit
(move to a pre-rollout Job) if a migration ever takes long enough to delay readiness.

## 7. Decisions needed

These change the design above and need a team decision before the affected phase starts.

1. **Escrow arrangement with the payment gateway.** The gateway is Midtrans (`titipdong-docs/PLAN.md`). Still to
   settle before launch: how escrowed funds are held and disbursed to jastipers with Midtrans (or another PJP
   partner), and the compliance points in `titipdong-docs/FINANCE.md` section 8. Blocks F04 payouts, not payment collection.
   Email and shipment-tracking providers also still need to be chosen.
2. **Identity verification (KYC).** The docs plan says no KTP verification; the FE has `/profile/kyc` and
   `/admin/kyc` routes. Blocks the F01 profile work and F08 scope.
3. **Roles.** Backend uses `buyer`/`jastiper`/`admin` with one role per user; the FE store expects
   `pembeli`/`jastiper`/`admin` and a `roles` array. Pick one naming and decide whether a user can be both buyer and
   jastiper. Blocks F01 refresh/profile and every role check.
4. **Trip capacity unit** (kg, number of items, or free-form). Blocks F02/F03.
5. **Deadlines:** payment window after approval, auto-release window after shipping. Blocks F03/F04 jobs.
6. **Frontend hosting and domains** (Cloudflare Pages vs Azure Static Web Apps): affects CORS origins and the refresh
   cookie domain.
7. **Brute-force protection** on login/register. The docs plan excludes Redis and rate limiting; options are
   Ingress-level rate limits or PostgreSQL-backed attempt counting.
8. **Staging environment.** Currently only production exists; PR previews of the FE have no backend to call.
9. **Fiber v2 vs v3.** Huma's primary adapter now targets Fiber v3. v2 still works; migrating later touches only
   `platform/httpserver` and middleware.

## 8. Order of work

Modules are owned by different people and can progress in parallel once their dependencies exist.

| Phase | Scope                                                                                                  | Done when                                                                          |
| ----- | ------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| 0     | Platform (section 3: config, logging, shutdown, readiness, CORS, `WithTx`, OpenAPI export), CI PR checks, k8s secrets + DB | `main` deploys a working API connected to Azure PostgreSQL; PRs are gated by lint/tests |
| 1     | F01 remainder (refresh, logout, profile, status, create-admin)                                          | FE login survives reloads; admin account exists                                    |
| 2     | F02 Trip & Catalog                                                                                     | Jastiper creates a trip; buyers browse and filter trips                            |
| 3     | F03 Order core + state machine; worker process + River; F07 in-app notifications                        | Buyer requests, jastiper approves with a price; both get notified                  |
| 4     | F04 payment, ledger, release/refund                                                                     | Payment moves the order to `Paid`; ledger balances; duplicate callbacks are no-ops |
| 5     | F05 shipment + confirm receipt → release                                                                 | All six steps of the business flow run end to end on AKS                           |
| 6     | F06 dispute, F08 admin, audit log                                                                       | Admin resolves a dispute with refund or release                                    |
| 7     | Production readiness: Ingress/TLS, blob storage, metrics, load and failover tests, backups, security review | Checklist in `titipdong-docs/MONITORING_PLAN.md` passes                            |

## 9. Adding a module (checklist)

1. Migration in `db/migrations/` for the module's tables.
2. Queries in `db/queries/<table>.sql` (reuse existing ones where possible); run `make sqlc` and commit the output.
3. `repository.go` (interface + pgx implementation), `service.go` (exported `Service`, domain types and errors),
   `handler.go` (`RegisterRoutes(api huma.API)`, tag = module name).
4. Wire it in `cmd/api/main.go` after `api.UseMiddleware(auth.Middleware(...))`.
5. Service tests with an in-memory repository, handler tests, and integration tests for anything touching money
   or order status.
6. Tell the FE owner to run `pnpm api` and commit the regenerated client.
