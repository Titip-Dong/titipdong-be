# TITIP DONG backend plan

How the backend is built: architecture, conventions, platform pieces, the final feature scope, and what is still
being finished. Product behaviour comes from `titipdong-docs/PLAN.md`; when the two disagree on _what_ the product
does, that document wins. This one decides _how_ the backend builds it.

Per-module detail (rules, tables, endpoints, sequence diagrams, remaining work) is in [`features/`](features/README.md).
The schema is in [`DATABASE.md`](DATABASE.md).

Last updated: 2026-10-16.

## 1. Status

Every module and platform item below is in scope; "to do" marks work that is not finished yet.

| Area                     | State                                                                                              |
| ------------------------ | -------------------------------------------------------------------------------------------------- |
| Platform: HTTP           | Done. Fiber v2 + Huma v2; OpenAPI 3.1 at `/openapi.json`, docs UI at `/docs`, RFC 9457 errors       |
| Platform: database       | Done. pgx v5 pool, sqlc, goose migrations embedded and applied at startup under an advisory lock    |
| Platform: transactions   | Done. `database.WithTx` carries the transaction in `context.Context`                                |
| Platform: observability  | Done. JSON logs with request ID, `/metrics`, local Prometheus/Grafana/Loki/Alloy stack              |
| Platform: runtime        | Done. Readiness probe, graceful shutdown, job runner, OpenAPI export, audit log, blob uploads       |
| Platform: remaining      | To do. CI checks                                                                                    |
| F01 Auth & User          | Done                                                                                               |
| F02 Trip & Catalog       | Partial. Create, list, get, close, my trips, capacity done; edit, cancel, catalog to do             |
| F03 Order                | Partial. State machine, request, approve, reject, cancel, notifications, capacity done; photo, per-trip inbox to do |
| F04 Payment & Escrow     | Partial. Fake gateway checkout, ledger, release, refunds, payouts, wallet balance done; wallet history to do |
| F05 Shipment             | Partial. Ship, get, confirm receipt, auto-release done; proof photo to do                           |
| F06 Dispute              | Done                                                                                               |
| F07 Notification         | Done                                                                                               |
| F08 Admin                | Done                                                                                               |

## 2. Scope decisions

| Topic               | Decision                                                                                                    |
| ------------------- | ----------------------------------------------------------------------------------------------------------- |
| Payment gateway     | A **fake gateway** (`PAYMENT_GATEWAY=fake`). The buyer completes payment with `POST /payments/{id}/simulate`, which settles through the same code a real gateway callback would use. Midtrans is a future adapter behind the same `Gateway` interface (section 7) |
| Escrow and payouts  | Escrow lives in the double-entry ledger. Jastipers request payouts; an admin transfers the money outside the system and marks the payout paid |
| Refunds             | Posted in the ledger and executed through `Gateway.Refund`; the fake gateway completes them immediately     |
| Background jobs     | A job loop inside the API process (`platform/jobs`). Each job runs under a PostgreSQL advisory lock, so only one replica runs it at a time. No separate worker or queue |
| File uploads        | Azure Blob Storage with short-lived SAS URLs; the browser uploads directly. Azurite emulates it locally     |
| Notifications       | In-app only, written in the same transaction as the change that caused them. The FE polls. No email         |
| Roles               | One role per user: `buyer`, `jastiper`, `admin`. Admins are created only from the CLI                       |
| Trip capacity       | Counted in **items**: an order reserves its `quantity`                                                      |
| Payment window      | 24 hours after approval, then the order is cancelled                                                        |
| Auto-release window | 7 days after shipping, if the buyer neither confirms receipt nor opens a dispute                            |
| Platform fee        | 5% of the order total, `floor(total × 5 / 100)`                                                             |

## 3. Architecture

### 3.1 Process

One codebase, one image, one process: `cmd/api`, the HTTP API. It is stateless apart from the background job loop,
so any replica serves any request. Subcommands of the same binary:

| Command                         | Purpose                                                      |
| ------------------------------- | ------------------------------------------------------------ |
| `api` (no argument)             | Run the HTTP API and the job loop                            |
| `api seed-demo`                 | Create the demo accounts (idempotent)                        |
| `api create-admin`              | Create an admin account                                      |
| `api openapi`                   | Print the OpenAPI spec without connecting to the database    |

### 3.2 Feature layout

Go code is organized by feature. SQL is kept in one place and generates one shared package:

```
db/
├── migrations/       goose migrations (+ embed.go, which embeds them into the binary)
└── queries/          all sqlc queries, one file per table or topic (users.sql, orders.sql, …)
internal/
├── db/               sqlc output for every query: one package, one set of model types (generated, never edit)
├── platform/         shared infrastructure; imports no feature package
└── <feature>/
    ├── handler.go    Huma operations and request/response DTOs (the OpenAPI contract)
    ├── service.go    business rules, domain types, errors
    ├── repository.go repository interface + implementation on internal/db; maps driver errors to domain errors
    └── *_test.go     service tests with an in-memory repository, handler tests through httpserver.New
```

Supporting files are fine when they keep a file focused (`auth` has `token.go`, `password.go`, `middleware.go`).

Rules:

1. **Queries are shared.** Any query in `db/queries/` may read and JOIN any table, and any feature's repository may
   call any generated query. Reuse an existing query before writing a new one. Repositories stay per feature and
   return that feature's domain types, so services never depend on generated types directly.
2. **Services may call other features' services directly**, one direction only (Go forbids import cycles):

   ```
   order    → trip, upload, notification
   trip     → upload
   payment  → order, notification
   shipment → order, payment, upload, notification
   dispute  → order, payment, upload, notification
   admin    → auth, payment, … (any)
   ```

   When two features need each other, the lower one exposes a method and the higher one calls it.
3. **Writes that protect money and order state go through one place.** Order status changes only through the order
   service (the state machine); ledger rows are written only by the payment service. Other features call those
   services instead of updating `orders.status` or inserting ledger rows themselves.
4. Handlers stay thin: decode, call the service, map domain errors to Huma errors. Business rules live in services.
5. Domain errors are sentinel errors (`ErrEmailTaken`) or typed errors carrying user-safe detail (`*InputError`).
   Anything else becomes a 500 with the detail logged, never returned.

### 3.3 Transactions across features

Money and order status must change together (for example: post the ledger, set the order `paid`, notify both
parties). Those steps run in different features' repositories, so the transaction travels in `context.Context`:

- `database.WithTx(ctx, pool, fn)` begins a transaction and stores it in the context. If the context already carries
  one, `fn` joins it, so nested service calls share a single transaction.
- Repositories run queries through `database.Conn(ctx, pool)`: the transaction when present, otherwise the pool.
- State changes lock the aggregate first (`SELECT … FOR UPDATE` on the order row) so two replicas cannot process the
  same order at once.
- Notifications and audit log rows are inserted on the same transaction, so they exist exactly when the change
  commits.

The service that starts a use case owns the transaction (for example `shipment.ConfirmReceipt` wraps
`order.MarkDelivered` and `payment.Release`).

### 3.4 HTTP conventions

- Tags equal module names (`auth`, `trip`, `order`, …). The FE's orval config splits generated code by tag.
- `operationId` is camelCase verb-first (`createTrip`, `listOrders`, `approveOrder`); it becomes the FE hook name.
- Protected operations declare `Security: auth.Security(roles...)`. The middleware returns 401 without a valid token
  and 403 for the wrong role. Frontend guards are UX only.
- Ownership checks happen in the service and return `ErrNotFound` rather than 403, so IDs of other users' resources
  are not confirmed.
- Invalid state transitions return 409; validation failures return 422 with an `errors` array.
- Lists use cursor pagination: `?limit=` (default 20, max 100) and `?cursor=`; responses carry `items` and
  `nextCursor`.
- Money is `int64` whole rupiah in the database, JSON, and Go. No floats. Totals and fees are computed only in the
  backend.
- Timestamps are `TIMESTAMPTZ`, serialized as RFC 3339.
- Files are referenced by upload ID in requests; responses carry a short-lived signed URL for reading (section 4.2).

## 4. Platform

| Item               | State | Design                                                                                                           |
| ------------------ | ----- | ---------------------------------------------------------------------------------------------------------------- |
| Config             | Done  | Env loader in `platform/config`; `.env` read in development, real env vars win                                    |
| Logging            | Done  | `log/slog` JSON; request middleware with `X-Request-ID` (also in the context, `logging.RequestID`), status, latency |
| CORS               | Done  | Allow-list from `CORS_ORIGINS`, `Allow-Credentials: true` for the refresh cookie                                   |
| Transactions       | Done  | `database.WithTx` / `database.Conn` (section 3.3)                                                                 |
| Metrics            | Done  | `/metrics` (Prometheus): request count and latency by route and status, Go runtime                                |
| Readiness          | Done  | `/health` stays liveness (no dependencies); `/health/ready` pings PostgreSQL and is the k8s readiness probe       |
| Graceful shutdown  | Done  | On SIGTERM: stop the job loop, drain requests (`ShutdownWithContext`, 20 s), close the pool                      |
| OpenAPI export     | Done  | `make openapi` (`api openapi > openapi.json`) registers every operation without a database                         |
| Background jobs    | Done  | `platform/jobs`: a ticker loop started by `main`; each job runs under `pg_try_advisory_lock` and is idempotent; runs are counted in `background_jobs_total` |
| Blob storage       | Done  | `platform/blob` on `azblob`: SAS URLs for upload (write) and read, private container; Azurite in `compose.yaml`   |
| Uploads            | Done  | `internal/upload`: `uploads` table and `POST /uploads`; modules attach an upload by ID (section 4.2)               |
| Audit log          | Done  | `platform/audit` writes `audit_logs` (with the request ID) inside money and admin transactions; F08 reads it      |
| Clock              | Done  | Services that use deadlines take `now func() time.Time` so tests control time                                     |

### 4.1 Background jobs

All jobs are idempotent SQL sweeps, so a missed or repeated run is harmless. The runner exists; each job below is
added together with its module.

| Job                     | Every   | Action                                                                                         |
| ----------------------- | ------- | ---------------------------------------------------------------------------------------------- |
| `orders.expire_unpaid`  | 1 min   | `approved` orders past `payment_deadline` → `cancelled` (system), trip capacity released (done) |
| `orders.auto_release`   | 5 min   | `shipped` orders whose shipment is older than 7 days → `delivered` (system) → funds released (F05, done) |
| `trips.close_expired`   | 5 min   | `open` trips past `request_deadline` → `closed`; `closed` trips past `returns_on` with no active orders → `completed` |
| `auth.purge_tokens`     | 1 hour  | Delete `refresh_tokens` rows past `expires_at` (done)                                           |
| `uploads.purge_pending` | 1 hour  | Delete blobs and rows of uploads never attached after 24 hours (done)                           |

The order expiry is also applied when an order is read or acted on, so behaviour does not depend on the loop's
timing.

### 4.2 File uploads

Images (JPEG, PNG, WebP, at most 5 MB) go from the browser straight to a private Blob Storage container:

1. `POST /uploads` `{ purpose, contentType, sizeBytes }` → `201 { id, uploadUrl, uploadMethod: "PUT", uploadHeaders,
   expiresAt }`. The URL can only create that one blob and expires after 15 minutes.
2. The browser sends `PUT uploadUrl` with the file as the body and every header in `uploadHeaders`
   (`x-ms-blob-type: BlockBlob`, `Content-Type`).
3. The browser sends the upload `id` to the endpoint that uses the file (for example `photoUploadId` on
   `createOrder`). That module calls `upload.Service.Attach` in its own transaction, which checks the owner, the
   purpose, that the blob exists, and that its type and size match; then it stores the object key.
4. Responses expose the file through `upload.Service.ReadURL(key)`: a read-only URL valid for one hour.

| Purpose            | Who can upload      | Used by                         |
| ------------------ | ------------------- | ------------------------------- |
| `order_photo`      | buyer               | F03 `createOrder`               |
| `trip_item_photo`  | jastiper            | F02 catalog items               |
| `shipment_proof`   | jastiper            | F05 `shipOrder`                 |
| `dispute_evidence` | buyer, jastiper     | F06 dispute evidence            |

Errors: 403 when the role cannot upload that purpose, 422 for type or size, 503 when storage is not configured
(`AZURE_STORAGE_CONNECTION_STRING` empty). Attaching an unknown, foreign, or already used upload returns 422
`Upload not found.`; attaching before the PUT returns 422 `File has not been uploaded yet.`

Signed URLs are built with `sas.BlobSignatureValues` directly: `blob.Client.GetSASURL` in azblob v1.8 signs an
escaped blob name for keys containing `/`. azblob stays on v1.8.0 because its service version (`2026-06-06`) is the
newest one Azurite 3.37 accepts.

## 5. Tooling and testing

- **Go 1.26** (`go.mod`, Dockerfile, CI).
- **Makefile** is the single entry point. sqlc, goose, and golangci-lint are pinned there and run through
  `go run pkg@version`. CI (`.github/workflows/check.yml`) calls `make check` rather than repeating the commands.
- **Local dependencies** run from `compose.yaml`: PostgreSQL, the API, and Azurite.
- **sqlc**: one entry in `sqlc.yaml` (`db/queries/` → `internal/db`). Generated code is committed; `make check` runs
  `sqlc diff` to catch drift.
- **goose**: sequential SQL files in `db/migrations/`, always with a `-- +goose Down`. Migrations must stay
  compatible with the previous release (expand, deploy, then contract), because old and new pods run side by side
  during a rollout.
- **Tests**:
  - Service tests with an in-memory repository (fast, no database), as in `internal/auth/service_test.go`.
  - Handler tests through `httpserver.New()` for status codes, auth, and problem bodies.
  - Integration tests against real PostgreSQL for the ledger, the state machine, and cross-module transactions.
    They skip unless `TEST_DATABASE_URL` is set; the upload test also needs `TEST_AZURE_STORAGE_CONNECTION_STRING`
    (Azurite from `compose.yaml`).
  - k6 load test in `monitoring/k6/`.

## 6. Delivery and infrastructure

| Item                                 | State | Notes                                                                                   |
| ------------------------------------ | ----- | --------------------------------------------------------------------------------------- |
| `main` → check → ACR image → AKS rollout | Done | `.github/workflows/deploy-aks.yml`, image tagged with the commit SHA                    |
| Check workflow on every non-`main` push | Done | `.github/workflows/check.yml`: `make check`, integration tests with PostgreSQL and Azurite services, OpenAPI artifact; reused by the deploy workflow |
| Secrets for `DATABASE_URL`, `JWT_SECRET` | Done | Secret `titipdong-api-secrets`, wired with `secretKeyRef`                             |
| 2 replicas with anti-affinity        | Done  | `deploy/k8s/deployment.yaml`                                                             |
| Readiness probe on `/health/ready`, `preStop` | Done | Graceful rollouts without dropped requests                     |
| Resource requests and limits         | Done  | 100m CPU / 128 Mi requested, 256 Mi memory limit                                         |
| Runtime env in the Deployment        | Done  | `PAYMENT_GATEWAY=fake`; optional `AZURE_STORAGE_CONNECTION_STRING` (Secret) and `CORS_ORIGINS` (ConfigMap `titipdong-api-config`) |
| Blob Storage account in Azure        | To do | Create the storage account and add its connection string to the Secret (`deploy/k8s/README.md`) |
| Frontend origin                      | To do | Set `CORS_ORIGINS` in the ConfigMap once the FE domain exists                            |

Migrations run at API startup under an advisory lock, which is safe with several replicas.

## 7. Not in scope

These are deliberately left out. Each can be added later without reshaping what exists.

| Item                          | Notes                                                                                                 |
| ----------------------------- | ----------------------------------------------------------------------------------------------------- |
| Midtrans                      | A `Gateway` adapter for Snap checkout, a signed `POST /payments/callback` deduplicated through a `payment_notifications` table, and the refund API. Production use also needs confirmation that holding funds and paying jastipers out manually is allowed (`titipdong-docs/FINANCE.md`) |
| Email                         | A mailer behind an interface, sent from the notification module                                       |
| Identity verification (KYC)   | The FE has `/profile/kyc` and `/admin/kyc` routes; no backend support                                 |
| Courier tracking provider     | Shipments store the courier and tracking number; the buyer checks status on the courier's site        |
| One user with several roles   | Would turn `users.role` into a `user_roles` table                                                     |
| Partial refunds               | A dispute decision refunds or releases the whole order                                                |
| Rate limiting                 | Left to the Ingress                                                                                   |

## 8. Adding a module (checklist)

1. Migration in `db/migrations/` for the module's tables (`make migration name=create_foo`).
2. Queries in `db/queries/<table>.sql` (reuse existing ones where possible); run `make sqlc` and commit the output.
3. `repository.go` (interface + pgx implementation), `service.go` (exported `Service`, domain types and errors),
   `handler.go` (`RegisterRoutes(api huma.API)`, tag = module name).
4. Wire it in `cmd/api/main.go` after `api.UseMiddleware(auth.Middleware(...))`.
5. Service tests with an in-memory repository, handler tests, and integration tests for anything touching money or
   order status.
6. Regenerate the FE client (`pnpm api` in `titipdong-fe`).
