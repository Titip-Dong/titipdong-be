-- +goose Up
CREATE TABLE uploads (
    id           UUID PRIMARY KEY,
    owner_id     UUID NOT NULL REFERENCES users (id),
    purpose      TEXT NOT NULL CHECK (purpose IN (
                     'order_photo', 'trip_item_photo', 'shipment_proof', 'dispute_evidence')),
    object_key   TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size_bytes   BIGINT NOT NULL CHECK (size_bytes > 0),
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'attached')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    attached_at  TIMESTAMPTZ,
    CHECK ((status = 'attached') = (attached_at IS NOT NULL))
);

CREATE INDEX uploads_cleanup_idx ON uploads (created_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE uploads;
