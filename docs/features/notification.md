# F07 Notification

| Item    | Value                   |
| ------- | ----------------------- |
| Owner   | Azfa                    |
| Package | `internal/notification` |
| Tag     | `notification`          |
| Status  | Not started             |

## Overview

Tells users about events on their orders (requested, approved, paid, shipped, disputed, released, …) through
**in-app notifications** and **email**. Sending happens in the worker process (`cmd/worker`), never inside an API
request.

`notification.Service.Notify(ctx, userID, event)` inserts a River job on the **caller's transaction** (`InsertTx`).
If the business change rolls back, no notification is sent; if it commits, the job is guaranteed to exist. The worker
then writes the in-app row and sends/records the email.

## Business rules

- Every module that notifies depends on `notification` (one-way); `notification` imports no feature package.
- `data` holds only IDs and display values the FE needs to render; no secrets.
- Email provider is behind an interface; development uses a logging implementation (Mailpit locally).
- Retries and attempt counts live in River's tables; `outbound_emails` records the delivery outcome.
- Users only see and mark their own notifications.

### Event types (initial)

| Type                  | Recipient          | Emitted by |
| --------------------- | ------------------ | ---------- |
| `order.requested`     | jastiper           | Order      |
| `order.approved`      | buyer              | Order      |
| `order.rejected`      | buyer              | Order      |
| `order.cancelled`     | buyer, jastiper    | Order      |
| `payment.received`    | buyer, jastiper    | Payment    |
| `order.shipped`       | buyer              | Shipment   |
| `order.released`      | jastiper           | Payment    |
| `order.refunded`      | buyer              | Payment    |
| `dispute.opened`      | jastiper, admins   | Dispute    |
| `dispute.resolved`    | buyer, jastiper    | Dispute    |

## Database tables

Full DDL in [DATABASE.md](../DATABASE.md#9-f07-notification).

```plantuml
@startuml
hide circle
skinparam linetype ortho

entity users <<F01>> {
  * id : UUID <<PK>>
  --
  * email : TEXT
}

entity notifications {
  * id : UUID <<PK>>
  --
  * user_id : UUID <<FK>>
  * type : TEXT
  * data : JSONB
  read_at : TIMESTAMPTZ
  * created_at : TIMESTAMPTZ
}

entity outbound_emails {
  * id : UUID <<PK>>
  --
  user_id : UUID <<FK>>
  * to_address : TEXT
  * template : TEXT
  * data : JSONB
  * status : TEXT
  provider_message_id : TEXT
  last_error : TEXT
  * created_at : TIMESTAMPTZ
  sent_at : TIMESTAMPTZ
}

entity "river_job (River)" as river_job {
  * id : BIGINT <<PK>>
  --
  * kind : TEXT
  * args : JSONB
  * state : TEXT
  * attempt : SMALLINT
}

users ||--o{ notifications : receives
users |o--o{ outbound_emails : "is sent"
river_job ..> notifications : "worker writes"
river_job ..> outbound_emails : "worker writes"
@enduml
```

### `notifications`

| Column       | Type          | Notes                                              |
| ------------ | ------------- | -------------------------------------------------- |
| `id`         | `UUID` PK     |                                                    |
| `user_id`    | `UUID` FK     |                                                    |
| `type`       | `TEXT`        | e.g. `order.approved`                              |
| `data`       | `JSONB`       | IDs/values for rendering                           |
| `read_at`    | `TIMESTAMPTZ` | `NULL` = unread                                    |
| `created_at` | `TIMESTAMPTZ` |                                                    |

Indexes: `(user_id, created_at DESC)`, partial `(user_id) WHERE read_at IS NULL` for the unread count.

### `outbound_emails`

| Column                | Type          | Notes                              |
| --------------------- | ------------- | ---------------------------------- |
| `id`                  | `UUID` PK     |                                    |
| `user_id`             | `UUID` FK     | Nullable                           |
| `to_address`          | `TEXT`        |                                    |
| `template`            | `TEXT`        |                                    |
| `data`                | `JSONB`       |                                    |
| `status`              | `TEXT`        | `queued`, `sent`, `failed`         |
| `provider_message_id` | `TEXT`        |                                    |
| `last_error`          | `TEXT`        |                                    |
| `created_at`, `sent_at` | `TIMESTAMPTZ` |                                  |

## Endpoints

| Method | Path                            | operationId              | Auth     | Description                    |
| ------ | ------------------------------- | ------------------------ | -------- | ------------------------------ |
| `GET`  | `/notifications`                | `listNotifications`      | Any role | Newest first; cursor           |
| `GET`  | `/notifications/unread-count`   | `getUnreadCount`         | Any role | `{ count }`                    |
| `POST` | `/notifications/{id}/read`      | `markNotificationRead`   | Any role | Owner only (else 404)          |
| `POST` | `/notifications/read-all`       | `markAllNotificationsRead` | Any role | Marks all unread as read     |

## Sequence diagrams

<details>
<summary><strong style="font-size:1.1em;">Notify from a business transaction</strong></summary>

---

```plantuml
@startuml
participant "order.Service\n(any caller)" as C
participant "notification.Service" as N
database PostgreSQL as DB
participant "Worker (River)" as W
participant "Email provider" as E

C -> DB : BEGIN
C -> DB : business change (e.g. UPDATE orders)
C -> N : Notify(ctx, userID, event)
N -> DB : River InsertTx(notify job) on ctx transaction
C -> DB : COMMIT
note over DB : rollback ⇒ job disappears too

W -> DB : fetch job
W -> DB : INSERT notifications
W -> DB : INSERT outbound_emails (queued)
W -> E : send(template, data)
alt sent
  E --> W : message id
  W -> DB : UPDATE outbound_emails SET status = sent
else error
  W -> DB : UPDATE status = failed, last_error
  W -> W : job returns error → River retries with backoff
end
@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">Read notifications</strong></summary>

---

```plantuml
@startuml
actor User
participant "notification.Handler" as H
database PostgreSQL as DB

User -> H : GET /notifications/unread-count
H -> DB : SELECT count(*) WHERE user_id = $1 AND read_at IS NULL
H --> User : 200 {count}
User -> H : GET /notifications?limit=20
H -> DB : SELECT … ORDER BY created_at DESC, id LIMIT 21
H --> User : 200 {items, nextCursor}
User -> H : POST /notifications/{id}/read
H -> DB : UPDATE … SET read_at = now() WHERE id = $1 AND user_id = $2
alt 0 rows
  H --> User : 404
else
  H --> User : 204
end
@enduml
```

</details>

## To do

MVP scope is defined in [README.md](README.md#mvp-scope). The MVP has no worker, so `Notify` inserts the in-app row
directly on the caller's transaction instead of enqueueing a River job. The signature stays the same, so callers do
not change when the worker arrives.

### MVP

- [ ] Migration `create_notifications` (`notifications` only) and queries.
- [ ] `Notify(ctx, userID, event)` inserting the row on the transaction in `ctx` (`database.WithTx`).
- [ ] `listNotifications`, `getUnreadCount`, `markAllNotificationsRead`. The FE polls the unread count.
- [ ] Event types for the MVP flow: `order.requested`, `order.approved`, `order.rejected`, `order.cancelled`,
      `payment.received`, `order.shipped`, `order.released`, `order.refunded`, `dispute.opened`, `dispute.resolved`.

### Post-MVP

- [ ] Platform: `cmd/worker` with River client and River migrations (PLAN section 3); move `Notify` to a River job.
- [ ] Email: choose the provider, `mailer` interface with a logging implementation, `outbound_emails`, templates
      (Indonesian copy, casual "kamu"), Mailpit in `compose.yaml`.
- [ ] `markNotificationRead` for a single notification.
