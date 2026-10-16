---
name: money-and-order-state
description: Rules for any change that touches order status, the ledger, payments, refunds, payouts, releases, or disputes in titipdong-be. Use before writing or reviewing such code.
---

# Money and order state

These are the invariants that keep escrow correct. Breaking one is a correctness bug even if tests pass.

## Ownership of writes

- `orders.status` changes **only** inside `internal/order` through the explicit methods (`Approve`, `Reject`,
  `Cancel`, `MarkPaid`, `MarkShipped`, `MarkDelivered`, `MarkDisputed`, `MarkReleased`, `MarkRefunded`, and new ones
  added the same way). Never write `UPDATE orders SET status` anywhere else; never add a generic `SetStatus`.
- A new transition = a new row in the `transitions` table in `internal/order/service.go` (from, to, actor type) and
  an update to the state diagram in `docs/features/order.md`. Nothing else makes a transition legal.
- Ledger rows (`ledger_*`) are written **only** by `internal/payment` through `s.post(ctx, ledgerTransaction{...})`.
  Other modules call payment service methods (`Release`, `Refund`, …).

## Transactions and locking

- Every money movement is one `WithTx`: lock → check state → ledger → order transition → notifications → audit log.
  If any step fails, everything rolls back.
- Lock the order first (`orders.Lock(ctx, id)`, `SELECT … FOR UPDATE`), then other rows. Re-check status **after**
  locking; a value read before the lock is stale.
- Nested service calls inside the closure must receive the closure's `ctx` so they join the same transaction.
- Jobs process one order per transaction and treat "state already changed" as a no-op, not an error.

## Ledger rules

- Double entry: debits = credits, at least two entries (a deferred trigger rejects the commit otherwise).
- Append-only: corrections are new reversing transactions, never UPDATE/DELETE.
- Every ledger transaction has a deterministic `idempotency_key`; `Post` is a no-op when the key exists. Keys:
  `payment_received:<payment_id>`, `release:<order_id>`, `refund:<order_id>`, `refund_paid:<refund_id>`,
  `payout:<payout_id>`.
- Accounts and postings are exactly the table in `docs/features/payment.md`. Do not invent accounts or kinds; adding
  one needs a migration (CHECK list) and a doc update.
- Fee: `fee := total * 5 / 100` (integer division = floor), jastiper gets `total - fee`. All `int64`, never float.
- Balances are `SUM` of entries, never a stored column.

## Side effects in the same transaction

- Notifications (`notification.Notify`) and audit log rows (`platform/audit`) are inserted on the same `ctx`
  transaction, after the state change, using the event names in `docs/features/notification.md` and
  `docs/features/admin.md`. Do not invent new event or action names without adding them to those docs.
- Trip capacity is released through `trip.Release` whenever an order leaves the active set without delivery
  (reject, cancel, expiry, refund).

## Tests required

- Integration test against PostgreSQL (`TEST_DATABASE_URL`) for every new posting or transition path, asserting:
  the order status, the ledger balances per account, and that running the same operation twice changes nothing.
- A service test for each forbidden transition (wrong actor, wrong status, wrong owner → `ErrNotFound`).
