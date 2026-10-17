-- +goose Up
CREATE TABLE trip_items (
    id              UUID PRIMARY KEY,
    trip_id         UUID NOT NULL REFERENCES trips (id),
    name            TEXT NOT NULL CHECK (char_length(name) <= 120),
    description     TEXT CHECK (char_length(description) <= 1000),
    photo_key       TEXT,
    reference_price BIGINT CHECK (reference_price >= 0),
    position        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX trip_items_trip_idx ON trip_items (trip_id, position);

-- +goose Down
DROP TABLE trip_items;
