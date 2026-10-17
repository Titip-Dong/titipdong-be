# Feature documentation

One file per backend module: overview, business rules, database tables, endpoints, sequence diagrams, and remaining
work. For a module that is already built, its migration and the generated OpenAPI spec (`/openapi.json`) are the
source of truth, and its file is kept in sync with them.

| ID  | Module                              | Package                 | Status                                                                 |
| --- | ----------------------------------- | ----------------------- | ---------------------------------------------------------------------- |
| F01 | [Auth & User](auth.md)              | `internal/auth`         | Partial: session done; profile, account status, `create-admin` to do   |
| F02 | [Trip & Catalog](trip.md)           | `internal/trip`         | Partial: core and capacity done; edit, cancel, catalog to do          |
| F03 | [Order](order.md)                   | `internal/order`        | Partial: state machine, notifications, capacity done; photo to do     |
| F04 | [Payment & Escrow](payment.md)      | `internal/payment`      | Partial: checkout, ledger, release, refunds, payouts done; wallet history to do |
| F05 | [Shipment](shipment.md)             | `internal/shipment`     | Partial: ship, confirm, auto-release done; proof photo to do           |
| F06 | [Dispute](dispute.md)               | `internal/dispute`      | Done                                                                   |
| F07 | [Notification](notification.md)     | `internal/notification` | Done                                                                   |
| F08 | [Admin](admin.md)                   | `internal/admin`        | To do                                                                  |

Scope decisions (fake payment gateway, in-process jobs, Azure Blob uploads, in-app notifications only) and what is
deliberately left out are in [PLAN.md](../PLAN.md#2-scope-decisions).

## Business flow

The main flow runs `requested → approved → paid → shipped → delivered → released`, with these exits:

- The jastiper rejects a request, or the buyer cancels before paying.
- The payment deadline passes and the order is cancelled.
- The jastiper cancels after payment, and the buyer is refunded.
- The buyer opens a dispute, and an admin decides between refund and release.
- The buyer never confirms receipt, and the funds are released automatically after 7 days.

The diagrams below cross modules. Per-module detail is in each feature file.

<details>
<summary><strong style="font-size:1.1em;">1. Session: register, login, refresh, logout</strong></summary>

---

```plantuml
@startuml session

title Flow Session (F01)

actor       "User"              as user
participant "SPA"               as fe
participant "API\nauth"         as auth
database    "PostgreSQL"        as db

autonumber
== Register / login ==
user -> fe      : Fill register or login form
fe -> auth      : POST /auth/register or /auth/login
auth -> db      : INSERT users / SELECT user by email
db --> auth     : User
auth -> auth    : Verify password and status (login), issue JWT (15 min)
auth -> db      : INSERT refresh_tokens (new family)
auth --> fe     : 201/200 {accessToken, user}\nSet-Cookie: refresh_token (HttpOnly, Path=/auth)
fe --> user     : Show home

== App reload / access token expired ==
fe -> auth      : POST /auth/refresh (cookie)
auth -> db      : SELECT refresh_tokens by hash FOR UPDATE

alt valid and user active
    auth -> db      : Revoke old, INSERT new (same family)
    auth --> fe     : 200 {accessToken, user} + new cookie
else revoked (reuse) / expired / unknown / suspended
    auth -> db      : Revoke whole family (reuse only)
    auth --> fe     : 401 + clear cookie
    fe --> user     : Redirect to login
end

== Logout ==
user -> fe      : Press 'Keluar'
fe -> auth      : POST /auth/logout (cookie)
auth -> db      : Revoke family
auth --> fe     : 204 + clear cookie
fe --> user     : Redirect to login

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">2. Trip: jastiper opens a trip, buyer browses</strong></summary>

---

```plantuml
@startuml trip

title Flow Trip (F02)

actor       "Jastiper"          as jas
actor       "Pembeli"           as buyer
participant "SPA"               as fe
participant "API\ntrip"         as trip
participant "Blob Storage"      as blob
database    "PostgreSQL"        as db

autonumber
== Open trip ==
jas -> fe       : Fill trip form (destination, dates, deadline, capacity)
fe -> trip      : POST /trips (Bearer, jastiper)
trip -> trip    : Validate dates, deadline, capacity; generate slug
trip -> db      : INSERT trips (status = open)
trip --> fe     : 201 trip

== Catalog ==
jas -> fe       : Add a popular item with photo
fe -> trip      : POST /uploads {purpose: trip_item_photo}
trip --> fe     : 201 {uploadId, uploadUrl}
fe -> blob      : PUT photo (SAS URL)
fe -> trip      : POST /trips/{id}/items {name, referencePrice, photoUploadId}
trip -> db      : INSERT trip_items, attach upload
trip --> fe     : 201 item

== Browse ==
buyer -> fe     : Search by city / dates
fe -> trip      : GET /trips?city=…&from=…&to=…
trip -> db      : SELECT open trips before request_deadline
trip --> fe     : 200 {items, nextCursor}
fe -> trip      : GET /trips/{slug}
trip --> fe     : 200 trip + catalog + remaining capacity
fe --> buyer    : Show trip detail + 'Titip' button

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">3. Order: request, approve / reject, cancel</strong></summary>

---

```plantuml
@startuml order

title Flow Order Request & Approval (F03)

actor       "Pembeli"           as buyer
actor       "Jastiper"          as jas
participant "SPA"               as fe
participant "API\norder"        as order
participant "API\ntrip"         as trip
participant "API\nnotification" as notif
database    "PostgreSQL"        as db

autonumber
== Request ==
buyer -> fe     : Fill request (product, link, qty, notes, photo)
fe -> order     : POST /trips/{slug}/orders (buyer)
order -> db     : BEGIN
order -> trip   : Reserve(tripID, quantity)

alt trip closed, deadline passed, or not enough capacity
    trip --> order  : ErrTripClosed / ErrCapacityFull
    order -> db     : ROLLBACK
    order --> fe    : 409
else reserved
    order -> db     : INSERT orders (requested), order_events
    order -> notif  : Notify(jastiper, order.requested)
    notif -> db     : INSERT notifications
    order -> db     : COMMIT
    order --> fe    : 201 order
end

== Jastiper decides ==
jas -> fe       : Open the trip inbox (GET /me/trips/{id}/orders)

alt approve
    fe -> order     : POST /orders/{id}/approve {itemPrice, jastipFee}
    order -> db     : BEGIN; SELECT order FOR UPDATE
    order -> order  : total = itemPrice × qty + jastipFee,\npayment_deadline = now + 24h
    order -> db     : status = approved, order_events
    order -> notif  : Notify(buyer, order.approved)
    order -> db     : COMMIT
else reject
    fe -> order     : POST /orders/{id}/reject {reason}
    order -> trip   : Release(tripID, quantity)
    order -> db     : status = rejected, order_events, notify buyer
end

== Cancel before payment ==
buyer -> fe     : Press 'Batalkan'
fe -> order     : POST /orders/{id}/cancel
order -> db     : SELECT order FOR UPDATE

alt status requested / approved
    order -> trip   : Release(tripID, quantity)
    order -> db     : status = cancelled, order_events, notify jastiper
    order --> fe    : 200 order
else paid or later
    order --> fe    : 409
end

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">4. Payment: checkout and settlement (fake gateway)</strong></summary>

---

```plantuml
@startuml payment

title Flow Payment (F04)

actor       "Pembeli"           as buyer
participant "SPA"               as fe
participant "API\npayment"      as pay
participant "API\norder"        as order
participant "API\nnotification" as notif
database    "PostgreSQL"        as db

autonumber
== Checkout ==
buyer -> fe     : Press 'Bayar'
fe -> pay       : POST /orders/{id}/payments (buyer)
pay -> db       : BEGIN; SELECT order FOR UPDATE

alt deadline passed
    pay -> order    : Cancel (system, payment deadline passed)
    pay --> fe      : 409
else pending payment exists
    pay --> fe      : 200 existing payment
else approved
    pay -> db       : INSERT payments (pending, amount = total)
    pay -> pay      : Gateway.Checkout (fake: token + URL)
    pay --> fe      : 201 payment
end

== Settle ==
buyer -> fe     : Press 'Bayar sekarang' on the fake checkout
fe -> pay       : POST /payments/{id}/simulate
pay -> db       : BEGIN; lock payment and order

alt already paid
    pay --> fe      : 200 (no-op)
else pending
    pay -> db       : UPDATE payments SET status = paid
    pay -> db       : Ledger payment:<payment_id>\n(debit gateway_clearing, credit escrow)
    pay -> order    : MarkPaid(orderID)
    order -> db     : status = paid, order_events
    pay -> notif    : Notify(buyer + jastiper, payment.received)
    pay -> db       : COMMIT
    pay --> fe      : 200 payment (paid)
end

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">5. Shipment and release: ship, confirm receipt, funds released</strong></summary>

---

```plantuml
@startuml shipment-release

title Flow Shipment & Release (F05 → F04)

actor       "Jastiper"          as jas
actor       "Pembeli"           as buyer
participant "SPA"               as fe
participant "API\nshipment"     as ship
participant "API\norder"        as order
participant "API\npayment"      as pay
participant "API\nnotification" as notif
database    "PostgreSQL"        as db

autonumber
== Ship ==
jas -> fe       : Fill method (COD / courier + tracking number), proof photo
fe -> ship      : PUT /orders/{id}/shipment
ship -> db      : BEGIN
ship -> order   : MarkShipped(orderID, jastiperID)

alt not owner / status != paid
    ship --> fe     : 404 / 409
else ok
    order -> db     : status = shipped, order_events
    ship -> db      : INSERT shipments, attach proof upload
    ship -> notif   : Notify(buyer, order.shipped)
    ship -> db      : COMMIT
    ship --> fe     : 200 shipment
end

== Confirm receipt ==
buyer -> fe     : Press 'Pesanan diterima'
fe -> ship      : POST /orders/{id}/confirm-receipt
ship -> db      : BEGIN
ship -> order   : MarkDelivered(orderID, buyer)
ship -> db      : UPDATE shipments SET delivered_at
ship -> pay     : Release(orderID)
pay -> pay      : fee = floor(total × 5 / 100)
pay -> db       : Ledger release:<order_id>: debit escrow,\ncredit jastiper_payable (95%) + platform_revenue (5%)
pay -> order    : MarkReleased(orderID)
pay -> notif    : Notify(jastiper, order.released)
ship -> db      : COMMIT
ship --> fe     : 200 (released)

== Buyer never confirms ==
note over ship, pay : job orders.auto_release, 7 days after shipped_at:\nMarkDelivered(system) → Release, same transaction

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">6. Dispute: buyer opens, admin decides refund or release</strong></summary>

---

```plantuml
@startuml dispute

title Flow Dispute (F06 → F04)

actor       "Pembeli"           as buyer
actor       "Admin"             as admin
participant "SPA"               as fe
participant "API\ndispute"      as dsp
participant "API\norder"        as order
participant "API\npayment"      as pay
participant "API\nnotification" as notif
database    "PostgreSQL"        as db

autonumber
== Open ==
buyer -> fe     : Press 'Ajukan komplain' (category, description, evidence photos)
fe -> dsp       : POST /orders/{id}/dispute
dsp -> db       : BEGIN
dsp -> order    : MarkDisputed(orderID, buyerID)

alt status != shipped
    dsp --> fe      : 409
else ok
    order -> db     : status = disputed, order_events
    dsp -> db       : INSERT disputes (open), dispute_evidence
    dsp -> notif    : Notify(jastiper + admins, dispute.opened)
    dsp -> db       : COMMIT
    dsp --> fe      : 201 dispute
end

== Discuss ==
buyer -> dsp    : POST /disputes/{id}/messages (also jastiper, admin)

== Decide ==
admin -> fe     : Open GET /admin/disputes → detail
fe -> dsp       : POST /admin/disputes/{id}/decision {decision, note}
dsp -> db       : BEGIN; SELECT dispute FOR UPDATE; resolve

alt refund
    dsp -> pay      : Refund(orderID, dispute_refund)
    pay -> db       : Ledger refund: debit escrow, credit buyer_refundable
    pay -> pay      : Gateway.Refund (fake: succeeds)
    pay -> db       : Ledger refund_paid: debit buyer_refundable,\ncredit gateway_clearing; refunds succeeded
    pay -> order    : MarkRefunded(orderID)
else release
    dsp -> order    : MarkDelivered(orderID, admin)
    dsp -> pay      : Release(orderID)
end

dsp -> db       : INSERT audit_logs (dispute.decided)
dsp -> notif    : Notify(buyer + jastiper, dispute.resolved)
dsp -> db       : COMMIT
dsp --> fe      : 200 dispute

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">7. Payout: jastiper requests, admin pays</strong></summary>

---

```plantuml
@startuml payout

title Flow Payout (F04, F08)

actor       "Jastiper"          as jas
actor       "Admin"             as admin
participant "SPA"               as fe
participant "API\npayment"      as pay
database    "PostgreSQL"        as db
participant "Bank"              as bank

autonumber
jas -> fe       : Save bank account
fe -> pay       : PUT /me/payout-account
jas -> fe       : Open wallet
fe -> pay       : GET /wallet
pay -> db       : SUM jastiper_payable entries − requested payouts
pay --> fe      : 200 {available, inEscrow, pendingPayout}
jas -> fe       : Request payout
fe -> pay       : POST /payouts {amount}
pay -> db       : BEGIN; lock jastiper's payout rows;\ncheck amount ≤ available; INSERT payouts (requested)
pay --> fe      : 201 payout

admin -> fe     : Open payout queue
fe -> pay       : GET /admin/payouts?status=requested
admin -> bank   : Transfer to the jastiper's account (outside the system)
fe -> pay       : POST /admin/payouts/{id}/paid {reference}
pay -> db       : Ledger payout:<payout_id>: debit jastiper_payable,\ncredit gateway_clearing; payouts paid; audit log
pay --> fe      : 200 payout

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">8. Notifications: polling</strong></summary>

---

```plantuml
@startuml notification

title Flow Notification (F07)

actor       "User"              as user
participant "SPA"               as fe
participant "API\nnotification" as notif
database    "PostgreSQL"        as db

autonumber
loop every N seconds while the app is open
    fe -> notif     : GET /notifications/unread-count
    notif -> db     : COUNT WHERE user_id = me AND read_at IS NULL
    notif --> fe    : 200 {count}
    fe --> user     : Update bell badge
end

user -> fe      : Open notifications
fe -> notif     : GET /notifications?limit=20
notif --> fe    : 200 {items, nextCursor}
fe -> notif     : POST /notifications/read-all
notif -> db     : UPDATE SET read_at = now() WHERE read_at IS NULL
notif --> fe    : 204
fe --> user     : Show list, clear badge

@enduml
```

</details>

## Related

[PLAN.md](../PLAN.md) (architecture, scope, platform), [DATABASE.md](../DATABASE.md) (full DDL, takes precedence for
table definitions), `titipdong-docs/PLAN.md` (business flow).

Diagrams are PlantUML (` ```plantuml ` blocks). Render them with the PlantUML IDE plugin, the `plantuml` CLI, or
<https://www.plantuml.com/plantuml>.
