---
name: db-change
description: Add or change database schema or SQL queries in titipdong-be (goose migration, db/queries, sqlc regeneration). Use whenever a table, column, index, constraint, or query is added or changed.
---

# Database change

## Rules

- Never edit an existing migration file that is already on `main`; add a new one. Never edit `internal/db/`.
- Create migrations only with `make migration name=<verb>_<thing>` (e.g. `add_trip_capacity`, `create_disputes`).
  Every file has `-- +goose Up` and a working `-- +goose Down`. Wrap functions/DO blocks in
  `-- +goose StatementBegin` / `-- +goose StatementEnd`.
- **Expand-only**: the previous release must keep working against the new schema (old and new pods run together).
  New `NOT NULL` columns need a `DEFAULT` or a backfill in the same migration; never drop or rename a column that the
  deployed code still reads.
- Conventions (from `docs/DATABASE.md` §1):
  - PK `UUID` (app-generated v7); append-only/event tables use `BIGINT GENERATED ALWAYS AS IDENTITY`.
  - Money `BIGINT` whole rupiah with `CHECK (... >= 0)` or `> 0`. No `NUMERIC`, no floats.
  - Status `TEXT` + `CHECK (status IN (...))`, lowercase values. No PostgreSQL enums.
  - `TEXT` with `CHECK (char_length(x) <= n)` matching the API limit.
  - `created_at` / `updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`; `updated_at` is set by the application.
  - Files are stored as blob object keys (`*_key`), never URLs.
  - Foreign keys everywhere a column references another table. No soft delete.
  - List indexes match the keyset order, e.g. `(owner_id, created_at DESC, id DESC)`.
- The design in `docs/DATABASE.md` is the target. If the migration must differ, update the doc in the same change.

## Queries

- One file per table or topic in `db/queries/` (`trips.sql`, `orders.sql`, …). Search first:
  `grep -n "name:" db/queries/*.sql` — reuse a query when one fits.
- Name them `VerbNoun` (`GetTripByID`, `ListOpenTrips`, `LockOrder`, `InsertLedgerEntry`). Annotations: `:one`,
  `:many`, `:exec`, `:execrows` (use `:execrows` when the caller must know if a conditional UPDATE matched).
- Named params via `sqlc.arg(name)` with explicit casts (`::timestamptz`, `::uuid`, `::text`); `sqlc.embed(t)` for
  joined rows; `sqlc.narg` for optional filters.
- Row locks: `SELECT ... FOR UPDATE` (or `FOR UPDATE OF o` with joins) in a dedicated `Lock...` query.
- Keyset pagination: `WHERE (sort_col, id) < (sqlc.arg(before_x), sqlc.arg(before_id)) ORDER BY sort_col DESC, id DESC
  LIMIT sqlc.arg(row_limit)` (or `>` for ascending).

## Steps

1. `make migration name=...`, write Up and Down.
2. Edit `db/queries/<table>.sql`.
3. `make sqlc` and read the generated signatures you will call (`grep -n "func (q \*Queries) Name" internal/db/`).
4. Apply locally: `make db-up` (the API applies migrations on start) or `make run`; check with `make db-shell` →
   `\d table`.
5. Test the Down: `go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir db/migrations postgres "$DATABASE_URL" down`
   then start the API again to re-apply.
6. Update the table's section and marker in `docs/DATABASE.md` (`to add` → `exists`) and the module doc table.
7. Commit the migration, the query file, and the regenerated `internal/db/` together. `make check` fails on drift.
