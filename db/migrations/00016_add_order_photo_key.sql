-- +goose Up
ALTER TABLE orders ADD COLUMN photo_key TEXT;

-- +goose Down
ALTER TABLE orders DROP COLUMN photo_key;
