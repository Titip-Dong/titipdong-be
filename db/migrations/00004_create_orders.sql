-- +goose Up
CREATE TABLE orders (
    id               UUID PRIMARY KEY,
    code             TEXT NOT NULL UNIQUE,
    trip_id          UUID NOT NULL REFERENCES trips (id),
    buyer_id         UUID NOT NULL REFERENCES users (id),
    jastiper_id      UUID NOT NULL REFERENCES users (id),
    product_name     TEXT NOT NULL CHECK (char_length(product_name) <= 200),
    product_url      TEXT NOT NULL DEFAULT '' CHECK (char_length(product_url) <= 2000),
    quantity         INTEGER NOT NULL CHECK (quantity > 0),
    notes            TEXT NOT NULL DEFAULT '' CHECK (char_length(notes) <= 1000),
    item_price       BIGINT CHECK (item_price >= 0),
    jastip_fee       BIGINT CHECK (jastip_fee >= 0),
    total            BIGINT CHECK (total > 0),
    status           TEXT NOT NULL DEFAULT 'requested' CHECK (status IN (
                         'requested', 'approved', 'rejected', 'cancelled', 'paid', 'shipped',
                         'delivered', 'disputed', 'released', 'refunded')),
    status_reason    TEXT NOT NULL DEFAULT '',
    payment_deadline TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (buyer_id <> jastiper_id),
    CHECK ((item_price IS NULL) = (total IS NULL) AND (jastip_fee IS NULL) = (total IS NULL)),
    CHECK (status IN ('requested', 'rejected', 'cancelled') OR total IS NOT NULL),
    CHECK (total IS NULL OR total = item_price * quantity + jastip_fee)
);

CREATE INDEX orders_buyer_idx ON orders (buyer_id, created_at DESC, id DESC);
CREATE INDEX orders_jastiper_idx ON orders (jastiper_id, created_at DESC, id DESC);
CREATE INDEX orders_trip_idx ON orders (trip_id, status);
CREATE INDEX orders_payment_deadline_idx ON orders (payment_deadline) WHERE status = 'approved';

CREATE TABLE order_events (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id    UUID NOT NULL REFERENCES orders (id),
    from_status TEXT,
    to_status   TEXT NOT NULL,
    actor_id    UUID REFERENCES users (id),
    actor_type  TEXT NOT NULL CHECK (actor_type IN ('buyer', 'jastiper', 'admin', 'system')),
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX order_events_order_idx ON order_events (order_id, id);

-- +goose Down
DROP TABLE order_events;
DROP TABLE orders;
