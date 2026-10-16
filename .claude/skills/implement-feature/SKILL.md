---
name: implement-feature
description: Implement or extend a backend feature (endpoint, service method, job) in titipdong-be following the module layout and the spec in docs/features. Use whenever adding or changing an endpoint, service, repository, or background job.
---

# Implement a feature

The spec is `docs/features/<module>.md` (rules, endpoints, "Remaining work") plus `docs/PLAN.md` (architecture, scope
decisions) and `docs/DATABASE.md` (tables). Code that disagrees with the spec is a bug in one of them: stop and say
which, do not silently pick one.

## 1. Ground yourself before writing code

1. Read the module's doc section for the item you are building. Note the exact `operationId`, path, auth role, body
   fields, status codes, and business rules.
2. Read the module's existing `handler.go`, `service.go`, `repository.go`, and one test file. Copy their shape.
3. Before calling any function, type, query, or column, confirm it exists:
   - `grep -rn "func (s \*service) Name\|func Name" internal/`
   - generated queries: `grep -n "func (q \*Queries)" internal/db/*.go`
   - columns: the migration in `db/migrations/`, not `docs/DATABASE.md` (the doc may describe columns not migrated yet).
   If it does not exist, create it (step 2) or say so. Never assume a helper exists because a similar module has one.
4. Out of scope (do not build, see `docs/PLAN.md` §7): Midtrans, email, KYC, courier tracking, multi-role users,
   partial refunds, rate limiting, a separate worker/queue.

## 2. Build in this order

1. **Schema**, if needed: use the `db-change` skill (migration → `db/queries/*.sql` → `make sqlc`).
2. **Repository** (`repository.go`):
   - Add the method to the `repository` interface; implement on `pgRepository` using `r.q(ctx)`
     (`db.New(database.Conn(ctx, r.pool))`) so it joins the transaction in `ctx`.
   - Map driver errors to domain errors here: `pgx.ErrNoRows` → `ErrNotFound` (see `notFound` helpers),
     `23505` unique violation → a sentinel like `ErrSlugTaken`.
   - Return domain types, never `db.*` types.
3. **Service** (`service.go`):
   - Business rules live here. Add the method to the exported `Service` interface.
   - Validation failures return `&InputError{Detail: "..."}` (user-safe English sentence ending in a period).
   - Ownership mismatch returns `ErrNotFound`, not a 403.
   - Multi-step writes go inside `s.repo.WithTx(ctx, func(ctx context.Context) error { ... })`. Use the `ctx` passed
     into the closure for every call inside it.
   - Time comes from `s.now()`, IDs from `uuid.Must(uuid.NewV7())`.
   - Anything touching order status, the ledger, refunds, or payouts: use the `money-and-order-state` skill.
4. **Handler** (`handler.go`):
   - Register with `huma.Register(api, huma.Operation{OperationID, Method, Path, Summary, Tags: []string{tag},
     Security: auth.Security(roles...), DefaultStatus, Errors: []int{...}}, h.method)`.
   - `OperationID` and path exactly as in the doc table. JSON fields are camelCase; validation via struct tags
     (`minLength`, `maxLength`, `minimum`, `maximum`, `pattern`, `enum`, `format`).
   - Get the caller with `p, _ := auth.PrincipalFrom(ctx)`.
   - Convert errors only in the module's `mapError(err)`: `InputError` → 422, `pagination.ErrInvalidCursor` → 422,
     `ErrNotFound` → 404, invalid state → 409. Unknown errors pass through unchanged (they become a logged 500).
   - Lists: input `limit` (default 20, max 100) + `cursor`; output `{ items, nextCursor }`; fetch `limit+1` rows and
     trim with `pagination.Page`.
   - Responses are explicit DTO structs with `toXxxResponse` mappers; money is `int64` rupiah.
5. **Wiring**: new services/handlers go in `cmd/api/main.go` after `api.UseMiddleware(auth.Middleware(...))`.
   Respect the dependency direction in `docs/PLAN.md` §3.2; an import cycle means the call belongs in the other module.
6. **Background job** (only via `platform/jobs`): an idempotent sweep, one transaction per item, re-check state after
   locking, `order.System` as the actor.

## 3. Tests (required, same change)

- Service: in-memory `fakeRepository` in `service_test.go` (mutex + maps; implement every interface method, including
  `WithTx` that just calls `fn(ctx)`). Inject time with a fixed `now`.
- Handler: `httpserver.New()` + `auth.Middleware` with a `fakeAuth` that maps tokens like `"buyer"` / `"jastiper"` to
  principals; assert status codes for success, 401, 403, 404, 409, 422 as listed in the doc.
- Money / order state / cross-module transactions: an integration test that skips without `TEST_DATABASE_URL`
  (pattern: `internal/payment/integration_test.go`).

## 4. Finish

Use the `finish-and-commit` skill: `make check`, integration tests, docs updated, commit.
