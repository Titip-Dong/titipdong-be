-- +goose Up
CREATE TABLE payments (
    id                      UUID PRIMARY KEY,
    order_id                UUID NOT NULL REFERENCES orders (id),
    amount                  BIGINT NOT NULL CHECK (amount > 0),
    provider                TEXT NOT NULL,
    provider_transaction_id TEXT UNIQUE,
    status                  TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'paid', 'failed', 'expired', 'cancelled')),
    checkout_token          TEXT NOT NULL DEFAULT '',
    checkout_url            TEXT NOT NULL DEFAULT '',
    expires_at              TIMESTAMPTZ NOT NULL,
    paid_at                 TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX payments_one_active_per_order ON payments (order_id) WHERE status IN ('pending', 'paid');

CREATE TABLE ledger_accounts (
    id         UUID PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN (
                   'gateway_clearing', 'escrow', 'jastiper_payable', 'platform_revenue', 'buyer_refundable')),
    user_id    UUID REFERENCES users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((kind IN ('jastiper_payable', 'buyer_refundable')) = (user_id IS NOT NULL))
);

CREATE UNIQUE INDEX ledger_accounts_kind_user_key ON ledger_accounts (kind, user_id) NULLS NOT DISTINCT;

CREATE TABLE ledger_transactions (
    id              UUID PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN ('payment_received', 'release', 'refund', 'refund_paid', 'payout')),
    order_id        UUID REFERENCES orders (id),
    idempotency_key TEXT NOT NULL UNIQUE,
    created_by      UUID REFERENCES users (id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id UUID NOT NULL REFERENCES ledger_transactions (id),
    account_id     UUID NOT NULL REFERENCES ledger_accounts (id),
    order_id       UUID REFERENCES orders (id),
    direction      TEXT NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount         BIGINT NOT NULL CHECK (amount > 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ledger_entries_account_idx ON ledger_entries (account_id, id);
CREATE INDEX ledger_entries_order_idx ON ledger_entries (order_id) WHERE order_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION ledger_check_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    debits  BIGINT;
    credits BIGINT;
    entries INTEGER;
BEGIN
    SELECT COALESCE(SUM(amount) FILTER (WHERE direction = 'debit'), 0),
           COALESCE(SUM(amount) FILTER (WHERE direction = 'credit'), 0),
           COUNT(*)
    INTO debits, credits, entries
    FROM ledger_entries
    WHERE transaction_id = NEW.transaction_id;

    IF debits <> credits OR entries < 2 THEN
        RAISE EXCEPTION 'ledger transaction % is unbalanced (debits %, credits %, entries %)',
            NEW.transaction_id, debits, credits, entries;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER ledger_entries_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_balanced();

-- +goose StatementBegin
CREATE FUNCTION ledger_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_accounts_append_only BEFORE UPDATE OR DELETE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER ledger_transactions_append_only BEFORE UPDATE OR DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER ledger_entries_append_only BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

-- +goose Down
DROP TABLE ledger_entries;
DROP TABLE ledger_transactions;
DROP TABLE ledger_accounts;
DROP FUNCTION ledger_append_only();
DROP FUNCTION ledger_check_balanced();
DROP TABLE payments;
