-- +goose Up
CREATE TABLE shipments (
    order_id        UUID PRIMARY KEY REFERENCES orders (id),
    method          TEXT NOT NULL CHECK (method IN ('cod', 'courier')),
    courier_code    TEXT NOT NULL DEFAULT '',
    tracking_number TEXT NOT NULL DEFAULT '',
    handover_note   TEXT NOT NULL DEFAULT '' CHECK (char_length(handover_note) <= 1000),
    shipped_at      TIMESTAMPTZ NOT NULL,
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (method = 'cod' OR (courier_code <> '' AND tracking_number <> ''))
);

-- +goose Down
DROP TABLE shipments;
