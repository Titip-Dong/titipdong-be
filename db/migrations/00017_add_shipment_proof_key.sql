-- +goose Up
ALTER TABLE shipments ADD COLUMN proof_key TEXT;

-- +goose Down
ALTER TABLE shipments DROP COLUMN proof_key;
