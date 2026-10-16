# F08 Admin

| Item    | Value            |
| ------- | ---------------- |
| Package | `internal/admin` |
| Tag     | `admin`          |
| Status  | To do            |

## Overview

Back-office for platform admins: dashboard statistics, every order and ledger transaction, user management
(suspend / reactivate), and the audit log. Dispute handling lives in F06 (`/admin/disputes`) and payout processing
in F04 (`/admin/payouts`); both share the `admin` tag in the API docs.

Admin **reads** with its own queries, JOINing any table it needs. It **writes** only through other modules'
services: user status through `auth.Service.SetStatus`, never by updating `users` directly. It only reads
`audit_logs`; the writer is `platform/audit`, used inside money and admin transactions.

## Business rules

- Every operation requires `auth.Security(auth.RoleAdmin)`.
- Admins are created only with `api create-admin`, never over HTTP.
- Suspending a user: login and refresh are rejected and their refresh tokens are revoked; an access token already
  issued works until it expires (≤ 15 minutes). Suspension and reactivation are written to `audit_logs`.
- An admin cannot suspend an admin (including themselves).
- Money figures come from ledger sums, never stored balances.

## Database tables

Admin owns **no tables**. It reads:

| Table                                            | Used for                                        |
| ------------------------------------------------ | ----------------------------------------------- |
| `users`                                          | User list, counts by role and status            |
| `orders`, `order_events`, `trips`                | Order list and counts per status, GMV           |
| `ledger_transactions`, `ledger_entries`, `ledger_accounts` | Transactions, escrow held, platform revenue, payable outstanding |
| `refunds`, `payouts`                             | Refunds and payouts in the transaction view, pending payout count |
| `disputes`                                       | Open dispute count                              |
| `audit_logs` (platform)                          | Audit log view                                  |

### `audit_logs` (platform, read here)

| Column        | Type                 | Notes                                           |
| ------------- | -------------------- | ----------------------------------------------- |
| `id`          | `BIGINT` identity PK | Append-only                                     |
| `actor_id`    | `UUID` FK            | `NULL` for system                               |
| `actor_type`  | `TEXT`               | `buyer`, `jastiper`, `admin`, `system`          |
| `action`      | `TEXT`               | See below                                       |
| `target_type` | `TEXT`               | `order`, `dispute`, `payout`, `user`            |
| `target_id`   | `TEXT`               |                                                 |
| `metadata`    | `JSONB`              | Amounts, reasons, previous values               |
| `request_id`  | `TEXT`               | From `X-Request-ID`                             |
| `created_at`  | `TIMESTAMPTZ`        |                                                 |

Actions: `order.released`, `order.refunded`, `dispute.decided`, `payout.paid`, `payout.rejected`, `user.suspended`,
`user.activated`.

Full DDL in [DATABASE.md](../DATABASE.md#10-platform).

## Endpoints

| Method  | Path                      | operationId             | Auth    | Description                                              |
| ------- | ------------------------- | ----------------------- | ------- | -------------------------------------------------------- |
| `GET`   | `/admin/stats`            | `getAdminStats`         | `admin` | Users by role/status, orders by status, GMV, escrow held, platform revenue, payable outstanding, open disputes, pending payouts |
| `GET`   | `/admin/orders`           | `listAdminOrders`       | `admin` | Every order; filter `status`, search by code; cursor     |
| `GET`   | `/admin/transactions`     | `listAdminTransactions` | `admin` | Ledger transactions with their entries; filter `kind`; cursor |
| `GET`   | `/admin/users`            | `listAdminUsers`        | `admin` | Search name/email, filter `role`/`status`; cursor        |
| `PATCH` | `/admin/users/{id}`       | `updateAdminUser`       | `admin` | Body `{ status: active\|suspended }`                     |
| `GET`   | `/admin/audit-logs`       | `listAuditLogs`         | `admin` | Filter `targetType`, `targetId`, `actorId`; cursor       |

An admin reads a single order through `GET /orders/{id}` (F03), which already allows admins.

## Sequence diagrams

<details>
<summary><strong style="font-size:1.1em;">Suspend a user</strong></summary>

---

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
alt user not found
  H --> Admin : 404
else target is an admin
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

</details>

<details>
<summary><strong style="font-size:1.1em;">Dashboard stats</strong></summary>

---

```plantuml
@startuml
actor Admin
participant "admin.Handler" as H
participant "admin.Service" as S
database PostgreSQL as DB

Admin -> H : GET /admin/stats
H -> S : Stats()
S -> DB : SELECT role, status, count(*) FROM users GROUP BY role, status
S -> DB : SELECT status, count(*), sum(total) FROM orders GROUP BY status
S -> DB : ledger balances of escrow, platform_revenue, jastiper_payable
S -> DB : SELECT count(*) FROM disputes WHERE status = 'open'
S -> DB : SELECT count(*), sum(amount) FROM payouts WHERE status = 'requested'
S --> H : Stats
H --> Admin : 200 stats
@enduml
```

</details>

## Remaining work

- [ ] `GET /admin/stats` (depends on F04 ledger summaries, F06 disputes, payouts).
- [ ] `GET /admin/orders`, `GET /admin/transactions`.
- [ ] `GET /admin/users` and `PATCH /admin/users/{id}` (depends on F01 `users.status` and `SetStatus`).
- [ ] `GET /admin/audit-logs` (depends on `platform/audit`).
