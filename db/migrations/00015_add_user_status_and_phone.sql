-- +goose Up
ALTER TABLE users
    ADD COLUMN status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    ADD COLUMN phone      TEXT CHECK (char_length(phone) <= 20),
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE users
    DROP COLUMN updated_at,
    DROP COLUMN phone,
    DROP COLUMN status;
