# F08 Admin

| Item    | Value            |
| ------- | ---------------- |
| Owner   | Azfa             |
| Package | `internal/admin` |
| Tag     | `admin`          |
| Status  | Not started      |

## Overview

Back-office for platform admins: platform statistics, transaction monitoring, user management (suspend/activate), and
the audit log. Dispute handling lives in F06 under `/admin/disputes`.

Admin **reads** with its own queries, JOINing any table it needs. It **writes** only through other modules'
services: user status through `auth.Service`, never by updating `users` directly. It only reads `audit_logs`; the
writer is `platform/audit`, used inside money/admin transactions (this avoids an `admin ↔ payment` import cycle).

## Business rules

- Every operation requires `auth.Security(auth.RoleAdmin)`.
- Admins are created only by the `create-admin` CLI (F01), never over HTTP.
- Suspending a user: login and refresh are rejected; an existing access token still works until it expires (≤ 15
  minutes). Suspension is written to `audit_logs`.
- An admin cannot suspend themselves or another admin.
- Money figures come from ledger sums, not stored balances.

## Database tables

Admin owns **no tables**. The diagram shows what it reads (and, through `auth.Service`, the one thing it changes:
`users.status`).

```plantuml
@startuml
hide circle
skinparam linetype ortho

entity users <<F01>> {
  * id : UUID <<PK>>
  --
  * name : VARCHAR(100)
  * email : TEXT
  * role : VARCHAR(16)
  * status : TEXT
}

entity orders <<F03>> {
  * id : UUID <<PK>>
  --
  * buyer_id : UUID <<FK>>
  * jastiper_id : UUID <<FK>>
  total : BIGINT
  * status : TEXT
}

entity payments <<F04>> {
  * id : UUID <<PK>>
  --
  * order_id : UUID <<FK>>
  * amount : BIGINT
  * status : TEXT
}

entity ledger_accounts <<F04>> {
  * id : UUID <<PK>>
  --
  * kind : TEXT
  user_id : UUID <<FK>>
}

entity ledger_entries <<F04>> {
  * id : BIGINT <<PK>>
  --
  * account_id : UUID <<FK>>
  order_id : UUID <<FK>>
  * direction : TEXT
  * amount : BIGINT
}

entity disputes <<F06>> {
  * id : UUID <<PK>>
  --
  * order_id : UUID <<FK>>
  * status : TEXT
}

entity audit_logs <<Platform>> {
  * id : BIGINT <<PK identity>>
  --
  actor_id : UUID <<FK>>
  * actor_type : TEXT
  * action : TEXT
  * target_type : TEXT
  * target_id : TEXT
  * metadata : JSONB
  request_id : TEXT
  * created_at : TIMESTAMPTZ
}

users ||--o{ orders : "buyer / jastiper"
orders ||--o{ payments : "paid by"
ledger_accounts ||--o{ ledger_entries : posts
orders |o--o{ ledger_entries : "escrow per order"
orders ||--o| disputes : "disputed in"
users |o--o{ audit_logs : "acts in"
@enduml
```

Tables read:

| Table                                   | Used for                                   |
| --------------------------------------- | ------------------------------------------ |
| `users`                                 | User list, status                          |
| `orders`, `order_events`                | Order counts per status, GMV               |
| `payments`, `ledger_entries`, `ledger_accounts` | Transactions, escrow balance, platform revenue |
| `disputes`                              | Open dispute count                         |
| `audit_logs` (platform)                 | Audit log view                             |

### `audit_logs` (platform, read here)

| Column        | Type          | Notes                                           |
| ------------- | ------------- | ----------------------------------------------- |
| `id`          | `BIGINT` identity PK | Append-only                              |
| `actor_id`    | `UUID` FK     | `NULL` for system                               |
| `actor_type`  | `TEXT`        | `buyer`, `jastiper`, `admin`, `system`          |
| `action`      | `TEXT`        | e.g. `order.released`, `user.suspended`         |
| `target_type` | `TEXT`        | e.g. `order`, `dispute`, `user`                 |
| `target_id`   | `TEXT`        |                                                 |
| `metadata`    | `JSONB`       |                                                 |
| `request_id`  | `TEXT`        | From `X-Request-ID`                             |
| `created_at`  | `TIMESTAMPTZ` |                                                 |

Full DDL in [DATABASE.md](../DATABASE.md#10-platform).

## Endpoints

| Method  | Path                    | operationId           | Auth    | Description                                              |
| ------- | ----------------------- | --------------------- | ------- | -------------------------------------------------------- |
| `GET`   | `/admin/stats`          | `getAdminStats`       | `admin` | Users, orders per status, escrow held, revenue           |
| `GET`   | `/admin/transactions`   | `listAdminTransactions` | `admin` | Payments / ledger transactions; filters; cursor        |
| `GET`   | `/admin/users`          | `listAdminUsers`      | `admin` | Search by name/email, filter role/status; cursor         |
| `PATCH` | `/admin/users/{id}`     | `updateAdminUser`     | `admin` | Body `{ status: active\|suspended }`                     |
| `GET`   | `/admin/audit-logs`     | `listAuditLogs`       | `admin` | Filter by target, actor, time; cursor                    |

Dispute endpoints (`/admin/disputes…`) are documented in [dispute.md](dispute.md).

## Sequence diagrams

### Suspend a user

```plantuml
@startuml
actor Admin
participant "auth.Middleware" as M
participant "admin.Handler" as H
participant "admin.Service" as S
participant "auth.Service" as A
database PostgreSQL as DB

Admin -> M : PATCH /admin/users/{id} {status: suspended}
M -> M : require role admin
M -> H : next(ctx + Principal)
H -> S : SetUserStatus(adminID, userID, suspended)
S -> DB : BEGIN
S -> A : SetStatus(userID, suspended)
A -> DB : SELECT user FOR UPDATE
alt target is admin or self
  A --> S : InputError
  H --> Admin : 422
else
  A -> DB : UPDATE users SET status = suspended, updated_at
  A -> DB : UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1
  S -> DB : INSERT audit_logs (user.suspended)
  S -> DB : COMMIT
  H --> Admin : 200 user
end
@enduml
```

### Dashboard stats

```plantuml
@startuml
actor Admin
participant "admin.Handler" as H
participant "admin.Service" as S
database PostgreSQL as DB

Admin -> H : GET /admin/stats
H -> S : Stats()
S -> DB : SELECT count(*) FROM users GROUP BY role, status
S -> DB : SELECT status, count(*) FROM orders GROUP BY status
S -> DB : SELECT sum(credit) - sum(debit) FROM ledger_entries\nJOIN ledger_accounts WHERE kind IN (escrow, platform_revenue)
S -> DB : SELECT count(*) FROM disputes WHERE status <> resolved
S --> H : Stats
H --> Admin : 200 stats
@enduml
```

## To do

- [ ] Depends on F01 account status (`users.status`, `auth.Service.SetStatus`) and `platform/audit`.
- [ ] Decide KYC scope (PLAN section 7, decision 2); the FE has `/admin/kyc`.
- [ ] Read queries for stats, transactions, users, audit logs.
- [ ] Endpoints above with cursor pagination.
- [ ] Revoke the user's refresh tokens on suspension (once F01 refresh tokens exist).
