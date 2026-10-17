-- +goose Up
CREATE TABLE payout_accounts (
    user_id        UUID PRIMARY KEY REFERENCES users (id),
    bank_name      TEXT NOT NULL CHECK (char_length(bank_name) <= 100),
    account_number TEXT NOT NULL CHECK (account_number ~ '^[0-9]{5,30}$'),
    account_name   TEXT NOT NULL CHECK (char_length(account_name) <= 100),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE payouts (
    id               UUID PRIMARY KEY,
    jastiper_id      UUID NOT NULL REFERENCES users (id),
    amount           BIGINT NOT NULL CHECK (amount > 0),
    bank_name        TEXT NOT NULL,
    account_number   TEXT NOT NULL,
    account_name     TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'paid', 'rejected')),
    reference        TEXT CHECK (char_length(reference) <= 100),
    rejection_reason TEXT CHECK (char_length(rejection_reason) <= 500),
    decided_by       UUID REFERENCES users (id),
    requested_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at       TIMESTAMPTZ,
    CHECK ((status = 'requested') = (decided_at IS NULL))
);

CREATE INDEX payouts_jastiper_idx ON payouts (jastiper_id, requested_at DESC, id DESC);
CREATE INDEX payouts_queue_idx ON payouts (status, requested_at, id);
CREATE UNIQUE INDEX payouts_one_open_per_jastiper ON payouts (jastiper_id) WHERE status = 'requested';

-- +goose Down
DROP TABLE payouts;
DROP TABLE payout_accounts;
