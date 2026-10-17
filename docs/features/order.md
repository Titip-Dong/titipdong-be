# F03 Order

| Item    | Value                                                                        |
| ------- | ---------------------------------------------------------------------------- |
| Package | `internal/order`                                                             |
| Tag     | `order`                                                                      |
| Status  | Partial: state machine, endpoints, notifications, capacity done; photo, trip inbox to do |

## Overview

A buyer requests an item on a jastiper's trip. The jastiper approves it with a price and fee (or rejects it), the
buyer pays (F04), the jastiper ships (F05), and the funds are released when the buyer confirms receipt.

Order owns the **state machine**: `orders.status` is changed only by this service. Other modules call explicit
methods (`MarkPaid`, `MarkShipped`, …), never a generic `SetStatus`.

## State machine

```plantuml
@startuml
[*] --> requested
requested --> approved : jastiper approves + sets price
requested --> rejected : jastiper rejects
requested --> cancelled : buyer cancels
approved --> paid : payment settled (F04)
approved --> cancelled : buyer cancels / payment deadline passed (system)
paid --> shipped : jastiper hands over (F05)
paid --> refunded : jastiper cancels after payment (F04 refund)
shipped --> delivered : buyer confirms receipt (F05) / 7 days without dispute (system)
shipped --> disputed : buyer opens dispute (F06)
disputed --> refunded : admin decides refund
disputed --> delivered : admin decides release
delivered --> released : funds released to jastiper (F04)
released --> [*]
refunded --> [*]
rejected --> [*]
cancelled --> [*]
@enduml
```

| Method            | From → To                            | Actor             | Called by                     |
| ----------------- | ------------------------------------ | ----------------- | ----------------------------- |
| `Create`          | – → `requested`                      | buyer             | Order handler                 |
| `Approve`         | `requested` → `approved`             | jastiper (owner)  | Order handler                 |
| `Reject`          | `requested` → `rejected`             | jastiper (owner)  | Order handler                 |
| `Cancel`          | `requested`/`approved` → `cancelled` | buyer (owner)     | Order handler                 |
| `ExpireUnpaid`    | `approved` → `cancelled`             | system            | Payment checkout, read path, job `orders.expire_unpaid` |
| `MarkPaid`        | `approved` → `paid`                  | system            | Payment                       |
| `MarkShipped`     | `paid` → `shipped`                   | jastiper (owner)  | Shipment                      |
| `MarkDelivered`   | `shipped` → `delivered`              | buyer (owner) / system | Shipment, auto-release job |
| `MarkDelivered`   | `disputed` → `delivered`             | admin             | Dispute (release decision)    |
| `MarkDisputed`    | `shipped` → `disputed`               | buyer (owner)     | Dispute                       |
| `MarkReleased`    | `delivered` → `released`             | system            | Payment                       |
| `MarkRefunded`    | `paid` → `refunded`                  | jastiper (owner)  | Payment (jastiper cancel)     |
| `MarkRefunded`    | `disputed` → `refunded`              | admin             | Payment (dispute refund)      |

## Business rules

- Buyer and jastiper must differ. `jastiper_id` is copied from the trip for access checks.
- Creating an order reserves `quantity` items of trip capacity (`trip.Reserve`). Reject, cancel, expiry, and refund
  release it (`trip.Release`). Delivered and released orders keep it: the item was carried.
- On approval: `total = item_price × quantity + jastip_fee`, all integer rupiah, computed only in the backend.
  `payment_deadline = now + 24h`.
- An `approved` order past its `payment_deadline` is cancelled by the system: by the `orders.expire_unpaid` job, when
  the order is read, and when checkout is attempted.
- Every transition, in **one transaction**: lock the order (`SELECT … FOR UPDATE`), check the transition table and
  the actor, update `status`, insert `order_events`, release capacity where needed, and notify (F07).
- Ownership: a buyer sees only their orders, a jastiper only orders on their trips, an admin every order. Others get
  404.
- The product photo is optional, an upload with purpose `order_photo`.

### Notifications sent

| Transition                       | Type              | Recipient        |
| -------------------------------- | ----------------- | ---------------- |
| → `requested`                    | `order.requested` | jastiper         |
| → `approved`                     | `order.approved`  | buyer            |
| → `rejected`                     | `order.rejected`  | buyer            |
| → `cancelled` (buyer)            | `order.cancelled` | jastiper         |
| → `cancelled` (payment deadline) | `order.cancelled` | buyer, jastiper  |

Payment, Shipment, and Dispute send the notifications for the transitions they drive.

## Database tables

Full DDL in [DATABASE.md](../DATABASE.md#5-f03-order).

```plantuml
@startuml
hide circle
skinparam linetype ortho

entity users <<F01>> {
  * id : UUID <<PK>>
}

entity trips <<F02>> {
  * id : UUID <<PK>>
  --
  * jastiper_id : UUID <<FK>>
}

entity orders {
  * id : UUID <<PK>>
  --
  * code : TEXT <<UQ>>
  * trip_id : UUID <<FK>>
  * buyer_id : UUID <<FK>>
  * jastiper_id : UUID <<FK>>
  * product_name : TEXT
  product_url : TEXT
  photo_key : TEXT
  * quantity : INTEGER
  notes : TEXT
  item_price : BIGINT
  jastip_fee : BIGINT
  total : BIGINT
  * status : TEXT
  status_reason : TEXT
  payment_deadline : TIMESTAMPTZ
  * created_at : TIMESTAMPTZ
  * updated_at : TIMESTAMPTZ
}

entity order_events {
  * id : BIGINT <<PK identity>>
  --
  * order_id : UUID <<FK>>
  from_status : TEXT
  * to_status : TEXT
  actor_id : UUID <<FK>>
  * actor_type : TEXT
  reason : TEXT
  * created_at : TIMESTAMPTZ
}

trips ||--o{ orders : receives
users ||--o{ orders : "buyer_id"
users ||--o{ orders : "jastiper_id"
orders ||--o{ order_events : "status history"
users |o--o{ order_events : actor
@enduml
```

### `orders`

| Column             | Type          | Notes                                                    |
| ------------------ | ------------- | -------------------------------------------------------- |
| `id`               | `UUID` PK     | UUIDv7                                                   |
| `code`             | `TEXT`        | Unique human-friendly reference, e.g. `TD-7K3P9Q`         |
| `trip_id`          | `UUID` FK     | → `trips.id`                                             |
| `buyer_id`         | `UUID` FK     | → `users.id`                                             |
| `jastiper_id`      | `UUID` FK     | → `users.id`, `<> buyer_id`                              |
| `product_name`     | `TEXT`        | ≤ 200                                                    |
| `product_url`      | `TEXT`        | ≤ 2000, default `''`                                     |
| `photo_key`        | `TEXT`        | Blob key, nullable (to add)                              |
| `quantity`         | `INTEGER`     | `> 0`; also the trip capacity this order reserves        |
| `notes`            | `TEXT`        | ≤ 1000                                                   |
| `item_price`       | `BIGINT`      | Per unit, set on approval                                |
| `jastip_fee`       | `BIGINT`      | Whole order, set on approval                             |
| `total`            | `BIGINT`      | `= item_price × quantity + jastip_fee`                   |
| `status`           | `TEXT`        | See state machine (lowercase)                            |
| `status_reason`    | `TEXT`        | Rejection / cancellation reason                          |
| `payment_deadline` | `TIMESTAMPTZ` | Set on approval                                          |
| `created_at`, `updated_at` | `TIMESTAMPTZ` |                                                  |

### `order_events` (append-only)

| Column        | Type          | Notes                                          |
| ------------- | ------------- | ---------------------------------------------- |
| `id`          | `BIGINT` identity PK |                                         |
| `order_id`    | `UUID` FK     |                                                |
| `from_status` | `TEXT`        | `NULL` for the creating event                  |
| `to_status`   | `TEXT`        |                                                |
| `actor_id`    | `UUID` FK     | `NULL` when the system acted                   |
| `actor_type`  | `TEXT`        | `buyer`, `jastiper`, `admin`, `system`         |
| `reason`      | `TEXT`        |                                                |
| `created_at`  | `TIMESTAMPTZ` | Timeline timestamps (paid at, shipped at, …)   |

## Endpoints

| Method | Path                        | operationId          | Auth                        | Status | Description                                  |
| ------ | --------------------------- | -------------------- | --------------------------- | ------ | -------------------------------------------- |
| `POST` | `/trips/{slug}/orders`      | `createOrder`        | `buyer`                     | Done   | Request an item on a trip                    |
| `GET`  | `/orders`                   | `listOrders`         | `buyer`, `jastiper`         | Done   | Caller's orders (view depends on role); filter `status`; cursor |
| `GET`  | `/orders/{id}`              | `getOrder`           | owner buyer/jastiper, admin | Done   | Detail + event timeline                      |
| `POST` | `/orders/{id}/approve`      | `approveOrder`       | `jastiper` (owner)          | Done   | Body `{ itemPrice, jastipFee }`              |
| `POST` | `/orders/{id}/reject`       | `rejectOrder`        | `jastiper` (owner)          | Done   | Body `{ reason }`                            |
| `POST` | `/orders/{id}/cancel`       | `cancelOrder`        | `buyer` (owner)             | Done   | Before payment only                          |
| `GET`  | `/me/trips/{id}/orders`     | `listTripOrders`     | `jastiper` (owner)          | To do  | Jastiper inbox for one trip; filter `status`; cursor |

`createOrder` gains an optional `photoUploadId`; order responses gain a signed `photoUrl`.

Invalid transitions return `409 Conflict`; a full trip returns `409` with `Trip is full.`

## Sequence diagrams

<details>
<summary><strong style="font-size:1.1em;">Create order</strong></summary>

---

```plantuml
@startuml
actor Pembeli
participant "order.Handler" as H
participant "order.Service" as S
participant "trip.Service" as T
participant "notification.Service" as N
database PostgreSQL as DB

Pembeli -> H : POST /trips/{slug}/orders (Bearer, buyer)
H -> S : Create(buyerID, slug, params)
S -> DB : BEGIN
S -> T : Reserve(tripID, quantity)
T -> DB : SELECT trip FOR UPDATE, UPDATE reserved_capacity
alt closed or full
  T --> S : ErrTripClosed / ErrCapacityFull
  S -> DB : ROLLBACK
  S --> H : error
  H --> Pembeli : 409
end
S -> DB : INSERT orders (status = requested)
S -> DB : INSERT order_events (null → requested)
S -> DB : attach photo upload (if any)
S -> N : Notify(jastiperID, order.requested)
N -> DB : INSERT notifications
S -> DB : COMMIT
H --> Pembeli : 201 order
@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">Approve order</strong></summary>

---

```plantuml
@startuml
actor Jastiper
participant "order.Handler" as H
participant "order.Service" as S
participant "notification.Service" as N
database PostgreSQL as DB

Jastiper -> H : POST /orders/{id}/approve {itemPrice, jastipFee}
H -> S : Approve(jastiperID, orderID, price, fee)
S -> DB : BEGIN; SELECT order FOR UPDATE
alt not owner
  S --> H : ErrNotFound
  H --> Jastiper : 404
else status != requested
  S --> H : ErrInvalidTransition
  H --> Jastiper : 409
else ok
  S -> S : total = itemPrice × qty + jastipFee
  S -> DB : UPDATE orders SET status = approved, prices, payment_deadline = now + 24h
  S -> DB : INSERT order_events
  S -> N : Notify(buyerID, order.approved)
  S -> DB : COMMIT
  H --> Jastiper : 200 order
end
@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">Expire unpaid orders (job)</strong></summary>

---

```plantuml
@startuml
participant "platform/jobs" as J
participant "order.Service" as S
participant "trip.Service" as T
participant "notification.Service" as N
database PostgreSQL as DB

J -> DB : pg_try_advisory_lock(orders.expire_unpaid)
J -> S : ExpireUnpaid(ctx)
S -> DB : SELECT id FROM orders\nWHERE status = 'approved' AND payment_deadline < now()
loop each order, own transaction
  S -> DB : BEGIN; SELECT order FOR UPDATE
  alt no longer approved (paid or cancelled meanwhile)
    S -> DB : COMMIT (no-op)
  else
    S -> DB : status = cancelled, reason = payment deadline passed,\nINSERT order_events (actor_type = system)
    S -> T : Release(tripID, quantity)
    S -> N : Notify(buyer + jastiper, order.cancelled)
    S -> DB : COMMIT
  end
end
@enduml
```

</details>

## Remaining work

Done:

- [x] `orders` and `order_events`, the full transition table, and the explicit methods.
- [x] Order code generator (`TD-XXXXXX`, unique, retry on conflict).
- [x] `createOrder`, `listOrders`, `getOrder` (with timeline), `approveOrder`, `rejectOrder`, `cancelOrder`.
- [x] Unpaid orders past the deadline are cancelled on read and on checkout.
- [x] Notifications (F07) inside each transition's transaction, including the expiry on read.
- [x] Capacity: `trip.Reserve` on create, `trip.Release` on reject, cancel, expiry, and refund.

Remaining:

- [ ] Transition `shipped → delivered` by the system, for auto-release.
- [ ] `photo_key` column, `photoUploadId` on create, `photoUrl` in responses.
- [ ] `GET /me/trips/{id}/orders`.
- [ ] Job `orders.expire_unpaid`.
- [ ] Integration test for concurrent transitions on the same order.
