# F06 Dispute

| Item    | Value              |
| ------- | ------------------ |
| Owner   | Barru              |
| Package | `internal/dispute` |
| Tag     | `dispute`          |
| Status  | Not started        |

## Overview

While an order is `shipped`, the buyer can open a dispute (item not received, not as described, damaged, …) with
evidence. The order becomes `disputed`, which stops auto-release. An admin reviews it and decides:

- `refund`: the escrow goes back to the buyer, order `refunded`.
- `release`: the claim is rejected, order `delivered` → funds released to the jastiper.

## Business rules

- Only the order's buyer can open a dispute, only while `shipped`, and an order can be disputed once.
- Evidence files use blob SAS uploads (`uploads.purpose = 'dispute_evidence'`).
- The decision is all-or-nothing (no partial refunds; see DATABASE.md note on `FINANCE.md`).
- The decision runs in **one transaction**: dispute update, `order.ResolveDispute`, `payment.Refund` or
  `payment.Release`, audit log.
- Buyer and jastiper of the order can read the dispute; others get 404.

## Database tables

Full DDL in [DATABASE.md](../DATABASE.md#8-f06-dispute).

```plantuml
@startuml
hide circle
skinparam linetype ortho

entity users <<F01>> {
  * id : UUID <<PK>>
}

entity orders <<F03>> {
  * id : UUID <<PK>>
  --
  * status : TEXT
}

entity disputes {
  * id : UUID <<PK>>
  --
  * order_id : UUID <<FK, UQ>>
  * opened_by : UUID <<FK>>
  * category : TEXT
  * description : TEXT
  * status : TEXT
  decision : TEXT
  decision_note : TEXT
  decided_by : UUID <<FK>>
  decided_at : TIMESTAMPTZ
  * created_at : TIMESTAMPTZ
  * updated_at : TIMESTAMPTZ
}

entity dispute_evidence {
  * id : UUID <<PK>>
  --
  * dispute_id : UUID <<FK>>
  * uploaded_by : UUID <<FK>>
  * file_key : TEXT
  note : TEXT
  * created_at : TIMESTAMPTZ
}

entity "dispute_messages (later)" as dispute_messages {
  * id : BIGINT <<PK identity>>
  --
  * dispute_id : UUID <<FK>>
  * author_id : UUID <<FK>>
  * body : TEXT
  * created_at : TIMESTAMPTZ
}

orders ||--o| disputes : "disputed in"
users ||--o{ disputes : "opened_by (buyer)"
users |o--o{ disputes : "decided_by (admin)"
disputes ||--o{ dispute_evidence : has
users ||--o{ dispute_evidence : uploads
disputes ||--o{ dispute_messages : has
users ||--o{ dispute_messages : writes
@enduml
```

### `disputes`

| Column          | Type          | Notes                                                             |
| --------------- | ------------- | ----------------------------------------------------------------- |
| `id`            | `UUID` PK     |                                                                   |
| `order_id`      | `UUID` FK     | Unique (one dispute per order)                                    |
| `opened_by`     | `UUID` FK     | Buyer                                                             |
| `category`      | `TEXT`        | `not_received`, `not_as_described`, `damaged`, `other`            |
| `description`   | `TEXT`        | ≤ 2000                                                            |
| `status`        | `TEXT`        | `open`, `under_review`, `resolved`                                |
| `decision`      | `TEXT`        | `refund` or `release`                                             |
| `decision_note` | `TEXT`        |                                                                   |
| `decided_by`    | `UUID` FK     | Admin                                                             |
| `decided_at`    | `TIMESTAMPTZ` | `resolved` ⇔ decision, decided_by, decided_at all set (CHECK)     |
| `created_at`, `updated_at` | `TIMESTAMPTZ` |                                                        |

### `dispute_evidence`

| Column        | Type        | Notes          |
| ------------- | ----------- | -------------- |
| `id`          | `UUID` PK   |                |
| `dispute_id`  | `UUID` FK   |                |
| `uploaded_by` | `UUID` FK   |                |
| `file_key`    | `TEXT`      | Blob key       |
| `note`        | `TEXT`      | ≤ 500          |
| `created_at`  | `TIMESTAMPTZ` |              |

### `dispute_messages` (after the initial release)

`id` (identity), `dispute_id`, `author_id`, `body` (≤ 2000), `created_at`.

## Endpoints

| Method | Path                              | operationId            | Auth                        | Description                              |
| ------ | --------------------------------- | ---------------------- | --------------------------- | ---------------------------------------- |
| `POST` | `/orders/{id}/dispute`            | `openDispute`          | `buyer` (owner)             | Body `{ category, description, evidence[] }` |
| `GET`  | `/orders/{id}/dispute`            | `getOrderDispute`      | order buyer/jastiper        | Dispute + evidence                       |
| `GET`  | `/admin/disputes`                 | `listDisputes`         | `admin`                     | Queue (open / under review first); cursor |
| `GET`  | `/admin/disputes/{id}`            | `getDispute`           | `admin`                     | Detail with order, evidence, timeline    |
| `POST` | `/admin/disputes/{id}/decision`   | `decideDispute`        | `admin`                     | Body `{ decision: refund\|release, note }` |

## Sequence diagrams

<details>
<summary><strong style="font-size:1.1em;">Open dispute</strong></summary>

---

```plantuml
@startuml
actor Pembeli
participant "dispute.Handler" as H
participant "dispute.Service" as S
participant "order.Service" as O
participant "notification.Service" as N
database PostgreSQL as DB

Pembeli -> H : POST /orders/{id}/dispute {category, description, evidence}
H -> S : Open(buyerID, orderID, params)
S -> DB : BEGIN
S -> O : MarkDisputed(orderID, buyerID)
alt not owner / status != shipped
  O --> S : error
  H --> Pembeli : 404 / 409
else
  S -> DB : INSERT disputes (open)
  alt order already disputed
    DB --> S : unique violation
    H --> Pembeli : 409
  end
  S -> DB : INSERT dispute_evidence (owned uploads only)
  S -> N : Notify(jastiper, admins: dispute.opened)
  S -> DB : COMMIT
  H --> Pembeli : 201 dispute
end
@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">Admin decision</strong></summary>

---

```plantuml
@startuml
actor Admin
participant "dispute.Handler" as H
participant "dispute.Service" as S
participant "order.Service" as O
participant "payment.Service" as P
database PostgreSQL as DB

Admin -> H : POST /admin/disputes/{id}/decision {decision, note}
H -> S : Decide(adminID, disputeID, decision, note)
S -> DB : BEGIN; SELECT dispute FOR UPDATE
alt already resolved
  H --> Admin : 409
else
  S -> DB : UPDATE disputes SET status = resolved, decision, decided_by, decided_at
  S -> O : ResolveDispute(orderID, decision)
  alt decision = refund
    O -> DB : status disputed → refunded
    S -> P : Refund(orderID, dispute_refund)
    P -> DB : ledger escrow → buyer_refundable, INSERT refunds
  else decision = release
    O -> DB : status disputed → delivered
    S -> P : Release(orderID)
    P -> DB : ledger escrow → jastiper + platform, order → released
  end
  S -> DB : INSERT audit_logs (dispute.decided)
  S -> DB : COMMIT
  H --> Admin : 200 dispute
end
@enduml
```

</details>

## To do

MVP scope is defined in [README.md](README.md#mvp-scope).

### MVP

- [ ] Migration `create_disputes` (`disputes` only; category + text description) and queries.
- [ ] `openDispute`, `getOrderDispute`, `listDisputes`, `getDispute`, `decideDispute`; depends on F03
      (`MarkDisputed`, `ResolveDispute`) and F04 (`Refund`, `Release`).
- [ ] Without auto-release, disputes are also how an admin releases funds when a buyer never confirms receipt.

### Post-MVP

- [ ] `dispute_evidence` with uploads via `platform/blob` (in the MVP, evidence is exchanged by chat or email).
- [ ] `dispute_messages` conversation.
- [ ] `platform/audit` writer for decisions (in the MVP, the trail is the dispute row, `order_events`, and the
      ledger).
- [ ] Decide whether partial refunds are needed (`FINANCE.md`).
