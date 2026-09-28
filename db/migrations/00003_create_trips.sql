-- +goose Up
CREATE TABLE trips (
    id                  UUID PRIMARY KEY,
    jastiper_id         UUID NOT NULL REFERENCES users (id),
    slug                TEXT NOT NULL UNIQUE,
    title               TEXT NOT NULL CHECK (char_length(title) <= 120),
    description         TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    destination_city    TEXT NOT NULL,
    destination_country CHAR(2) NOT NULL,
    departs_on          DATE NOT NULL,
    returns_on          DATE NOT NULL,
    request_deadline    TIMESTAMPTZ NOT NULL,
    status              TEXT NOT NULL DEFAULT 'open'
                        CHECK (status IN ('open', 'closed', 'completed', 'cancelled')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (returns_on >= departs_on)
);

CREATE INDEX trips_browse_idx ON trips (departs_on, id) WHERE status = 'open';
CREATE INDEX trips_jastiper_idx ON trips (jastiper_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE trips;
