# Feature documentation

One file per backend module: overview, business rules, database tables, endpoints, PlantUML sequence diagrams, and a
to-do list. Endpoint and table designs for modules that are not built yet are plans; once a module ships, its
migration and the generated OpenAPI spec (`/openapi.json`) are the source of truth and the file is updated to match.

| ID  | Module                              | Owner  | Status                                        |
| --- | ----------------------------------- | ------ | --------------------------------------------- |
| F01 | [Auth & User](auth.md)              | Carlo  | Partial: register, login, refresh, logout, `GET /me`, middleware |
| F02 | [Trip & Catalog](trip.md)           | Carlo  | Not started                                   |
| F03 | [Order](order.md)                   | Alfian | Not started                                   |
| F04 | [Payment & Escrow](payment.md)      | Barru  | Not started                                   |
| F05 | [Shipment](shipment.md)             | Alfian | Not started                                   |
| F06 | [Dispute](dispute.md)               | Barru  | Not started                                   |
| F07 | [Notification](notification.md)     | Azfa   | Not started                                   |
| F08 | [Admin](admin.md)                   | Azfa   | Not started                                   |

## MVP scope

The MVP is the smallest set of features that runs the main business flow end to end with real money through
Midtrans: `requested → approved → paid → shipped → delivered → released`, plus the exits when something goes wrong
(reject, cancel before payment, dispute → refund). Each feature file splits its to-do list into **MVP** and
**Post-MVP**.

Cut from the MVP as a whole:

- **Worker and River.** No background jobs: deadlines are checked when data is read or acted on, notifications are
  inserted directly in the caller's transaction, and there is no auto-release (admins release through disputes).
- **Uploads / Blob Storage.** Product links, tracking numbers, and text instead of photos.
- **Trip capacity.** The unit is undecided; jastipers close a trip manually when full.

Not cut: the money rules (double-entry ledger, idempotent payment callbacks, one transaction with an order row lock)
and the full order state machine.

### Platform work for the MVP

| MVP                                                                                     | Post-MVP                                           |
| --------------------------------------------------------------------------------------- | -------------------------------------------------- |
| k8s Secrets for `DATABASE_URL` / `JWT_SECRET` and Azure PostgreSQL (else pods crash-loop) | Worker (`cmd/worker`) and River                    |
| `database.WithTx`                                                                       | Blob storage (`platform/blob`)                     |
| CORS allow-list with credentials (refresh cookie)                                       | Metrics                                            |
| `/health/ready` and graceful shutdown                                                   | `platform/audit` and `audit_logs`                  |
| OpenAPI export command for the FE                                                       | Config via `caarlos0/env`                          |
| PR workflow running `make check`                                                        |                                                    |

### Decisions needed before starting

From [PLAN.md](../PLAN.md) section 7:

1. **Roles** (decision 3): naming shared by BE and FE, and whether one user can be both buyer and jastiper.
2. **Payment deadline** (decision 5), for example 24 hours after approval.
3. **Midtrans** (decision 1): sandbox account for development; before production, confirm that holding funds and
   paying jastipers out manually is allowed.
4. **FE hosting** (decision 6): sets the CORS origins and the refresh cookie domain.

### Order of work

1. Platform, then F01 refresh tokens (Carlo).
2. F02 Trip (Carlo) in parallel with F03 Order (Alfian); Order only needs `trip.Trip()`.
3. F07 in-app notifications (Azfa); small, and used by F03 onwards.
4. F04 Payment (Barru); the longest, so start the Midtrans sandbox integration early and in parallel.
5. F05 Shipment (Alfian), then F06 Dispute (Barru) and F08 transactions list (Azfa).

**Done when:** on AKS with Midtrans sandbox, one order runs from request to `released`, one dispute is decided as a
refund by an admin, and the ledger stays balanced.

## MVP flows

End-to-end sequence diagrams across modules for the MVP scope above (no worker, no uploads, no trip capacity).
Per-module detail is in each feature file.

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
auth -> auth    : Verify password (login), issue JWT (15 min)
auth -> db      : INSERT refresh_tokens (new family)
auth --> fe     : 201/200 {accessToken, user}\nSet-Cookie: refresh_token (HttpOnly, Path=/auth)
fe --> user     : Show home

== App reload / access token expired ==
fe -> auth      : POST /auth/refresh (cookie)
auth -> db      : SELECT refresh_tokens by hash FOR UPDATE

alt valid
    auth -> db      : Revoke old, INSERT new (same family)
    auth --> fe     : 200 {accessToken, user} + new cookie
else revoked (reuse) / expired / unknown
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
database    "PostgreSQL"        as db

autonumber
== Open trip ==
jas -> fe       : Fill trip form (destination, dates, deadline)
fe -> trip      : POST /trips (Bearer, jastiper)
trip -> trip    : Validate dates and deadline, generate slug
trip -> db      : INSERT trips (status = open)
trip --> fe     : 201 trip
fe --> jas      : Show trip page /trip/:slug

== Browse ==
buyer -> fe     : Search by city
fe -> trip      : GET /trips?city=…&limit=20
trip -> db      : SELECT open trips before request_deadline
trip --> fe     : 200 {items, nextCursor}
buyer -> fe     : Open a trip
fe -> trip      : GET /trips/{slug}
trip --> fe     : 200 trip
fe --> buyer    : Show trip detail + 'Titip' button

== Close trip (full) ==
jas -> fe       : Press 'Tutup trip'
fe -> trip      : POST /trips/{id}/close
trip -> db      : UPDATE trips SET status = closed
trip --> fe     : 200 trip

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
buyer -> fe     : Fill request (product, link, qty, notes)
fe -> order     : POST /trips/{slug}/orders (buyer)
order -> trip   : Trip(slug)

alt trip closed or deadline passed
    order --> fe    : 409
    fe --> buyer    : Show 'Trip sudah tutup'
else open
    autonumber 5
    order -> db     : BEGIN; INSERT orders (requested), order_events
    order -> notif  : Notify(jastiper, order.requested)
    notif -> db     : INSERT notifications
    order -> db     : COMMIT
    order --> fe    : 201 order
    fe --> buyer    : Show order 'Menunggu persetujuan'
end

== Jastiper decides ==
jas -> fe       : Open order from GET /orders

alt approve
    jas -> fe       : Set item price + jastip fee
    fe -> order     : POST /orders/{id}/approve {itemPrice, jastipFee}
    order -> db     : BEGIN; SELECT order FOR UPDATE
    order -> order  : total = itemPrice × qty + jastipFee,\npayment_deadline = now + window
    order -> db     : UPDATE status = approved, INSERT order_events
    order -> notif  : Notify(buyer, order.approved)
    order -> db     : COMMIT
    order --> fe    : 200 order
else reject
    fe -> order     : POST /orders/{id}/reject {reason}
    order -> db     : status = rejected, order_events, notify buyer
    order --> fe    : 200 order
end

== Buyer cancels (before payment only) ==
buyer -> fe     : Press 'Batalkan'
fe -> order     : POST /orders/{id}/cancel
order -> db     : SELECT order FOR UPDATE

alt status requested / approved
    order -> db     : status = cancelled, order_events, notify jastiper
    order --> fe    : 200 order
else paid or later
    order --> fe    : 409
end

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">4. Payment: checkout and Midtrans callback</strong></summary>

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
participant "Midtrans"          as mt

autonumber
== Checkout ==
buyer -> fe     : Press 'Bayar'
fe -> pay       : POST /orders/{id}/payments (buyer)
pay -> db       : SELECT order FOR UPDATE

alt deadline passed
    pay -> order    : Cancel (system, payment deadline passed)
    pay --> fe      : 409
    fe --> buyer    : Show 'Batas bayar habis'
else pending payment exists
    autonumber 4
    pay --> fe      : 200 existing Snap token
else approved
    autonumber 4
    pay -> db       : INSERT payments (pending, amount = total)
    pay -> mt       : Snap create transaction (order_id = payment.id)
    mt --> pay      : token, redirect_url
    pay --> fe      : 201 {token, redirectUrl}
end

fe -> mt        : Open Snap popup
buyer -> mt     : Pay (VA / QRIS / e-wallet)

== Callback (may arrive more than once) ==
mt -> pay       : POST /payments/callback
pay -> pay      : Verify signature_key
pay -> db       : BEGIN; INSERT payment_notifications ON CONFLICT DO NOTHING

alt duplicate
    pay -> db       : COMMIT
    pay --> mt      : 200 (no-op)
else invalid signature
    pay -> db       : outcome = rejected; COMMIT
    pay --> mt      : 401
else settlement
    pay -> db       : UPDATE payments SET status = paid
    pay -> db       : Ledger: debit gateway_clearing, credit escrow
    pay -> order    : MarkPaid(orderID)
    order -> db     : status = paid, order_events
    pay -> notif    : Notify(buyer + jastiper, payment.received)
    pay -> db       : COMMIT
    pay --> mt      : 200
else expire / cancel / deny
    pay -> db       : UPDATE payments SET status = expired/failed; COMMIT
    pay --> mt      : 200
end

fe -> order     : GET /orders/{id} (after Snap closes)
order --> fe    : 200 order (paid)
fe --> buyer    : Show 'Pembayaran berhasil'

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
jas -> fe       : Fill method (COD / courier + tracking number)
fe -> ship      : PUT /orders/{id}/shipment
ship -> db      : BEGIN
ship -> order   : MarkShipped(orderID, jastiperID)

alt not owner / status != paid
    order --> ship  : error
    ship --> fe     : 404 / 409
else ok
    order -> db     : status = shipped, order_events
    ship -> db      : INSERT shipments
    ship -> notif   : Notify(buyer, order.shipped)
    ship -> db      : COMMIT
    ship --> fe     : 200 shipment
end

== Confirm receipt ==
buyer -> fe     : Press 'Pesanan diterima'
fe -> ship      : POST /orders/{id}/confirm-receipt
ship -> db      : BEGIN
ship -> order   : MarkDelivered(orderID, buyerID)
order -> db     : status = delivered, order_events
ship -> db      : UPDATE shipments SET delivered_at
ship -> pay     : Release(orderID)
pay -> pay      : fee = floor(total × 5 / 100)
pay -> db       : Ledger (release:<order_id>): debit escrow,\ncredit jastiper_payable (95%) + platform_revenue (5%)
pay -> order    : MarkReleased(orderID)
order -> db     : status = released, order_events
pay -> notif    : Notify(jastiper, order.released)
ship -> db      : COMMIT
ship --> fe     : 200 order (released)
fe --> buyer    : Show 'Selesai'

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
participant "Midtrans\nDashboard" as mt

autonumber
== Open ==
buyer -> fe     : Press 'Ajukan komplain' (category + description)
fe -> dsp       : POST /orders/{id}/dispute
dsp -> db       : BEGIN
dsp -> order    : MarkDisputed(orderID, buyerID)

alt status != shipped / already disputed
    dsp --> fe      : 409
else ok
    order -> db     : status = disputed, order_events
    dsp -> db       : INSERT disputes (open)
    dsp -> notif    : Notify(jastiper + admins, dispute.opened)
    dsp -> db       : COMMIT
    dsp --> fe      : 201 dispute
end

== Decide ==
admin -> fe     : Open GET /admin/disputes → detail
fe -> dsp       : POST /admin/disputes/{id}/decision {decision, note}
dsp -> db       : BEGIN; SELECT dispute FOR UPDATE; UPDATE resolved

alt refund
    dsp -> order    : ResolveDispute(refund)
    order -> db     : status = refunded, order_events
    dsp -> pay      : Refund(orderID, dispute_refund)
    pay -> db       : Ledger: debit escrow, credit buyer_refundable;\nINSERT refunds (pending)
else release
    dsp -> order    : ResolveDispute(release)
    order -> db     : status = delivered, order_events
    dsp -> pay      : Release(orderID)
    pay -> db       : Ledger release, order → released
end

dsp -> notif    : Notify(buyer + jastiper, dispute.resolved)
dsp -> db       : COMMIT
dsp --> fe      : 200 dispute

opt refund (manual in the MVP)
    admin -> mt     : Refund the transaction in the Midtrans dashboard
    admin -> db     : Mark refund succeeded\n(ledger: debit buyer_refundable, credit gateway_clearing)
end

@enduml
```

</details>

<details>
<summary><strong style="font-size:1.1em;">7. Payout: admin pays jastipers manually</strong></summary>

---

```plantuml
@startuml payout

title Flow Manual Payout (F08, F04)

actor       "Jastiper"          as jas
actor       "Admin"             as admin
participant "SPA"               as fe
participant "API\npayment"      as pay
participant "API\nadmin"        as adm
database    "PostgreSQL"        as db
participant "Bank"              as bank

autonumber
jas -> fe       : Open wallet
fe -> pay       : GET /wallet (jastiper)
pay -> db       : SUM ledger_entries of jastiper_payable
pay --> fe      : 200 {balance}

admin -> fe     : Open transactions
fe -> adm       : GET /admin/transactions
adm -> db       : SELECT released orders (jastiper share, fee)\n+ refunds pending
adm --> fe      : 200 {items, nextCursor}
admin -> bank   : Transfer jastiper share (outside the system)
note right of admin
  Recording the payout in the ledger
  (debit jastiper_payable, credit gateway_clearing)
  comes with payouts post-MVP.
end note

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

[PLAN.md](../PLAN.md) (implementation plan and open decisions), [DATABASE.md](../DATABASE.md) (full DDL,
takes precedence for table definitions), `titipdong-docs/PLAN.md` (business flow).

Diagrams are PlantUML (` ```plantuml ` blocks). Render them with the PlantUML IDE plugin, `plantuml` CLI, or
<https://www.plantuml.com/plantuml>.
