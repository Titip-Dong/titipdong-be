-- +goose Up
CREATE TABLE refunds (
    id                 UUID PRIMARY KEY,
    order_id           UUID NOT NULL REFERENCES orders (id),
    payment_id         UUID NOT NULL REFERENCES payments (id),
    amount             BIGINT NOT NULL CHECK (amount > 0),
    reason             TEXT NOT NULL CHECK (reason IN ('jastiper_cancelled', 'dispute_refund')),
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed')),
    provider_refund_id TEXT UNIQUE,
    failure_reason     TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at       TIMESTAMPTZ
);

CREATE UNIQUE INDEX refunds_one_per_order ON refunds (order_id) WHERE status <> 'failed';

-- +goose Down
DROP TABLE refunds;
